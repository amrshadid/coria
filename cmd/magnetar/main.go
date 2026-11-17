package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/amrshadid/magnetar/internal/version"
)

// Exit codes of the command line. They are fixed so a script can tell a
// mistake in its own arguments (2) from a command that ran and failed (1).
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole program with its arguments and writers passed in, so a test
// can drive it and read what it printed without starting a process.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		if _, err := fmt.Fprintln(stdout, version.Get()); err != nil {
			// Standard output could not take the line: a full disk or a
			// device that refuses writes (on Linux, > /dev/full), or on
			// Windows a closed pipe. On Unix a closed pipe never gets here,
			// because Go lets SIGPIPE end the process on a write to standard
			// output.
			// A script must see the failure in the exit code, not success
			// with nothing printed.
			_, _ = fmt.Fprintf(stderr, "magnetar: writing the version: %v\n", err)
			return exitFailed
		}
		return exitOK
	}
	// %q, not %s: the argument comes from the caller and is printed to a
	// terminal, so control characters in it must not reach the terminal as
	// they are.
	what := "no command"
	if len(args) > 0 {
		what = fmt.Sprintf("unknown command %q", strings.Join(args, " "))
	}
	_, _ = fmt.Fprintf(stderr, "magnetar: %s; the only command so far is \"magnetar version\"\n", what)
	return exitUsage
}
