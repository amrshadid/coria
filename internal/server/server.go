package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/grpchealth/v2"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/obs"
)

// The limits of the server (decision D38, access.proto "Limits"). Each is
// a number the RPC library or net/http would otherwise leave unbounded or
// set for another purpose.
const (
	// MaxMessageBytes is the largest message read or sent: 4 MiB, which is
	// what a stock gRPC client accepts by default (grpc-go's
	// defaultClientMaxReceiveMessageSize), so a stock client never gets an
	// answer it refuses, and what bounds the memory one message can cost.
	MaxMessageBytes = 4 << 20
	// DefaultDeadline is applied to a call whose client sent no deadline,
	// so no call holds a handler, a connection and a database slot
	// forever.
	DefaultDeadline = 30 * time.Second
	// DefaultDrain is how long Stop waits for requests in flight before it
	// cancels them: longer than one call under the default deadline is
	// likely to need, short of what an orchestrator waits before a kill
	// (Kubernetes: 30 seconds).
	DefaultDrain = 20 * time.Second
	// DefaultClose is how long Stop gives the access package to write the
	// audit entries it holds, and the registry to close, after the drain.
	DefaultClose = 5 * time.Second

	// maxHeaderBytes bounds a request's headers (HTTP/1 and HTTP/2): 64
	// KiB. A request needs a few hundred bytes (a key is 61 characters);
	// gRPC's own default for metadata is 8 KiB, and net/http's 1 MiB
	// default would let each connection make the server hold a megabyte
	// before any check.
	maxHeaderBytes = 64 << 10
	// maxConcurrentStreams bounds the requests one HTTP/2 connection may
	// have open at once: 100, the number gRPC servers and the HTTP/2
	// specification's advice use, so one client cannot open thousands of
	// handlers over one connection.
	maxConcurrentStreams = 100
	// readHeaderTimeout bounds the time a client takes to send a request's
	// headers, so a client that opens connections and sends slowly
	// (slowloris) holds each for 10 seconds at most.
	readHeaderTimeout = 10 * time.Second
	// idleTimeout closes a connection that has no request for two minutes.
	idleTimeout = 2 * time.Minute
	// pingTimeout closes an HTTP/2 connection whose peer does not answer a
	// ping within 15 seconds, so a vanished client does not hold a stream.
	pingTimeout = 15 * time.Second
)

// MetricsPattern is the route of the metrics.
const MetricsPattern = "GET /metrics"

// Checker is what the server needs of the access package: the check, the
// recording of an allowed read, the counts it reports in the metrics, and
// Close, which writes the audit entries it holds. *access.Access is one.
type Checker interface {
	Check(ctx context.Context, cred access.Credential, req access.Requirement, facts access.Request) (access.Decision, error)
	Record(d access.Decision, outcome master.Outcome, reason string)
	AuditStats() access.AuditStats
	MarkStats() access.MarkStats
	Close(ctx context.Context) error
}

// Registry is what the server needs of the tenant registry: how many
// tenants are in each state, for the metrics, and Close, at stop. It is an
// interface so the server does not depend on the registry's package.
type Registry interface {
	TenantsByState() map[string]int
	Close(ctx context.Context) error
}

// Config is what New needs. Access and Register are required, and Lock
// unless the server is started with no local socket (tests only).
type Config struct {
	// DataDir is the installation's data directory; the local socket is
	// <DataDir>/run/magnetar.sock.
	DataDir string
	// Lock is the hold on DataDir taken by TakeLock. The server releases it
	// last, after everything else is closed.
	Lock *Lock
	// Listen is the network address, HOST:PORT; empty for no network
	// listener. Without TLSCert and TLSKey only a loopback address is
	// accepted.
	Listen string
	// TLSCert and TLSKey are the PEM files of the network listener's
	// certificate and private key; both or neither.
	TLSCert, TLSKey string
	// NoSocket leaves out the local socket. Only tests set it.
	NoSocket bool

	// Access is the check; the server closes it at stop.
	Access Checker
	// Registry is the tenant registry, or nil; the server closes it at stop.
	Registry Registry
	// Master is closed after the registry, at stop; nil for none.
	Master io.Closer
	// Log receives the server's lines; nil discards them.
	Log *slog.Logger

	// Register registers the API's services on the server's
	// connect.Server, such as
	// magnetarv1connect.RegisterTenantServiceHandler(s, h). That
	// connect.Server is built with the request path's interceptor, so every
	// method registered on it is checked; it is the only connect.Server the
	// server mounts.
	Register func(s *connect.Server)
	// HTTPRoutes are plain HTTP routes besides /metrics, each wrapped in the
	// request path.
	HTTPRoutes []HTTPRoute
	// ToConnect turns an error with a fault kind into the API error the
	// caller is sent: the code of its kind and its message for the caller.
	// Nil is DefaultToConnect.
	ToConnect func(error) *connect.Error

	// DefaultDeadline replaces the 30-second default deadline; zero keeps
	// it. Tests shorten it.
	DefaultDeadline time.Duration
	// Drain and Close replace DefaultDrain and DefaultClose; zero keeps
	// them.
	Drain, Close time.Duration
}

// Server serves the API on the local socket and, when asked, on the
// network. It owns its goroutines: each listener's serve loop, which Stop
// ends and waits for.
type Server struct {
	cfg        Config
	access     Checker
	log        *slog.Logger
	metrics    *obs.Metrics
	toConnect  func(error) *connect.Error
	deadline   time.Duration
	rules      map[string]rule
	registered map[string]bool
	health     *grpchealth.StaticChecker
	cert       *tls.Certificate
	mux        *routeMux
	handler    http.Handler

	stopMu   sync.RWMutex
	stopping bool
	inFlight sync.WaitGroup

	// baseCtx is the context of every request; Stop cancels it when the
	// drain's deadline passes, so a handler still running sees its context
	// end.
	baseCtx    context.Context
	baseCancel context.CancelFunc
	servers    []*http.Server
	netAddr    net.Addr
	socketUser string
	serving    sync.WaitGroup
	serveErrs  chan error
	started    bool
	stopOnce   sync.Once
	stopErr    error
}

// New checks cfg and builds the server's routes. It opens nothing: Start
// listens. It refuses a method registered without an entry in the
// method-to-requirement table, so no method can be served unchecked.
func New(cfg Config) (*Server, error) {
	if cfg.Access == nil || cfg.Register == nil {
		return nil, fault.New(fault.Internal, "server: an access check and the API's services are required")
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return nil, fault.New(fault.Invalid, "server: --tls-cert and --tls-key go together; give both or neither")
	}
	if cfg.Listen == "" && cfg.TLSCert != "" {
		return nil, fault.New(fault.Invalid, "server: --tls-cert and --tls-key need --listen")
	}
	if cfg.Listen != "" {
		if err := checkListen(cfg.Listen, cfg.TLSCert != ""); err != nil {
			return nil, err
		}
	}
	if !cfg.NoSocket && (cfg.DataDir == "" || cfg.Lock == nil) {
		return nil, fault.New(fault.Internal, "server: the local socket needs the data directory and its lock")
	}
	s := &Server{
		cfg:        cfg,
		access:     cfg.Access,
		log:        cfg.Log,
		toConnect:  cfg.ToConnect,
		deadline:   cfg.DefaultDeadline,
		rules:      copyRules(rules),
		registered: map[string]bool{},
		health:     grpchealth.NewStaticChecker(),
		serveErrs:  make(chan error, 2),
		socketUser: selfUser(),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.toConnect == nil {
		s.toConnect = DefaultToConnect
	}
	if s.deadline <= 0 {
		s.deadline = DefaultDeadline
	}
	if cfg.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			// The error names the files and why they do not load; it holds no
			// key material.
			return nil, fault.Wrap(fault.Invalid, err, "server: the TLS certificate and key do not load: "+err.Error())
		}
		s.cert = &cert
	}
	s.metrics = obs.NewMetrics(obs.Sources{
		TenantsByState: s.tenantsByState(),
		Audit: func() obs.Counts {
			a := s.access.AuditStats()
			return obs.Counts{Written: a.Written, Dropped: a.Dropped, Failed: a.Failed}
		},
		Marks: func() obs.Counts {
			m := s.access.MarkStats()
			return obs.Counts{Written: m.Written, Dropped: m.Dropped, Failed: m.Failed}
		},
	})
	if err := s.buildRoutes(); err != nil {
		return nil, err
	}
	s.baseCtx, s.baseCancel = context.WithCancel(context.Background())
	return s, nil
}

// copyRules copies the package's table, so a test can add to one server's
// table without changing another's.
func copyRules(m map[string]rule) map[string]rule {
	out := make(map[string]rule, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// tenantsByState is the metrics' source of tenant states: the registry's,
// or none.
func (s *Server) tenantsByState() func() map[string]int {
	if s.cfg.Registry == nil {
		return nil
	}
	return s.cfg.Registry.TenantsByState
}

// buildRoutes makes the one connect.Server, registers the API's services
// and the health service on it, and mounts it with the metrics and the
// other plain HTTP routes on the server's mux, each through the request
// path.
func (s *Server) buildRoutes() error {
	rpc := connect.NewServer(s.interceptor)
	grpchealth.Register(rpc, s.health)
	s.cfg.Register(rpc)
	// A method nobody registered still passes the request path: an
	// unauthenticated caller is refused before it is told the method does
	// not exist.
	rpc.SetUnknownHandler(func(context.Context, connect.Spec, connect.ServerStream) error {
		return connect.NewError(connect.CodeUnimplemented, "no such method")
	})
	var missing []string
	for spec := range rpc.Specs() {
		s.registered[spec.Procedure] = true
		if _, ok := s.rules[spec.Procedure]; !ok {
			missing = append(missing, spec.Procedure)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fault.New(fault.Internal, "server: methods registered without a requirement in the table: "+joinComma(missing))
	}

	s.mux = newRouteMux()
	connecthttp.Mount(s.mux, rpc,
		connecthttp.WithReadMaxBytes(MaxMessageBytes),
		connecthttp.WithSendMaxBytes(MaxMessageBytes),
	)
	routes := append([]HTTPRoute{{Pattern: MetricsPattern, Need: access.OnInstallation(master.RoleReader), Handler: s.serveMetrics}}, s.cfg.HTTPRoutes...)
	// Every other path still passes the request path, so even a request
	// for nothing is checked before it is told there is nothing.
	routes = append(routes, HTTPRoute{Pattern: "/", Need: access.Authenticated(), Handler: notFound})
	for _, r := range routes {
		if err := s.handleHTTP(r); err != nil {
			return err
		}
	}
	s.handler = s.envelope(s.mux)
	return nil
}

// handleHTTP adds a plain HTTP route, wrapped in the request path. It is
// the only way a plain route reaches the mux.
func (s *Server) handleHTTP(r HTTPRoute) (err error) {
	if r.Handler == nil || r.Pattern == "" {
		return fault.New(fault.Internal, "server: a route needs a pattern and a handler")
	}
	defer func() {
		// http.ServeMux panics on a pattern that is malformed or registered
		// twice; at setup that is a bug to report, not a crash.
		if p := recover(); p != nil {
			err = fault.New(fault.Internal, "server: the route "+r.Pattern+" cannot be registered")
		}
	}()
	s.mux.Handle(r.Pattern, s.middleware(r))
	return nil
}

// notFound answers a path that no route serves: UNIMPLEMENTED to an RPC
// client, as a gRPC server answers a service it does not have, and
// NOT_FOUND to a plain HTTP request.
func notFound(_ context.Context, _ http.ResponseWriter, r *http.Request) error {
	if errorWriter.IsSupported(r) {
		return connect.NewError(connect.CodeUnimplemented, "no such service")
	}
	return fault.New(fault.NotFound, "no such route")
}

// serveMetrics writes the metrics, and records the read in the audit log
// like any other allowed read.
func (s *Server) serveMetrics(ctx context.Context, w http.ResponseWriter, _ *http.Request) error {
	call, _ := CallFrom(ctx)
	w.Header().Set("Content-Type", obs.ContentType)
	err := s.metrics.WriteText(w)
	if call != nil {
		s.access.Record(call.Decision, master.OutcomeDone, "")
	}
	return err
}

// Handler returns the handler every listener serves: the envelope around
// the mux. Tests serve it with httptest.
func (s *Server) Handler() http.Handler { return s.handler }

// Metrics returns the server's metrics.
func (s *Server) Metrics() *obs.Metrics { return s.metrics }

// Start listens on the local socket and, when asked, on the network, and
// serves until Stop. It returns once every listener is open, or the first
// error, having closed what it opened.
func (s *Server) Start() error {
	if s.started {
		return fault.New(fault.Internal, "server: started twice")
	}
	s.started = true
	type opened struct {
		ln    net.Listener
		srv   *http.Server
		tlsOn bool
	}
	var all []opened
	closeAll := func() {
		for _, o := range all {
			_ = o.ln.Close()
		}
	}
	if !s.cfg.NoSocket {
		ln, err := listenSocket(s.cfg.DataDir)
		if err != nil {
			return err
		}
		pl := &peerListener{Listener: ln, self: s.socketUser, peerUser: peerUser, log: s.log}
		srv := s.newHTTPServer(false)
		srv.ConnContext = connContext
		all = append(all, opened{ln: pl, srv: srv})
		s.log.Info("server: listening on the local socket", "path", SocketPath(s.cfg.DataDir), "peer_check", peerCheck, "directory", dirGuard)
	}
	if s.cfg.Listen != "" {
		ln, err := net.Listen("tcp", s.cfg.Listen)
		if err != nil {
			closeAll()
			return fault.Wrap(fault.Unavailable, err, "server: cannot listen on "+s.cfg.Listen)
		}
		srv := s.newHTTPServer(s.cert != nil)
		if s.cert != nil {
			// ServeTLS adds the ALPN protocols that srv.Protocols allows (h2,
			// http/1.1) to a copy of this configuration.
			srv.TLSConfig = tlsConfig(*s.cert)
		}
		all = append(all, opened{ln: ln, srv: srv, tlsOn: s.cert != nil})
		s.netAddr = ln.Addr()
		s.log.Info("server: listening on the network", "address", ln.Addr().String(), "tls", s.cert != nil)
	}
	for _, o := range all {
		s.servers = append(s.servers, o.srv)
		s.serving.Add(1)
		go func() {
			defer s.serving.Done()
			var err error
			if o.tlsOn {
				err = o.srv.ServeTLS(o.ln, "", "")
			} else {
				err = o.srv.Serve(o.ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.serveErrs <- err
			}
		}()
	}
	return nil
}

// newHTTPServer returns an http.Server for one listener with the limits
// above. The local socket and a loopback listener without TLS speak HTTP/2
// without TLS (h2c, with prior knowledge, which gRPC clients use) and
// HTTP/1.1; the standard library has done h2c since Go 1.24
// (http.Protocols.SetUnencryptedHTTP2), so golang.org/x/net is not needed.
// A TLS listener speaks HTTP/2 and HTTP/1.1. There is no read or write
// timeout for a whole request: it would cut a long stream; the deadline of
// each call does that job.
func (s *Server) newHTTPServer(tlsOn bool) *http.Server {
	var p http.Protocols
	p.SetHTTP1(true)
	if tlsOn {
		p.SetHTTP2(true)
	} else {
		p.SetUnencryptedHTTP2(true)
	}
	return &http.Server{
		Handler:           s.handler,
		Protocols:         &p,
		MaxHeaderBytes:    maxHeaderBytes,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams: maxConcurrentStreams,
			PingTimeout:          pingTimeout,
		},
		BaseContext: func(net.Listener) context.Context { return s.baseCtx },
		ErrorLog:    slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
}

// Addr returns the network listener's address once Start has opened it,
// or nil; a Listen with port 0 learns its port here.
func (s *Server) Addr() net.Addr { return s.netAddr }

// Errors returns a channel that receives an error when a listener stops
// serving by itself, which a caller treats like a stop signal.
func (s *Server) Errors() <-chan error { return s.serveErrs }

// Stop stops the server and closes what it was given, in order:
//
//  1. new requests are refused with UNAVAILABLE and the health service
//     answers NOT_SERVING;
//  2. the listeners close and the requests in flight have until the drain
//     deadline to finish; then their contexts are canceled and their
//     connections closed;
//  3. the access package is closed, which writes the audit entries it
//     holds;
//  4. the tenant registry, then the master database, are closed;
//  5. the lock on the data directory is released.
//
// Every step runs even when one before it failed; the errors are joined.
// Stop may be called more than once; later calls return the first result.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() { s.stopErr = s.stop() })
	return s.stopErr
}

// stop does Stop's work, once.
func (s *Server) stop() error {
	s.stopMu.Lock()
	s.stopping = true
	s.stopMu.Unlock()
	s.health.SetStatus("", grpchealth.StatusNotServing)

	drain := orDefault(s.cfg.Drain, DefaultDrain)
	closeWithin := orDefault(s.cfg.Close, DefaultClose)
	var errs []error

	ctx, cancel := context.WithTimeout(context.Background(), drain)
	var wg sync.WaitGroup
	for _, srv := range s.servers {
		wg.Go(func() {
			if err := srv.Shutdown(ctx); err != nil {
				// The deadline passed with requests still running: end them.
				s.baseCancel()
				_ = srv.Close()
			}
		})
	}
	wg.Wait()
	cancel()
	drained := make(chan struct{})
	go func() {
		s.inFlight.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(drain):
		s.baseCancel()
		errs = append(errs, fault.New(fault.Internal, "server: requests still running after the drain deadline"))
		<-drained
	}
	s.baseCancel()
	s.serving.Wait()

	closeCtx, closeCancel := context.WithTimeout(context.Background(), closeWithin)
	defer closeCancel()
	if err := s.access.Close(closeCtx); err != nil {
		errs = append(errs, fault.Wrap(fault.Internal, err, "server: the audit entries were not all written before the deadline"))
	}
	if s.cfg.Registry != nil {
		if err := s.cfg.Registry.Close(closeCtx); err != nil {
			errs = append(errs, fault.Wrap(fault.Internal, err, "server: closing the tenant registry"))
		}
	}
	if s.cfg.Master != nil {
		if err := s.cfg.Master.Close(); err != nil {
			errs = append(errs, fault.Wrap(fault.Internal, err, "server: closing the master database"))
		}
	}
	if err := s.cfg.Lock.Release(); err != nil {
		errs = append(errs, fault.Wrap(fault.Internal, err, "server: releasing the lock"))
	}
	a := s.access.AuditStats()
	s.log.Info("server: stopped", "audit_written", a.Written, "audit_dropped", a.Dropped, "audit_failed", a.Failed)
	return errors.Join(errs...)
}

// orDefault returns d, or def when d is not positive.
func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// joinComma joins names with commas.
func joinComma(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// DefaultToConnect is the mapping of fault kinds to API codes the server
// uses until package internal/api/codes is merged, when Config.ToConnect
// becomes codes.ToConnect. It is the same table: each kind to the one code
// access.proto names for it, and the caller sees only fault.Message, never
// the error's cause.
func DefaultToConnect(err error) *connect.Error {
	var code connect.Code
	switch fault.KindOf(err) {
	case fault.Invalid:
		code = connect.CodeInvalidArgument
	case fault.Unauthenticated:
		code = connect.CodeUnauthenticated
	case fault.Denied:
		code = connect.CodePermissionDenied
	case fault.NotFound:
		code = connect.CodeNotFound
	case fault.Conflict:
		code = connect.CodeAlreadyExists
	case fault.Unavailable:
		code = connect.CodeUnavailable
	default:
		code = connect.CodeInternal
	}
	return connect.NewError(code, fault.Message(err))
}
