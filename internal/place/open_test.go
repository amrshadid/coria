package place

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/amrshadid/magnetar/internal/core/settings"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// TestCheckAppliesTheRulesOfOpen guards Check, which the reception pipeline
// runs at every use of a storage it keeps open: it passes while Open would,
// and refuses, as Open does, a storage whose directory was replaced by a
// symbolic link below the root, one whose root is no longer listed, and one
// whose directory is gone; it makes and opens nothing.
func TestCheckAppliesTheRulesOfOpen(t *testing.T) {
	ctx := context.Background()
	root := realTemp(t)
	dir := filepath.Join(root, "lab", "main")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listed := fixedValues{roots: settings.DirectoryRoots{{Name: "fast", Path: root}}}
	s := tenant.Storage{Name: "main", Type: tenant.StorageDirectory, Directory: tenant.DirectoryStorage{Root: "fast", Path: "main"}}
	lab := Tenant{ID: "t1", Name: "lab"}
	o := Opener{Values: listed}
	if err := o.Check(ctx, lab, s); err != nil {
		t.Fatalf("Check of an allowed storage: %v", err)
	}
	bs, err := o.Open(ctx, lab, s)
	if err != nil {
		t.Fatalf("Open of an allowed storage: %v", err)
	}
	_ = bs.Close()

	if err := (Opener{Values: fixedValues{}}).Check(ctx, lab, s); err == nil {
		t.Error("Check passed a storage whose root is no longer listed")
	}

	elsewhere := realTemp(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	symlink(t, elsewhere, dir)
	errCheck := o.Check(ctx, lab, s)
	_, errOpen := o.Open(ctx, lab, s)
	if errCheck == nil || errOpen == nil {
		t.Fatalf("a link planted at the storage's directory: Check %v, Open %v; both must refuse", errCheck, errOpen)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := o.Check(ctx, lab, s); err == nil {
		t.Error("Check passed a storage whose directory is gone")
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Error("Check made the storage's directory")
	}
}
