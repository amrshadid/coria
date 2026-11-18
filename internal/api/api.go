package api

import (
	"context"
	"io"
	"log/slog"

	connect "connectrpc.com/connect/v2"

	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/api/codes"
	"github.com/amrshadid/magnetar/internal/core/settings"
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
	// Settings is the list of settings the server knows. Nil is
	// settings.MustBuiltin(), the list the binary ships; a test passes a
	// table with definitions of its own added, so no setting made for tests
	// ships.
	Settings *settings.Table
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
	known := cfg.Settings
	if known == nil {
		known = settings.MustBuiltin()
	}
	c := &core{access: cfg.Access, reg: cfg.Registry, master: cfg.Master, newID: cfg.NewID, log: log, settings: known}
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
// there is one that carries a caller. A zero Decision is reported as none:
// only access.Check makes one that carries rights.
func DecisionFrom(ctx context.Context) (access.Decision, bool) {
	d, ok := ctx.Value(decisionKey{}).(access.Decision)
	if !ok {
		return access.Decision{}, false
	}
	p := d.Principal()
	if _, isKey := p.Key(); !isKey && !p.IsLocalOperator() {
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
	// settings is the list of settings the server knows.
	settings *settings.Table
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
		e := d.Entry(master.OutcomeFailed, "", access.Facts{})
		c.log.Error("api: internal error", "request_id", e.RequestID, "method", e.Method, "error", err.Error())
		return connect.NewError(connect.CodeInternal, internalMessage)
	case fault.Unavailable:
		e := d.Entry(master.OutcomeFailed, "", access.Facts{})
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

// record hands the audit entry of a request that changed nothing (a read, or
// a request that did not complete) to the access package's audit writer, in
// the one place that chooses its log: the tenant's log when st, the store of
// the tenant the request concerns, is in hand (the contract: "one entry in
// the log of the scope asked for"), and the installation's log otherwise. It
// never blocks and never fails the request: an entry the tenant's log does
// not take is written to the installation's log with access.DetailRerouted,
// and one that cannot be queued is counted. facts is what the handler learned
// after the check.
func (c *core) record(d access.Decision, st tenant.Store, outcome master.Outcome, reason string, facts access.Facts) {
	if st != nil {
		c.access.RecordTo(st, d, outcome, reason, facts)
		return
	}
	c.access.Record(d, outcome, reason, facts)
}

// fail records a request that did not complete in the installation's log,
// and returns its API error. It is for an error no package below has
// recorded, met before any tenant's store was in hand, in a request whose
// facts the check already has.
func (c *core) fail(d access.Decision, err error) error {
	return c.failWith(d, nil, access.Facts{}, err)
}

// failIn is fail for a request into a tenant whose store is in hand: its
// entry goes to the tenant's log.
func (c *core) failIn(d access.Decision, st tenant.Store, err error) error {
	return c.failWith(d, st, access.Facts{}, err)
}

// failWith records a request that did not complete, in the log record
// chooses for st, with the facts the handler learned, and returns its API
// error.
func (c *core) failWith(d access.Decision, st tenant.Store, facts access.Facts, err error) error {
	outcome, reason := outcomeOf(err)
	c.record(d, st, outcome, reason, facts)
	return c.toAPI(d, err)
}

// done records a read that answered, in the log record chooses for st, after
// the read.
func (c *core) done(d access.Decision, st tenant.Store) {
	c.record(d, st, master.OutcomeDone, "", access.Facts{})
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

// reroutedValue is the value of access.DetailRerouted on the entry of a
// request whose tenant's log could not be had at all, so the entry was
// written to the installation's log from the start.
const reroutedValue = "the tenant's database is not ready; written to the installation's log"

// tenantStore returns the store of the tenant named name, for this request,
// after making sure it is the tenant the check allowed: a tenant removed and
// created again under the same name between the check and now has another
// id, and the grant the check read was on the old one. Such a tenant, and one
// the registry or the master no longer holds, is answered as noTenant says;
// to the caller, the tenant it was allowed on is gone. A tenant that is not
// ready is Unavailable.
//
// The store is read before the master's row: a removal takes the row away
// before it unregisters the store, and a creation writes the row before it
// registers one, so a row that still has the checked id means the store read
// before it was that tenant's too.
func (c *core) tenantStore(ctx context.Context, d access.Decision, name string) (tenant.Store, error) {
	st, serr := c.reg.Store(name)
	if fault.KindOf(serr) == fault.NotFound {
		return nil, noTenant(d)
	}
	ref, ok := d.Tenant()
	row, err := c.master.TenantByName(ctx, name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil, noTenant(d)
	case err != nil:
		return nil, err
	case !ok || row.ID != ref.ID:
		return nil, noTenant(d)
	}
	return st, serr
}

// sinkFor returns the log every later record of a request on scope goes to,
// chosen once, as soon as the scope is known to be valid: nil for the
// installation, and for a tenant its store (an allowed request that concerns
// a tenant is recorded in its log, whatever its outcome). When the tenant's
// store cannot be had, the request is recorded here and its error returned:
// in the installation's log, with the tenant's name and, when the tenant is
// not ready, access.DetailRerouted. details are the request's audit details,
// which the rerouted entry keeps.
func (c *core) sinkFor(ctx context.Context, d access.Decision, scope access.Scope, details map[string]string) (tenant.Store, error) {
	if scope.IsInstallation() {
		return nil, nil
	}
	st, err := c.tenantStore(ctx, d, scope.Tenant)
	if err == nil {
		return st, nil
	}
	facts := access.Facts{TenantName: scope.Tenant}
	if fault.KindOf(err) == fault.Unavailable {
		facts.Details = make(map[string]string, len(details)+1)
		for k, v := range details {
			facts.Details[k] = v
		}
		facts.Details[access.DetailRerouted] = reroutedValue
	}
	return nil, c.failWith(d, nil, facts, err)
}
