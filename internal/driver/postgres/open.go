package postgres

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amrshadid/magnetar/internal/fault"
)

// minConnections is the fewest connections the pool may have. Migrate holds
// the advisory lock on one connection while the runner works on another, so
// a pool of one would wait on itself.
const minConnections = 2

// openPool parses address, checks it is a postgres:// URL, opens a pool of
// connections to it and checks that the database answers. It returns the
// redactor that scrubs this address out of every later error.
//
// No error from here repeats the address: pgx's parse errors quote the URL
// (Go's own url.Error holds it whole, password included, and pgx's redaction
// does not reach inside it), so a parse failure is reported with no cause at
// all.
func openPool(ctx context.Context, address string) (*pgxpool.Pool, *redactor, error) {
	lower := strings.ToLower(address)
	if !strings.HasPrefix(lower, "postgres://") && !strings.HasPrefix(lower, "postgresql://") {
		return nil, nil, fault.New(fault.Invalid, "a Postgres database's address is a postgres:// URL")
	}
	cfg, err := pgxpool.ParseConfig(address)
	if err != nil {
		return nil, nil, fault.New(fault.Invalid, "the Postgres address is not a valid postgres:// URL")
	}
	red := newRedactor(address, cfg.ConnConfig)

	if cfg.MaxConns < minConnections {
		cfg.MaxConns = minConnections
	}
	// Set only when the URL did not, so an operator's choice stands. The
	// application name shows in pg_stat_activity, which is how an operator
	// finds the connections of this program; UTC makes the times a psql
	// session shows for these tables unsurprising.
	if _, ok := cfg.ConnConfig.RuntimeParams["application_name"]; !ok {
		cfg.ConnConfig.RuntimeParams["application_name"] = "magnetar"
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["timezone"]; !ok {
		cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, nil, classify(red, err, "the master database cannot be opened")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, classify(red, err, "the master database cannot be opened")
	}
	return pool, red, nil
}

// redactor removes an address and what is in it from error text.
type redactor struct {
	secrets []string
}

// newRedactor collects what in the address must never appear in an error:
// the address itself, the password, the user, the hosts and the database
// name, each also in its URL-escaped form.
func newRedactor(address string, cfg *pgx.ConnConfig) *redactor {
	var all []string
	add := func(s string) {
		if s == "" {
			return
		}
		all = append(all, s, url.QueryEscape(s), url.PathEscape(s))
	}
	add(address)
	if cfg != nil {
		add(cfg.Password)
		add(cfg.User)
		add(cfg.Host)
		add(cfg.Database)
		for _, fb := range cfg.Fallbacks {
			add(fb.Host)
		}
	}
	// Longest first, so the whole address goes before the pieces of it.
	sort.Slice(all, func(i, j int) bool { return len(all[i]) > len(all[j]) })
	return &redactor{secrets: all}
}

// text returns s with every secret replaced.
func (r *redactor) text(s string) string {
	if r == nil {
		return s
	}
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}

// scrubbed is an error whose text has had the address taken out. It keeps the
// original in its chain, for errors.Is and errors.As (a canceled context, a
// PgError's code), but never prints it.
type scrubbed struct {
	text  string
	cause error
}

func (e *scrubbed) Error() string { return e.text }
func (e *scrubbed) Unwrap() error { return e.cause }

// scrub returns err with its text scrubbed, or nil for nil.
func (r *redactor) scrub(err error) error {
	if err == nil {
		return nil
	}
	return &scrubbed{text: r.text(err.Error()), cause: err}
}

// SQLSTATE classes and codes (appendix A of the Postgres manual) that mean
// "the database cannot be used now", so a request may be tried again later.
const (
	sqlstateUndefinedTable  = "42P01"
	sqlstateUniqueViolation = "23505"
	sqlstateForeignKey      = "23503"
	sqlstateLockNotAvail    = "55P03"
)

// unavailableClasses are the two-character SQLSTATE classes of failures that
// are about the server, not the request: connection exceptions (08), invalid
// authorization (28), invalid catalog name (3D), insufficient resources (53),
// transaction rollback, serialization failures and deadlocks (40), operator
// intervention, shutdown and cancel (57) and system errors (58).
var unavailableClasses = map[string]bool{"08": true, "28": true, "3D": true, "53": true, "40": true, "57": true, "58": true}

// classify turns an error from pgx into a fault with a kind, from the error
// code and never from the text of its message. An error that already has a
// kind keeps it. message is what the caller is shown; the driver's own text,
// scrubbed of the address, stays in the chain for the log.
func classify(red *redactor, err error, message string) error {
	if err == nil {
		return nil
	}
	var fe *fault.Error
	if errors.As(err, &fe) {
		return err
	}
	cause := red.scrub(err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fault.Wrap(fault.Unavailable, cause, message)
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch {
		case pe.Code == sqlstateUndefinedTable:
			return fault.Wrap(fault.Unavailable, cause, "the master database is not ready; has it been migrated?")
		case pe.Code == sqlstateLockNotAvail, len(pe.Code) >= 2 && unavailableClasses[pe.Code[:2]]:
			return fault.Wrap(fault.Unavailable, cause, message)
		}
		return fault.Wrap(fault.Internal, cause, message)
	}
	var ne net.Error
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) || errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.Timeout(err) || pgconn.SafeToRetry(err) {
		return fault.Wrap(fault.Unavailable, cause, message)
	}
	return fault.Wrap(fault.Internal, cause, message)
}

// constraintViolation reports whether err is Postgres refusing a row for
// breaking the unique or primary key constraint named name, found by
// SQLSTATE 23505 and the constraint's name, never by the message.
func constraintViolation(err error, name string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == sqlstateUniqueViolation && pe.ConstraintName == name
}

// isUniqueViolation reports whether err is any unique or primary key
// constraint failing.
func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == sqlstateUniqueViolation
}

// isForeignKeyViolation reports whether err is a foreign key constraint
// failing.
func isForeignKeyViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == sqlstateForeignKey
}
