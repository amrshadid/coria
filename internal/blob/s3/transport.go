package s3

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// AllowPublic is an AllowAddress that passes only addresses on the public
// internet: it refuses loopback, private (RFC 1918 and unique local),
// shared (100.64.0.0/10), link-local (which holds the instance metadata
// services of the clouds), multicast and unspecified addresses. An address
// mapped into IPv6 is judged as the IPv4 address it holds.
func AllowPublic(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// newTransport returns the HTTP transport a store uses. It reads no
// environment, so HTTP_PROXY and its kind change nothing, and its dialer
// asks s.allow about the address of every connection it makes, before the
// connection is opened: Control runs after the name is resolved, with the
// address about to be connected to, so no window lies between the check and
// the connection in which a resolver could answer differently
// (net.Dialer.Control).
func newTransport(s settings) *http.Transport {
	d := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Resolver:  s.resolver,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !s.allow(ap.Addr().Unmap().WithZone("")) {
				return errAddressRefused
			}
			return nil
		},
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.roots},
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
	}
}

// noRedirect makes the HTTP client return a redirect as the answer it is,
// instead of following it to a server the caller never named.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
