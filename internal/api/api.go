package api

import (
	"context"
	"log/slog"
	"sync"
	"time"

	connect "connectrpc.com/connect/v2"

	"github.com/amrshadid/coria/gen/coria/v1/coriav1connect"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/api/codes"
	"github.com/amrshadid/coria/internal/checkedread"
	"github.com/amrshadid/coria/internal/core/settings"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/place"
	"github.com/amrshadid/coria/internal/receive/stow"
	"github.com/amrshadid/coria/internal/registry"
	"github.com/amrshadid/coria/internal/secret"
	"github.com/amrshadid/coria/internal/tenant"
)

// RequestIDHeader is the response header that carries the request's id
// (access.proto, "The request id"). The server sets it; a handler that has no
// Decision reads it back to name the request in its log line.
const RequestIDHeader = "Coria-Request-Id"

// internalMessage is the whole message of every INTERNAL answer: the contract
// says it tells nothing about the cause, which is in the log.
const internalMessage = "internal error"

// Config is what New needs. Access, Registry, Master, NewID and Keys are
// required; every other field has a default.
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
	// Keys is the installation key file. An S3 storage's secret access key
	// is sealed with it, bound to the storage, before it reaches the
	// tenant's store, and opened with it only to test the storage.
	Keys *secret.Keys
	// Now is the server's clock, which the time of every change made
	// through the tenant's store is read from. Nil is time.Now.
	Now func() time.Time
	// Receivers answers a receiver's health. Nil answers every receiver
	// down, as a server that runs none (notStarted).
	Receivers Receivers
	// Incoming removes files from the incoming area after a discard or a
	// rejection is recorded. Nil removes nothing and leaves them to the
	// reception pipeline (leaveForPipeline).
	Incoming Incoming
	// Headers reads a quarantined file's header. Nil reads none (noHeaders).
	Headers Headers
	// Live answers what the server holds in memory of a tenant, for a
	// status. Nil holds nothing (noLive).
	Live Live
	// Feeds streams tenants' events for Watch. Nil answers every Watch
	// unavailable.
	Feeds Feeds
	// SenderHealths answers a sender's health, and is told when a tenant's
	// senders change if it implements SenderChanges. Nil answers every
	// sender down when disabled and unknown otherwise, as a server that
	// sends nothing yet (unknownSenders).
	SenderHealths SenderHealths
	// SenderTests tries a sender's peer for TestSender. Nil answers every
	// test unavailable (noSenderTests).
	SenderTests SenderTester
	// STOW finds the running stow receiver that takes a key's STOW-RS
	// requests. Nil takes none: every such request is refused.
	STOW *stow.Directory
}

// Handlers is the services of the contract this package serves, each an
// implementation of the generated Connect handler interface, ready to
// register on a connect.Server behind the server's access check.
type Handlers struct {
	// Tenants is TenantService.
	Tenants coriav1connect.TenantServiceHandler
	// Access is AccessService.
	Access coriav1connect.AccessServiceHandler
	// Settings is SettingsService.
	Settings coriav1connect.SettingsServiceHandler
	// Audit is AuditService.
	Audit coriav1connect.AuditServiceHandler
	// Storages is StorageService.
	Storages coriav1connect.StorageServiceHandler
	// Receivers is ReceiverService.
	Receivers coriav1connect.ReceiverServiceHandler
	// Receptions is ReceptionService.
	Receptions coriav1connect.ReceptionServiceHandler
	// Quarantine is QuarantineService.
	Quarantine coriav1connect.QuarantineServiceHandler
	// Monitor is MonitorService.
	Monitor coriav1connect.MonitorServiceHandler
	// Metadata is MetadataService.
	Metadata coriav1connect.MetadataServiceHandler
	// Catalog is CatalogService.
	Catalog coriav1connect.CatalogServiceHandler
	// Data is DataService.
	Data coriav1connect.DataServiceHandler
	// Senders is SenderService.
	Senders coriav1connect.SenderServiceHandler
	// Jobs is JobService.
	Jobs coriav1connect.JobServiceHandler

	// c is what the services share, kept for the test seams
	// (export_test.go).
	c *core
}

// New returns the services over cfg. It fails as Internal when a required
// field is missing.
func New(cfg Config) (Handlers, error) {
	if cfg.Access == nil || cfg.Registry == nil || cfg.Master == nil || cfg.NewID == nil || cfg.Keys == nil {
		return Handlers{}, fault.New(fault.Internal, "api: the access package, the registry, the master, an id source and the installation key file are required")
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	known := cfg.Settings
	if known == nil {
		known = settings.MustBuiltin()
	}
	c := &core{
		access: cfg.Access, reg: cfg.Registry, master: cfg.Master, newID: cfg.NewID, log: log, settings: known,
		keys: cfg.Keys, now: cfg.Now, receivers: cfg.Receivers, incoming: cfg.Incoming, headers: cfg.Headers,
		live: cfg.Live, feeds: cfg.Feeds, stowReceivers: cfg.STOW, senders: cfg.SenderHealths, senderTests: cfg.SenderTests,
	}
	if c.live == nil {
		c.live = noLive{}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.receivers == nil {
		c.receivers = notStarted{}
	}
	if c.senders == nil {
		c.senders = unknownSenders{}
	}
	if c.senderTests == nil {
		c.senderTests = noSenderTests{}
	}
	if c.incoming == nil {
		c.incoming = leaveForPipeline{}
	}
	if c.headers == nil {
		c.headers = noHeaders{}
	}
	return Handlers{
		Tenants:    tenantService{c},
		Access:     accessService{c},
		Settings:   settingsService{c},
		Audit:      auditService{c},
		Storages:   storageService{c},
		Receivers:  receiverService{c},
		Receptions: receptionService{c},
		Quarantine: quarantineService{c},
		Monitor:    monitorService{c},
		Metadata:   metadataService{c},
		Catalog:    catalogService{c},
		Data:       dataService{c},
		Senders:    senderService{c},
		Jobs:       jobService{c},
		c:          c,
	}, nil
}

// Register registers the services on srv.
func (h Handlers) Register(srv *connect.Server) {
	coriav1connect.RegisterTenantServiceHandler(srv, h.Tenants)
	coriav1connect.RegisterAccessServiceHandler(srv, h.Access)
	coriav1connect.RegisterSettingsServiceHandler(srv, h.Settings)
	coriav1connect.RegisterAuditServiceHandler(srv, h.Audit)
	coriav1connect.RegisterStorageServiceHandler(srv, h.Storages)
	coriav1connect.RegisterReceiverServiceHandler(srv, h.Receivers)
	coriav1connect.RegisterReceptionServiceHandler(srv, h.Receptions)
	coriav1connect.RegisterQuarantineServiceHandler(srv, h.Quarantine)
	coriav1connect.RegisterMonitorServiceHandler(srv, h.Monitor)
	coriav1connect.RegisterMetadataServiceHandler(srv, h.Metadata)
	coriav1connect.RegisterCatalogServiceHandler(srv, h.Catalog)
	coriav1connect.RegisterDataServiceHandler(srv, h.Data)
	coriav1connect.RegisterSenderServiceHandler(srv, h.Senders)
	coriav1connect.RegisterJobServiceHandler(srv, h.Jobs)
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
	// keys seals and opens an S3 storage's secret access key.
	keys *secret.Keys
	// now is the server's clock.
	now func() time.Time
	// receivers answers a receiver's health.
	receivers Receivers
	// senders answers a sender's health.
	senders SenderHealths
	// senderTests tries a sender's peer.
	senderTests SenderTester
	// incoming carries a release, a retry and the removal of bytes to the
	// tenant's reception pipeline.
	incoming Incoming
	// headers reads a quarantined file's header.
	headers Headers
	// live answers what is held in memory of a tenant.
	live Live
	// feeds streams tenants' events.
	feeds Feeds
	// stowReceivers finds the stow receiver that takes a key's requests.
	stowReceivers *stow.Directory
	// reads counts the streams of ReadFiles and the downloads open now,
	// for their bounds.
	reads checkedread.Slots
	// places orders the changes that depend on where the installation lets
	// data go (its roots, endpoints, ports and bind addresses) against the
	// changes of those settings: a storage or a receiver is checked and
	// stored under a read lock, a setting that could take away what one
	// uses is checked and stored under the write lock, so neither passes the
	// other unseen. One process serves an installation (the lock on its
	// data directory), so a lock in memory is enough.
	places sync.RWMutex
	// seams are set by tests only (export_test.go).
	seams struct {
		// probe, when set, stands for place.SystemProbe in the choice of a
		// free port.
		probe place.Probe
	}
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

// changing returns the Decision of a request that changes something, after
// the access package has checked the caller's key again (access.Confirm),
// before the handler validates the request or reads anything it names. So a
// key that lapsed since the check is answered UNAUTHENTICATED or
// PERMISSION_DENIED whatever it asked, as the contract's order of checks
// says, and never told whether what it named exists or whether its request
// was valid. The change itself checks the key again in its transaction. A
// refusal is recorded by the access package.
func (c *core) changing(ctx context.Context, procedure string) (access.Decision, error) {
	d, err := c.decision(ctx, procedure)
	if err != nil {
		return d, err
	}
	if d, err = c.access.Confirm(ctx, d); err != nil {
		return d, c.toAPI(d, err)
	}
	return d, nil
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
// error. A change the master refused because the caller's key may no longer
// act is answered as the check answers such a key (access.RefusedAtChange)
// and recorded as refused in the installation's log, whatever st is, as the
// check's own refusals are: a refused caller causes no write in a tenant's
// database.
func (c *core) failWith(d access.Decision, st tenant.Store, facts access.Facts, err error) error {
	if reason, refusal := access.RefusedAtChange(err); refusal != nil {
		c.record(d, nil, master.OutcomeRefused, reason, facts)
		return c.toAPI(d, refusal)
	}
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
//
// A request whose requirement was admin on the installation, though it names
// a tenant (a change of a setting the installation sets on each tenant), has
// no tenant in its Decision: its caller may act on every tenant, so the
// tenant that holds the name now is the one it acts on, and one that holds
// none is answered NotFound, as to any caller who may list every tenant.
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
	case !ok && d.Principal().Role(master.Installation).Includes(master.RoleAdmin):
		return st, serr
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
