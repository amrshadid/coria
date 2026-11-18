// Package postgres is the Postgres database type: it implements master.Store
// and tenant.Store on a Postgres database through github.com/jackc/pgx/v5 and its pool, pure
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
// The driver is closed by default. pgx's default, sslmode=prefer, encrypts
// when the server offers it and falls back to plain text when it does not, so
// an address whose host is not loopback and not a Unix socket path is refused
// at open, as Invalid, unless a person wrote its sslmode down as verify-full,
// verify-ca, require or disable (the error says to add sslmode=verify-full
// with sslrootcert, or sslmode=disable if the network is trusted). Loopback
// and socket addresses keep pgx's default.
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
// # One database for each tenant
//
// A tenant has the address of a database of its own. When two tenants are
// pointed at one database and schema they would share one set of settings and
// one audit log, each seeing and changing the other's, so the driver refuses
// it: the tenant's schema records the id of the tenant it was made for
// (table tenant_identity, written by the first Migrate and compared by every
// open and every Migrate), and a tenant whose id differs is refused as
// Invalid. That also catches an address pasted for the wrong tenant. Two
// tenants may share a database if each has a schema of its own
// (?search_path=lab); a master and a tenant may not share a schema, and their
// migrations differ, so that is refused as well.
//
// A tenant's pool is small, four connections by default with idle ones let go
// after a minute (DefaultTenantConnections, TenantIdleTime), because a server
// holds many tenants and each connection is a process on the database server;
// pool_max_conns and pool_max_conn_idle_time in the address change them.
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
// # Row locks
//
// A change of the master locks the rows it depends on, under read committed,
// in one order: keys' rows first, several in the order of their ids
// (lockKeys), then tenants' rows, then the row it changes. The one exception
// is a setting's change, which checks its actor last, after it locked the
// setting's row; no change locks a key's row and then a setting's, so that
// order closes no cycle. The check of a change's actor holds its key's row
// for share, and the tenant's row when its role is on a tenant, for key
// share or for the stronger lock the change itself will take on that row, so
// no transaction asks for a stronger lock on a row it already holds; a
// revocation, a change of its grants or a removal of the tenant waits for the
// change or is waited for. The check takes its locks first and reads the key
// and its grant after, in a statement of its own: under read committed a
// statement sees what was committed when it began, and a locking statement
// that waited re-reads only the row it locked, and only if that row changed,
// which a change of grants (it locks the key's row and changes another
// table) does not do. Taken in one order, these
// locks cannot wait on each other in a cycle, which Postgres would break by
// failing one of the changes as a deadlock.
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
