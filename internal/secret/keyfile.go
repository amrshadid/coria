package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amrshadid/magnetar/internal/fault"
)

// keyLinePrefix starts every line of a key file. It names the line's format,
// so a later format can sit beside it in the same file.
const keyLinePrefix = "magnetar-key-v1"

// keySize is the length of a key in bytes: AES-256 takes a 256-bit key.
const keySize = 32

// maxKeyFileSize bounds how much of a key file is read. A real one holds a
// line of 63 bytes per key version; a file larger than this is not a key file,
// and reading it whole would let a mistaken path exhaust memory.
const maxKeyFileSize = 64 << 10

// Keys is a loaded key file: every key version it holds, ready to open, and
// the highest version, which seals. Its zero value holds no key and seals and
// opens nothing.
//
// Keys prints as a fixed text whatever verb formats it, so a log line or an
// error that includes it by mistake cannot carry the keys.
type Keys struct {
	aeads       map[uint32]cipher.AEAD
	sealVersion uint32
}

// Format writes a fixed text in place of the keys, for every verb, including
// %#v and %+v, which would otherwise print the cipher's unexported fields.
func (k Keys) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "secret.Keys{"+strconv.Itoa(len(k.aeads))+" versions}")
}

// Versions returns how many key versions k holds.
func (k *Keys) Versions() int {
	return len(k.aeads)
}

// SealVersion returns the key version Seal uses: the highest in the file.
func (k *Keys) SealVersion() uint32 {
	return k.sealVersion
}

// CreateKeyFile writes a new key file at path holding one random key, version
// 1. It never replaces a file that exists: losing the key that sealed the
// secrets loses the secrets, so an existing file is a Conflict.
//
// The file is written in full to a temporary file in the same directory,
// created with the narrowest permission the system has (see perm_unix.go and
// perm_windows.go), and only then given its name, so a crash leaves either
// no key file or a whole one, never a short one that would be loaded later.
func CreateKeyFile(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return existsError(path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be checked")
	}

	key := make([]byte, keySize)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return fault.Wrap(fault.Internal, err, "secret: no random key could be made")
	}
	line := []byte(keyLinePrefix + " 1 " + base64.StdEncoding.EncodeToString(key) + "\n")
	defer clear(line)

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fault.Wrap(fault.Internal, err, "secret: no random name could be made")
	}
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(suffix))

	f, err := createPrivate(tmp)
	if err != nil {
		return fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be created")
	}
	// The temporary file is removed on every path; once it has been linked to
	// its final name, removing it removes only the temporary name.
	defer func() { _ = os.Remove(tmp) }()

	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be written")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be written")
	}
	if err := f.Close(); err != nil {
		return fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be written")
	}

	// A hard link is a rename that refuses to replace: os.Rename replaces an
	// existing file on every system, so a file made at path since the check
	// above would be lost. Link fails with fs.ErrExist instead, on Linux,
	// macOS and Windows (NTFS) alike, and the file keeps the permission it was
	// created with, because it is the same file under a second name.
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return existsError(path)
		}
		return fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be created")
	}
	if err := syncDir(dir); err != nil {
		return fault.Wrap(fault.Internal, err, "secret: the directory of the key file "+path+" cannot be synced")
	}
	return nil
}

// existsError is the refusal to replace an existing key file.
func existsError(path string) error {
	return fault.New(fault.Conflict, "secret: the key file "+path+" exists, and a key file is never replaced")
}

// LoadKeyFile reads the key file at path. It refuses a file that other users
// may open (with the command that fixes it in the message), a file over
// 64 KiB, and any line that does not follow the format in the package
// documentation.
//
// The permission is checked on the open file, not on the path, so the file
// checked is the file read.
func LoadKeyFile(path string) (*Keys, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the operator's own configuration, read on purpose.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fault.Wrap(fault.NotFound, err, "secret: there is no key file at "+path)
		}
		return nil, fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be opened")
	}
	defer func() { _ = f.Close() }()

	if err := checkPrivate(f, path); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize+1))
	defer clear(data)
	if err != nil {
		return nil, fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be read")
	}
	if len(data) > maxKeyFileSize {
		return nil, fault.New(fault.Invalid, "secret: "+path+" is larger than any key file ("+strconv.Itoa(maxKeyFileSize)+" bytes at most)")
	}
	keys, err := parseKeyFile(data)
	if err != nil {
		// The parser's message already says which line and why; wrapping
		// the same error as the cause would only repeat it in the log.
		return nil, fault.New(fault.KindOf(err), "secret: the key file "+path+" is not valid: "+fault.Message(err))
	}
	return keys, nil
}

// parseKeyFile reads the text of a key file. Its errors name the line and the
// field at fault, and never quote a line, since a line holds a key.
func parseKeyFile(data []byte) (*Keys, error) {
	text := string(data)
	if text == "" {
		return nil, fault.New(fault.Invalid, "it is empty")
	}
	lines := strings.Split(text, "\n")
	// A final line feed ends the last line; it does not start an empty one.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	keys := &Keys{aeads: make(map[uint32]cipher.AEAD, len(lines))}
	for i, line := range lines {
		version, key, err := parseKeyLine(strings.TrimSuffix(line, "\r"))
		if err != nil {
			return nil, fault.New(fault.Invalid, "line "+strconv.Itoa(i+1)+": "+fault.Message(err))
		}
		if _, ok := keys.aeads[version]; ok {
			clear(key)
			return nil, fault.New(fault.Invalid, "line "+strconv.Itoa(i+1)+": version "+strconv.FormatUint(uint64(version), 10)+" appears twice")
		}
		aead, err := newAEAD(key)
		clear(key)
		if err != nil {
			return nil, fault.Wrap(fault.Internal, err, "line "+strconv.Itoa(i+1)+": the key cannot be used")
		}
		keys.aeads[version] = aead
		keys.sealVersion = max(keys.sealVersion, version)
	}
	return keys, nil
}

// parseKeyLine reads one line: the prefix, the version and the key, each
// written in exactly one way. Every field is compared with its canonical
// spelling, because base64 decoding skips line breaks and integer parsing
// accepts leading zeros, and a file read two ways by two programs is a file
// whose meaning is not fixed.
func parseKeyLine(line string) (uint32, []byte, error) {
	fields := strings.Split(line, " ")
	if len(fields) != 3 {
		return 0, nil, fault.New(fault.Invalid, "a line must be \""+keyLinePrefix+" <version> <key>\", with one space between fields")
	}
	if fields[0] != keyLinePrefix {
		return 0, nil, fault.New(fault.Invalid, "a line must start with \""+keyLinePrefix+"\"")
	}
	v, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil || v == 0 || strconv.FormatUint(v, 10) != fields[1] {
		return 0, nil, fault.New(fault.Invalid, "the version must be a whole number from 1 to 4294967295, without leading zeros")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(fields[2])
	if err != nil || len(key) != keySize || base64.StdEncoding.EncodeToString(key) != fields[2] {
		clear(key)
		return 0, nil, fault.New(fault.Invalid, "the key must be "+strconv.Itoa(keySize)+" bytes in standard base64 with padding")
	}
	return uint32(v), key, nil
}

// newAEAD returns AES-256-GCM under key, with a random 96-bit nonce made by
// the standard library for every seal and carried in front of the
// ciphertext. The standard library makes the nonce, rather than this
// package, so no code here can reuse one.
func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}
