package api

import (
	"context"
	"io"
	"log/slog"

	connect "connectrpc.com/connect/v2"

	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/api/codes"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/registry"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// RequestIDHeader is the response header that carries the request's id
// (access.proto, "The request id"). The server sets it; a handler that has no
// Decision reads it back to name the request in its log line.
const RequestIDHeader = "Magnetar-Request-Id"

// internalMessage is the whole message of every INTERNAL answer: the contract
// says it tells nothing about the cause, which is in the log.
const internalMessage = "internal error"

// Config is what New needs. Every field but Log is required.
type Config struct {
	// Access is the access package: keys and grants are changed through it,
	// and reads are recorded through its audit writer.
	Access *access.Access
	// Registry holds every tenant's store; tenants are created and removed
	// through it. It seals a tenant's database address with the key file it
	// was given, so this package needs no key file of its own.
	Registry *registry.Registry
	// Master is the master database: tenants, the installation's settings and
	// its audit log are read from it.
	Master master.Store
	// NewID makes a new tenant's id: 1 to 64 of A-Z, a-z, 0-9, "_" and "-",
	// unique among tenants.
	NewID func() string
	// Log receives a line for each internal fault, with the request's id and
	// its cause, and for each read whose audit entry the tenant's log
	// refused. Nil discards.
	Log *slog.Logger
}

// Handlers is the four services of the contract, each an implementation of
// the generated Connect handler interface, ready to register on a
// connect.Server behind the server's access check.
type Handlers struct {
	// Tenants is TenantService.
	Tenants magnetarv1connect.TenantServiceHandler
	// Access is AccessService.
	Access magnetarv1connect.AccessServiceHandler
	// Settings is SettingsService.
	Settings magnetarv1connect.SettingsServiceHandler
	// Audit is AuditService.
	Audit magnetarv1connect.AuditServiceHandler
}

// New returns the four services over cfg. It fails as Internal when a
// required field is missing.
func New(cfg Config) (Handlers, error) {
	if cfg.Access == nil || cfg.Registry == nil || cfg.Master == nil || cfg.NewID == nil {
		return Handlers{}, fault.New(fault.Internal, "api: the access package, the registry, the master and an id source are required")
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c := &core{access: cfg.Access, reg: cfg.Registry, master: cfg.Master, newID: cfg.NewID, log: log}
	return Handlers{
		Tenants:  tenantService{c},
		Access:   accessService{c},
		Settings: settingsService{c},
		Audit:    auditService{c},
	}, nil
}

// Register registers the four services on srv.
func (h Handlers) Register(srv *connect.Server) {
	magnetarv1connect.RegisterTenantServiceHandler(srv, h.Tenants)
	magnetarv1connect.RegisterAccessServiceHandler(srv, h.Access)
	magnetarv1connect.RegisterSettingsServiceHandler(srv, h.Settings)
	magnetarv1connect.RegisterAuditServiceHandler(srv, h.Audit)
}

// decisionKey is the context key of the Decision. Unexported, so only
// WithDecision can set it.
type decisionKey struct{}

// WithDecision returns ctx carrying d, the Decision access.Check made for the
// request. The server calls it once per request, after the check allowed the
// request and before the handler runs.
func WithDecision(ctx context.Context, d access.Decision) context.Context {
	return context.WithValue(ctx, decisionKey{}, d)
}

// DecisionFrom returns the Decision WithDecision put in ctx, and whether
// there is one that carries a caller and an audit entry. A zero Decision is
// reported as none: only access.Check makes one that carries rights.
func DecisionFrom(ctx context.Context) (access.Decision, bool) {
	d, ok := ctx.Value(decisionKey{}).(access.Decision)
	if !ok {
		return access.Decision{}, false
	}
	p := d.Principal()
	if _, isKey := p.Key(); !isKey && !p.IsLocalOperator() {
		return access.Decision{}, false
	}
	if d.Entry(master.OutcomeDone, "").ID == "" {
		return access.Decision{}, false
	}
	return d, true
}

// core is what every service shares.
type core struct {
	access *access.Access
	reg    *registry.Registry
	master master.Store
	newID  func() string
	log    *slog.Logger
}

// decision returns the request's Decision. A request without one reached the
// handler without the server's check: it is refused as INTERNAL, logged, and
// never decided here (doc.go, "What this package must never do").
func (c *core) decision(ctx context.Context, procedure string) (access.Decision, error) {
	if d, ok := DecisionFrom(ctx); ok {
		return d, nil
	}
	requestID := ""
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		requestID = info.ResponseHeader().Get(RequestIDHeader)
	}
	c.log.Error("api: a request reached its handler without an access decision and was refused",
		"method", procedure, "request_id", requestID)
	return access.Decision{}, connect.NewError(connect.CodeInternal, internalMessage)
}

// toAPI returns the API error for err, an error a package below already
// recorded in the audit log or one that needs no entry. An internal fault's
// cause is logged with the request's id and replaced by the fixed message; an
// unavailable one is logged too, since a database that does not answer is
// for the operator to see.
func (c *core) toAPI(d access.Decision, err error) error {
	switch fault.KindOf(err) {
	case fault.Internal:
		e := d.Entry(master.OutcomeFailed, "")
		c.log.Error("api: internal error", "request_id", e.RequestID, "method", e.Method, "error", err.Error())
		return connect.NewError(connect.CodeInternal, internalMessage)
	case fault.Unavailable:
		e := d.Entry(master.OutcomeFailed, "")
		c.log.Warn("api: unavailable", "request_id", e.RequestID, "method", e.Method, "error", err.Error())
	}
	return codes.ToConnect(err)
}

// outcomeOf returns the outcome and the reason an audit entry records for a
// request that did not complete with err. A refusal is refused; anything else
// the caller was allowed to try is failed. The reason is the message the
// caller may see, except for an internal fault, whose cause is only in the
// log, under the same request id.
func outcomeOf(err error) (master.Outcome, string) {
	switch fault.KindOf(err) {
	case fault.Denied:
		return master.OutcomeRefused, fault.Message(err)
	case fault.Internal:
		return master.OutcomeFailed, "internal error; the cause is in the server's log under this request's id"
	}
	return master.OutcomeFailed, fault.Message(err)
}

// record writes the audit entry of a request that ended with outcome, in the
// one place that chooses its log: the tenant's log when st, the store of the
// tenant the request concerns, is in hand (the contract: "one entry in the
// log of the scope asked for"), and the installation's log otherwise,
// through the access package's audit writer, which never blocks or fails. An
// entry the tenant's log does not take goes to the installation's log, with
// a line in the server's log, so recording never fails the request.
func (c *core) record(ctx context.Context, d access.Decision, st tenant.Store, outcome master.Outcome, reason string) {
	if st != nil {
		e := d.Entry(outcome, reason)
		err := st.AppendAudit(ctx, e)
		if err == nil {
			return
		}
		c.log.Warn("api: an audit entry was not written to the tenant's log; it goes to the installation's log",
			"request_id", e.RequestID, "method", e.Method, "kind", fault.KindOf(err).String())
	}
	c.access.Record(d, outcome, reason)
}

// fail records a request that did not complete in the installation's log,
// and returns its API error. It is for an error no package below has
// recorded, met before any tenant's store was in hand.
func (c *core) fail(d access.Decision, err error) error {
	return c.failIn(context.Background(), d, nil, err)
}

// failIn records a request that did not complete in the log record chooses
// for st, and returns its API error.
func (c *core) failIn(ctx context.Context, d access.Decision, st tenant.Store, err error) error {
	outcome, reason := outcomeOf(err)
	c.record(ctx, d, st, outcome, reason)
	return c.toAPI(d, err)
}

// done records a read that answered, in the log record chooses for st, after
// the read.
func (c *core) done(ctx context.Context, d access.Decision, st tenant.Store) {
	c.record(ctx, d, st, master.OutcomeDone, "")
}

// noTenant is the answer for a tenant a lookup after the check did not find
// (it was removed meanwhile, or the registry does not hold it): NOT_FOUND for
// a caller who may list every tenant, and otherwise PERMISSION_DENIED with the
// same message as for no grant, so a later lookup cannot undo what the check
// decided (access.proto, "A tenant that does not exist").
func noTenant(d access.Decision) error {
	if d.Principal().CanListTenants() {
		return fault.New(fault.NotFound, access.MessageNoTenant)
	}
	return fault.New(fault.Denied, access.MessageDenied)
}

// tenantStore returns the store of the tenant named name, for this request.
// A tenant that is not ready is Unavailable; one the registry does not hold
// is answered as noTenant says.
func (c *core) tenantStore(d access.Decision, name string) (tenant.Store, error) {
	st, err := c.reg.Store(name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, noTenant(d)
	}
	return st, err
}
