package dir

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/amrshadid/magnetar/internal/fault"
)

// wrap returns an error of the kind and message given that wraps cause with
// the storage's path taken out of it (see pathless). Every error the store
// returns with a cause goes through here.
func (s *Store) wrap(kind fault.Kind, cause error, message string) error {
	return fault.Wrap(kind, s.pathless(cause), message)
}

// pathless returns err as it reads in a log, with every mention of the
// storage's directory replaced by "<storage>", and keeps what errors.Is and
// errors.As need of it: the system's error number, a context's end, a
// sentinel of package fs.
//
// The errors of package os name the whole path of the file they concern,
// and a file opened through os.Root is named by the root's path joined to
// its own. Where a storage is mounted is the operator's business and no
// caller's, and a log of errors is read by more people than the
// configuration, so no error leaves this package with it, in its message or
// in the text of its cause.
func (s *Store) pathless(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	clean := text
	for _, p := range s.rootPaths {
		if p == "" {
			continue
		}
		clean = strings.ReplaceAll(clean, p+string(filepath.Separator), "<storage>"+string(filepath.Separator))
		clean = strings.ReplaceAll(clean, p, "<storage>")
	}
	if clean == text {
		return err
	}
	return &pathlessError{text: clean, cause: innermost(err)}
}

// innermost returns what lies under err's paths: the error a PathError,
// LinkError or SyscallError carries, or err itself when it carries none of
// them. It is what errors.Is and errors.As are answered from once the
// paths are taken out.
func innermost(err error) error {
	var pe *fs.PathError
	var le *os.LinkError
	var se *os.SyscallError
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.As(err, &pe):
		return pe.Err
	case errors.As(err, &le):
		return le.Err
	case errors.As(err, &se):
		return se.Err
	}
	return errors.New("cause withheld")
}

// pathlessError is an error whose text no longer names the storage's path.
type pathlessError struct {
	text  string
	cause error
}

func (e *pathlessError) Error() string { return e.text }
func (e *pathlessError) Unwrap() error { return e.cause }
