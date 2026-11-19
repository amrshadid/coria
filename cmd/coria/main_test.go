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

// Exit codes of the command line, as the documentation fixes them.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// versionLine is the shape of what the version command prints: the version,
// the commit and the Go release, on one line.
var versionLine = regexp.MustCompile(`^coria \S+ \(commit \S+, \S*go1\S*\)\n$`)

// TestVersionPrintsOneLine guards both spellings of the version command: each
// prints the version line to standard output, nothing to standard error, and
// exits 0.
func TestVersionPrintsOneLine(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), []string{arg}, &stdout, &stderr)
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
// nothing to standard output, and exit 2, so a script can tell a mistake in
// its arguments from a failure.
func TestAnythingElseIsAUsageError(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"unknown word":   {[]string{"nope"}, `coria: unknown command "nope" for "coria"; try "coria --help"` + "\n"},
		"no word":        {nil, `coria: no command (usage: coria <noun> <verb> [flags], for example coria tenant list); try "coria --help"` + "\n"},
		"extra words":    {[]string{"version", "now"}, `coria: unknown command "now" for "coria version"; try "coria version --help"` + "\n"},
		"terminal codes": {[]string{"\x1b[2J"}, `coria: unknown command "\x1b[2J" for "coria"; try "coria --help"` + "\n"},
	}
	for name, c := range cases {
		var stdout, stderr bytes.Buffer
		code := run(t.Context(), c.args, &stdout, &stderr)
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

// failingWriter is a standard output that cannot be written, as a full disk.
type failingWriter struct{}

// Write fails every time.
func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// TestVersionFailsWhenOutputFails guards the exit code when the version cannot
// be written: a script reading the output must see a failure, not success
// with nothing printed.
func TestVersionFailsWhenOutputFails(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{"version"}, failingWriter{}, &stderr); code != exitFailed {
		t.Errorf("exit code %d, want %d", code, exitFailed)
	}
	if !strings.Contains(stderr.String(), "no space left on device") {
		t.Errorf("standard error %q, want the write error", stderr.String())
	}
}

// TestTestSupportIsNotLinked guards the rule that test support never reaches
// the real binary: the fake server is an ordinary package, so nothing but this
// check stops a non-test file from importing it. It asks the go command for
// every package the binary is built from, tests excluded, and fails if the
// fake server, or the testing package it brings in, is one of them.
func TestTestSupportIsNotLinked(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 20 {
		t.Fatalf("go list named only %d packages; the check is not looking at the binary", len(deps))
	}
	for _, d := range deps {
		if strings.HasSuffix(d, "/internal/cli/fakeserver") || d == "testing" {
			t.Errorf("the binary is built from %s, which is test support", d)
		}
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
	bin := filepath.Join(t.TempDir(), "coria")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	const pkg = "github.com/amrshadid/coria/internal/version"
	build := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X "+pkg+".Version=v9.9.9-test -X "+pkg+".Commit=abcdef123456", ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "version")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("coria version: %v; standard error %q", err, stderr.String())
	}
	want := "coria v9.9.9-test (commit abcdef123456, " + runtime.Version() + ")\n"
	if got := strings.ReplaceAll(stdout.String(), "\r\n", "\n"); got != want {
		t.Errorf("coria version printed %q, want %q", got, want)
	}

	// An unknown command exits 2 with one line, and a command that cannot
	// reach a server exits 1 with one line: the codes cross the process
	// boundary as the table says.
	for name, c := range map[string]struct {
		args []string
		exit int
		line string
	}{
		"usage":  {[]string{"nope"}, exitUsage, `coria: unknown command "nope" for "coria"; try "coria --help"`},
		"failed": {[]string{"--data-dir", t.TempDir(), "whoami"}, exitFailed, "coria: whoami: cannot reach the server at "},
	} {
		stdout.Reset()
		stderr.Reset()
		cmd = exec.Command(bin, c.args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != c.exit {
			t.Fatalf("%s: %v, want exit code %d", name, err, c.exit)
		}
		if got := strings.TrimRight(stderr.String(), "\r\n"); !strings.HasPrefix(got, c.line) || strings.Count(stderr.String(), "\n") != 1 {
			t.Errorf("%s wrote %q to standard error, want one line starting %q", name, stderr.String(), c.line)
		}
		if stdout.Len() != 0 {
			t.Errorf("%s wrote %q to standard output, want nothing", name, stdout.String())
		}
	}
}
