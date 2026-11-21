package secret

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// knownBackupPlaintext is what knownBackupHex holds.
const knownBackupPlaintext = "a backup made outside Go, in three chunks"

// knownBackupHex is knownBackupPlaintext written as a backup under version 2
// of keyLine(1, 1)+keyLine(2, 2), with 16-byte chunks, once, by
// scripts/check/backup-cipher.py make (HKDF over Python's hmac and hashlib,
// AES-GCM from the cryptography package), not by this package. It is fixed,
// so the fuzz workers, which are separate processes, agree on the one
// genuine file, and so a change to the format that still round-trips in Go
// alone fails here.
const knownBackupHex = "434f5249412d424b010000000200000010452e2a90bb7220e942ac3de361d3dd8f5fd7043fee1d672f0b56eb8e7abd8fef7343724f5ef3d01c7e7880293703877baca0544648fd4ea58ae6e40286b8ba71c33024adbd63c94b8091ed015f5fa2271faa86530ee054b3c829079f81ded7204703737b37aebf73bd1211283622d52e0e5aa82a7b0a7707dc"

// knownBackup returns knownBackupHex as bytes.
func knownBackup(tb testing.TB) []byte {
	tb.Helper()
	b, err := hex.DecodeString(knownBackupHex)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// TestBackupOpensKnownFile guards the format against drift: a backup written
// once by another implementation still opens to its plaintext.
func TestBackupOpensKnownFile(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1)+keyLine(2, 2))
	got, err := readBackup(keys, knownBackup(t))
	if err != nil {
		t.Fatalf("the backup Python wrote does not open: %v", err)
	}
	if string(got) != knownBackupPlaintext {
		t.Fatalf("the backup Python wrote opens to %q", got)
	}
}

// FuzzBackupReader guards the reader against any input: it never panics,
// whatever it returns before an error is a prefix of the genuine plaintext
// (each chunk is authenticated before its bytes are given), and it reads to
// the end only the one genuine file of the seed corpus. Anything else that
// read to the end would be a forgery.
func FuzzBackupReader(f *testing.F) {
	keys, err := parseKeyFile([]byte(keyLine(1, 1) + keyLine(2, 2)))
	if err != nil {
		f.Fatal(err)
	}
	genuine := knownBackup(f)
	f.Add(genuine)
	f.Add(genuine[:len(genuine)-1])
	f.Add(genuine[:backupHeaderSize+smallChunk+gcmTagSize])
	f.Add(append(append([]byte(nil), genuine...), genuine[backupHeaderSize:backupHeaderSize+smallChunk+gcmTagSize]...))
	f.Add(genuine[:backupHeaderSize])
	f.Add([]byte(backupMagic))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, file []byte) {
		got, err := readBackup(keys, file)
		if !bytes.HasPrefix([]byte(knownBackupPlaintext), got) {
			t.Fatalf("returned %d bytes that are not the genuine plaintext's start", len(got))
		}
		if err == nil && (!bytes.Equal(file, genuine) || string(got) != knownBackupPlaintext) {
			t.Fatalf("read to the end a file that was not written: %d bytes", len(file))
		}
	})
}
