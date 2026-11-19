package servecmd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amrshadid/coria/internal/cli/servecmd"
	"github.com/amrshadid/coria/internal/driver/sqlite"
	"github.com/amrshadid/coria/internal/server"
)

// TestServeRefusesAnotherMaster guards that serve pointed at a master that is
// not the one init made refuses to start, and changes nothing in it. Without
// it, serve given another address would silently serve an empty or foreign
// master, with none of this installation's tenants, keys or grants. An empty
// database, one with the tables and no installation id, and another
// installation's master are each refused with a message saying the master is
// not this installation's; the right master is accepted (as far as the
// identity goes) in every other test.
func TestServeRefusesAnotherMaster(t *testing.T) {
	dir := shortDir(t)
	parent := filepath.Dir(dir)
	ours := "sqlite:" + filepath.Join(parent, "ours.db")
	if _, err := run(t, map[string]string{"M": ours}, "init", "--data-dir", dir, "--master-env", "M"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(parent, "other")
	otherMaster := "sqlite:" + filepath.Join(parent, "theirs.db")
	if _, err := run(t, map[string]string{"M": otherMaster}, "init", "--data-dir", other, "--master-env", "M"); err != nil {
		t.Fatal(err)
	}
	// Tables and no id: a master migrated by something else.
	bare := filepath.Join(parent, "bare.db")
	m, err := sqlite.OpenMaster(context.Background(), bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()

	empty := filepath.Join(parent, "empty.db")
	for _, c := range []struct {
		address, want string
	}{
		{"sqlite:" + empty, "it is empty"},
		{"sqlite:" + bare, "it holds no installation id"},
		{otherMaster, "it belongs to another installation"},
	} {
		// A serve that is not refused starts serving and does not return, so
		// a watchdog fails the test then.
		result := make(chan error, 1)
		go func() {
			_, err := run(t, map[string]string{"M": c.address}, "serve", "--data-dir", dir, "--master-env", "M")
			result <- err
		}()
		var err error
		select {
		case err = <-result:
		case <-time.After(10 * time.Second):
			t.Fatalf("serve started on %s, a master that is not this installation's", c.address)
		}
		if err == nil || !strings.Contains(err.Error(), "the master database is not the one this installation was made with") || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("serve on %s: %v", c.address, err)
		}
	}
	// The empty database was not migrated by the refused serve.
	m, err = sqlite.OpenMaster(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if v, err := m.Version(context.Background()); err != nil || v != 0 {
		t.Fatalf("the refused serve changed the empty master: version %d, %v", v, err)
	}
}

// TestInitRefusesAMasterInUse guards that init does not take a master that
// already belongs to an installation.
func TestInitRefusesAMasterInUse(t *testing.T) {
	dir := shortDir(t)
	address := "sqlite:" + filepath.Join(filepath.Dir(dir), "shared.db")
	if _, err := run(t, map[string]string{"M": address}, "init", "--data-dir", dir, "--master-env", "M"); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(filepath.Dir(dir), "second")
	_, err := run(t, map[string]string{"M": address}, "init", "--data-dir", second, "--master-env", "M")
	if err == nil || !strings.Contains(err.Error(), "already belongs to an installation") {
		t.Fatalf("a second init on the same master: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, servecmd.KeyFileName)); err == nil {
		t.Fatal("the refused init left a key file")
	}
}

// TestHalfMadeDirectory guards that a data directory whose init was
// interrupted says so, and what to remove, for each way it can be half
// made.
func TestHalfMadeDirectory(t *testing.T) {
	full := shortDir(t)
	if _, err := run(t, nil, "init", "--data-dir", full); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		remove []string
		want   string
	}{
		{"master only", []string{servecmd.KeyFileName, server.IDFileName}, "init was interrupted before it wrote the key file"},
		{"key and master, no id", []string{server.IDFileName}, "has a key file but no installation id: init was interrupted"},
		{"key and id, no master", []string{servecmd.MasterFileName}, "if init was interrupted, remove"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := shortDir(t)
			if _, err := run(t, nil, "init", "--data-dir", dir); err != nil {
				t.Fatal(err)
			}
			for _, name := range c.remove {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := run(t, nil, "serve", "--data-dir", dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("serve: %v", err)
			}
		})
	}
	// A directory with nothing of an installation says to run init.
	if _, err := run(t, nil, "serve", "--data-dir", shortDir(t)); err == nil || !strings.Contains(err.Error(), "make one with: coria init") {
		t.Fatalf("serve on nothing: %v", err)
	}
}

// TestDrainMustBePositive guards that --drain 0 and a negative drain are
// usage errors, not a silent default.
func TestDrainMustBePositive(t *testing.T) {
	dir := shortDir(t)
	if _, err := run(t, nil, "init", "--data-dir", dir); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0", "-1s"} {
		if _, err := run(t, nil, "serve", "--data-dir", dir, "--drain", v); err == nil || !strings.Contains(err.Error(), "--drain is a positive duration") {
			t.Fatalf("--drain %s: %v", v, err)
		}
	}
}
