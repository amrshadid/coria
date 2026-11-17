package secret

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"strconv"
	"testing"
)

// testKey returns a fixed, synthetic 32-byte key that differs for each seed,
// so a test knows the key bytes it later searches for.
func testKey(seed byte) []byte {
	key := make([]byte, keySize)
	for i := range key {
		key[i] = seed*31 + byte(i)*7 + 1
	}
	return key
}

// keyLine returns one key file line for version with testKey(seed).
func keyLine(version uint32, seed byte) string {
	return keyLinePrefix + " " + strconv.FormatUint(uint64(version), 10) + " " +
		base64.StdEncoding.EncodeToString(testKey(seed)) + "\n"
}

// writePrivate writes data to a new file at path that only the current user
// may open, through the same function CreateKeyFile uses, so the file passes
// LoadKeyFile's permission check on every system. os.WriteFile would not: on
// Windows its file inherits the directory's access list.
func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := createPrivate(path)
	if err != nil {
		t.Fatalf("createPrivate: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// loadText writes text as a private key file in a new directory and loads it.
func loadText(t *testing.T, text string) (*Keys, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "magnetar.key")
	writePrivate(t, path, []byte(text))
	return LoadKeyFile(path)
}

// mustKeys loads text as a key file and fails the test if it does not load.
func mustKeys(t *testing.T, text string) *Keys {
	t.Helper()
	keys, err := loadText(t, text)
	if err != nil {
		t.Fatalf("LoadKeyFile: %v", err)
	}
	return keys
}

// mustSeal seals plaintext for context and fails the test on an error.
func mustSeal(t *testing.T, keys *Keys, context string, plaintext []byte) []byte {
	t.Helper()
	sealed, err := keys.Seal(context, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return sealed
}

// containsAny reports whether s contains any of the needles, ignoring empty
// ones, and returns the first it found.
func containsAny(s string, needles ...[]byte) ([]byte, bool) {
	for _, n := range needles {
		if len(n) > 0 && bytes.Contains([]byte(s), n) {
			return n, true
		}
	}
	return nil, false
}
