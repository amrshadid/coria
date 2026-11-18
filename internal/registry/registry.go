package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/secret"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// Driver opens the tenant databases of one database type. Package driver
// implements it for each type (driver.SQLiteTenants); the server gives the
// registry one per type it supports, by the name a tenant's row records.
//
// An error from Open or Location must never contain the address, nor any
// part of it (host, user, password), in its message or its cause: the
// registry shows a driver error's message (fault.Message) in a tenant's state
// reason, its errors and its log, and never its Error() text, but a cause
// may still reach a log a person reads elsewhere. The message should say what
// went wrong in words an operator can act on, since it is all they see.
type Driver interface {
	// Open opens the database of the tenant whose id is id and whose name is
	// name, at address, the plaintext of the tenant's sealed address. The
	// store binds its cursors to id (tenant.Store). It does not migrate. It
	// returns Invalid for an address or a database file this driver refuses,
	// Unavailable for a database that may answer later, and Internal for the
	// rest.
	Open(ctx context.Context, id, name, address string) (tenant.Store, error)
	// Location says where the tenant's data is, holding no user and no
	// password: a file's path, or host:port/database. It returns Invalid
	// for an address this driver does not take, and is called before a
	// tenant is created, to refuse such an address before anything is
	// written. The registry replaces its error with a fixed message of the
	// same kind.
	Location(name, address string) (string, error)
}

// StateChangeInterval is the shortest time between two writes to the master
// of a tenant's change between ready and unreachable, or of a change of
// reason alone. A database that flaps writes one change a minute, not one
// per check. It bounds the writes to the master only: State answers the
// current view at once.
const StateChangeInterval = time.Minute

// The methods of the audit entries the registry writes on its own. Their
// actor is the system: no key, no local user, no address, and the detail
// "actor" set to "system".
const (
	// SystemMethod records a change of a tenant's state.
	SystemMethod = "magnetar.registry/SetTenantState"
	// StartMethod records a creation started: the tenant's row written as
	// upgrading. It carries the request's id; the request's own entry is
	// written when the creation settles.
	StartMethod = "magnetar.registry/CreateTenantStarted"
)

// The details of the registry's audit entries.
const (
	DetailActor        = "actor"
	DetailDatabaseType = "database_type"
	DetailLocation     = "data_location"
	// DetailRequestEntry, on a StartMethod entry, is the id of the request's
	// own entry, written when the creation settles.
	DetailRequestEntry = "request_entry"
	// DetailCreationRequest, on the state change that finishes a creation a
	// crash interrupted, is the id of the request that began it.
	DetailCreationRequest = "creation_request"
	// DetailUndone, on a failed creation's request entry, is "true": the row
	// the creation wrote was removed again.
	DetailUndone = "undone"
	// ActorSystem is the value of DetailActor on the registry's entries.
	ActorSystem = "system"
)

// Config is what the registry needs. Every field but Log and NewID is
// required: the clock, the interval and the deadlines are given, never
// package variables, so a test sets them.
type Config struct {
	// Master is the master database, where tenants and their states are.
	Master master.Store
	// Keys opens each tenant's sealed address.
	Keys *secret.Keys
	// Drivers opens tenant databases, by database type.
	Drivers map[string]Driver
	// Now is the clock: audit times, creation times, the time a state was
	// reached, and the once-a-minute limit on state writes are read from it.
	Now func() time.Time
	// Interval is how often every tenant is checked, and the registry
	// reconciled with the master, after Start.
	Interval time.Duration
	// OpenTimeout is the deadline of one tenant's open, ping and migration.
	OpenTimeout time.Duration
	// PingTimeout is the deadline of one check of a tenant whose store is
	// open. It is short, and takes no opening slot, so a ready tenant that
	// goes down is seen soon whatever else is being opened.
	PingTimeout time.Duration
	// MaxOpening bounds how many tenants are opened at once, so a start with
	// many tenants does not open them all in the same instant.
	MaxOpening int
	// Log receives a line for each change of a tenant's state and each
	// failure, with no address in it. Nil logs nothing.
	Log *slog.Logger
	// NewID makes the ids of the registry's audit entries. Nil uses 128
	// random bits in hex.
	NewID func() string
}

// State is a tenant's state as the registry sees it now.
type State struct {
	// State is the tenant's state.
	State master.TenantState
	// Reason says why, in the registry's words and its driver's safe
	// message; empty when the tenant is ready.
	Reason string
	// Since is when the tenant reached this state and reason, by
	// Config.Now; the zero time before its first check.
	Since time.Time
}

// Removed says where a removed tenant's data was left.
type Removed struct {
	// Driver is the database type.
	Driver string
	// Location is where the data is, with no user and no password; empty
	// when it cannot be told (the address did not open).
	Location string
}

// Registry holds one open store per tenant. It is safe for concurrent use.
type Registry struct {
	cfg Config
	log *slog.Logger

	// entries maps a tenant's name to its entry. The map is never changed
	// once stored: a change copies it under mu and stores the copy, so
	// Store reads it with one atomic load and no lock.
	entries atomic.Pointer[map[string]*entry]
	mu      sync.Mutex
	closed  atomic.Bool
	started bool // under mu

	// slots bounds the opens running at once; pings take none.
	slots chan struct{}

	// names serializes Create and Remove of the same name, so a removal
	// cannot slip between a creation's reading of the master and its
	// registering of the tenant. listing is held for writing while Start or
	// a reconciliation reads the tenants and registers them, and for reading
	// by Create and Remove, so neither changes a tenant between the two.
	names       nameLocks
	listing     sync.RWMutex
	reconciling atomic.Bool

	// ctx is canceled by Close; every entry's context derives from it.
	ctx    context.Context
	cancel context.CancelFunc
	// wg counts every goroutine the registry starts, and every Start,
	// Create and Remove in progress, so Close waits for all of them.
	wg sync.WaitGroup
	// inflight counts the driver calls running now, which Close reports
	// when it has to abandon them.
	inflight atomic.Int64

	// afterRow, when set by a test, runs in Create after the tenant's row
	// is written and before its database is opened; an error from it ends
	// Create there, as a crash would.
	afterRow func() error
	// fileLimit returns the process's limit on open files, or -1 when it
	// cannot be read; a test replaces it.
	fileLimit func() int64
}

// entry is one tenant.
type entry struct {
	id, name, driver string
	sealed           []byte

	// view is what Store and State read: the state and the store a request
	// gets.
	view atomic.Pointer[view]

	// ctx ends when the tenant is removed or the registry closed, which
	// ends an open in progress for it.
	ctx    context.Context
	cancel context.CancelFunc

	// opening is set while a driver call that outlived its deadline is
	// still running, so a check does not start a second one beside it.
	opening atomic.Bool

	// op orders the changes of this tenant: an open, a check, a creation's
	// steps, a removal. Store never takes it, so a slow tenant holding it
	// delays only its own changes.
	op sync.Mutex
	// The fields below are guarded by op.
	store         tenant.Store
	written       master.TenantState // the state the master holds
	writtenReason string             // and its reason
	lastWrite     time.Time          // when the registry last wrote it
	lastKind      fault.Kind         // the kind of the last failure to open
	removed       bool
	// holdWrite is set by Create while it opens the tenant: the state write
	// that settles the creation is its own, with the request's entry.
	holdWrite bool
}

// view is a tenant's state and its store, replaced whole.
type view struct {
	state  master.TenantState
	reason string
	since  time.Time
	store  tenant.Store
}

// New returns a registry that has opened nothing yet. Start opens every
// tenant and starts watching them.
func New(cfg Config) (*Registry, error) {
	switch {
	case cfg.Master == nil:
		return nil, fault.New(fault.Internal, "registry: no master database")
	case cfg.Keys == nil:
		return nil, fault.New(fault.Internal, "registry: no key file")
	case len(cfg.Drivers) == 0:
		return nil, fault.New(fault.Internal, "registry: no database driver")
	case cfg.Now == nil:
		return nil, fault.New(fault.Internal, "registry: no clock")
	case cfg.Interval <= 0:
		return nil, fault.New(fault.Internal, "registry: no interval between checks")
	case cfg.OpenTimeout <= 0:
		return nil, fault.New(fault.Internal, "registry: no deadline for an open")
	case cfg.PingTimeout <= 0:
		return nil, fault.New(fault.Internal, "registry: no deadline for a check")
	case cfg.MaxOpening <= 0:
		return nil, fault.New(fault.Internal, "registry: no limit on opens at once")
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Registry{cfg: cfg, log: log, slots: make(chan struct{}, cfg.MaxOpening), fileLimit: openFileLimit}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	empty := map[string]*entry{}
	r.entries.Store(&empty)
	return r, nil
}

// randomID returns 128 random bits in hex: unique without coordination,
// within master.MaxIDLength.
func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go 1.24).
	return hex.EncodeToString(b[:])
}

// Store returns the store of the tenant named name, for one request. It is
// on the path of every request: one atomic load of the map, one map read,
// one atomic load of the tenant's view, no lock and no call to the master.
// An unknown tenant is NotFound; a tenant that is not ready is Unavailable,
// with its state and reason in the message.
//
// The caller must not keep the store past the request: a removal or a
// reopen closes it, after which it fails as Unavailable.
func (r *Registry) Store(name string) (tenant.Store, error) {
	e := (*r.entries.Load())[name]
	if e == nil {
		if r.closed.Load() {
			return nil, fault.New(fault.Unavailable, "the server is shutting down")
		}
		return nil, fault.New(fault.NotFound, "no such tenant")
	}
	v := e.view.Load()
	if v.state != master.StateReady || v.store == nil {
		return nil, notReady(name, v)
	}
	return v.store, nil
}

// notReady is the refusal of a tenant that is not ready. It is kept out of
// Store so the path of a ready tenant stays small. Its message holds only the
// registry's phrases and the reason, which is made of the same.
func notReady(name string, v *view) error {
	msg := "the tenant " + name + " is " + string(v.state)
	if v.reason != "" {
		msg += ": " + v.reason
	}
	return fault.New(fault.Unavailable, msg)
}

// State returns the state the registry sees now for the tenant named name,
// with its reason and the time it was reached, and whether it knows the
// tenant. It is correct the moment a check completes, and is what the API
// answers as a tenant's state; the master's row may lag it by up to
// StateChangeInterval.
func (r *Registry) State(name string) (State, bool) {
	e := (*r.entries.Load())[name]
	if e == nil {
		return State{}, false
	}
	v := e.view.Load()
	return State{State: v.state, Reason: v.reason, Since: v.since}, true
}

// enter counts a call in progress, so Close waits for it, or refuses it
// once Close has begun. Checked under mu, where Close sets closed, so no
// call is counted after Close starts waiting.
func (r *Registry) enter() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return fault.New(fault.Unavailable, "the server is shutting down")
	}
	r.wg.Add(1)
	return nil
}

// goOwned starts fn in a goroutine counted by wg, which recovers a panic in
// it and logs the stack, so no goroutine of the registry can take the
// process down.
func (r *Registry) goOwned(what string, fn func()) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.recoverLog(what)
		fn()
	}()
}

// recoverLog recovers a panic and logs where it happened and the stack. The
// panic's value is not logged: it may hold anything a driver had in hand.
func (r *Registry) recoverLog(what string) {
	if p := recover(); p != nil {
		r.log.Error("a panic was recovered", "in", what, "stack", string(debug.Stack()))
	}
}

// Start reads every tenant from the master, opens each in parallel (at most
// MaxOpening at once, each under its own deadline), waits until each is
// ready, unreachable or failed, then checks every tenant each Interval until
// Close. It fails only when the master cannot list the tenants, or when it
// was called before.
func (r *Registry) Start(ctx context.Context) error {
	if err := r.enter(); err != nil {
		return err
	}
	defer r.wg.Done()
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fault.New(fault.Internal, "registry: started twice")
	}
	r.started = true
	r.mu.Unlock()

	entries, err := r.registerAll(ctx)
	if err != nil {
		return err
	}
	r.warnFileLimit(len(entries))
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		r.goOwned("start", func() {
			defer wg.Done()
			e.op.Lock()
			defer e.op.Unlock()
			r.refresh(e, true)
		})
	}
	wg.Wait()

	r.goOwned("watch", r.watch)
	return nil
}

// listAll reads every tenant from the master, a page at a time.
func (r *Registry) listAll(ctx context.Context) ([]master.Tenant, error) {
	var rows []master.Tenant
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		batch, next, err := r.cfg.Master.ListTenants(ctx, page)
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
		if next == "" {
			return rows, nil
		}
		page.Cursor = next
	}
}

// registerAll reads every tenant from the master and registers it, with no
// Create or Remove running between the two.
func (r *Registry) registerAll(ctx context.Context) ([]*entry, error) {
	r.listing.Lock()
	defer r.listing.Unlock()
	rows, err := r.listAll(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]*entry, 0, len(rows))
	for _, row := range rows {
		if e := r.register(row); e != nil {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// nameLocks is one mutex per tenant name in use, made on first use and
// dropped when nobody holds or waits for it, so it does not grow with every
// name ever created.
type nameLocks struct {
	mu sync.Mutex
	m  map[string]*nameLock
}

// nameLock is one name's mutex and how many hold or wait for it.
type nameLock struct {
	sync.Mutex
	refs int
}

// lock locks name and returns the function that unlocks it.
func (n *nameLocks) lock(name string) func() {
	n.mu.Lock()
	if n.m == nil {
		n.m = map[string]*nameLock{}
	}
	l := n.m[name]
	if l == nil {
		l = &nameLock{}
		n.m[name] = l
	}
	l.refs++
	n.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		n.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(n.m, name)
		}
		n.mu.Unlock()
	}
}

// watch checks every tenant, and reconciles the registry with the master,
// each Interval until the registry closes.
func (r *Registry) watch() {
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			r.checkAll()
			r.goOwned("reconcile", r.reconcile)
		}
	}
}

// checkAll starts one check of every registered tenant and does not wait
// for them: a tenant whose open hangs until its deadline delays no other
// tenant's check, this round or the next. It returns a group a test may wait
// on; the timer does not.
func (r *Registry) checkAll() *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, e := range *r.entries.Load() {
		wg.Add(1)
		r.goOwned("check", func() {
			defer wg.Done()
			r.check(e)
		})
	}
	return &wg
}

// check is one timer check of e: a ping when its store is open, an open
// when it has none, nothing when it is failed. An entry whose lock is held
// (an open, a check, a creation or a removal still running) is skipped.
func (r *Registry) check(e *entry) {
	if !e.op.TryLock() {
		return
	}
	defer e.op.Unlock()
	r.refresh(e, false)
}

// reconcile brings the registry to the master's list of tenants: an entry
// whose row is gone, or belongs to another id, is unregistered and closed;
// a row the registry does not know is registered and opened. One runs at a
// time; one that finds another running does nothing.
func (r *Registry) reconcile() {
	if !r.reconciling.CompareAndSwap(false, true) {
		return
	}
	defer r.reconciling.Store(false)
	ctx, cancel := context.WithTimeout(r.ctx, r.cfg.OpenTimeout)
	defer cancel()

	r.listing.Lock()
	rows, err := r.listAll(ctx)
	if err != nil {
		r.listing.Unlock()
		r.log.Warn("the tenants cannot be reconciled with the master", "kind", fault.KindOf(err).String(), "message", fault.Message(err))
		return
	}
	byName := make(map[string]master.Tenant, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}
	var gone, fresh []*entry
	for name, e := range *r.entries.Load() {
		if row, ok := byName[name]; !ok || row.ID != e.id {
			r.unregister(e)
			gone = append(gone, e)
		}
	}
	known := *r.entries.Load()
	for _, row := range rows {
		if _, ok := known[row.Name]; !ok {
			if e := r.register(row); e != nil {
				fresh = append(fresh, e)
			}
		}
	}
	r.listing.Unlock()

	for _, e := range gone {
		r.log.Info("tenant unregistered: the master no longer has it", "tenant", e.name)
		r.retire(e)
	}
	for _, e := range fresh {
		r.log.Info("tenant registered: the master has it", "tenant", e.name)
		r.goOwned("open", func() {
			e.op.Lock()
			defer e.op.Unlock()
			r.refresh(e, true)
		})
	}
}

// register adds an entry for row unless one with its name exists, and
// returns the entry for row's id, or nil when the name belongs to another
// id or the registry is closed.
func (r *Registry) register(row master.Tenant) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return nil
	}
	old := *r.entries.Load()
	if e, ok := old[row.Name]; ok {
		if e.id == row.ID {
			return e
		}
		return nil
	}
	e := &entry{id: row.ID, name: row.Name, driver: row.Driver, sealed: row.SealedAddress,
		written: row.State, writtenReason: row.StateReason}
	e.ctx, e.cancel = context.WithCancel(r.ctx)
	// Until its first open the tenant is shown as being opened, with no
	// store, so it is refused rather than served from nothing.
	e.view.Store(&view{state: master.StateUpgrading})
	next := make(map[string]*entry, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[row.Name] = e
	r.entries.Store(&next)
	return e
}

// unregister removes the entry named name when it is e, so a removal never
// takes away an entry a later creation of the same name made.
func (r *Registry) unregister(e *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := *r.entries.Load()
	if old[e.name] != e {
		return
	}
	next := make(map[string]*entry, len(old))
	for k, v := range old {
		if k != e.name {
			next[k] = v
		}
	}
	r.entries.Store(&next)
}

// retire ends an entry already unregistered: it ends the entry's open in
// progress, waits for its lock, and closes its store.
func (r *Registry) retire(e *entry) {
	e.cancel()
	e.op.Lock()
	defer e.op.Unlock()
	r.retireLocked(e)
}

// retireLocked is retire for a caller that holds e.op.
func (r *Registry) retireLocked(e *entry) {
	e.removed = true
	e.view.Store(&view{state: master.StateUnreachable, reason: "the tenant was removed", since: r.cfg.Now()})
	if err := e.closeStore(); err != nil {
		r.log.Warn("a removed tenant's database did not close cleanly", "tenant", e.name, "message", fault.Message(err))
	}
}

// retireName unregisters and closes the entry named name, whatever its id:
// the master has no tenant of that name.
func (r *Registry) retireName(name string) {
	e := (*r.entries.Load())[name]
	if e == nil {
		return
	}
	r.unregister(e)
	r.retire(e)
}

// Close stops the timer, ends every open in progress and waits for it, and
// for Start, Create and Remove calls in progress, then closes every store.
// It waits for driver calls no longer than ctx allows: a driver that ignores
// its context is abandoned, and Close logs and returns an error saying how
// many calls it left running. Calls made after Close fail as Unavailable. A
// second Close does nothing.
func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed.Swap(true) {
		r.mu.Unlock()
		return nil
	}
	m := *r.entries.Load()
	empty := map[string]*entry{}
	r.entries.Store(&empty)
	r.mu.Unlock()

	r.cancel()
	// The waiting goroutine is the one left running when Close gives up;
	// it ends as soon as the abandoned driver calls return.
	waited := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(waited)
	}()
	var abandoned error
	select {
	case <-waited:
	case <-ctx.Done():
		n := r.inflight.Load()
		r.log.Error("the registry closed without waiting for driver calls that ignore their deadline", "abandoned", n)
		abandoned = fault.New(fault.Internal, "the registry closed with "+strconv.FormatInt(n, 10)+" driver calls still running; they were abandoned")
	}
	err := r.closeAll(m)
	if abandoned != nil {
		return abandoned
	}
	return err
}

// closeAll closes the store of every entry of m, MaxOpening at a time.
// Closing a SQLite store checkpoints its log and syncs it, which is most of
// a millisecond to tens of them; 1,000 of them one by one took 12 seconds on
// a laptop (TestThousandTenants), so they are closed as they were opened.
func (r *Registry) closeAll(m map[string]*entry) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	work := make(chan *entry)
	for range min(r.cfg.MaxOpening, len(m)) {
		wg.Go(func() {
			defer r.recoverLog("close")
			for e := range work {
				e.op.Lock()
				e.removed = true
				err := e.closeStore()
				e.op.Unlock()
				if err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		})
	}
	for _, e := range m {
		work <- e
	}
	close(work)
	wg.Wait()
	return first
}

// closeStore closes the entry's store and forgets it. The caller holds op.
func (e *entry) closeStore() error {
	s := e.store
	e.store = nil
	if s == nil {
		return nil
	}
	return s.Close()
}
