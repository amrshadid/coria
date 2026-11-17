package secret

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// knownSealedHex is testPlaintext sealed for testContext under version 2 of
// keyLine(1, 1)+keyLine(2, 2), made once by Python's cryptography package
// following the format in doc.go, not by this package. It is fixed, so the
// fuzz workers (separate processes) all agree on the one genuine value, and
// so a change to the format that still round-trips in Go alone fails here.
const knownSealedHex = "0100000002e1867bd724e0fddc881008f57ed5decb1ab017c79c97556d79f3422dc8f8a235fba6ea8c0588aa10ace1e3f8462a7c45f6ed4e5fab9a2a95e2684c94c437f595b012a1a57f32ceff44e80610a0cb9e54df931ece66b0dda9bdf10005481e"

// knownSealed returns knownSealedHex as bytes.
func knownSealed(tb testing.TB) []byte {
	tb.Helper()
	b, err := hex.DecodeString(knownSealedHex)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// TestOpenKnownValue guards the format against drift: a value sealed once by
// another implementation still opens to its plaintext.
func TestOpenKnownValue(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1)+keyLine(2, 2))
	opened, err := keys.Open(testContext, knownSealed(t))
	if err != nil {
		t.Fatalf("the value Python sealed does not open: %v", err)
	}
	if string(opened) != testPlaintext {
		t.Fatal("the value Python sealed opens to a different plaintext")
	}
}

// FuzzOpen guards Open against any sealed value and context: it never
// panics, a failure returns no bytes, and it opens nothing but the one
// genuine value in the seed corpus with its own context. Anything else that
// opened would be a forgery.
func FuzzOpen(f *testing.F) {
	keys, err := parseKeyFile([]byte(keyLine(1, 1) + keyLine(2, 2)))
	if err != nil {
		f.Fatal(err)
	}
	genuine := knownSealed(f)
	f.Add(testContext, genuine)
	f.Add("tenant/0f3a/databasf", genuine)
	f.Add(testContext, genuine[:len(genuine)-1])
	f.Add(testContext, []byte{0x01, 0, 0, 0, 1})
	f.Add("", []byte{})
	f.Add(testContext, bytes.Repeat([]byte{0x01}, 64))

	f.Fuzz(func(t *testing.T, context string, sealed []byte) {
		opened, err := keys.Open(context, sealed)
		if err != nil {
			if opened != nil {
				t.Fatal("a failure returned bytes")
			}
			return
		}
		if context != testContext || !bytes.Equal(sealed, genuine) || string(opened) != testPlaintext {
			t.Fatalf("opened a value that was not sealed: context %q, %d bytes", context, len(sealed))
		}
	})
}
