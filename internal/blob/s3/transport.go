package s3

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// The special-purpose address blocks AllowPublic refuses. They are the
// entries of the IANA IPv4 Special-Purpose Address Registry
// (www.iana.org/assignments/iana-ipv4-special-registry) and of the IPv6
// Special-Purpose Address Registry
// (www.iana.org/assignments/iana-ipv6-special-registry), whether or not an
// entry is marked globally reachable (an anycast service such as AS112 is
// no storage), with the blocks that are not unicast.
var (
	// refusedV4 is every IPv4 block of the registry, and 224.0.0.0/3,
	// which is multicast (224.0.0.0/4) and the rest of the reserved space
	// (240.0.0.0/4, which holds the broadcast address).
	refusedV4 = mustPrefixes(
		"0.0.0.0/8",       // "This network" (RFC 791)
		"10.0.0.0/8",      // private use (RFC 1918)
		"100.64.0.0/10",   // shared address space (RFC 6598)
		"127.0.0.0/8",     // loopback (RFC 1122)
		"169.254.0.0/16",  // link local, with the clouds' metadata services (RFC 3927)
		"172.16.0.0/12",   // private use (RFC 1918)
		"192.0.0.0/24",    // IETF protocol assignments (RFC 6890)
		"192.0.2.0/24",    // documentation, TEST-NET-1 (RFC 5737)
		"192.31.196.0/24", // AS112-v4 (RFC 7535)
		"192.52.193.0/24", // AMT (RFC 7450)
		"192.88.99.0/24",  // deprecated 6to4 relay anycast (RFC 7526)
		"192.168.0.0/16",  // private use (RFC 1918)
		"192.175.48.0/24", // direct delegation AS112 service (RFC 7534)
		"198.18.0.0/15",   // benchmarking (RFC 2544)
		"198.51.100.0/24", // documentation, TEST-NET-2 (RFC 5737)
		"203.0.113.0/24",  // documentation, TEST-NET-3 (RFC 5737)
		"224.0.0.0/3",     // multicast (RFC 5771), reserved (RFC 1112), broadcast (RFC 919)
	)
	// globalV6 is the part of the IPv6 space allocated for global unicast
	// (2000::/3, RFC 4291); everything outside it is refused, which covers
	// the unspecified and loopback addresses, the IPv4-compatible block
	// (::/96), NAT64 (64:ff9b::/96 and 64:ff9b:1::/48), the discard prefixes
	// (100::/64, 100:0:0:1::/64), unique local (fc00::/7), link local
	// (fe80::/10), site local (fec0::/10), SRv6 (5f00::/16) and multicast
	// (ff00::/8).
	globalV6 = netip.MustParsePrefix("2000::/3")
	// refusedV6 is every block of the IPv6 registry inside 2000::/3.
	refusedV6 = mustPrefixes(
		"2001::/23",         // IETF protocol assignments: Teredo (2001::/32), benchmarking, AMT, AS112, ORCHID (RFC 6890)
		"2001:db8::/32",     // documentation (RFC 3849)
		"2002::/16",         // 6to4, which embeds an IPv4 address (RFC 3056)
		"2620:4f:8000::/48", // direct delegation AS112 service (RFC 7534)
		"3fff::/20",         // documentation (RFC 9637)
	)
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// AllowPublic is an AllowAddress that passes global unicast addresses and
// nothing else: it refuses every block of the IANA special-purpose
// registries (see refusedV4 and refusedV6), the multicast and reserved
// space, and every IPv6 address outside 2000::/3. The IPv6 prefixes that
// embed an IPv4 address (NAT64, 6to4, Teredo, the IPv4-compatible block)
// are refused outright, without judging the address inside: a NAT64
// gateway forwards 64:ff9b::a9fe:a9fe to 169.254.169.254. An address mapped
// into IPv6 (::ffff:a.b.c.d) is judged as the IPv4 address it holds.
func AllowPublic(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	switch {
	case !a.IsValid():
		return false
	case a.Is4():
		for _, p := range refusedV4 {
			if p.Contains(a) {
				return false
			}
		}
		return true
	}
	if !globalV6.Contains(a) {
		return false
	}
	for _, p := range refusedV6 {
		if p.Contains(a) {
			return false
		}
	}
	return true
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
				return ErrAddressNotAllowed
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

// maxAnswer bounds every answer that is not an object's bytes, which the
// driver reads whole and parses: a listing page of 1,000 entries, each a key
// of up to 1,024 bytes and about 512 bytes of markup (modified time, ETag,
// size, class, owner), is about 1.5 MB; a list of 1,000 unfinished uploads
// is of the same size; an error or a completion is a few hundred bytes.
// 4 MiB is more than twice the largest, for escaped characters, and far
// below what a hostile endpoint could otherwise make the process hold.
const maxAnswer = 4 << 20

// errAnswerTooLarge marks an answer longer than maxAnswer.
var errAnswerTooLarge = errors.New("the storage's answer is longer than any answer should be")

// objectStream marks a request whose answer, when it is a success, is the
// bytes of an object, which is streamed to the caller and counted there
// against what was asked. The value is carried in the request's context.
type objectStream struct{}

// limitTransport cuts every answer that is not an object's bytes at
// maxAnswer: it refuses at once an answer that says it is longer, and ends
// one that turns out to be, with errAnswerTooLarge, which the driver treats
// as a malformed answer and does not retry. An error status is never an
// object's bytes, whatever the request.
type limitTransport struct{ rt http.RoundTripper }

func (l limitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := l.rt.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if r.Context().Value(objectStream{}) != nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) {
		return resp, nil
	}
	if r.Method != http.MethodHead && resp.ContentLength > maxAnswer { // A HEAD answer states a length and has no body.
		_ = resp.Body.Close() // The answer is refused whatever closing says.
		return nil, errAnswerTooLarge
	}
	resp.Body = &limitedBody{rc: resp.Body, left: maxAnswer}
	return resp, nil
}

// limitedBody reads at most left bytes and then fails.
type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One byte more tells an answer of exactly the limit from a longer one.
		var one [1]byte
		if n, err := b.rc.Read(one[:]); n > 0 {
			return 0, errAnswerTooLarge
		} else if err != nil {
			return 0, err
		}
		return 0, nil
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// noRedirect makes the HTTP client return a redirect as the answer it is,
// instead of following it to a server the caller never named.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
