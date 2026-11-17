package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/sqlkit"
)

// busyTimeout is how long a connection waits for a lock another process
// holds before it fails with SQLITE_BUSY: another magnetar starting and
// migrating, a backup made with the sqlite3 tool, a checkpoint of the
// write-ahead log. Inside the process writers never meet it, since they
// queue for the one write connection. modernc.org/sqlite returns
// SQLITE_BUSY once this passes, where the cgo driver waited longer, and
// Gitea raised its timeout after switching to it (research R6), so it is set
// well above a moment.
const busyTimeout = 10 * time.Second

// readConnections is the size of the read pool. The master is small and its
// reads are single indexed lookups, so a few connections serve many
// concurrent requests; each one holds a page cache, so not more.
const readConnections = 4

// pools are the two pools of connections to one SQLite file.
type pools struct {
	write *sql.DB
	read  *sql.DB
}

// openPools opens the write and read pools on the file at path, creating it
// when it does not exist, and checks that it is in write-ahead mode.
func openPools(ctx context.Context, path string) (*pools, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	if err := createPrivate(path); err != nil {
		return nil, err
	}

	// Every connection, read or write, gets these. modernc.org/sqlite
	// applies each key as a PRAGMA on every new connection, busy_timeout
	// first (driver.go and sqlite.go in v1.62.0), so they hold for every
	// connection a pool opens, not only the first.
	common := url.Values{}
	// busy_timeout: see busyTimeout.
	common.Set("_busy_timeout", strconv.FormatInt(busyTimeout.Milliseconds(), 10))
	// foreign_keys: SQLite leaves them off on each new connection unless
	// asked, and a grant pointing at a removed tenant must be refused.
	common.Set("_foreign_keys", "on")
	// synchronous=FULL: in write-ahead mode NORMAL is safe from corruption
	// but "a transaction committed ... might roll back following a power
	// loss" (sqlite.org/pragma.html). The master's rows are few and writes
	// rare, and a revoked key that comes back after a power cut is a
	// security failure, so every commit is synced (research R6).
	common.Set("_synchronous", "FULL")
	// fullfsync: on macOS fsync() does not reach the disk's own cache, and
	// only fcntl(F_FULLFSYNC) does, which SQLite uses only when asked
	// (sqlite.org/pragma.html#pragma_fullfsync). Without it FULL above would
	// not keep its promise there. Other systems ignore it. The checkpoint
	// copies the log into the database, so it is synced the same way.
	common.Add("_pragma", "fullfsync = 1")
	common.Add("_pragma", "checkpoint_fullfsync = 1")

	w := cloneValues(common)
	// journal_mode=WAL: readers do not block the writer nor the writer the
	// readers (sqlite.org/wal.html). The mode is kept in the file, so
	// setting it on the write connection sets it for every connection.
	w.Set("_journal_mode", "WAL")
	// _txlock=immediate: every transaction on this pool begins with BEGIN
	// IMMEDIATE, taking the write lock where the busy timeout applies; a
	// deferred transaction that reads first and writes later fails at once
	// with SQLITE_BUSY when another process wrote meanwhile
	// (sqlite.org/lang_transaction.html).
	w.Set("_txlock", "immediate")

	r := cloneValues(common)
	// query_only: a read connection that tries to write fails, so a write
	// can never bypass the one write connection and its lock order.
	r.Set("_query_only", "on")

	write, err := sql.Open("sqlite", path+"?"+w.Encode())
	if err != nil {
		return nil, fault.Wrap(fault.Internal, err, "the SQLite driver cannot be set up")
	}
	// One connection writes; database/sql queues the others for it. It is
	// kept open forever, so its prepared statements are never lost.
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	write.SetConnMaxIdleTime(0)

	mode, err := journalMode(ctx, write)
	if err != nil {
		_ = write.Close() // The open failure is the one reported.
		return nil, classify(err, "the master database cannot be opened")
	}
	if !strings.EqualFold(mode, "wal") {
		_ = write.Close() // The mode is the failure reported.
		return nil, fault.New(fault.Unavailable, "the master database cannot use write-ahead mode; is it on a local disk?")
	}

	read, err := sql.Open("sqlite", path+"?"+r.Encode())
	if err != nil {
		_ = write.Close() // The open failure is the one reported.
		return nil, fault.Wrap(fault.Internal, err, "the SQLite driver cannot be set up")
	}
	read.SetMaxOpenConns(readConnections)
	read.SetMaxIdleConns(readConnections)
	read.SetConnMaxLifetime(0)
	read.SetConnMaxIdleTime(0)
	if err := read.PingContext(ctx); err != nil {
		_ = write.Close() // The ping failure is the one reported.
		_ = read.Close()
		return nil, classify(err, "the master database cannot be opened")
	}
	return &pools{write: write, read: read}, nil
}

// journalMode opens the first write connection, which switches the file to
// write-ahead mode, and returns the mode the file is then in.
//
// Several processes opening one new file at the same moment each try that
// switch, which needs the file to themselves, and SQLite can answer
// SQLITE_BUSY at once, without waiting the busy timeout, to the ones that
// lose (seen in TestMigrateAcrossProcesses: one process in a few rounds).
// So a busy open is tried again, for as long as the busy timeout would have
// waited.
func journalMode(ctx context.Context, write *sql.DB) (string, error) {
	deadline := time.Now().Add(busyTimeout)
	for {
		var mode string
		err := write.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return mode, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// isBusy reports whether err is SQLite's "database is locked".
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// close closes both pools and returns the first error.
func (p *pools) close() error {
	werr := p.write.Close()
	rerr := p.read.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// checkPath refuses a path the driver would misread. The driver takes what
// follows a "?" as its own parameters, a "file:" path as a URI with SQLite's
// own parameters, and ":memory:" or an empty path as a database private to
// each connection, which the two pools would not share. The message does
// not repeat the path: a database address never appears in an error.
func checkPath(path string) error {
	switch {
	case path == "", path == ":memory:":
		return fault.New(fault.Invalid, "a SQLite database is a file; give its path")
	case strings.ContainsAny(path, "?\x00"):
		return fault.New(fault.Invalid, "a SQLite database's path may not hold ? or a NUL byte")
	case strings.HasPrefix(path, "file:"):
		return fault.New(fault.Invalid, "a SQLite database's path is a plain path, not a file: URI")
	}
	return nil
}

// createPrivate makes the database file, empty and readable by its owner
// only, when it does not exist. SQLite would make it with the process's
// umask, often readable by everyone, and the master holds key hashes and
// sealed addresses. SQLite gives the -wal and -shm files it makes the mode
// of the database file (os_unix.c, findCreateFileMode), so they are private
// too. On Windows the mode is ignored and the directory's access list
// decides.
func createPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 G703 -- the path is the operator's own choice of database file.
	if err != nil {
		// The cause may name the path, so it is not wrapped: the kind
		// and a fixed message go to the caller, and the operator sees the
		// path in the command they typed.
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return fault.New(fault.Unavailable, "the master database file cannot be opened: "+pathErr.Err.Error())
		}
		return fault.New(fault.Unavailable, "the master database file cannot be opened")
	}
	if err := f.Close(); err != nil {
		return fault.New(fault.Unavailable, "the master database file cannot be opened")
	}
	return nil
}

// cloneValues returns a copy of v.
func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// dialect is how the migration runner works on SQLite. BeginLocked begins on
// the write pool, whose transactions are BEGIN IMMEDIATE, so the lock is the
// database's write lock and holds across processes.
var dialect = sqlkit.Dialect{
	HasTable:      `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations'`,
	CreateTable:   `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL PRIMARY KEY, name TEXT NOT NULL, checksum BLOB NOT NULL, applied INTEGER NOT NULL) STRICT`,
	SelectApplied: `SELECT version, name, checksum FROM schema_migrations ORDER BY version`,
	InsertApplied: `INSERT INTO schema_migrations (version, name, checksum, applied) VALUES (?, ?, ?, ?)`,
	BeginLocked: func(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
		return db.BeginTx(ctx, nil)
	},
}

// classify turns an error from the driver into a fault with a kind. An error
// that already has one keeps it. message is what the caller is shown; the
// driver's own text, which may say more about the database than a caller
// should learn, stays in the chain for the log.
func classify(err error, message string) error {
	if err == nil {
		return nil
	}
	var fe *fault.Error
	if errors.As(err, &fe) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fault.Wrap(fault.Unavailable, err, message)
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_IOERR,
			sqlite3.SQLITE_CANTOPEN, sqlite3.SQLITE_FULL, sqlite3.SQLITE_INTERRUPT:
			return fault.Wrap(fault.Unavailable, err, message)
		}
	}
	return fault.Wrap(fault.Internal, err, message)
}

// isUniqueViolation reports whether err is a unique or primary key
// constraint failing.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
