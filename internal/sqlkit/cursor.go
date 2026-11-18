package sqlkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/amrshadid/magnetar/internal/fault"
)

// MaxCursorLength bounds a cursor a caller hands back. A cursor holds the
// sort key of one row and a short binding; anything longer was not made here.
const MaxCursorLength = 1024

// cursorVersion is the first field of every cursor. A cursor of another
// version, from an older or newer binary, is refused rather than misread.
const cursorVersion = "c1"

// checkLength is how many bytes of SHA-256 end a cursor. It is a check
// against a cursor altered or cut short by accident, not a signature: a
// cursor holds nothing the caller was not shown, and a forged one can only
// start its own list at another place.
const checkLength = 8

// errBadCursor is what every refused cursor reports, so a caller learns
// nothing about which part was wrong.
func errBadCursor() error {
	return fault.New(fault.Invalid, "the page token is not one this list returned with these arguments")
}

// Binding returns a short, fixed-length text that stands for parts: the
// arguments a list was called with, such as a filter's fields. Two calls
// with the same parts give the same binding, and with different parts a
// different one. Each part is written with its length, so ("a", "bc") and
// ("ab", "c") differ.
func Binding(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// EncodeCursor returns an opaque cursor for a list, named by list, called
// with the arguments binding stands for (a key id, a scope, or Binding of a
// filter; empty when the list takes none), resuming after the row whose
// sort key is fields. A list resumes with a keyset condition such as
// "WHERE (a, b) > (?, ?)", never an offset, so rows inserted or removed
// meanwhile neither shift a page nor repeat a row.
//
// The API contract refuses a page token used with other arguments than it
// was made with; binding is what lets DecodeCursor do that.
func EncodeCursor(list, binding string, fields ...string) string {
	var b bytes.Buffer
	writeField(&b, cursorVersion)
	writeField(&b, list)
	writeField(&b, binding)
	for _, f := range fields {
		writeField(&b, f)
	}
	sum := sha256.Sum256(b.Bytes())
	b.Write(sum[:checkLength])
	return base64.RawURLEncoding.EncodeToString(b.Bytes())
}

// writeField writes one field with its length, so a field may hold any byte.
func writeField(b *bytes.Buffer, f string) {
	b.WriteString(strconv.Itoa(len(f)))
	b.WriteByte(':')
	b.WriteString(f)
}

// DecodeCursor returns the n sort-key fields of a cursor that EncodeCursor
// made for the same list and binding. A cursor that is too long, malformed,
// altered, cut short, of another version, made for another list or with
// another binding, or holding another number of fields, is Invalid, with one
// message for all of them.
func DecodeCursor(cursor, list, binding string, n int) ([]string, error) {
	if len(cursor) > MaxCursorLength {
		return nil, errBadCursor()
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) < checkLength {
		return nil, errBadCursor()
	}
	body, check := raw[:len(raw)-checkLength], raw[len(raw)-checkLength:]
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:checkLength], check) {
		return nil, errBadCursor()
	}
	var fields []string
	s := string(body)
	for s != "" {
		lengthText, rest, ok := strings.Cut(s, ":")
		if !ok {
			return nil, errBadCursor()
		}
		length, err := strconv.Atoi(lengthText)
		if err != nil || length < 0 || length > len(rest) || lengthText != strconv.Itoa(length) {
			return nil, errBadCursor()
		}
		fields = append(fields, rest[:length])
		s = rest[length:]
	}
	if len(fields) != 3+n || fields[0] != cursorVersion || fields[1] != list || fields[2] != binding {
		return nil, errBadCursor()
	}
	return fields[3:], nil
}

// DecodeCursorInt parses a field of a cursor that holds an integer, such as
// a time in microseconds. A field that is not one is Invalid.
func DecodeCursorInt(field string) (int64, error) {
	v, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		return 0, errBadCursor()
	}
	return v, nil
}
