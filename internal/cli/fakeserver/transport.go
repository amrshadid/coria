package fakeserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

// handler returns the fake as an HTTP handler, the way the real server mounts
// its services.
func (s *Server) handler() http.Handler {
	srv := connect.NewServer(s.interceptor)
	s.Register(srv)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	return mux
}

// serve runs an HTTP server on a listener until the test ends, and fails the
// test if the server stops for any reason but being closed.
func serve(tb testing.TB, srv *http.Server, ln net.Listener, tlsOn bool) {
	tb.Helper()
	// A client that refuses the certificate makes the server log a handshake
	// error; that is the test's intent, so it is not printed.
	srv.ErrorLog = log.New(io.Discard, "", 0)
	done := make(chan error, 1)
	go func() {
		if tlsOn {
			done <- srv.ServeTLS(ln, "", "")
		} else {
			done <- srv.Serve(ln)
		}
	}()
	tb.Cleanup(func() {
		_ = srv.Close()
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			tb.Errorf("the fake server stopped: %v", err)
		}
	})
}

// ServeUnix serves the fake on the local socket of a new data directory, with
// HTTP/2 without TLS, and returns the directory. The socket is at
// run/magnetar.sock, where the installation's layout puts it. The directory is
// short and under the system's temporary directory, because a socket path has
// a small limit that a test's own long directory name could pass.
func (s *Server) ServeUnix(tb testing.TB) (dataDir string) {
	tb.Helper()
	dir, err := os.MkdirTemp("", "mgt")
	if err != nil {
		tb.Fatalf("making a data directory: %v", err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Mkdir(filepath.Join(dir, "run"), 0o700); err != nil {
		tb.Fatalf("making run/: %v", err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "run", "magnetar.sock"))
	if err != nil {
		tb.Fatalf("listening on a unix socket: %v", err)
	}
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	serve(tb, &http.Server{Handler: s.handler(), Protocols: p, ReadHeaderTimeout: 10 * time.Second}, ln, false)
	return dir
}

// ServeTLS serves the fake over TLS on the loopback address with a certificate
// made here for it, and returns its https address and a pool that trusts that
// certificate. A client with the system's authorities does not trust it.
func (s *Server) ServeTLS(tb testing.TB) (addr string, roots *x509.CertPool) {
	tb.Helper()
	cert, pool := selfSigned(tb)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listening on loopback: %v", err)
	}
	srv := &http.Server{
		Handler:           s.handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	serve(tb, srv, ln, true)
	return "https://" + ln.Addr().String(), pool
}

// ServeHTTP serves the fake over plain http on the loopback address, the way
// a server behind a TLS proxy on the same machine would be reached, and
// returns its address.
func (s *Server) ServeHTTP(tb testing.TB) (addr string) {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listening on loopback: %v", err)
	}
	serve(tb, &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}, ln, false)
	return "http://" + ln.Addr().String()
}

// selfSigned makes a certificate valid for the loopback names and a pool that
// trusts exactly it.
func selfSigned(tb testing.TB) (tls.Certificate, *x509.CertPool) {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("making a key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "magnetar test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatalf("making a certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatalf("parsing the certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}
