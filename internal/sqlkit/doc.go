// Package sqlkit holds what the SQL database drivers share: the migration
// runner, and the opaque cursors their lists are paged by.
//
// # Migrations
//
// A driver embeds its schema as forward-only SQL files named
// 0001_<what>.sql, 0002_<what>.sql and so on, numbered from 1 with no gap.
// Migrate applies the ones a database lacks, in order, each in its own
// transaction begun under a lock that holds across processes (the driver's
// Dialect says how its database takes that lock), and records each applied
// file with its SHA-256. Before it applies anything it refuses a database
// whose recorded checksum for a file no longer matches the file, or that
// records a version this binary has no file for: either means the schema is
// not what this binary's queries were written against, and going on would
// corrupt data or fail later in a place harder to understand.
//
// A file whose first line is exactly
//
//	-- magnetar:no-transaction
//
// runs outside a transaction, for a statement a database refuses inside one
// (Postgres's CREATE INDEX CONCURRENTLY). Such a file is recorded only after
// it has run, so it must be written to be run again safely. None is needed
// in M0.
//
// # What this package must never do
//
// It must never import a database driver: a driver passes in its *sql.DB and
// its Dialect, so this package compiles into a binary with no SQL database at
// all. It knows nothing of tenants, keys or any other table of the archive.
// It never puts a database address in an error.
package sqlkit
