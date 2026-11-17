package fault

import (
	"errors"
	"strconv"
)

// Kind says what sort of failure an error is.
type Kind int

// The kinds. Internal is the zero value, so an error that was never given a
// kind, and a Kind that was never set, both count as Internal.
const (
	// Internal is a failure of the archive itself: a bug, a broken invariant,
	// or a cause the caller must not be told about.
	Internal Kind = iota
	// Invalid is a request that is wrong as written: a malformed name, a
	// missing argument, a value out of range.
	Invalid
	// Unauthenticated is a request with no key, or a key that is malformed,
	// unknown, expired or revoked. All of these are one kind, so a caller
	// cannot tell which one it was.
	Unauthenticated
	// Denied is a request from a known key that holds no grant for what it
	// asks.
	Denied
	// NotFound is a request for something that does not exist, among the
	// things the caller may see.
	NotFound
	// Conflict is a request that clashes with the archive's current state,
	// such as making something that already exists.
	Conflict
	// Unavailable is a failure that may pass if the request is tried again
	// later: a database or a storage that cannot be reached.
	Unavailable
)

// String returns the kind's name in lowercase, as it appears in a log.
func (k Kind) String() string {
	switch k {
	case Internal:
		return "internal"
	case Invalid:
		return "invalid"
	case Unauthenticated:
		return "unauthenticated"
	case Denied:
		return "denied"
	case NotFound:
		return "not found"
	case Conflict:
		return "conflict"
	case Unavailable:
		return "unavailable"
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// ExitCode returns the status the command line exits with for an error of
// this kind. Scripts tell a usage mistake (2), a refusal (3) and a missing
// thing (4) apart by it; every other failure is 1. Unauthenticated and Denied
// share 3 because a caller must not learn from the status whether its key or
// its grant was the problem.
func (k Kind) ExitCode() int {
	switch k {
	case Invalid:
		return 2
	case Unauthenticated, Denied:
		return 3
	case NotFound:
		return 4
	}
	return 1
}

// Error is an error with a kind, a message that is safe to show the caller,
// and optionally the error it wraps, which is for the log only.
type Error struct {
	kind    Kind
	message string
	cause   error
}

// New returns an error of the given kind with a message that is safe to show
// the caller.
func New(kind Kind, message string) error {
	return &Error{kind: kind, message: message}
}

// Wrap returns an error of the given kind that wraps cause. The message is
// what the caller is shown; cause is kept for the log and for errors.Is and
// errors.As, and is never part of the caller's message. Wrap of a nil cause
// is the same as New, so a caller that wraps unconditionally still gets an
// error.
func Wrap(kind Kind, cause error, message string) error {
	return &Error{kind: kind, message: message, cause: cause}
}

// Error returns the message followed by the wrapped error's text, for the log.
// It is not for the caller: use Message for that.
func (e *Error) Error() string {
	if e.cause == nil {
		return e.message
	}
	return e.message + ": " + e.cause.Error()
}

// Unwrap returns the wrapped error, so errors.Is and errors.As see through an
// Error to its cause.
func (e *Error) Unwrap() error {
	return e.cause
}

// Kind returns the error's kind.
func (e *Error) Kind() Kind {
	return e.kind
}

// Message returns the message that is safe to show the caller, without the
// wrapped error.
func (e *Error) Message() string {
	return e.message
}

// KindOf returns the kind of the first Error in err's chain, and Internal
// when there is none or err is nil. The first is the outermost, so a layer
// that wraps a lower one with a new kind decides what the caller sees.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.kind
	}
	return Internal
}

// internalMessage is what the caller is shown for an error that carries no
// message of ours: its text was never written for a caller, so none of it is
// shown.
const internalMessage = "internal error"

// Message returns the text that is safe to show the caller for err: the
// message of the first Error in its chain, or "internal error" when there is
// none. It returns the empty string for a nil error.
func Message(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.message
	}
	return internalMessage
}
