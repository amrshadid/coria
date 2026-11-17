package sqlkit

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/amrshadid/magnetar/internal/fault"
)

// MaxCursorLength bounds a cursor a caller hands back. A cursor holds the
// sort key of one row, a few short fields; anything longer was not made here.
const MaxCursorLength = 1024

// EncodeCursor returns an opaque cursor that names a list (kind) and the sort
// key of the last row of a page (fields). A list resumes after that row with
// a keyset condition such as "WHERE (a, b) > (?, ?)", never an offset, so
// rows inserted meanwhile neither shift a page nor repeat a row.
//
// The cursor is not secret and not signed: it holds only values the caller
// was already shown, and a forged one can only start a list at another
// place in it.
func EncodeCursor(kind string, fields ...string) string {
	var b strings.Builder
	b.WriteString(kind)
	for _, f := range fields {
		// Each field is written with its length, so a field may hold any
		// byte, the separator included.
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(b.String()))
}

// DecodeCursor returns the n fields of a cursor EncodeCursor made for kind.
// A cursor that is too long, malformed, made for another list or holding
// another number of fields is Invalid.
func DecodeCursor(cursor, kind string, n int) ([]string, error) {
	bad := fault.New(fault.Invalid, "the cursor is not one this list returned")
	if len(cursor) > MaxCursorLength {
		return nil, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, bad
	}
	s, ok := strings.CutPrefix(string(raw), kind)
	if !ok {
		return nil, bad
	}
	fields := make([]string, 0, n)
	for s != "" {
		if s[0] != ':' {
			return nil, bad
		}
		lengthText, rest, ok := strings.Cut(s[1:], ":")
		if !ok {
			return nil, bad
		}
		length, err := strconv.Atoi(lengthText)
		if err != nil || length < 0 || length > len(rest) || lengthText != strconv.Itoa(length) {
			return nil, bad
		}
		fields = append(fields, rest[:length])
		s = rest[length:]
	}
	if len(fields) != n {
		return nil, bad
	}
	return fields, nil
}

// DecodeCursorInt parses a field of a cursor that holds an integer, such as
// a time in microseconds. A field that is not one is Invalid.
func DecodeCursorInt(field string) (int64, error) {
	v, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		return 0, fault.New(fault.Invalid, "the cursor is not one this list returned")
	}
	return v, nil
}
