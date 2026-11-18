package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/grpchealth/v2"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/obs"
)

// The limits of the server (access.proto, "Limits"). Each is a number the RPC
// library or net/http would otherwise leave unbounded or set for another
// purpose.
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
	// MaxDeadline is the longest any call may run, whatever deadline its
	// client asks for: 5 minutes, as this version has no long call (no
	// download, no stream). Without it, a client that sends a long deadline,
	// or a handler that waits, holds a connection, a goroutine and a file
	// descriptor as long as it likes. A client's shorter deadline is kept.
	MaxDeadline = 5 * time.Minute
	// BodyReadTimeout bounds the time to read a request's body until the
	// check has allowed the request: 10 seconds, ample for a message of at
	// most 4 MiB on any working link. A method whose requirement depends on
	// the request reads its first message before the check, and without
	// this a caller with no key could trickle a body and hold the request
	// unchecked; when it runs out the caller is refused (unauthenticated,
	// denied) or told its request was invalid, in the usual order.
	BodyReadTimeout = 10 * time.Second
	// DefaultDrain is how long Stop waits for requests in flight before it
	// cancels them: longer than one call under the default deadline is
	// likely to need, and with the grace and the closes below it keeps the
	// whole stop under the 30 seconds an orchestrator waits before a kill
	// (Kubernetes' default grace period).
	DefaultDrain = 15 * time.Second
	// StopGrace is how long Stop waits, after canceling the requests still
	// running at the drain's end, for their handlers to return. A handler
	// that ignores its context is then given up on, and the close sequence
	// goes on without it.
	StopGrace = 2 * time.Second
	// DefaultClose is how long Stop gives each of the access package (to
	// write the audit entries it holds), the registry and the master to
	// close. The worst case of a whole stop is DefaultDrain + StopGrace +
	// 3 x DefaultClose = 15 + 2 + 12 = 29 seconds.
	DefaultClose = 4 * time.Second

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

// Checker is what the server needs of the access package: the judgment of a
// credential before routing, the check, the recording of an allowed read and
// of a request no check took up, the counts it reports in the metrics, and
// Close, which writes the audit entries it holds. *access.Access is one.
type Checker interface {
	Authenticate(ctx context.Context, cred access.Credential, facts access.Request) (access.Credential, error)
	Check(ctx context.Context, cred access.Credential, req access.Requirement, facts access.Request) (access.Decision, error)
	Record(d access.Decision, outcome master.Outcome, reason string, facts access.Facts)
	RecordUnchecked(cred access.Credential, facts access.Request, reason string)
	AuditStats() access.AuditStats
	MarkStats() access.MarkStats
	Close(ctx context.Context) error
}

// Registry is what the server needs of the tenant registry: Close, at stop,
// within the close deadline. *registry.Registry is one; it is an interface
// so the server does not depend on the registry's package.
type Registry interface {
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
	// TenantsByState returns how many tenants are in each state, for
	// /metrics; nil leaves the family out.
	TenantsByState func() map[string]int
	// Master is closed after the registry, at stop; nil for none.
	Master io.Closer
	// Log receives the server's lines; nil discards them.
	Log *slog.Logger

	// Register registers the API's services on the server's
	// connect.Server, such as
	// magnetarv1connect.RegisterTenantServiceHandler(s, h). That
	// connect.Server is built with the request path's interceptor, so every
	// method registered on it is checked; it is the only connect.Server the
	// server mounts. Every method registered must be known to Methods.
	Register func(s *connect.Server)
	// Methods is the method-to-requirement table (package internal/api's).
	// Nil knows no method: then only the health service and methods nobody
	// registered are served, and New refuses any registered method.
	Methods Methods
	// WithDecision hands each handler the check's Decision in its context;
	// nil leaves it to CallFrom.
	WithDecision WithDecision
	// HTTPRoutes are plain HTTP routes besides /metrics, each wrapped in the
	// request path.
	HTTPRoutes []HTTPRoute

	// DefaultDeadline, MaxDeadline and BodyReadTimeout replace the
	// constants of the same names; zero keeps them. Tests shorten them.
	DefaultDeadline, MaxDeadline, BodyReadTimeout time.Duration
	// Drain, Grace and Close replace DefaultDrain, StopGrace and
	// DefaultClose; zero keeps them.
	Drain, Grace, Close time.Duration
}

// Server serves the API on the local socket and, when asked, on the
// network. It owns its goroutines: each listener's serve loop, which Stop
// ends and waits for.
type Server struct {
	cfg          Config
	access       Checker
	log          *slog.Logger
	metrics      *obs.Metrics
	deadline     time.Duration
	maxDeadline  time.Duration
	bodyRead     time.Duration
	methods      Methods
	withDecision WithDecision
	health       *grpchealth.StaticChecker
	cert         *tls.Certificate
	mux          *routeMux
	handler      http.Handler
	// procs maps each served procedure, "/package.Service/Method", to its
	// handler from the one checked connect.Server; the router dispatches a
	// call by exact match against it and against plain.
	procs map[string]http.Handler
	// plain maps the path of each plain HTTP route to its pattern.
	plain map[string]string

	stopMu    sync.RWMutex
	stopping  bool
	inFlight  sync.WaitGroup
	inFlightN atomic.Int64

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
		cfg:          cfg,
		access:       cfg.Access,
		log:          cfg.Log,
		deadline:     orDefault(cfg.DefaultDeadline, DefaultDeadline),
		maxDeadline:  orDefault(cfg.MaxDeadline, MaxDeadline),
		bodyRead:     orDefault(cfg.BodyReadTimeout, BodyReadTimeout),
		methods:      cfg.Methods,
		withDecision: cfg.WithDecision,
		health:       grpchealth.NewStaticChecker(),
		serveErrs:    make(chan error, 2),
		socketUser:   selfUser(),
	}
	if s.methods == nil {
		s.methods = noMethods{}
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
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
	s.metrics = obs.NewMetrics(obs.Sources{TenantsByState: cfg.TenantsByState, Counters: s.counters})
	if err := s.buildRoutes(); err != nil {
		return nil, err
	}
	s.baseCtx, s.baseCancel = context.WithCancel(context.Background())
	return s, nil
}

// counters are the counts the access package keeps, as metrics: what
// became of the audit entries of refusals and reads, and of the keys'
// last-use marks.
func (s *Server) counters() []obs.Counter {
	a, m := s.access.AuditStats(), s.access.MarkStats()
	c := func(name, help string, v uint64) obs.Counter {
		return obs.Counter{Name: "magnetar_" + name + "_total", Help: help, Value: v}
	}
	return []obs.Counter{
		c("audit_entries_written", "Audit entries of refusals and reads that a log took.", a.Written),
		c("audit_entries_failed", "Audit entries a log refused one by one, or that were not valid.", a.Failed),
		c("audit_entries_dropped_refusals", "Audit entries of refusals dropped because their queue was full or the server was closing.", a.DroppedRefusals),
		c("audit_entries_dropped_records", "Audit entries of allowed requests dropped because their queue was full or the server was closing.", a.DroppedRecords),
		c("audit_entries_dropped_writes", "Audit entries the writer gave up on: the database did not answer after the retries, or the close deadline passed.", a.DroppedWrites),
		c("audit_entries_rerouted", "Audit entries for a tenant's log written to the master's because the tenant's did not answer.", a.Rerouted),
		c("audit_entries_notices", "Audit entries the writer wrote to say entries were dropped.", a.Notices),
		c("key_last_use_marks_written", "Last-use marks of keys the database took.", m.Written),
		c("key_last_use_marks_dropped", "Last-use marks of keys dropped because their queue was full or the server was closing.", m.Dropped),
		c("key_last_use_marks_failed", "Last-use marks of keys the database refused or failed.", m.Failed),
	}
}

// buildRoutes makes the one connect.Server, registers the API's services
// and the health service on it, and records each of its procedures, and
// each plain HTTP route (the metrics and Config.HTTPRoutes, each in the
// middleware), as an exact path the router serves. The handler of every
// listener is then the envelope, around the front step, around the router:
// no route is chosen before the caller is known.
func (s *Server) buildRoutes() error {
	rpc := connect.NewServer(s.interceptor)
	// Check only: grpchealth serves Watch when its checker is also a
	// Watcher, and the wrapper hides that, so Watch answers UNIMPLEMENTED.
	// Watch is a stream that never ends, and it is public: any caller could
	// hold as many open as the system's file limit allows.
	grpchealth.Register(rpc, checkOnly{s.health})
	s.cfg.Register(rpc)
	var missing []string
	for spec := range rpc.Specs() {
		if _, ok := s.ruleFor(spec.Procedure); !ok {
			missing = append(missing, spec.Procedure)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fault.New(fault.Internal, "server: methods registered without a requirement in the table: "+joinComma(missing))
	}

	s.mux = newRouteMux()
	s.procs = make(map[string]http.Handler)
	s.plain = make(map[string]string)
	connecthttp.Mount(procedureMux{s}, rpc,
		connecthttp.WithReadMaxBytes(MaxMessageBytes),
		connecthttp.WithSendMaxBytes(MaxMessageBytes),
	)
	routes := append([]HTTPRoute{{Pattern: MetricsPattern, Need: access.OnInstallation(master.RoleReader), Handler: s.serveMetrics}}, s.cfg.HTTPRoutes...)
	for _, r := range routes {
		if err := s.handleHTTP(r); err != nil {
			return err
		}
	}
	s.handler = s.envelope(s.front(http.HandlerFunc(s.route)))
	return nil
}

// procedureMux is what connecthttp.Mount registers on. It keeps each
// procedure's handler for the router and records its pattern, and drops the
// handler Mount adds for each service's subtree ("/package.Service/"): that
// is the RPC library's handler of unknown procedures, which would dispatch a
// path that is not exactly a procedure (a percent-encoded one, say) to a
// served handler with the request's stream shape instead of the method's,
// and refuse calls to unknown names on HTTP/1.1 before any check. A call to
// a procedure nothing serves is answered by the server itself (notServed).
type procedureMux struct{ s *Server }

// Handle keeps h as the handler of the procedure pattern, or drops it when
// pattern is a subtree.
func (m procedureMux) Handle(pattern string, h http.Handler) {
	if strings.HasSuffix(pattern, "/") {
		return
	}
	m.s.procs[pattern] = h
	m.s.mux.Handle(pattern, h)
}

// handleHTTP adds a plain HTTP route, wrapped in the request path. It is
// the only way a plain route is served: the router reaches the mux only for
// a path recorded here. The pattern must be one exact path (plainPattern),
// not a procedure's and not one already taken.
func (s *Server) handleHTTP(r HTTPRoute) (err error) {
	if r.Handler == nil || r.Pattern == "" {
		return fault.New(fault.Internal, "server: a route needs a pattern and a handler")
	}
	path, ok := plainPattern(r.Pattern)
	if !ok {
		return fault.New(fault.Internal, "server: the route "+r.Pattern+" is not one exact path")
	}
	if _, taken := s.procs[path]; taken {
		return fault.New(fault.Internal, "server: the route "+r.Pattern+" is a procedure's path")
	}
	if _, taken := s.plain[path]; taken {
		return fault.New(fault.Internal, "server: the route "+r.Pattern+" has a path already taken")
	}
	defer func() {
		// http.ServeMux panics on a pattern that is malformed or registered
		// twice; at setup that is a bug to report, not a crash.
		if p := recover(); p != nil {
			err = fault.New(fault.Internal, "server: the route "+r.Pattern+" cannot be registered")
		}
	}()
	s.mux.Handle(r.Pattern, s.middleware(r))
	s.plain[path] = r.Pattern
	return nil
}

// serveMetrics writes the metrics. An allowed scrape writes no audit entry:
// the metrics are counts with no tenant's content, and a scraper reads them
// every few seconds. A refused scrape is audited by the check, like any
// refusal.
func (s *Server) serveMetrics(_ context.Context, w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", obs.ContentType)
	return s.metrics.WriteText(w)
}

// checkOnly is a health checker that is not a grpchealth.Watcher, so the
// health service serves Check and answers Watch UNIMPLEMENTED.
type checkOnly struct{ c grpchealth.Checker }

// Check answers the health of the server, or of a service by name.
func (h checkOnly) Check(ctx context.Context, req *grpchealth.CheckRequest) (*grpchealth.CheckResponse, error) {
	return h.c.Check(ctx, req)
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
			return fault.Wrap(fault.Unavailable, err, "server: cannot listen on "+s.cfg.Listen+": "+err.Error())
		}
		ln = newLimitListener(ln, maxNetworkConns())
		srv := s.newHTTPServer(s.cert != nil)
		if s.cert != nil {
			// ServeTLS adds the ALPN protocols that srv.Protocols allows (h2,
			// http/1.1) to a copy of this configuration.
			srv.TLSConfig = tlsConfig(*s.cert)
		}
		all = append(all, opened{ln: ln, srv: srv, tlsOn: s.cert != nil})
		s.netAddr = ln.Addr()
		s.log.Info("server: listening on the network", "address", ln.Addr().String(), "tls", s.cert != nil, "max_connections", maxNetworkConns())
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
		// "OPTIONS *" is passed to the handler, so it meets the front step
		// like every other request instead of net/http's own answer.
		DisableGeneralOptionsHandler: true,
		BaseContext:                  func(net.Listener) context.Context { return s.baseCtx },
		ErrorLog:                     slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
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
//  1. new requests are refused with UNAVAILABLE, and the health service's
//     Check, still answered, says NOT_SERVING;
//  2. the listeners close and the requests in flight have until the drain
//     deadline to finish; then their contexts are canceled and their
//     connections closed, and their handlers have the grace to return;
//  3. a handler still running then (one that ignores its context) is given
//     up on: Stop goes on and its error says how many were abandoned;
//  4. the access package is closed, which writes the audit entries it
//     holds; then the tenant registry, then the master database, each
//     within its own close deadline, enforced even if it ignores its
//     context;
//  5. the lock on the data directory is released.
//
// The whole of Stop is bounded: drain + grace + 3 x close, 29 seconds with
// the defaults. Every step runs even when one before it failed; the errors
// are joined. Stop may be called more than once; later calls return the
// first result.
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
	grace := orDefault(s.cfg.Grace, StopGrace)
	closeWithin := orDefault(s.cfg.Close, DefaultClose)
	var errs []error

	drainEnd := time.Now().Add(drain)
	ctx, cancel := context.WithDeadline(context.Background(), drainEnd)
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
	case <-time.After(time.Until(drainEnd)):
	}
	s.baseCancel()
	select {
	case <-drained:
	case <-time.After(grace):
		// A handler that ignores its context would hold Stop, and with it
		// the audit writer, the registry, the master and the lock, for ever.
		// It is given up on; its goroutine stays until the process ends.
		n := s.inFlightN.Load()
		s.log.Error("server: requests did not end after the drain and the grace and were abandoned", "abandoned", n)
		errs = append(errs, fault.New(fault.Internal, fmt.Sprintf("server: %d requests did not end after the drain and the grace and were abandoned", n)))
	}
	s.serving.Wait()

	// Each part gets its own close deadline, so one that uses all of its
	// time does not take the next one's: the access package first (its
	// audit writer may still write to a tenant's log through the
	// registry), then the registry, then the master both of them use. A
	// close that does not return within its deadline, context or not, is
	// given up on in the same way.
	closeEach := func(what string, close func(context.Context) error) {
		ctx, cancel := context.WithTimeout(context.Background(), closeWithin)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- close(ctx) }()
		var err error
		select {
		case err = <-done:
		case <-time.After(closeWithin + closeWithin/4):
			err = fault.New(fault.Internal, "it did not return within its close deadline and was abandoned")
		}
		if err != nil {
			s.log.Error("server: closing "+what+" failed", "error", err)
			errs = append(errs, fault.Wrap(fault.Internal, err, "server: closing "+what))
		}
	}
	closeEach("the access check and its audit writer", s.access.Close)
	if s.cfg.Registry != nil {
		closeEach("the tenant registry", s.cfg.Registry.Close)
	}
	if s.cfg.Master != nil {
		closeEach("the master database", func(context.Context) error { return s.cfg.Master.Close() })
	}
	if err := s.cfg.Lock.Release(); err != nil {
		errs = append(errs, fault.Wrap(fault.Internal, err, "server: releasing the lock"))
	}
	a := s.access.AuditStats()
	s.log.Info("server: stopped", "audit_written", a.Written, "audit_failed", a.Failed,
		"audit_dropped_refusals", a.DroppedRefusals, "audit_dropped_records", a.DroppedRecords,
		"audit_dropped_writes", a.DroppedWrites, "audit_dropped", a.Dropped())
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
