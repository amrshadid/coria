//go:build windows

package secret

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/fault"
)

// TestCreatedKeyFileACL guards that a new key file's access list names the
// current user and nobody else. It reads the list twice: through the
// package's own fileAccess, compared with the current user's SID, and
// through icacls, the system's own tool, compared with the account name
// whoami reports, so a mistake in how the package reads a DACL cannot hide
// one in how it writes one.
func TestCreatedKeyFileACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "magnetar.key")
	if err := CreateKeyFile(path); err != nil {
		t.Fatal(err)
	}

	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sids, err := fileAccess(f)
	_ = f.Close()
	if err != nil {
		t.Fatalf("fileAccess: %v", err)
	}
	if len(sids) != 1 || sids[0] != user {
		t.Errorf("the access list grants %v, want only the current user %s", sids, user)
	}

	who, err := exec.Command("whoami").Output()
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	account := strings.TrimSpace(string(who))
	out, err := icacls(path)
	if err != nil {
		t.Fatalf("icacls: %v\n%s", err, out)
	}
	t.Logf("icacls %s:\n%s", filepath.Base(path), out)
	entries := icaclsEntries(out, filepath.Base(path))
	if len(entries) != 1 {
		t.Fatalf("icacls lists %d entries %q, want one, for %s", len(entries), entries, account)
	}
	name, rights, ok := strings.Cut(entries[0], ":")
	if !ok || !strings.EqualFold(name, account) || rights != "(F)" {
		t.Errorf("icacls lists %q, want %s:(F)", entries[0], account)
	}
}

// icacls runs icacls on path from its own directory with the bare file name,
// so the output starts with that name and not with a spelling of the full
// path (a short 8.3 name, say) the test would have to predict.
func icacls(path string, args ...string) (string, error) {
	cmd := exec.Command("icacls", append([]string{filepath.Base(path)}, args...)...) //nolint:gosec // G204: the test's own file and fixed arguments.
	cmd.Dir = filepath.Dir(path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// icaclsEntries returns the access entries in icacls output: the first line
// starts with the path, each further entry is on its own indented line, and
// the output ends with a summary line after a blank one.
func icaclsEntries(out, path string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, path))
		if line != "" {
			entries = append(entries, line)
		}
	}
	return entries
}

// TestLoadRefusesInheritedACL guards that a key file whose access list
// grants anyone besides the current user is refused, with the icacls command
// that fixes it. A file made by os.WriteFile inherits the access list of the
// temporary directory, which on Windows grants SYSTEM and the Administrators
// group as well as the user.
func TestLoadRefusesInheritedACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "magnetar.key")
	if err := os.WriteFile(path, []byte(keyLine(1, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := icacls(path)
	t.Logf("icacls %s:\n%s", filepath.Base(path), out)

	_, err := LoadKeyFile(path)
	if err == nil {
		t.Fatal("loaded a key file other users may open")
	}
	if fault.KindOf(err) != fault.Invalid {
		t.Errorf("kind %v, want Invalid: %v", fault.KindOf(err), err)
	}
	if msg := fault.Message(err); !strings.Contains(msg, "icacls") {
		t.Errorf("message %q does not say how to fix it", msg)
	}
	t.Logf("refusal: %s", fault.Message(err))
}

// TestLoadAfterIcaclsFix guards that the command the refusal suggests does
// make the file load: an instruction that does not work is worse than none.
func TestLoadAfterIcaclsFix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "magnetar.key")
	if err := os.WriteFile(path, []byte(keyLine(1, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	who, err := exec.Command("whoami").Output()
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	account := strings.TrimSpace(string(who))
	if out, err := icacls(path, "/inheritance:r", "/grant:r", account+":F"); err != nil {
		t.Fatalf("icacls: %v\n%s", err, out)
	}
	if _, err := LoadKeyFile(path); err != nil {
		t.Fatalf("LoadKeyFile after the suggested icacls: %v", err)
	}
}
