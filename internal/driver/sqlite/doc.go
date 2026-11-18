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
// SQLITE_BUSY, and across processes the write lock is taken at the start of
// a transaction, where the busy timeout applies, rather than on its first
// write, where SQLite fails at once (research R6, databases.md section 1).
// The read pool has a few connections that cannot write (query_only), and
// in write-ahead mode they read a snapshot while the writer works.
//
// The migration lock is BEGIN IMMEDIATE too: a second process that starts
// migrating waits for the first, under the busy timeout, then finds the files
// applied.
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
