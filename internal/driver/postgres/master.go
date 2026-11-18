package postgres

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/sqlkit"
)

// masterFiles holds the master database's migrations, embedded so the binary
// upgrades its own database with no file beside it.
//
//go:embed migrations/master/*.sql
var masterFiles embed.FS

// masterMigrations are the master's migrations, read once. A broken embedded
// directory is a build mistake that every test of this package shows.
var masterMigrations, masterMigrationsErr = sqlkit.Load(masterFiles, "migrations/master")

// Master is the master database on Postgres. It is safe for concurrent use.
type Master struct {
	pool *pgxpool.Pool
	// mig is the pool seen as a *sql.DB, which is what the migration runner
	// takes. It holds no connection of its own: each is borrowed from pool
	// for as long as the runner uses it.
	mig    *sql.DB
	red    *redactor
	closed atomic.Bool
}

var _ master.Store = (*Master)(nil)

// OpenMaster opens the master database at address, a postgres:// URL. It does
// not migrate: the caller calls Migrate, so that opening a database never
// changes it unasked. It does reach the server, so an address that cannot be
// used fails here, as Unavailable, with the address not repeated.
func OpenMaster(ctx context.Context, address string) (*Master, error) {
	if masterMigrationsErr != nil {
		return nil, fault.Wrap(fault.Internal, masterMigrationsErr, "the master's migrations cannot be read")
	}
	pool, red, err := openPool(ctx, address)
	if err != nil {
		return nil, err
	}
	return &Master{pool: pool, mig: stdlib.OpenDBFromPool(pool), red: red}, nil
}

// The names of the lists, as their cursors carry them, so a cursor from one
// list is refused by every other.
const (
	listTenants       = "tenants"
	listKeys          = "keys"
	listGrantsByKey   = "grants-by-key"
	listGrantsByScope = "grants-by-scope"
	listSettings      = "settings"
	listAudit         = "audit"
)

// fail gives err its kind. An error that already has one keeps it: a NotFound
// or Conflict decided inside a transaction is the answer even if the context
// then ended, since nothing was committed either way. Otherwise a context
// that ended makes it Unavailable, keeping the context's cause and the
// driver's scrubbed error in the chain for errors.Is; a store closed under
// the call makes it Unavailable; and the driver's SQLSTATE decides the rest
// (classify).
func (m *Master) fail(ctx context.Context, err error, message string) error {
	if err == nil {
		return nil
	}
	var fe *fault.Error
	if errors.As(err, &fe) {
		return err
	}
	if ctx.Err() != nil {
		return fault.Wrap(fault.Unavailable, errors.Join(context.Cause(ctx), m.red.scrub(err)),
			"the request ended before the master database answered")
	}
	if m.closed.Load() {
		return fault.Wrap(fault.Unavailable, m.red.scrub(err), "the master database is closed")
	}
	return classify(m.red, err, message)
}

// runnerErr is fail for an error from the migration runner. The runner
// wraps what pgx returned with kinds of its own, and its chain may hold a
// connection error that names the host, so the whole chain's text is
// scrubbed and put behind the same kind and message; errors.Is still sees
// ErrChecksum and ErrNewerSchema through it.
func (m *Master) runnerErr(ctx context.Context, err error, message string) error {
	var fe *fault.Error
	if errors.As(err, &fe) {
		return fault.Wrap(fe.Kind(), m.red.scrub(err), fe.Message())
	}
	return m.fail(ctx, err, message)
}

// Migrate implements master.Store.
func (m *Master) Migrate(ctx context.Context) error {
	_, err := m.migrate(ctx, masterMigrations)
	return err
}

// migrate applies migrations under the migration lock and returns the schema
// version the database is then at. Migrate passes the master's own; a test
// passes others, so the lock and the runner are tried with files that fail
// when applied twice.
func (m *Master) migrate(ctx context.Context, migrations []sqlkit.Migration) (int, error) {
	if err := m.open(); err != nil {
		return 0, err
	}
	var version int
	err := m.withMigrationLock(ctx, func() error {
		var err error
		version, err = sqlkit.Migrate(ctx, m.mig, dialect, migrations)
		return err
	})
	return version, m.runnerErr(ctx, err, "the master database cannot be migrated")
}

// Version implements master.Store.
func (m *Master) Version(ctx context.Context) (int, error) {
	if err := m.open(); err != nil {
		return 0, err
	}
	v, err := sqlkit.Version(ctx, m.mig, dialect)
	return v, m.runnerErr(ctx, err, "the master's schema version cannot be read")
}

// Ping implements master.Store.
func (m *Master) Ping(ctx context.Context) error {
	if err := m.open(); err != nil {
		return err
	}
	if err := m.pool.Ping(ctx); err != nil {
		return fault.Wrap(fault.Unavailable, m.red.scrub(err), "the master database does not answer")
	}
	return nil
}

// Close implements master.Store. It waits for the calls in progress to give
// their connections back, then closes every connection.
func (m *Master) Close() error {
	if m.closed.Swap(true) {
		return nil
	}
	// The *sql.DB holds no connection of its own (see mig), so closing it
	// first only stops it handing out new ones.
	if err := m.mig.Close(); err != nil {
		m.pool.Close()
		return fault.Wrap(fault.Internal, m.red.scrub(err), "the master database did not close cleanly")
	}
	m.pool.Close()
	return nil
}

// open fails once the store is closed, with a kind, rather than with pgx's
// unclassified "closed pool".
func (m *Master) open() error {
	if m.closed.Load() {
		return fault.New(fault.Unavailable, "the master database is closed")
	}
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
func (m *Master) write(ctx context.Context, message string, fn func(tx pgx.Tx) error) error {
	if err := m.open(); err != nil {
		return err
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return m.fail(ctx, err, message)
	}
	if err := fn(tx); err != nil {
		m.rollback(tx)
		return m.fail(ctx, err, message)
	}
	// A context that ended before the COMMIT is sent ends the call with
	// nothing committed. Once it is sent the COMMIT is not tied to the
	// context: canceling it while the server answers would return an error
	// for a transaction that committed, and a call that fails must have
	// committed nothing. So the call returns nil exactly when the commit
	// happened.
	if ctx.Err() != nil {
		m.rollback(tx)
		return m.fail(ctx, ctx.Err(), message)
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
	defer cancel()
	if err := tx.Commit(cctx); err != nil {
		m.rollback(tx)
		return m.fail(ctx, err, message)
	}
	return nil
}

// rollback ends a transaction that failed. It does not use the request's
// context, which may be the reason for the failure and would then leave the
// connection with its transaction open; the pool destroys a connection whose
// rollback fails, so none is leaked.
func (m *Master) rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), rollbackTimeout)
	defer cancel()
	_ = tx.Rollback(ctx) // The error that made the rollback necessary is the one returned.
}

// --- Tenants ---

const tenantColumns = `id, name, driver, address, state, state_reason, created`

// scanner is what pgx.Row and pgx.CollectableRow share.
type scanner interface{ Scan(...any) error }

// scanTenant reads one row of tenantColumns.
func scanTenant(row scanner) (master.Tenant, error) {
	var t master.Tenant
	var state string
	var created time.Time
	err := row.Scan(&t.ID, &t.Name, &t.Driver, &t.SealedAddress, &state, &t.StateReason, &created)
	t.State = master.TenantState(state)
	t.Created = created.UTC()
	return t, err
}

// CreateTenant implements master.Store.
func (m *Master) CreateTenant(ctx context.Context, t master.Tenant, audit master.AuditEntry) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the tenant cannot be created", func(tx pgx.Tx) error {
		// No look-up first: the unique constraints decide, so two creations
		// racing in two processes cannot both succeed, and the constraint
		// that failed says whether the name or the id was taken.
		_, err := tx.Exec(ctx, `INSERT INTO tenants (`+tenantColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			t.ID, t.Name, t.Driver, t.SealedAddress, string(t.State), t.StateReason, micros(t.Created))
		switch {
		case constraintViolation(err, "tenants_name_key"):
			return fault.Wrap(fault.Conflict, m.red.scrub(err), "a tenant named "+t.Name+" exists")
		case isUniqueViolation(err):
			return fault.Wrap(fault.Conflict, m.red.scrub(err), "a tenant with this id exists")
		case err != nil:
			return err
		}
		return insertAudit(ctx, tx, m.red, audit)
	})
}

// Tenant implements master.Store.
func (m *Master) Tenant(ctx context.Context, id string) (master.Tenant, error) {
	if err := master.ValidateID("tenant id", id); err != nil {
		return master.Tenant{}, err
	}
	if err := m.open(); err != nil {
		return master.Tenant{}, err
	}
	t, err := scanTenant(m.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE id = $1`, id))
	return t, m.notFound(ctx, err, "no such tenant", "the tenant cannot be read")
}

// TenantByName implements master.Store: one read of the name's unique index.
func (m *Master) TenantByName(ctx context.Context, name string) (master.Tenant, error) {
	if err := master.ValidateTenantName(name); err != nil {
		return master.Tenant{}, err
	}
	if err := m.open(); err != nil {
		return master.Tenant{}, err
	}
	t, err := scanTenant(m.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE name = $1`, name))
	return t, m.notFound(ctx, err, "no such tenant", "the tenant cannot be read")
}

// ListTenants implements master.Store.
func (m *Master) ListTenants(ctx context.Context, page master.Page) ([]master.Tenant, string, error) {
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	after := ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listTenants, "", 1)
		if err != nil {
			return nil, "", err
		}
		after = f[0]
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	// Names are never empty, so "after the empty name" is the first page.
	rows, err := m.pool.Query(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE name > $1 ORDER BY name LIMIT $2`, after, page.Limit+1)
	out, err := collect(rows, err, scanTenant)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the tenants cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listTenants, "", last.Name), nil
}

// SetTenantState implements master.Store. The old state is read under a row
// lock and the new one written in one transaction.
func (m *Master) SetTenantState(ctx context.Context, id string, state master.TenantState, reason string, audit master.AuditEntry) error {
	if err := master.ValidateID("tenant id", id); err != nil {
		return err
	}
	if err := master.ValidateTenantState(state, reason); err != nil {
		return err
	}
	if err := audit.ValidateWith(master.DetailOldState, master.DetailNewState); err != nil {
		return err
	}
	return m.write(ctx, "the tenant's state cannot be set", func(tx pgx.Tx) error {
		// The row is locked as it is read, so the state in the audit entry
		// is the one this call replaced even when another call changes the
		// same tenant at once.
		var old string
		if err := tx.QueryRow(ctx, `SELECT state FROM tenants WHERE id = $1 FOR UPDATE`, id).Scan(&old); err != nil {
			return notFoundIn(err, "no such tenant")
		}
		tag, err := tx.Exec(ctx, `UPDATE tenants SET state = $1, state_reason = $2 WHERE id = $3`, string(state), reason, id)
		if err := oneRow(tag, err, "no such tenant"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, m.red, audit.WithStateChange(master.TenantState(old), state))
	})
}

// RemoveTenant implements master.Store.
func (m *Master) RemoveTenant(ctx context.Context, id string, audit master.AuditEntry) error {
	if err := master.ValidateID("tenant id", id); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the tenant cannot be removed", func(tx pgx.Tx) error {
		// The tenant's row is locked first. A SetGrant on this tenant holds
		// a share lock on it until it commits, so this waits for that grant
		// to exist and then removes it with the others; one that starts
		// after this finds no tenant. Without the lock a grant added between
		// the two deletes would make the second fail on the foreign key.
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = $1 FOR UPDATE`, id).Scan(&one); err != nil {
			return notFoundIn(err, "no such tenant")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM grants WHERE scope = $1`, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
		if err := oneRow(tag, err, "no such tenant"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, m.red, audit)
	})
}

// --- Keys ---

const keyColumns = `id, hash, holder, created, expires, last_used, revoked`

// scanKey reads one row of keyColumns.
func scanKey(row scanner) (master.Key, error) {
	var k master.Key
	var created time.Time
	var expires, lastUsed, revoked *time.Time
	err := row.Scan(&k.ID, &k.Hash, &k.Holder, &created, &expires, &lastUsed, &revoked)
	k.Created = created.UTC()
	k.Expires = fromNullMicros(expires)
	k.LastUsed = fromNullMicros(lastUsed)
	k.Revoked = fromNullMicros(revoked)
	return k, err
}

// CreateKey implements master.Store.
func (m *Master) CreateKey(ctx context.Context, k master.Key, audit master.AuditEntry) error {
	if err := k.Validate(); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the key cannot be created", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO api_keys (id, hash, holder, created, expires) VALUES ($1, $2, $3, $4, $5)`,
			k.ID, k.Hash, k.Holder, micros(k.Created), nullMicros(k.Expires))
		if isUniqueViolation(err) {
			return fault.Wrap(fault.Conflict, m.red.scrub(err), "a key with this id exists")
		}
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, m.red, audit)
	})
}

// Key implements master.Store: one read of the primary key's index.
func (m *Master) Key(ctx context.Context, id string) (master.Key, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return master.Key{}, err
	}
	if err := m.open(); err != nil {
		return master.Key{}, err
	}
	k, err := scanKey(m.pool.QueryRow(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE id = $1`, id))
	return k, m.notFound(ctx, err, "no such key", "the key cannot be read")
}

// ListKeys implements master.Store.
func (m *Master) ListKeys(ctx context.Context, page master.Page) ([]master.Key, string, error) {
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	// Creation times are never before 1970, so (-1, "") is before every key.
	afterCreated, afterID := int64(-1), ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listKeys, "", 2)
		if err != nil {
			return nil, "", err
		}
		if afterCreated, err = sqlkit.DecodeCursorInt(f[0]); err != nil {
			return nil, "", err
		}
		afterID = f[1]
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	rows, err := m.pool.Query(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE (created, id) > ($1, $2) ORDER BY created, id LIMIT $3`,
		microsTime(afterCreated), afterID, page.Limit+1)
	out, err := collect(rows, err, scanKey)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the keys cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listKeys, "", strconv.FormatInt(last.Created.UnixMicro(), 10), last.ID), nil
}

// MarkKeyUsed implements master.Store with one UPDATE whose WHERE holds the
// throttle, so a call within the minute matches no row, takes no row lock and
// writes nothing: no read first, and no commit to wait for. When two calls
// race on a row that does match, the second waits for the first's commit and
// then re-evaluates the WHERE against the row the first wrote (read
// committed), so it matches nothing: a busy key costs one write a minute.
func (m *Master) MarkKeyUsed(ctx context.Context, id string, at time.Time) (bool, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return false, err
	}
	if err := master.ValidateTime("time of use", at); err != nil {
		return false, err
	}
	if err := m.open(); err != nil {
		return false, err
	}
	tag, err := m.pool.Exec(ctx, `UPDATE api_keys SET last_used = $1 WHERE id = $2 AND (last_used IS NULL OR last_used < $3)`,
		micros(at), id, micros(at.Add(-time.Minute)))
	if err != nil {
		return false, m.fail(ctx, err, "the key's use cannot be recorded")
	}
	return tag.RowsAffected() == 1, nil
}

// RevokeKey implements master.Store. The key's row is locked as its revoked
// time is read (the lock SetGrant takes too), so the audit entry says whether
// this call revoked the key or found it revoked, and a SetGrant racing this
// call is ordered with it: it either finishes before the revocation or finds
// the key revoked.
func (m *Master) RevokeKey(ctx context.Context, id string, at time.Time, audit master.AuditEntry) (time.Time, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return time.Time{}, err
	}
	if err := master.ValidateTime("revocation time", at); err != nil {
		return time.Time{}, err
	}
	if err := audit.ValidateWith(master.DetailAlreadyRevoked); err != nil {
		return time.Time{}, err
	}
	var revoked time.Time
	err := m.write(ctx, "the key cannot be revoked", func(tx pgx.Tx) error {
		var before *time.Time
		if err := tx.QueryRow(ctx, `SELECT revoked FROM api_keys WHERE id = $1 FOR NO KEY UPDATE`, id).Scan(&before); err != nil {
			return notFoundIn(err, "no such key")
		}
		if before != nil {
			revoked = before.UTC()
			return insertAudit(ctx, tx, m.red, audit.WithRevocation(true))
		}
		if _, err := tx.Exec(ctx, `UPDATE api_keys SET revoked = $1 WHERE id = $2`, micros(at), id); err != nil {
			return err
		}
		revoked = micros(at)
		return insertAudit(ctx, tx, m.red, audit.WithRevocation(false))
	})
	if err != nil {
		return time.Time{}, err
	}
	return revoked, nil
}

// --- Grants ---

// SetGrant implements master.Store. A key's grants are changed one writer at
// a time: the key's row is locked first (FOR NO KEY UPDATE, which the foreign
// key checks of other grants do not conflict with), so two concurrent sets on
// one key are ordered, and the one statement below, which reads the role
// there was and writes the new one, sees what the other left even under read
// committed. Without the lock two sets of a grant that does not exist yet
// would both read "none". The key's lock also answers whether it exists.
func (m *Master) SetGrant(ctx context.Context, g master.Grant, audit master.AuditEntry) (master.Role, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	if err := audit.ValidateRoleChange(); err != nil {
		return "", err
	}
	var previous master.Role
	err := m.write(ctx, "the grant cannot be set", func(tx pgx.Tx) error {
		revoked, err := lockGrantTargets(ctx, tx, g.KeyID, g.Scope)
		if err != nil {
			return err
		}
		if revoked {
			// Decided under the key's row lock, which RevokeKey also takes,
			// so no grant is written after the key's revocation.
			return fault.New(fault.Conflict, "the key is revoked")
		}
		var old *string
		err = tx.QueryRow(ctx, `WITH previous AS (SELECT role FROM grants WHERE key_id = $1 AND scope = $2)
			INSERT INTO grants (key_id, scope, tenant_id, role) VALUES ($1, $2, $3, $4)
			ON CONFLICT (key_id, scope) DO UPDATE SET role = EXCLUDED.role
			RETURNING (SELECT role FROM previous)`,
			g.KeyID, g.Scope.TenantID, nullText(g.Scope.TenantID), string(g.Role)).Scan(&old)
		if isForeignKeyViolation(err) {
			// The tenant was removed between the check and the insert.
			return fault.Wrap(fault.NotFound, m.red.scrub(err), "no such tenant")
		}
		if err != nil {
			return err
		}
		if old != nil {
			previous = master.Role(*old)
		}
		return insertAudit(ctx, tx, m.red, audit.WithRoleChange(previous, g.Role))
	})
	if err != nil {
		return "", err
	}
	return previous, nil
}

// lockGrantTargets returns NotFound unless the key, and the tenant of a
// tenant scope, exist, reports whether the key is revoked, and locks them
// against the changes that would undo that before the transaction ends. The
// key is locked FOR NO KEY UPDATE, so grant changes and a revocation on it are
// ordered; the tenant is locked for key share, so RemoveTenant waits for the
// grant being made.
func lockGrantTargets(ctx context.Context, tx pgx.Tx, keyID string, scope master.Scope) (revoked bool, err error) {
	var at *time.Time
	if err := tx.QueryRow(ctx, `SELECT revoked FROM api_keys WHERE id = $1 FOR NO KEY UPDATE`, keyID).Scan(&at); err != nil {
		return false, notFoundIn(err, "no such key")
	}
	if !scope.IsInstallation() {
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = $1 FOR KEY SHARE`, scope.TenantID).Scan(&one); err != nil {
			return false, notFoundIn(err, "no such tenant")
		}
	}
	return at != nil, nil
}

// grantColumns are a grant's columns with its tenant's name, joined.
const grantColumns = `g.key_id, g.scope, g.role, coalesce(t.name, '')`

// grantFrom joins each grant to its tenant, for the name.
const grantFrom = ` FROM grants g LEFT JOIN tenants t ON t.id = g.tenant_id`

// scanGrant reads one row of grantColumns.
func scanGrant(row scanner) (master.Grant, error) {
	var g master.Grant
	var role string
	err := row.Scan(&g.KeyID, &g.Scope.TenantID, &role, &g.TenantName)
	g.Role = master.Role(role)
	return g, err
}

// GrantsByKey implements master.Store: one read of the primary key's index,
// whose order (key, scope) puts the installation's empty scope first.
func (m *Master) GrantsByKey(ctx context.Context, keyID string, page master.Page) ([]master.Grant, string, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	var rows pgx.Rows
	var err error
	if page.Cursor == "" {
		// The first page has its own statement: the installation's scope
		// is the empty string, so no "scope > $2" includes it.
		rows, err = m.pool.Query(ctx, `SELECT `+grantColumns+grantFrom+` WHERE g.key_id = $1 ORDER BY g.key_id, g.scope LIMIT $2`, keyID, page.Limit+1)
	} else {
		f, derr := sqlkit.DecodeCursor(page.Cursor, listGrantsByKey, keyID, 1)
		if derr != nil {
			return nil, "", derr
		}
		rows, err = m.pool.Query(ctx, `SELECT `+grantColumns+grantFrom+` WHERE g.key_id = $1 AND g.scope > $2 ORDER BY g.key_id, g.scope LIMIT $3`, keyID, f[0], page.Limit+1)
	}
	out, err := collect(rows, err, scanGrant)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the key's grants cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listGrantsByKey, keyID, last.Scope.TenantID), nil
}

// scopeBinding is what a GrantsByScope cursor is bound to. The installation
// and a tenant can never give the same text, since a tenant id is not empty.
func scopeBinding(scope master.Scope) string {
	return "scope:" + scope.TenantID
}

// GrantsByScope implements master.Store. The cursor is bound to the scope.
func (m *Master) GrantsByScope(ctx context.Context, scope master.Scope, page master.Page) ([]master.Grant, string, error) {
	if err := scope.Validate(); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	after := ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listGrantsByScope, scopeBinding(scope), 1)
		if err != nil {
			return nil, "", err
		}
		after = f[0]
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	// Key ids are never empty, so "after the empty id" is the first page.
	rows, err := m.pool.Query(ctx, `SELECT `+grantColumns+grantFrom+` WHERE g.scope = $1 AND g.key_id > $2 ORDER BY g.scope, g.key_id LIMIT $3`,
		scope.TenantID, after, page.Limit+1)
	out, err := collect(rows, err, scanGrant)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the grants cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listGrantsByScope, scopeBinding(scope), last.KeyID), nil
}

// RolesFor implements master.Store with one statement (rolesForSQL). A name
// that is not a valid tenant name is asked as the empty name, which no
// tenant has, so the statement runs all the same and finds no tenant; it is
// never sent as it was, since Postgres refuses text that is not UTF-8 or
// holds a NUL.
func (m *Master) RolesFor(ctx context.Context, keyID, tenantName string) (master.Roles, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return master.Roles{}, err
	}
	if master.ValidateTenantName(tenantName) != nil {
		tenantName = ""
	}
	if err := m.open(); err != nil {
		return master.Roles{}, err
	}
	var tenantID, inst, ten *string
	err := m.pool.QueryRow(ctx, rolesForSQL, keyID, tenantName).Scan(&tenantID, &inst, &ten)
	if err != nil {
		return master.Roles{}, m.fail(ctx, err, "the key's roles cannot be read")
	}
	var r master.Roles
	if tenantID != nil {
		r.TenantID, r.TenantFound = *tenantID, true
	}
	if inst != nil {
		r.Installation = master.Role(*inst)
	}
	if ten != nil {
		r.Tenant = master.Role(*ten)
	}
	return r, nil
}

// rolesForSQL is RolesFor's statement, which always returns exactly one
// row: the tenant's id, NULL when no tenant has the name (the LEFT JOIN of a
// one-row table keeps the row), then the key's role on the installation and
// on that tenant, each NULL when there is no grant. The second grant
// lookup runs whether or not a tenant was found: without one it looks for
// the scope '*', which no id can be, so a missing tenant costs the same
// lookups as a tenant without a grant. Each is an exact lookup:
// the tenants' unique index on name, and the grants' primary key at (key,
// the empty scope) and at (key, the tenant's id), however many grants the key has.
const rolesForSQL = `SELECT t.id,
	(SELECT role FROM grants WHERE key_id = $1 AND scope = ''),
	(SELECT role FROM grants WHERE key_id = $1 AND scope = coalesce(t.id, '*'))
	FROM (SELECT 1) AS one LEFT JOIN tenants AS t ON t.name = $2`

// RemoveGrant implements master.Store. The key's row is locked as SetGrant
// does, so the role it reports is the one the grant had when it was removed;
// one DELETE ... RETURNING then removes the grant and gives that role. A
// key or a tenant that does not exist is NotFound; a grant that does not
// exist, on a key and a tenant that do, is success.
func (m *Master) RemoveGrant(ctx context.Context, keyID string, scope master.Scope, audit master.AuditEntry) (master.Role, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return "", err
	}
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if err := audit.ValidateRoleChange(); err != nil {
		return "", err
	}
	var previous master.Role
	err := m.write(ctx, "the grant cannot be removed", func(tx pgx.Tx) error {
		if _, err := lockGrantTargets(ctx, tx, keyID, scope); err != nil {
			return err
		}
		var old string
		err := tx.QueryRow(ctx, `DELETE FROM grants WHERE key_id = $1 AND scope = $2 RETURNING role`, keyID, scope.TenantID).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		previous = master.Role(old)
		return insertAudit(ctx, tx, m.red, audit.WithRoleChange(previous, ""))
	})
	if err != nil {
		return "", err
	}
	return previous, nil
}

// --- Settings ---

// Setting implements master.Store.
func (m *Master) Setting(ctx context.Context, name string) (string, error) {
	if err := master.ValidateSettingName(name); err != nil {
		return "", err
	}
	if err := m.open(); err != nil {
		return "", err
	}
	var value string
	err := m.pool.QueryRow(ctx, `SELECT value FROM settings WHERE name = $1`, name).Scan(&value)
	return value, m.notFound(ctx, err, "no such setting", "the setting cannot be read")
}

// SetSetting implements master.Store.
func (m *Master) SetSetting(ctx context.Context, name, value string, audit master.AuditEntry) error {
	if err := master.ValidateSettingName(name); err != nil {
		return err
	}
	if err := master.ValidateSettingValue(value); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the setting cannot be set", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO settings (name, value) VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`, name, value); err != nil {
			return err
		}
		return insertAudit(ctx, tx, m.red, audit)
	})
}

// UnsetSetting implements master.Store.
func (m *Master) UnsetSetting(ctx context.Context, name string, audit master.AuditEntry) (bool, error) {
	if err := master.ValidateSettingName(name); err != nil {
		return false, err
	}
	if err := audit.Validate(); err != nil {
		return false, err
	}
	var removed bool
	err := m.write(ctx, "the setting cannot be unset", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM settings WHERE name = $1`, name)
		if err != nil {
			return err
		}
		removed = tag.RowsAffected() == 1
		return insertAudit(ctx, tx, m.red, audit)
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// ListSettings implements master.Store.
func (m *Master) ListSettings(ctx context.Context, page master.Page) ([]master.Setting, string, error) {
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	after := ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listSettings, "", 1)
		if err != nil {
			return nil, "", err
		}
		after = f[0]
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	rows, err := m.pool.Query(ctx, `SELECT name, value FROM settings WHERE name > $1 ORDER BY name LIMIT $2`, after, page.Limit+1)
	out, err := collect(rows, err, func(row scanner) (master.Setting, error) {
		var st master.Setting
		err := row.Scan(&st.Name, &st.Value)
		return st, err
	})
	if err != nil {
		return nil, "", m.fail(ctx, err, "the settings cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listSettings, "", last.Name), nil
}

// --- Audit ---

// AppendAudit implements master.Store. The batch is one INSERT, all or
// nothing, in a transaction of its own like every other write: a bare INSERT
// whose context ends while the server answers could return an error for rows
// that were stored, and a call that fails must have stored nothing.
func (m *Master) AppendAudit(ctx context.Context, entries ...master.AuditEntry) error {
	if len(entries) > master.MaxAuditBatch {
		return fault.New(fault.Invalid, "at most "+strconv.Itoa(master.MaxAuditBatch)+" audit entries are appended at once")
	}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	if len(entries) == 0 {
		return m.open()
	}
	if err := m.open(); err != nil {
		return err
	}
	return m.write(ctx, "the audit entries cannot be written", func(tx pgx.Tx) error {
		return execAudit(ctx, tx, m.red, entries)
	})
}

const auditColumns = `id, at, key_id, local_user, remote_addr, method, tenant_name, object, outcome, reason, details, request_id`

// insertAudit writes one audit entry inside the transaction of the change it
// records.
func insertAudit(ctx context.Context, tx pgx.Tx, red *redactor, e master.AuditEntry) error {
	return execAudit(ctx, tx, red, []master.AuditEntry{e})
}

// execer is what execAudit needs of a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// execAudit writes entries with one statement: a VALUES list for the one
// entry every request ends with, and unnest over twelve arrays for a batch,
// so a batch of 10,000 is still twelve parameters and one round trip. An id
// already taken is Conflict.
func execAudit(ctx context.Context, q execer, red *redactor, entries []master.AuditEntry) error {
	var err error
	if len(entries) == 1 {
		e := entries[0]
		var details string
		if details, err = encodeDetails(e.Details); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO audit (`+auditColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			e.ID, micros(e.Time), e.KeyID, e.LocalUser, e.RemoteAddress, e.Method, e.TenantName, e.Object,
			string(e.Outcome), e.Reason, details, e.RequestID)
	} else {
		n := len(entries)
		col := func() []string { return make([]string, 0, n) }
		ids, keys, users, addrs, methods, tenants, objects, outcomes, reasons, dets, reqs := col(), col(), col(), col(), col(), col(), col(), col(), col(), col(), col()
		times := make([]time.Time, 0, n)
		for _, e := range entries {
			var details string
			if details, err = encodeDetails(e.Details); err != nil {
				return err
			}
			ids, times, keys = append(ids, e.ID), append(times, micros(e.Time)), append(keys, e.KeyID)
			users, addrs, methods = append(users, e.LocalUser), append(addrs, e.RemoteAddress), append(methods, e.Method)
			tenants, objects, outcomes = append(tenants, e.TenantName), append(objects, e.Object), append(outcomes, string(e.Outcome))
			reasons, dets, reqs = append(reasons, e.Reason), append(dets, details), append(reqs, e.RequestID)
		}
		_, err = q.Exec(ctx, `INSERT INTO audit (`+auditColumns+`)
			SELECT id, at, key_id, local_user, remote_addr, method, tenant_name, object, outcome, reason, details::jsonb, request_id
			FROM unnest($1::text[], $2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::text[])
			AS t(id, at, key_id, local_user, remote_addr, method, tenant_name, object, outcome, reason, details, request_id)`,
			ids, times, keys, users, addrs, methods, tenants, objects, outcomes, reasons, dets, reqs)
	}
	if constraintViolation(err, "audit_pkey") {
		// The audit table's only unique key is the id: a duplicate id is
		// master.ErrDuplicateAuditID, never a Conflict.
		return fault.Wrap(fault.Internal, errors.Join(master.ErrDuplicateAuditID, red.scrub(err)), "an audit entry with this id exists")
	}
	return err
}

// scanAudit reads one row of auditColumns.
func scanAudit(row scanner) (master.AuditEntry, error) {
	var e master.AuditEntry
	var at time.Time
	var outcome, details string
	if err := row.Scan(&e.ID, &at, &e.KeyID, &e.LocalUser, &e.RemoteAddress, &e.Method,
		&e.TenantName, &e.Object, &outcome, &e.Reason, &details, &e.RequestID); err != nil {
		return e, err
	}
	e.Time = at.UTC()
	e.Outcome = master.Outcome(outcome)
	var err error
	e.Details, err = decodeDetails(details)
	return e, err
}

// auditBinding is what a ListAudit cursor is bound to: every field of the
// filter, so a cursor made with one filter is refused with another.
func auditBinding(f master.AuditFilter) string {
	t := func(x time.Time) string {
		if x.IsZero() {
			return ""
		}
		return strconv.FormatInt(x.UnixMicro(), 10)
	}
	return sqlkit.Binding(f.TenantName, f.KeyID, string(f.Outcome), t(f.Since), t(f.Until))
}

// auditPosition is the (at, id) of the last row of the previous page.
type auditPosition struct {
	at time.Time
	id string
}

// auditQuery returns the statement and arguments of one page of ListAudit.
// The statement is put together from fixed fragments according to which
// filters are set, so that each combination reads the index named for it in
// 0001_init.sql instead of testing "parameter is NULL OR column = parameter",
// which no index answers. No value is ever written into the text; every one
// is a parameter, and pgx's statement cache keeps one prepared statement per
// combination.
func auditQuery(filter master.AuditFilter, after *auditPosition, limit int) (string, []any) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if filter.TenantName != "" {
		where = append(where, "tenant_name = "+arg(filter.TenantName))
	}
	if filter.KeyID != "" {
		where = append(where, "key_id = "+arg(filter.KeyID))
	}
	if filter.Outcome != "" {
		where = append(where, "outcome = "+arg(string(filter.Outcome)))
	}
	if !filter.Since.IsZero() {
		where = append(where, "at >= "+arg(micros(filter.Since)))
	}
	if !filter.Until.IsZero() {
		where = append(where, "at < "+arg(micros(filter.Until)))
	}
	if after != nil {
		// Newest first, so the next page is what sorts before the last row.
		where = append(where, "(at, id) < ("+arg(after.at)+", "+arg(after.id)+")")
	}
	query := "SELECT " + auditColumns + " FROM audit"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- every fragment is a constant above; values are parameters.
	}
	// The id breaks ties between entries of the same time, so the order is
	// total and a cursor at (at, id) is unambiguous.
	query += " ORDER BY at DESC, id DESC LIMIT " + arg(limit)
	return query, args
}

// ListAudit implements master.Store. The cursor is bound to the whole filter. The statement is put together from
// fixed fragments according to which filters are set, so that each
// combination reads the index named for it in 0001_init.sql instead of
// testing "parameter is NULL OR column = parameter", which no index answers.
// No value is ever written into the text; every one is a parameter, and
// pgx's statement cache keeps one prepared statement per combination.
func (m *Master) ListAudit(ctx context.Context, filter master.AuditFilter, page master.Page) ([]master.AuditEntry, string, error) {
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	binding := auditBinding(filter)
	var after *auditPosition
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listAudit, binding, 2)
		if err != nil {
			return nil, "", err
		}
		at, err := sqlkit.DecodeCursorInt(f[0])
		if err != nil {
			return nil, "", err
		}
		after = &auditPosition{at: microsTime(at), id: f[1]}
	}
	query, args := auditQuery(filter, after, page.Limit+1)

	if err := m.open(); err != nil {
		return nil, "", err
	}
	rows, err := m.pool.Query(ctx, query, args...)
	out, err := collect(rows, err, scanAudit)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the audit log cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listAudit, binding, strconv.FormatInt(last.Time.UnixMicro(), 10), last.ID), nil
}

// --- Helpers ---

// micros returns t cut to a whole microsecond, in UTC. Postgres keeps
// timestamptz to the microsecond; what is finer is dropped here, the same
// way, so what is written is exactly what is read back (master package
// documentation). master.ValidateTime keeps t after 1970, where dropping is
// the same as rounding down.
func micros(t time.Time) time.Time {
	return microsTime(t.UnixMicro())
}

// microsTime is the time v microseconds after 1970, in UTC.
func microsTime(v int64) time.Time {
	return time.UnixMicro(v).UTC()
}

// nullMicros is micros, with NULL for the zero time ("never").
func nullMicros(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return micros(t)
}

// fromNullMicros reads a nullable timestamptz, with the zero time for NULL,
// in UTC.
func fromNullMicros(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// nullText returns NULL for the empty string, for the grants' tenant_id.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// encodeDetails writes an audit entry's details as a JSON object, which the
// column (jsonb) stores and returns whole. encoding/json sorts the keys, so
// the text sent does not depend on the map's order.
func encodeDetails(d map[string]string) (string, error) {
	if len(d) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", fault.Wrap(fault.Internal, err, "the audit entry's details cannot be written")
	}
	return string(b), nil
}

// decodeDetails reads what the column holds; no details is nil.
func decodeDetails(s string) (map[string]string, error) {
	var d map[string]string
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return nil, fault.Wrap(fault.Internal, err, "an audit entry's details cannot be read")
	}
	if len(d) == 0 {
		return nil, nil
	}
	return d, nil
}

// notFound turns pgx.ErrNoRows into NotFound with what, and classifies any
// other error with message.
func (m *Master) notFound(ctx context.Context, err error, what, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.NotFound, what)
	}
	return m.fail(ctx, err, message)
}

// notFoundIn turns pgx.ErrNoRows into NotFound with what and passes any
// other error through for write to classify.
func notFoundIn(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.NotFound, what)
	}
	return err
}

// oneRow returns NotFound with what unless the statement changed a row.
func oneRow(tag pgconn.CommandTag, err error, what string) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fault.New(fault.NotFound, what)
	}
	return nil
}

// collect reads every row of a query with scan, closing the rows.
func collect[T any](rows pgx.Rows, err error, scan func(scanner) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (T, error) { return scan(row) })
}

// cut trims a page read with limit+1 rows to limit, and reports whether
// there was more and which row is the last one returned.
func cut[T any](rows []T, limit int) ([]T, T, bool) {
	var zero T
	if len(rows) <= limit {
		return rows, zero, false
	}
	rows = rows[:limit]
	return rows, rows[limit-1], true
}
