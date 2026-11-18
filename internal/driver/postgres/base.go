package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/sqlkit"
)

// base is what every store on one Postgres database has, whatever it holds:
// its pool, whether it is closed, and its name in error messages. Master and
// Tenant embed it, so the two classify a failure the same way, close the same
// way, migrate under the same lock and run a write transaction the same way.
type base struct {
	pool *pgxpool.Pool
	// mig is the pool seen as a *sql.DB, which is what the migration runner
	// takes. It holds no connection of its own: each is borrowed from pool
	// for as long as the runner uses it.
	mig    *sql.DB
	red    *redactor
	closed atomic.Bool
	// name is how an error message calls the database ("master database",
	// "tenant database"), and short how it calls its owner ("master",
	// "tenant").
	name, short string
}

// connect opens the pool for address and fills the base around it.
func (b *base) connect(ctx context.Context, address, name, short string, opts poolOptions) error {
	pool, red, err := openPool(ctx, address, name, opts)
	if err != nil {
		return err
	}
	b.pool, b.mig, b.red, b.name, b.short = pool, stdlib.OpenDBFromPool(pool), red, name, short
	return nil
}

// open fails once the store is closed, with a kind, rather than with pgx's
// unclassified "closed pool".
func (b *base) open() error {
	if b.closed.Load() {
		return fault.New(fault.Unavailable, "the "+b.name+" is closed")
	}
	return nil
}

// fail gives err its kind. An error that already has one keeps it: a NotFound
// or Conflict decided inside a transaction is the answer even if the context
// then ended, since nothing was committed either way. Otherwise a context
// that ended makes it Unavailable, keeping the context's cause and the
// driver's scrubbed error in the chain for errors.Is; a store closed under
// the call makes it Unavailable; and the driver's SQLSTATE decides the rest
// (classify).
func (b *base) fail(ctx context.Context, err error, message string) error {
	if err == nil {
		return nil
	}
	var fe *fault.Error
	if errors.As(err, &fe) {
		return err
	}
	if ctx.Err() != nil {
		return fault.Wrap(fault.Unavailable, errors.Join(context.Cause(ctx), b.red.scrub(err)),
			"the request ended before the "+b.name+" answered")
	}
	if b.closed.Load() {
		return fault.Wrap(fault.Unavailable, b.red.scrub(err), "the "+b.name+" is closed")
	}
	return classify(b.red, err, message)
}

// runnerErr is fail for an error from the migration runner. The runner
// wraps what pgx returned with kinds of its own, and its chain may hold a
// connection error that names the host, so the whole chain's text is
// scrubbed and put behind the same kind and message; errors.Is still sees
// ErrChecksum and ErrNewerSchema through it.
func (b *base) runnerErr(ctx context.Context, err error, message string) error {
	var fe *fault.Error
	if errors.As(err, &fe) {
		return fault.Wrap(fe.Kind(), b.red.scrub(err), fe.Message())
	}
	return b.fail(ctx, err, message)
}

// migrate applies migrations under the migration lock and returns the schema
// version the database is then at. A store's Migrate passes its own files; a
// test passes others, so the lock and the runner are tried with files that
// fail when applied twice.
func (b *base) migrate(ctx context.Context, migrations []sqlkit.Migration) (int, error) {
	if err := b.open(); err != nil {
		return 0, err
	}
	var version int
	err := b.withMigrationLock(ctx, func() error {
		var err error
		version, err = sqlkit.Migrate(ctx, b.mig, dialect, migrations)
		return err
	})
	return version, b.runnerErr(ctx, err, "the "+b.name+" cannot be migrated")
}

// version returns the schema version the database is at.
func (b *base) version(ctx context.Context) (int, error) {
	if err := b.open(); err != nil {
		return 0, err
	}
	v, err := sqlkit.Version(ctx, b.mig, dialect)
	return v, b.runnerErr(ctx, err, "the "+b.short+"'s schema version cannot be read")
}

// ping reports whether the database answers.
func (b *base) ping(ctx context.Context) error {
	if err := b.open(); err != nil {
		return err
	}
	if err := b.pool.Ping(ctx); err != nil {
		return fault.Wrap(fault.Unavailable, b.red.scrub(err), "the "+b.name+" does not answer")
	}
	return nil
}

// shutdown waits for the calls in progress to give their connections back,
// then closes every connection, once; a second call does nothing.
func (b *base) shutdown() error {
	if b.closed.Swap(true) {
		return nil
	}
	// The *sql.DB holds no connection of its own (see mig), so closing it
	// first only stops it handing out new ones.
	if err := b.mig.Close(); err != nil {
		b.pool.Close()
		return fault.Wrap(fault.Internal, b.red.scrub(err), "the "+b.name+" did not close cleanly")
	}
	b.pool.Close()
	return nil
}

// rollbackTimeout bounds how long a failed transaction waits for its
// ROLLBACK.
const rollbackTimeout = 5 * time.Second

// commitTimeout bounds how long a COMMIT, once sent, waits for the server.
const commitTimeout = 30 * time.Second

// write runs fn in one transaction and commits only when fn returns nil.
// Every changing operation and its audit entry go through here together,
// which is what makes them both or neither. The transaction is read
// committed; the statements in fn are written to be right under it (row
// locks and compare-and-set updates, never read, decide, write).
func (b *base) write(ctx context.Context, message string, fn func(tx pgx.Tx) error) error {
	if err := b.open(); err != nil {
		return err
	}
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return b.fail(ctx, err, message)
	}
	if err := fn(tx); err != nil {
		b.rollback(tx)
		return b.fail(ctx, err, message)
	}
	// A context that ended before the COMMIT is sent ends the call with
	// nothing committed. Once it is sent the COMMIT is not tied to the
	// context: canceling it while the server answers would return an error
	// for a transaction that committed, and a call that fails must have
	// committed nothing. So the call returns nil exactly when the commit
	// happened.
	if ctx.Err() != nil {
		b.rollback(tx)
		return b.fail(ctx, ctx.Err(), message)
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
	defer cancel()
	if err := tx.Commit(cctx); err != nil {
		b.rollback(tx)
		return b.fail(ctx, err, message)
	}
	return nil
}

// rollback ends a transaction that failed. It does not use the request's
// context, which may be the reason for the failure and would then leave the
// connection with its transaction open; the pool destroys a connection whose
// rollback fails, so none is leaked.
func (b *base) rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), rollbackTimeout)
	defer cancel()
	_ = tx.Rollback(ctx) // The error that made the rollback necessary is the one returned.
}

// notFound turns pgx.ErrNoRows into NotFound with what, and classifies any
// other error with message.
func (b *base) notFound(ctx context.Context, err error, what, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.NotFound, what)
	}
	return b.fail(ctx, err, message)
}
