//go:build !windows

package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/fault"
)

// TestCreatedKeyFileMode guards that a new key file gives nobody but its
// owner any access: mode 0600 exactly.
func TestCreatedKeyFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "magnetar.key")
	if err := CreateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode %04o, want 0600", got)
	}
}

// TestLoadRefusesOpenModes guards that a key file the group or other users
// may read or write is refused, with the chmod that fixes it, and that a
// file only its owner may open loads.
func TestLoadRefusesOpenModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o620, 0o602, 0o660, 0o666, 0o610, 0o601} {
		path := filepath.Join(t.TempDir(), "magnetar.key")
		if err := os.WriteFile(path, []byte(keyLine(1, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		// Chmod after the write, because the umask would remove bits from
		// the mode given to WriteFile.
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := LoadKeyFile(path)
		if err == nil {
			t.Errorf("mode %04o: loaded, want refused", mode)
			continue
		}
		if fault.KindOf(err) != fault.Invalid {
			t.Errorf("mode %04o: kind %v, want Invalid", mode, fault.KindOf(err))
		}
		if msg := fault.Message(err); !strings.Contains(msg, "chmod 600 "+path) {
			t.Errorf("mode %04o: message %q does not say how to fix it", mode, msg)
		}
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		path := filepath.Join(t.TempDir(), "magnetar.key")
		if err := os.WriteFile(path, []byte(keyLine(1, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadKeyFile(path); err != nil {
			t.Errorf("mode %04o: %v, want loaded", mode, err)
		}
	}
}

// TestLoadRefusesDirectory guards that a directory at the key file's path is
// refused as not a regular file, rather than read as an empty key file.
func TestLoadRefusesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "magnetar.key")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyFile(dir); fault.KindOf(err) != fault.Invalid {
		t.Errorf("LoadKeyFile of a directory: %v, want Invalid", err)
	}
}
