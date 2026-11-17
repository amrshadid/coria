package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/master/mastertest"
)

// openTest opens a fresh, migrated master in a new file under tb's
// temporary directory, closed when tb ends.
func openTest(tb testing.TB) master.Store {
	tb.Helper()
	ctx := context.Background()
	m, err := OpenMaster(ctx, filepath.Join(tb.TempDir(), "master.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = m.Close() })
	if err := m.Migrate(ctx); err != nil {
		tb.Fatal(err)
	}
	return m
}

// TestMasterContract runs the master port's contract suite on SQLite.
func TestMasterContract(t *testing.T) {
	mastertest.Run(t, openTest)
}

// TestShippedMigrationsUnchanged guards the master's migration files against
// an edit after release: an applied file that changes makes every database
// that applied it refuse to start (sqlkit.ErrChecksum). A new file is added
// to the list; a listed checksum is never changed.
func TestShippedMigrationsUnchanged(t *testing.T) {
	want := []string{
		"0001_init.sql " + shippedInit,
	}
	if masterMigrationsErr != nil {
		t.Fatal(masterMigrationsErr)
	}
	if len(masterMigrations) < len(want) {
		t.Fatalf("%d migrations, want at least %d", len(masterMigrations), len(want))
	}
	for i, w := range want {
		m := masterMigrations[i]
		if got := m.Name + " " + hex.EncodeToString(m.Sum[:]); got != w {
			t.Errorf("migration %d is %s, want %s: an applied migration is never changed", i+1, got, w)
		}
	}
}

// shippedInit is the SHA-256 of 0001_init.sql as this change ships it.
const shippedInit = "294f40182be2c97a37b4335a5de67b47ea85c10fdaa63bf0ac4985b724ba3f59"

// TestPragmas guards the settings each connection of each pool is opened
// with, read back from SQLite itself, since a DSN key the driver did not
// understand would be ignored silently.
func TestPragmas(t *testing.T) {
	ctx := context.Background()
	p, err := openPools(ctx, filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close() })
	want := map[string]map[string]string{
		"write": {"journal_mode": "wal", "foreign_keys": "1", "synchronous": "2", "busy_timeout": "10000", "query_only": "0", "fullfsync": "1", "checkpoint_fullfsync": "1"},
		"read":  {"journal_mode": "wal", "foreign_keys": "1", "synchronous": "2", "busy_timeout": "10000", "query_only": "1", "fullfsync": "1", "checkpoint_fullfsync": "1"},
	}
	for pool, pragmas := range want {
		db := p.write
		if pool == "read" {
			db = p.read
		}
		for pragma, value := range pragmas {
			var got string
			if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
				t.Fatalf("%s PRAGMA %s: %v", pool, pragma, err)
			}
			if got != value {
				t.Errorf("%s pool: PRAGMA %s = %s, want %s", pool, pragma, got, value)
			}
		}
	}
	if _, err := p.read.ExecContext(ctx, "CREATE TABLE x (y INTEGER)"); err == nil {
		t.Error("a read connection wrote")
	}
}

// TestFileIsPrivate guards that a new master database file is readable by
// its owner only, since it holds key hashes and sealed addresses. Windows
// ignores the mode; the directory's access list decides there.
func TestFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not used on Windows")
	}
	path := filepath.Join(t.TempDir(), "master.db")
	m, err := OpenMaster(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s has mode %v, want no access for group or others", filepath.Base(f), mode)
		}
	}
}

// TestOpenRefusesPaths guards the paths the driver would misread: empty,
// in memory, a file: URI and one with a "?", each Invalid, with the path
// not repeated in the error.
func TestOpenRefusesPaths(t *testing.T) {
	for _, path := range []string{"", ":memory:", "file:/tmp/x.db", "/tmp/a?_pragma=x"} {
		_, err := OpenMaster(context.Background(), path)
		if fault.KindOf(err) != fault.Invalid {
			t.Errorf("OpenMaster(%q) = %v, want Invalid", path, err)
		}
		if path != "" && err != nil && strings.Contains(err.Error(), path) {
			t.Errorf("the error repeats the path: %v", err)
		}
	}
	_, err := OpenMaster(context.Background(), filepath.Join(t.TempDir(), "no", "such", "dir", "m.db"))
	if fault.KindOf(err) != fault.Unavailable {
		t.Errorf("a path in no directory: %v, want Unavailable", err)
	}
}

// TestNotMigrated guards that a store used before Migrate fails as
// Unavailable with a message that says so, instead of a SQL error.
func TestNotMigrated(t *testing.T) {
	m, err := OpenMaster(context.Background(), filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if v, err := m.Version(context.Background()); err != nil || v != 0 {
		t.Errorf("Version before Migrate = %d, %v; want 0", v, err)
	}
	_, err = m.Key(context.Background(), "k")
	if fault.KindOf(err) != fault.Unavailable || !strings.Contains(fault.Message(err), "migrated") {
		t.Errorf("Key before Migrate = %v, want Unavailable saying it is not migrated", err)
	}
}

// TestWriteSample writes the contract suite's sample data into a database
// for the outside check (scripts/check-master-sqlite.sh), which reads it
// with the sqlite3 tool. It runs only when MAGNETAR_SAMPLE_DIR names a
// directory; there it leaves master.db and the sample key's hash in hex,
// and never the secret, which the script holds itself.
func TestWriteSample(t *testing.T) {
	dir := os.Getenv("MAGNETAR_SAMPLE_DIR")
	if dir == "" {
		t.Skip("MAGNETAR_SAMPLE_DIR is not set; scripts/check-master-sqlite.sh sets it")
	}
	path := filepath.Join(dir, "master.db")
	ctx := context.Background()
	m, err := OpenMaster(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mastertest.WriteSample(t, m)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(mastertest.SampleSecret))
	hashFile := filepath.Join(dir, "hash.hex")
	if err := os.WriteFile(hashFile, []byte(hex.EncodeToString(sum[:])+"\n"), 0o600); err != nil { // #nosec G703 -- the directory is the check script's own temporary one.
		t.Fatal(err)
	}
}
