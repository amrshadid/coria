// Package postgres is the Postgres database type: it implements master.Store
// on a Postgres database through github.com/jackc/pgx/v5 and its pool, pure
// Go, so the binary stays static (CGO_ENABLED=0) on every target it ships
// for.
//
// # What it needs from the server
//
// The address is a postgres:// (or postgresql://) URL. The driver creates
// and upgrades its tables in the schema the connection's search_path starts
// with, which is "public" unless the URL says otherwise
// (?search_path=magnetar). It never creates a database, a schema or a role,
// and works with a login that owns that schema and nothing else. It is
// tested on Postgres 16 and 18; pgx supports 14 and later.
//
// Whether the connection is encrypted is the URL's to say: pgx's default,
// sslmode=prefer, encrypts when the server offers it and falls back to plain
// text when it does not, so an address that must never go in the clear says
// sslmode=verify-full.
//
// # How it reaches the database
//
// One pgxpool. Every changing operation and its audit entry are one
// transaction (read committed). A statement is prepared on a connection the
// first time it is used there and kept in pgx's statement cache
// (QueryExecModeCacheStatement, the default, 512 statements per connection),
// so the reads of every request are one round trip each after the first. A
// proxy that pools by transaction, such as PgBouncer before its prepared
// statement support, cannot keep a prepared statement from one call to the
// next; for it, give the URL default_query_exec_mode=exec, which pgx reads.
//
// # The migration lock
//
// Migrate takes a session advisory lock, named for the schema, on one
// dedicated connection and holds it until the migration ends, so a second
// process that starts migrating waits for the first and then finds the files
// applied. The lock is released on every path; when its release cannot be
// confirmed the connection is closed, which ends the session and with it the
// lock. The pool therefore needs two connections.
//
// # Ordering
//
// Text that is compared or ordered is COLLATE "C" in the schema, so names
// order byte by byte whatever collation the database was created with.
//
// # What this package must never do
//
// It must never be imported by anything but package driver and its own
// tests: nothing else may know that a database is Postgres (a lint rule
// holds it). It never puts the address, the password or the host in an
// error, a log line or an audit entry: pgx's own errors and connection
// strings can carry them, so every error from pgx is scrubbed of them before
// it joins the chain.
package postgres
