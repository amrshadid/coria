package driver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/fault"
)

// TestSQLiteTenantPath guards the file a tenant on SQLite gets: exactly
// <data-dir>/tenants/<name>.db, absolute, for a valid name, and nothing at
// all for a name that would put a file anywhere else. The master validates
// names before they get here; this is the second wall, so it is tested with
// names the master would refuse.
func TestSQLiteTenantPath(t *testing.T) {
	data := t.TempDir()
	d := SQLiteTenants{DataDir: data}
	got, err := d.Path("lab-2")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(filepath.Join(data, "tenants", "lab-2.db"))
	if got != want {
		t.Errorf("Path(lab-2) = %s, want %s", got, want)
	}
	hostile := []string{
		"", ".", "..", "../escape", "a/../../escape", "/etc/passwd", `..\escape`, `a\b`,
		"tenants/../../x", "lab\x00", "c:x", "Lab", "-lab", strings.Repeat("a", 64),
	}
	for _, name := range hostile {
		p, err := d.Path(name)
		if err == nil {
			t.Errorf("Path(%q) = %s, want an error", name, p)
			continue
		}
		if fault.KindOf(err) != fault.Invalid {
			t.Errorf("Path(%q): %v, want Invalid", name, err)
		}
	}
	// Windows reserves device names: a file named con.db is the console,
	// not a file, before Windows 11. The tenant name rule refuses them on
	// every system, so a data directory moved to Windows keeps working.
	for _, device := range []string{"con", "nul", "com1", "lpt9"} {
		if _, err := d.Path(device); fault.KindOf(err) != fault.Invalid {
			t.Errorf("Path(%s) = %v, want Invalid on %s too", device, err, runtime.GOOS)
		}
	}
	if _, err := (SQLiteTenants{}).Path("lab"); fault.KindOf(err) != fault.Internal {
		t.Errorf("no data directory: %v, want Internal", err)
	}
}

// TestSQLiteTenantOpen guards that opening a tenant on SQLite makes the
// tenants directory private and the file where Path says, that an address
// is refused as the API contract says, and that the store works.
func TestSQLiteTenantOpen(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	d := SQLiteTenants{DataDir: data}
	if _, err := d.Open(ctx, "id-lab", "lab", "sqlite:/elsewhere.db"); fault.KindOf(err) != fault.Invalid {
		t.Errorf("an address for a tenant on SQLite: %v, want Invalid", err)
	}
	if _, err := d.Location("lab", "x"); fault.KindOf(err) != fault.Invalid {
		t.Errorf("Location with an address: %v, want Invalid", err)
	}
	s, err := d.Open(ctx, "id-lab", "lab", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	path, _ := d.Path("lab")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the tenant's file is not at %s: %v", path, err)
	}
	if loc, err := d.Location("lab", ""); err != nil || loc != path {
		t.Errorf("Location = %q, %v; want %s", loc, err, path)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(data, "tenants"))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("the tenants directory has mode %v, want no access for group or others", mode)
		}
	}
}

// TestSQLiteTenantsDirectoryCannotBeMade guards the failure to make the
// tenants directory, here because the data directory is a file: the open is
// Unavailable (the registry retries it), says which directory, and names no
// path.
func TestSQLiteTenantsDirectoryCannotBeMade(t *testing.T) {
	data := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(data, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SQLiteTenants{DataDir: data}.Open(context.Background(), "id-lab", "lab", "")
	if fault.KindOf(err) != fault.Unavailable || !strings.HasPrefix(fault.Message(err), "the tenants directory cannot be made") {
		t.Errorf("Open under a file = %v, want Unavailable saying the tenants directory cannot be made", err)
	}
	if err != nil && strings.Contains(err.Error(), data) {
		t.Errorf("the error names the path: %v", err)
	}
}
