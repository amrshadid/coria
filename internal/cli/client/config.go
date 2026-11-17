package client

import (
	"crypto/x509"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
)

// Environment variables the command line reads. They are named here so the
// help text and the tests use the one spelling.
const (
	// EnvDataDir names the data directory whose run/magnetar.sock is the
	// default way to reach the API.
	EnvDataDir = "MAGNETAR_DATA_DIR"
	// EnvServer names the address of a server on the network.
	EnvServer = "MAGNETAR_SERVER"
	// EnvKey holds the key used over the network. It is the only place a key
	// can come from besides a file.
	EnvKey = "MAGNETAR_KEY"
)

// DefaultTimeout is how long one call may take when the person gives no
// --timeout, the same period the contract says the server applies.
const DefaultTimeout = 30 * time.Second

// maxKeyFile bounds the key file that is read: a whole key is under 100 bytes,
// so a larger file is a mistake (a binary, a log) and is refused unread.
const maxKeyFile = 1024

// SocketPath returns where the local socket of a data directory is, as the
// installation's layout fixes it.
func SocketPath(dataDir string) string {
	return filepath.Join(dataDir, "run", "magnetar.sock")
}

// Config says how to reach the API. Exactly one of DataDir and Server is set.
type Config struct {
	// DataDir reaches the API on the local socket of this data directory,
	// where the caller is the local operator and sends no key.
	DataDir string
	// Server is the address of the API on the network, https://host:port, or
	// http:// for a loopback host.
	Server string
	// KeyFile is the path of a file holding the key, for Server. When empty
	// the key is read from the EnvKey variable.
	KeyFile string
	// Getenv reads the environment; os.Getenv when nil. It is a field so a
	// test can pass an environment without changing the process's.
	Getenv func(string) string
	// Timeout bounds each call; DefaultTimeout when zero.
	Timeout time.Duration
	// RootCAs, when set, replaces the system's certificate authorities for
	// the network. It is for tests that serve TLS with a certificate made in
	// the test; no flag sets it.
	RootCAs *x509.CertPool
}

// getenv returns the configured environment reader.
func (c Config) getenv() func(string) string {
	if c.Getenv != nil {
		return c.Getenv
	}
	return os.Getenv
}

// ParseServer checks the address of a server and returns it without a
// trailing slash. https is accepted for any host; http only for a loopback
// host, because a key sent over plain http to another machine can be read on
// the way. An address with a user name or password in it is refused, as that
// is a secret in a place that is logged. No message quotes the address
// itself, which may hold such a secret.
func ParseServer(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return nil, fault.New(fault.Invalid, "the server address is not a URL such as https://host:8443")
	}
	if u.User != nil {
		return nil, fault.New(fault.Invalid, "the server address must not hold a user name or password; the key is read from "+EnvKey+" or --key-file")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fault.New(fault.Invalid, "the server address must not have a query or a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, fault.New(fault.Invalid, "the server address "+u.Scheme+"://"+u.Host+" is refused: http is allowed only for a loopback host (localhost, 127.0.0.1, ::1); use https://")
		}
	default:
		return nil, fault.New(fault.Invalid, "the server address must start with https:// (or http:// for a loopback host)")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// isLoopback reports whether a host names this machine only: the name
// localhost or an address in the loopback range. Any other name could resolve
// anywhere, so it does not count.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ReadKey returns the key to send: the content of keyFile when it is given,
// otherwise the EnvKey variable. Only the key's shape is checked, enough to
// keep a header safe (printable ASCII, no spaces); the server decides whether
// it is good. A message names where the key was looked for and never shows
// any of it.
func ReadKey(keyFile string, getenv func(string) string) (string, error) {
	var key, where string
	if keyFile != "" {
		where = "the key file " + keyFile
		f, err := os.Open(keyFile) // #nosec G304 -- the path is the person's own --key-file
		if err != nil {
			return "", fault.Wrap(fault.Invalid, err, "cannot read the key file "+keyFile)
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
		if err != nil {
			return "", fault.Wrap(fault.Invalid, err, "cannot read the key file "+keyFile)
		}
		if len(b) > maxKeyFile {
			return "", fault.New(fault.Invalid, "the key file "+keyFile+" is larger than a key can be")
		}
		key = strings.TrimSpace(string(b))
	} else {
		where = "the " + EnvKey + " variable"
		key = strings.TrimSpace(getenv(EnvKey))
	}
	if key == "" {
		return "", fault.New(fault.Invalid, "no key: set "+EnvKey+" or pass --key-file PATH; "+where+" is empty")
	}
	for i := range len(key) {
		if key[i] <= ' ' || key[i] > '~' {
			return "", fault.New(fault.Invalid, where+" holds characters a key does not have")
		}
	}
	return key, nil
}
