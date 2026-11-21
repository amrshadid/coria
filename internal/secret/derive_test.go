package secret

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/amrshadid/coria/internal/fault"
)

// knownDerived holds keys derived from testKey(1) (version 1) and testKey(2)
// (version 2), and the Ed25519 public keys of the audit seeds. They were
// computed outside Go, with Python's hmac and hashlib following RFC 5869 by
// hand and checked equal to the cryptography package's HKDF, and the public
// keys with cryptography's Ed25519PrivateKey.from_private_bytes:
//
//	prk = hmac.new(b"\x00" * 32, key, hashlib.sha256).digest()
//	okm = hmac.new(prk, label + b"\x01", hashlib.sha256).digest()
var knownDerived = []struct {
	version uint32
	purpose Purpose
	key     string
	public  string
}{
	{1, AuditSeal, "f316752c3ec66c6590a0fe8267eaf8cedb2c83eaf5bfad078090b044c54eddad",
		"ede5efcc41058dea1a2a5ef2a82fe7b3fe874350374b6e3e60b8977c3aeeac5f"},
	{1, Backup, "654e9be6913e2a302cc255875b8ba67155b59dac84f08997614887dc349b00ef", ""},
	{2, AuditSeal, "bf19da9d836465f63677b69540d7cabaf88e9eba079bce254ee00e6e0a71bb4c",
		"87e812d262816f79c2dc1a2c6e30a2591d6d94950d164d4db7ce7f2499c95c3a"},
	{2, Backup, "3ce72105588563bd2ebdada5bbf130868118f32efc0a9f7d76e297915ff7fd69", ""},
}

// mustDerive derives p at version from keys and fails the test on an error.
func mustDerive(t *testing.T, keys *Keys, p Purpose, version uint32) *DerivedKey {
	t.Helper()
	d, err := keys.DeriveVersion(p, version)
	if err != nil {
		t.Fatalf("DeriveVersion(%d, %d): %v", p, version, err)
	}
	return d
}

// TestDeriveKnownAnswers guards that Derive is HKDF-SHA-256 as RFC 5869
// defines it, with no salt, the documented labels and 32 bytes of output,
// and that the audit signer's seed gives the Ed25519 key RFC 8032 gives it:
// each value equals one computed independently in Python. A change of label,
// salt, hash or length, which would make every signature and backup made so
// far unverifiable, fails here.
func TestDeriveKnownAnswers(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1)+keyLine(2, 2))
	for _, k := range knownDerived {
		d := mustDerive(t, keys, k.purpose, k.version)
		if got := hex.EncodeToString(d.d.key[:]); got != k.key {
			t.Errorf("version %d %s: derived %s, want %s", k.version, k.purpose.Label(), got, k.key)
		}
		if d.Version() != k.version || d.Purpose() != k.purpose {
			t.Errorf("version %d %s: the key says version %d, purpose %d", k.version, k.purpose.Label(), d.Version(), d.Purpose())
		}
		if k.public == "" {
			continue
		}
		s, err := d.Signer()
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(s.Public()); got != k.public {
			t.Errorf("version %d: public key %s, want %s", k.version, got, k.public)
		}
		if s.KeyVersion() != k.version {
			t.Errorf("version %d: the signer says key version %d", k.version, s.KeyVersion())
		}
	}
}

// TestDeriveIsStableAcrossLoads guards that the same key file loaded twice,
// as by two runs of the server, derives the same keys, so a checkpoint
// signed before a restart verifies after it and a backup opens.
func TestDeriveIsStableAcrossLoads(t *testing.T) {
	path := filepath.Join(privateDir(t), "coria.key")
	writePrivate(t, path, []byte(keyLine(1, 7)+keyLine(3, 8)))
	first, err := LoadKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Purpose{AuditSeal, Backup} {
		for _, v := range []uint32{1, 3} {
			a, b := mustDerive(t, first, p, v), mustDerive(t, second, p, v)
			if *a.d.key != *b.d.key {
				t.Errorf("%s version %d differs between two loads of one file", p.Label(), v)
			}
		}
	}
}

// TestDeriveFollowsSealVersion guards that Derive uses the key file's
// highest version, the one a new record is made with, and DeriveVersion the
// one named.
func TestDeriveFollowsSealVersion(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1)+keyLine(2, 2))
	d, err := keys.Derive(AuditSeal)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version() != 2 || *d.d.key != *mustDerive(t, keys, AuditSeal, 2).d.key {
		t.Errorf("Derive used version %d, want 2", d.Version())
	}
}

// TestPurposesNeverShareAKey guards that the purposes' labels are distinct
// and non-empty, and that no two (purpose, version) pairs of a key file give
// the same key, so a backup key can never sign an audit checkpoint or the
// other way round.
func TestPurposesNeverShareAKey(t *testing.T) {
	seen := map[string]Purpose{}
	for p, label := range labels {
		if label == "" {
			t.Errorf("purpose %d has an empty label", p)
		}
		if q, ok := seen[label]; ok {
			t.Errorf("purposes %d and %d share the label %q", p, q, label)
		}
		seen[label] = p
	}
	keys := mustKeys(t, keyLine(1, 1)+keyLine(2, 2)+keyLine(3, 3))
	derivedKeys := map[[derivedSize]byte]string{}
	for p := range labels {
		for _, v := range []uint32{1, 2, 3} {
			d := mustDerive(t, keys, p, v)
			name := fmt.Sprintf("%s version %d", p.Label(), v)
			if other, ok := derivedKeys[*d.d.key]; ok {
				t.Errorf("%s and %s derive the same key", name, other)
			}
			derivedKeys[*d.d.key] = name
			for _, seed := range []byte{1, 2, 3} {
				if bytes.Equal(d.d.key[:], testKey(seed)) {
					t.Errorf("%s equals an installation key", name)
				}
			}
		}
	}
}

// TestDeriveRefuses guards the refusals: a value that is not a purpose, a
// key version the file does not hold (NotFound, so a caller can say which
// version is missing), and no key at all.
func TestDeriveRefuses(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1))
	for _, p := range []Purpose{0, Backup + 1, 255} {
		if _, err := keys.DeriveVersion(p, 1); fault.KindOf(err) != fault.Internal {
			t.Errorf("purpose %d: err = %v, want Internal", p, err)
		}
		if p.Label() != "" {
			t.Errorf("purpose %d has the label %q", p, p.Label())
		}
	}
	if _, err := keys.DeriveVersion(AuditSeal, 2); fault.KindOf(err) != fault.NotFound {
		t.Errorf("missing version: err = %v, want NotFound", err)
	}
	var none *Keys
	if _, err := none.Derive(AuditSeal); err == nil {
		t.Error("a nil Keys derived a key")
	}
	if _, err := (&Keys{}).Derive(Backup); err == nil {
		t.Error("the zero Keys derived a key")
	}
}

// TestDerivedKeyServesOnlyItsPurpose guards that a key derived for one
// purpose is refused by another's code: a Backup key makes no signer, and an
// AuditSeal key opens no backup file.
func TestDerivedKeyServesOnlyItsPurpose(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1))
	if _, err := mustDerive(t, keys, Backup, 1).Signer(); err == nil {
		t.Error("a Backup key made a signer")
	}
	if _, err := fileAEAD(mustDerive(t, keys, AuditSeal, 1), make([]byte, backupSaltSize)); err == nil {
		t.Error("an AuditSeal key made a backup cipher")
	}
	var none *DerivedKey
	if _, err := none.Signer(); err == nil {
		t.Error("a nil derived key made a signer")
	}
	var noSigner *Signer
	if _, err := noSigner.Sign([]byte("m")); err == nil {
		t.Error("a nil signer signed")
	}
}

// TestSignerSignatureVerifies guards that the signer makes pure Ed25519
// signatures (RFC 8032 5.1.6) that the standard verifier accepts with the
// public key alone, and that it rejects for another message.
func TestSignerSignatureVerifies(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1))
	s, err := mustDerive(t, keys, AuditSeal, 1).Signer()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("checkpoint text")
	sig, err := s.Sign(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(s.Public(), msg, sig) {
		t.Fatalf("a %d-byte signature does not verify", len(sig))
	}
	if ed25519.Verify(s.Public(), []byte("checkpoint texT"), sig) {
		t.Error("the signature verifies for another message")
	}
}
