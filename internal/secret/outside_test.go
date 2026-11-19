package secret

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pythonEnv names the Python interpreter, with the cryptography package
// installed, that TestSealOpensInPython runs. make check-seal sets it.
const pythonEnv = "MAGNETAR_CHECK_SEAL_PYTHON"

// TestSealOpensInPython guards the sealed format against another
// implementation: scripts/check/seal.py, written from the package
// documentation with Python's cryptography package (OpenSSL's AES-GCM), opens
// a value this package sealed and refuses it under another context or with a
// byte changed; then this package opens a value Python sealed, under an older
// key version than the one Go seals with, and refuses it under another
// context. It runs only when MAGNETAR_CHECK_SEAL_PYTHON names an interpreter,
// as make check-seal and CI do, because the other tests must not need Python.
func TestSealOpensInPython(t *testing.T) {
	// Unset means the outside check was not asked for, so the test skips.
	// Set, even to nothing, means it was, and then a missing interpreter is a
	// failure: a check that was asked for and quietly did not run would be
	// reported as passed.
	python, asked := os.LookupEnv(pythonEnv)
	if !asked {
		t.Skip(pythonEnv + " is not set; run make check-seal")
	}
	if python == "" {
		t.Fatal(pythonEnv + " is set but empty: name a Python interpreter that has the cryptography package")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "check", "seal.py"))
	if err != nil {
		t.Fatal(err)
	}

	keyFile := filepath.Join(privateDir(t), "magnetar.key")
	writePrivate(t, keyFile, []byte(keyLine(1, 1)+keyLine(2, 2)))
	keys, err := LoadKeyFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(testPlaintext + " ünïcödé")
	goSealed := mustSeal(t, keys, testContext, plaintext)
	if v := binary.BigEndian.Uint32(goSealed[1:5]); v != 2 {
		t.Fatalf("Go sealed with version %d, want 2", v)
	}

	// -I keeps the interpreter from loading modules from the current
	// directory or the environment, so the script runs against the installed
	// cryptography package and nothing else.
	args := []string{"-I", script, keyFile, testContext,
		hex.EncodeToString(goSealed), hex.EncodeToString(plaintext), "1"}
	cmd := exec.Command(python, args...) //nolint:gosec // G702: the interpreter is the one the person running the test named.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("seal.py: %v\n%s%s", err, out, stderr.String())
	}

	var pySealed []byte
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if h, ok := strings.CutPrefix(line, "value "); ok {
			if pySealed, err = hex.DecodeString(h); err != nil {
				t.Fatalf("seal.py printed a value that is not hex: %v", err)
			}
			continue
		}
		t.Logf("python: %s", line)
	}
	if pySealed == nil {
		t.Fatalf("seal.py printed no sealed value:\n%s", out)
	}
	if pySealed[0] != formatV1 || binary.BigEndian.Uint32(pySealed[1:5]) != 1 {
		t.Fatalf("Python's value has header %x, want 01 00000001", pySealed[:5])
	}
	opened, err := keys.Open(testContext, pySealed)
	if err != nil {
		t.Fatalf("the value Python sealed does not open in Go: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatal("the value Python sealed opens in Go to a different plaintext")
	}
	t.Logf("go: opened the %d-byte value Python sealed with key version 1", len(pySealed))
	if _, err := keys.Open(testContext+"x", pySealed); err == nil {
		t.Fatal("the value Python sealed opens in Go under another context")
	}
	t.Log("go: refused it under another context")
}
