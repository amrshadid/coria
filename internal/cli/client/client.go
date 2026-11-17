package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"time"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/api/codes"
	"github.com/amrshadid/magnetar/internal/fault"
)

// RequestIDHeader is the response header in which the server returns the id
// it made for the request, on success and on error alike; the contract says a
// client cannot set it.
const RequestIDHeader = "Magnetar-Request-Id"

// maxMessage is the contract's limit on a message, 4 MB. Setting it here too
// means an answer larger than the server may send is refused rather than read
// into memory.
const maxMessage = 4 << 20

// Client holds the four service clients of the API over one transport.
type Client struct {
	// Tenants is the TenantService.
	Tenants magnetarv1connect.TenantServiceClient
	// Access is the AccessService: keys, grants and WhoAmI.
	Access magnetarv1connect.AccessServiceClient
	// Settings is the SettingsService.
	Settings magnetarv1connect.SettingsServiceClient
	// Audit is the AuditService.
	Audit magnetarv1connect.AuditServiceClient

	timeout time.Duration
	target  string
	http    *http.Client
}

// New makes a client for a configuration. It does not connect: the first call
// does, so a command that fails on its own arguments never touches the
// network.
func New(cfg Config) (*Client, error) {
	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, fault.New(fault.Invalid, "the timeout must not be negative")
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	var (
		hc       *http.Client
		base     string
		target   string
		interc   []connect.ClientInterceptor
		haveData = cfg.DataDir != ""
		haveSrv  = cfg.Server != ""
	)
	switch {
	case haveData == haveSrv:
		return nil, fault.New(fault.Invalid, "give either a data directory (--data-dir or "+EnvDataDir+") or a server (--server or "+EnvServer+")")
	case haveData:
		target = SocketPath(cfg.DataDir)
		hc = unixClient(target)
		// The host is a placeholder: the dialer ignores it and always opens
		// the socket. HTTP/2 without TLS is what the server speaks there.
		base = "http://magnetar.local"
	default:
		u, err := ParseServer(cfg.Server)
		if err != nil {
			return nil, err
		}
		key, err := ReadKey(cfg.KeyFile, cfg.getenv())
		if err != nil {
			return nil, err
		}
		target = u.Scheme + "://" + u.Host
		hc = networkClient(u.Scheme == "https", cfg.RootCAs)
		base = u.String()
		interc = append(interc, bearer(key))
	}
	c := connect.NewClient(
		connecthttp.NewTransport(hc, base,
			connecthttp.WithReadMaxBytes(maxMessage),
			connecthttp.WithSendMaxBytes(maxMessage)),
		interc...)
	return &Client{
		Tenants:  magnetarv1connect.NewTenantServiceClient(c),
		Access:   magnetarv1connect.NewAccessServiceClient(c),
		Settings: magnetarv1connect.NewSettingsServiceClient(c),
		Audit:    magnetarv1connect.NewAuditServiceClient(c),
		timeout:  timeout,
		target:   target,
		http:     hc,
	}, nil
}

// Close releases the connections the client holds.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// bearer sends the key as RFC 6750 says, in the Authorization header. It is an
// interceptor so that no call site can forget it or write it to a log.
func bearer(key string) connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if info, ok := connect.CallInfoForClientContext(ctx); ok {
				info.RequestHeader().Set("Authorization", "Bearer "+key)
			}
			return next(ctx, spec)
		}
	}
}

// unixClient is an HTTP client whose every connection is the unix socket at
// path, speaking HTTP/2 without TLS (prior knowledge), as the server's local
// listener does. AF_UNIX works on Windows too, so the same code serves both.
func unixClient(path string) *http.Client {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
			Protocols: protocols,
		},
		CheckRedirect: noRedirect,
	}
}

// networkClient is an HTTP client for the network: TLS 1.2 or later with the
// system's authorities (or roots, in a test), HTTP/2 where the server offers
// it. useTLS is false only for a loopback http address.
func networkClient(useTLS bool, roots *x509.CertPool) *http.Client {
	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: true,
	}
	if useTLS {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	return &http.Client{Transport: tr, CheckRedirect: noRedirect}
}

// noRedirect refuses to follow a redirect: the Authorization header would go
// with it to wherever it points, and the API does not redirect.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// CallError is the error of a failed call. It is a fault error (so
// fault.KindOf and the exit code work through it) that also knows which call
// failed and the request id the server made for it.
type CallError struct {
	// What names the call, such as "tenant create", for the message.
	What string
	// RequestID is the server's id for the request; empty when no answer came.
	RequestID string
	err       error
}

// Error returns the message and, when there is one, the request id.
func (e *CallError) Error() string {
	msg := e.What + ": " + fault.Message(e.err)
	if e.RequestID != "" {
		msg += " (request id " + e.RequestID + ")"
	}
	return msg
}

// Unwrap returns the fault error, so fault.KindOf sees its kind.
func (e *CallError) Unwrap() error { return e.err }

// Call runs one API call with the client's deadline and returns its answer. On
// failure it returns a *CallError holding the kind from the table in the
// codes package, the server's message and the request id from the response
// header. The id is read from the response whether the call succeeded or not,
// because the contract has the server send it either way.
func Call[T any](ctx context.Context, c *Client, what string, fn func(context.Context) (*T, error)) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	ctx, info := connect.NewClientContext(ctx)
	res, err := fn(ctx)
	if err == nil {
		return res, nil
	}
	return nil, &CallError{
		What:      what,
		RequestID: info.ResponseHeader().Get(RequestIDHeader),
		err:       c.classify(err),
	}
}

// classify turns the error of a call into a fault error. An API error maps
// through the codes table. Anything else never reached an answer: a refused
// connection, a certificate the system does not trust, a deadline. Each is
// said in words, with the target but never with a key or a header.
func (c *Client) classify(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.IsRemote() {
		return codes.FromConnect(ce)
	}
	var (
		unknown x509.UnknownAuthorityError
		invalid x509.CertificateInvalidError
		host    x509.HostnameError
		op      *net.OpError
	)
	switch {
	case errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &host):
		return fault.Wrap(fault.Unavailable, err, "refusing to connect to "+c.target+": its certificate is not trusted by this system ("+tlsReason(err)+")")
	case errors.Is(err, context.DeadlineExceeded):
		return fault.Wrap(fault.Unavailable, err, "no answer from "+c.target+" within "+c.timeout.String()+" (see --timeout)")
	case errors.As(err, &op):
		return fault.Wrap(fault.Unavailable, err, "cannot reach the server at "+c.target+": "+op.Err.Error())
	}
	if ce != nil {
		return codes.FromConnect(ce)
	}
	return fault.Wrap(fault.Unavailable, err, "the call to "+c.target+" failed: "+err.Error())
}

// tlsReason is the short text of a certificate failure, without the long
// prefix the standard library puts in front of it.
func tlsReason(err error) string {
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	switch {
	case errors.As(err, &unknown):
		return "signed by an unknown authority"
	case errors.As(err, &host):
		return "not valid for the address used"
	case errors.As(err, &invalid):
		return invalid.Error()
	}
	return err.Error()
}
