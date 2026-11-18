package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
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
type Driver interface {
	// Open opens the database of the tenant named name at address, the
	// plaintext of the tenant's sealed address. It does not migrate. It
	// returns Invalid or Internal for a database that will not open however
	// often it is tried, and Unavailable for one that may answer later.
	Open(ctx context.Context, name, address string) (tenant.Store, error)
	// Location says where the tenant's data is, holding no user and no
	// password: a file's path, or host:port/database. It returns Invalid
	// for an address this driver does not take, and is called before a
	// tenant is created, to refuse such an address before anything is
	// written.
	Location(name, address string) (string, error)
}

// StateChangeInterval is the shortest time between two writes to the master
// of a tenant's change between ready and unreachable. A database that
// flaps writes one change a minute, not one per check.
const StateChangeInterval = time.Minute

// SystemMethod is the method of the audit entries the registry writes when
// it records a tenant's state. Their actor is the system: no key, no local
// user, no address, and the detail "actor" set to "system".
const SystemMethod = "magnetar.registry/SetTenantState"

// The details of the registry's audit entries.
const (
	DetailActor        = "actor"
	DetailDatabaseType = "database_type"
	DetailLocation     = "data_location"
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
	// Now is the clock: audit times, creation times, and the once-a-minute
	// limit on state writes are read from it.
	Now func() time.Time
	// Interval is how often every tenant is checked after Start.
	Interval time.Duration
	// OpenTimeout is the deadline of one tenant's open, migration and ping,
	// and of each check.
	OpenTimeout time.Duration
	// MaxOpening bounds how many tenants are opened or checked at once, so a
	// start with many tenants does not open them all in the same instant.
	MaxOpening int
	// Log receives a line for each change of a tenant's state, with the
	// reason stripped of addresses. Nil logs nothing.
	Log *slog.Logger
	// NewID makes the ids of the registry's audit entries. Nil uses 128
	// random bits in hex.
	NewID func() string
}

// State is a tenant's state as the registry sees it now.
type State struct {
	State  master.TenantState
	Reason string
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

	// slots bounds the opens and checks running at once.
	slots chan struct{}

	// names serializes Create and Remove of the same name, so a removal
	// cannot slip between a creation's reading of the master and its
	// registering of the tenant. listing is held for writing while Start
	// reads the tenants and registers them, and for reading by Create and
	// Remove, so neither changes a tenant between the two.
	names   nameLocks
	listing sync.RWMutex

	// ctx is canceled by Close; every entry's context derives from it.
	ctx    context.Context
	cancel context.CancelFunc
	// wg counts every goroutine the registry starts, and every Start,
	// Create and Remove in progress, so Close waits for all of them.
	wg sync.WaitGroup

	// afterRow, when set by a test, runs in Create after the tenant's row
	// is written and before its database is opened; an error from it ends
	// Create there, as a crash would.
	afterRow func() error
}

// entry is one tenant.
type entry struct {
	id, name, driver string
	sealed           []byte

	// view is what Store reads: the state and the store a request gets.
	view atomic.Pointer[view]

	// ctx ends when the tenant is removed or the registry closed, which
	// ends an open in progress for it.
	ctx    context.Context
	cancel context.CancelFunc

	// opening is set while an open that outlived its deadline is still
	// running, so a check does not start a second one beside it.
	opening atomic.Bool

	// op orders the changes of this tenant: an open, a check, a creation's
	// steps, a removal. Store never takes it, so a slow tenant holding it
	// delays only its own changes.
	op sync.Mutex
	// The fields below are guarded by op.
	store     tenant.Store
	written   master.TenantState // the state the master holds
	lastWrite time.Time          // when the registry last wrote it
	logged    State              // the state last logged
	removed   bool
}

// view is a tenant's state and its store, replaced whole.
type view struct {
	state  master.TenantState
	reason string
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
	r := &Registry{cfg: cfg, log: log, slots: make(chan struct{}, cfg.MaxOpening)}
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
// with its state in the message and its reason kept for the log.
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
// Store so the path of a ready tenant stays small.
func notReady(name string, v *view) error {
	return fault.Wrap(fault.Unavailable, reasonError(v.reason), "the tenant "+name+" is "+string(v.state))
}

// reasonError carries a state's reason, already stripped of addresses, as
// the cause of an error: for the log, never shown to the caller.
type reasonError string

// Error returns the reason.
func (e reasonError) Error() string { return string(e) }

// State returns the state the registry sees for the tenant named name, and
// whether it knows the tenant.
func (r *Registry) State(name string) (State, bool) {
	e := (*r.entries.Load())[name]
	if e == nil {
		return State{}, false
	}
	v := e.view.Load()
	return State{State: v.state, Reason: v.reason}, true
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
	r.round(entries, true)

	r.wg.Add(1)
	go r.watch()
	return nil
}

// registerAll reads every tenant from the master and registers it, with no
// Create or Remove running between the two.
func (r *Registry) registerAll(ctx context.Context) ([]*entry, error) {
	r.listing.Lock()
	defer r.listing.Unlock()
	var rows []master.Tenant
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		batch, next, err := r.cfg.Master.ListTenants(ctx, page)
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
		if next == "" {
			break
		}
		page.Cursor = next
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

// watch checks every tenant each Interval until the registry closes. A
// round waits for its checks, each bounded by OpenTimeout, so rounds never
// pile up behind a slow tenant.
func (r *Registry) watch() {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			r.checkAll()
		}
	}
}

// checkAll checks every registered tenant once.
func (r *Registry) checkAll() {
	m := *r.entries.Load()
	entries := make([]*entry, 0, len(m))
	for _, e := range m {
		entries = append(entries, e)
	}
	r.round(entries, false)
}

// round refreshes each entry in its own goroutine, at most MaxOpening at
// once, and waits for all of them. At start (first) each entry is opened
// whatever its state, and waited for; on the timer an entry whose op lock is
// held (a creation, a removal, a check still running) is skipped this round.
func (r *Registry) round(entries []*entry, first bool) {
	var wg sync.WaitGroup
	for _, e := range entries {
		select {
		case r.slots <- struct{}{}:
		case <-r.ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer wg.Done()
			defer func() { <-r.slots }()
			if first {
				e.op.Lock()
			} else if !e.op.TryLock() {
				return
			}
			defer e.op.Unlock()
			r.refresh(e, first)
		}()
	}
	wg.Wait()
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
	e := &entry{id: row.ID, name: row.Name, driver: row.Driver, sealed: row.SealedAddress, written: row.State}
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

// Close stops the timer, ends every open in progress and waits for it,
// waits for Start, Create and Remove calls in progress, and closes every
// store. Calls made after it fail as Unavailable. A second Close does
// nothing.
func (r *Registry) Close() error {
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
	r.wg.Wait()
	// Closing a SQLite store checkpoints its log and syncs it, which is
	// most of a millisecond to tens of them; 1,000 of them one by one took
	// 12 seconds on a laptop (TestThousandTenants), so they are closed
	// MaxOpening at a time, as they were opened.
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	work := make(chan *entry)
	for range min(r.cfg.MaxOpening, len(m)) {
		wg.Go(func() {
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
