// Package sqlite is the SQLite database type: it implements master.Store and
// tenant.Store, each on a SQLite file of its own, through modernc.org/sqlite,
// which is SQLite translated to Go, so the binary stays pure Go and static
// (CGO_ENABLED=0) on every target it ships for, linux/386 and windows/amd64
// included.
//
// # How it reaches the file
//
// Two pools of connections to one file. The write pool has exactly one
// connection, and its transactions begin with BEGIN IMMEDIATE: inside the
// process, writers queue for that connection instead of failing with
// SQLITE_BUSY, and across processes the write lock is taken at the start of a
// transaction, where the busy timeout applies, rather than on its first
// write, where SQLite fails at once without waiting
// (sqlite.org/lang_transaction.html). The read pool has a few connections
// that cannot write (query_only), and in write-ahead mode they read a
// snapshot while the writer works.
//
// The migration lock is BEGIN IMMEDIATE too: a second process that starts
// migrating waits for the first, under the busy timeout, then finds the files
// applied.
//
// # One process writes a SQLite database
//
// Inside one process a database is one store with one writing connection,
// and writers queue for it in the order they came. A second process with
// its own store on the same file is safe for the data: SQLite's locking lets
// one writer at a time commit, and every change is whole or absent
// (sqlite.org/lockingv3.html, sqlite.org/wal.html). It is not fair. A writer
// that finds the file locked by another process sleeps and tries again, in
// steps that grow to 100 milliseconds, until the busy timeout
// (sqlite.org/c3ref/busy_handler.html, sqlite.org/c3ref/busy_timeout.html);
// a process that keeps writing gives the lock up only for the moment between
// two of its transactions, which the sleeper rarely catches. So a writer in
// one process can wait for as long as another keeps writing, and fails with
// the busy error once that passes the busy timeout. Measured: a store whose
// first write began while another store on the same file made 200 writes
// back to back waited until all 200 were done (1.05 s for 1.03 s of writes).
//
// That is why one server serves a data directory: the server's lock on the
// directory refuses a second one before it opens any database, and the
// server runs every migration itself at start. A tool that writes a SQLite
// database while the server runs, or several servers on one database, is not
// supported. Postgres is the database for anything that needs several
// writing processes.
//
// A SQLite database must be on a local disk. Its write-ahead log does not
// work on a network file system (sqlite.org/wal.html), and that cannot be
// detected reliably, so it is documented, not enforced.
//
// # What this package must never do
//
// It must never be imported by anything but package driver and its own
// tests: nothing else may know that a database is SQLite (a lint rule holds
// it). It never puts the file's path in an error, a log line or an audit
// entry.
package sqlite
