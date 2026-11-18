package driver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/fault"
)

// TestOpenMasterByScheme guards that a sqlite: address opens a working
// master store, and that an unknown scheme is Invalid without the address,
// which may hold a password, in the error.
func TestOpenMasterByScheme(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMaster(ctx, SQLiteScheme+filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if v, err := store.Version(ctx); err != nil || v < 1 {
		t.Errorf("Version = %d, %v; want at least 1", v, err)
	}

	const address = "mysql://admin:hunter2@db.example/master" //nolint:gosec // G101: a made-up password whose absence from the error is the point.
	_, err = OpenMaster(ctx, address)
	if fault.KindOf(err) != fault.Invalid {
		t.Errorf("unknown scheme: %v, want Invalid", err)
	}
	if err != nil && (strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "db.example")) {
		t.Errorf("the error repeats the address: %v", err)
	}
}
