package server

import (
	"context"
	"crypto/tls"
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

	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/obs"
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
	// <DataDir>/run/coria.sock.
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
	// coriav1connect.RegisterTenantServiceHandler(s, h). That
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
	// hook, set only by tests before Start, is called at the points named
	// by the point constants (Server.at), to stop a request there.
	hook func(ctx context.Context, point string)

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
		return obs.Counter{Name: "coria_" + name + "_total", Help: help, Value: v}
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
