package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// versionLine is the shape of what the version command prints: the version,
// the commit and the Go release, on one line.
var versionLine = regexp.MustCompile(`^magnetar \S+ \(commit \S+, \S*go1\S*\)\n$`)

// TestVersionPrintsOneLine guards both spellings of the version command: each
// prints the version line to standard output, nothing to standard error, and
// exits 0.
func TestVersionPrintsOneLine(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{arg}, &stdout, &stderr)
		if code != exitOK {
			t.Errorf("%s: exit code %d, want %d", arg, code, exitOK)
		}
		if !versionLine.MatchString(stdout.String()) {
			t.Errorf("%s: standard output %q is not a version line", arg, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%s: standard error %q, want nothing", arg, stderr.String())
		}
	}
}

// TestAnythingElseIsAUsageError guards the usage error: an unknown word, no
// word, or extra words print one line to standard error naming what was wrong,
// nothing to standard output, and exit 2, so a script can tell a mistake in its
// arguments from a failure.
func TestAnythingElseIsAUsageError(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"unknown word":   {[]string{"nope"}, `magnetar: unknown command "nope"; the only command so far is "magnetar version"` + "\n"},
		"no word":        {nil, `magnetar: no command; the only command so far is "magnetar version"` + "\n"},
		"extra words":    {[]string{"version", "now"}, `magnetar: unknown command "version now"; the only command so far is "magnetar version"` + "\n"},
		"single dash":    {[]string{"-version"}, `magnetar: unknown command "-version"; the only command so far is "magnetar version"` + "\n"},
		"terminal codes": {[]string{"\x1b[2J"}, `magnetar: unknown command "\x1b[2J"; the only command so far is "magnetar version"` + "\n"},
	}
	for name, c := range cases {
		var stdout, stderr bytes.Buffer
		code := run(c.args, &stdout, &stderr)
		if code != exitUsage {
			t.Errorf("%s: exit code %d, want %d", name, code, exitUsage)
		}
		if stderr.String() != c.want {
			t.Errorf("%s: standard error %q, want %q", name, stderr.String(), c.want)
		}
		if stdout.Len() != 0 {
			t.Errorf("%s: standard output %q, want nothing", name, stdout.String())
		}
	}
}

// failingWriter is a standard output that cannot be written, as a closed pipe.
type failingWriter struct{}

// Write fails every time.
func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// TestVersionFailsWhenOutputFails guards the exit code when the version cannot
// be written: a script piping the output must see a failure, not success with
// nothing printed.
func TestVersionFailsWhenOutputFails(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"version"}, failingWriter{}, &stderr); code != exitFailed {
		t.Errorf("exit code %d, want %d", code, exitFailed)
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Errorf("standard error %q, want the write error", stderr.String())
	}
}

// TestBuiltBinary guards what run cannot show: that main passes run's code to
// the operating system, and that -ldflags reaches the variables under the
// package path written in the documentation and the Makefile. It builds the
// real binary and runs it.
func TestBuiltBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary; skipped with -short")
	}
	bin := filepath.Join(t.TempDir(), "magnetar")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	const pkg = "github.com/amrshadid/magnetar/internal/version"
	build := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X "+pkg+".Version=v9.9.9-test -X "+pkg+".Commit=abcdef123456", ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "version")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("magnetar version: %v; standard error %q", err, stderr.String())
	}
	want := "magnetar v9.9.9-test (commit abcdef123456, " + runtime.Version() + ")\n"
	if got := strings.ReplaceAll(stdout.String(), "\r\n", "\n"); got != want {
		t.Errorf("magnetar version printed %q, want %q", got, want)
	}

	stdout.Reset()
	stderr.Reset()
	cmd = exec.Command(bin, "nope")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != exitUsage {
		t.Fatalf("magnetar nope: %v, want exit code %d", err, exitUsage)
	}
	wantErr := `magnetar: unknown command "nope"; the only command so far is "magnetar version"`
	if got := strings.TrimRight(stderr.String(), "\r\n"); got != wantErr || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("magnetar nope wrote %q to standard error, want the one line %q", stderr.String(), wantErr)
	}
	if stdout.Len() != 0 {
		t.Errorf("magnetar nope wrote %q to standard output, want nothing", stdout.String())
	}
}
