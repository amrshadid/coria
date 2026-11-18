package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/driver"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/secret"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// TestMain runs the package's tests, then fails if goroutines are left
// running that were not running before: every goroutine the registry starts
// must have an owner that waits for it (engineering.md, "Bounded workers").
// database/sql's own goroutines end when a pool closes, so a store left
// open shows here too.
func TestMain(m *testing.M) {
	before := runtime.NumGoroutine()
	code := m.Run()
	if code == 0 {
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if after := runtime.NumGoroutine(); after > before {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			fmt.Fprintf(os.Stderr, "goroutine leak: %d before the tests, %d after\n%s\n", before, after, buf[:n])
			code = 1
		} else {
			fmt.Printf("goroutines: %d before the tests, %d after\n", before, after)
		}
	}
	os.Exit(code)
}

// t0 is the fake clock's start.
var t0 = time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)

// clock is a clock a test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

// newClock returns a clock at t0.
func newClock() *clock { return &clock{t: t0} }

// Now returns the clock's time.
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Add moves the clock forward by d.
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// privateDir returns a new directory only the current user may use, as the
// key file's directory must be.
func privateDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory needs its execute bit.
		t.Fatal(err)
	}
	return dir
}

// newKeys makes a new key file and loads it.
func newKeys(t testing.TB) *secret.Keys {
	t.Helper()
	path := filepath.Join(privateDir(t), "key")
	if err := secret.CreateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	k, err := secret.LoadKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// newMaster opens a migrated SQLite master in a new directory.
func newMaster(t testing.TB) master.Store {
	t.Helper()
	ctx := context.Background()
	m, err := driver.OpenMaster(ctx, driver.SQLiteScheme+filepath.Join(t.TempDir(), "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return m
}

// env is one test's installation: a master, a key file, a data directory,
// the SQLite driver and the test driver, a clock and a log.
type env struct {
	t      testing.TB
	master master.Store
	keys   *secret.Keys
	data   string
	flaky  *flaky
	clock  *clock
	logBuf *syncBuffer
	seq    atomic.Int64
}

// newEnv returns an installation with a SQLite master.
func newEnv(t testing.TB) *env {
	t.Helper()
	data := privateDir(t)
	return &env{
		t: t, master: newMaster(t), keys: newKeys(t), data: data,
		flaky: newFlaky(t), clock: newClock(), logBuf: &syncBuffer{},
	}
}

// config returns a registry configuration for the installation, with the
// SQLite driver as "sqlite" and the test driver as "flaky".
func (e *env) config() Config {
	return Config{
		Master:      e.master,
		Keys:        e.keys,
		Drivers:     map[string]Driver{driver.SQLiteDriver: driver.SQLiteTenants{DataDir: e.data}, flakyDriver: e.flaky},
		Now:         e.clock.Now,
		Interval:    time.Hour, // tests call checkAll themselves unless they test the timer
		OpenTimeout: 2 * time.Second,
		MaxOpening:  8,
		Log:         slog.New(slog.NewTextHandler(e.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// newRegistry makes a registry with cfg, closed when the test ends.
func (e *env) newRegistry(cfg Config) *Registry {
	e.t.Helper()
	r, err := New(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = r.Close() })
	return r
}

// audit returns a new audit entry for a request by the local operator.
func (e *env) audit(method string) master.AuditEntry {
	return master.AuditEntry{
		ID: fmt.Sprintf("req-%06d", e.seq.Add(1)), Time: e.clock.Now(), LocalUser: "501",
		Method: method, Outcome: master.OutcomeDone, RequestID: "r",
	}
}

// create creates a tenant through r, failing the test on an error.
func (e *env) create(r *Registry, id, name, drv, address string) master.Tenant {
	e.t.Helper()
	tn, err := r.Create(context.Background(), NewTenant{ID: id, Name: name, Driver: drv, Address: address}, e.audit("CreateTenant"))
	if err != nil {
		e.t.Fatalf("Create(%s): %v", name, err)
	}
	return tn
}

// masterTenant reads a tenant's row from the master.
func (e *env) masterTenant(name string) master.Tenant {
	e.t.Helper()
	tn, err := e.master.TenantByName(context.Background(), name)
	if err != nil {
		e.t.Fatalf("TenantByName(%s): %v", name, err)
	}
	return tn
}

// systemEntries returns the registry's audit entries for the tenant named
// name, oldest first.
func (e *env) systemEntries(name string) []master.AuditEntry {
	e.t.Helper()
	var out []master.AuditEntry
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		entries, next, err := e.master.ListAudit(context.Background(), master.AuditFilter{TenantName: name}, page)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, a := range entries {
			if a.Method == SystemMethod {
				out = append([]master.AuditEntry{a}, out...)
			}
		}
		if next == "" {
			return out
		}
		page.Cursor = next
	}
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends p.
func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns what was written.
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// flakyDriver is the name of the test driver's database type.
const flakyDriver = "flaky"

// password is the known password in every test driver address; the tests
// search every reason, error, log line and audit entry for it.
const password = "Hunter2-NotARealPassword-91f3" //nolint:gosec // G101: made up, so its absence can be searched for.

// flakyHost is the host in every test driver address, also kept out of
// reasons.
const flakyHost = "db.flaky.example"

// flakyAddress returns the address of a test driver tenant.
func flakyAddress(name string) string {
	return "flaky://admin:" + password + "@" + flakyHost + ":5432/" + name
}

// mode is how the test driver behaves for one tenant.
type mode int

const (
	// up opens and pings normally.
	up mode = iota
	// down fails to open and to ping, as Unavailable, with an error text
	// holding the whole address and the password, as a driver's may.
	down
	// hang blocks an open or a ping until its context ends.
	hang
	// stuck blocks an open or a ping until the test releases it, whatever
	// its context does: a driver that ignores its deadline.
	stuck
	// broken fails to open as Internal, as a failed migration does.
	broken
)

// flaky is the test driver. A tenant on it is a real SQLite database in its
// own directory, wrapped so that its open and its ping can be made to fail,
// hang and recover. It is registered only in this package's tests; the real
// SQLite driver is not changed for them.
type flaky struct {
	sqlite  driver.SQLiteTenants
	mu      sync.Mutex
	modes   map[string]mode
	release chan struct{}

	opens, closes atomic.Int64 // stores opened and closed
	opening       atomic.Int64 // opens running now
	maxOpening    atomic.Int64 // the most that ran at once
}

// newFlaky returns a test driver whose databases are under a new directory.
func newFlaky(t testing.TB) *flaky {
	return &flaky{sqlite: driver.SQLiteTenants{DataDir: t.TempDir()}, modes: map[string]mode{}, release: make(chan struct{})}
}

// set sets the mode of the tenant named name.
func (f *flaky) set(name string, m mode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modes[name] = m
}

// unstick releases every stuck open and ping, once.
func (f *flaky) unstick() { close(f.release) }

// behave acts out the tenant's mode for an open or a ping of address.
func (f *flaky) behave(ctx context.Context, name, address string) error {
	f.mu.Lock()
	m := f.modes[name]
	f.mu.Unlock()
	switch m {
	case down:
		return fault.Wrap(fault.Unavailable,
			fmt.Errorf("dial %s: connection refused (user admin, password %s, host %s)", address, password, flakyHost),
			"the database does not answer")
	case hang:
		<-ctx.Done()
		return fault.Wrap(fault.Unavailable, ctx.Err(), "the database does not answer")
	case stuck:
		<-f.release
		return fault.New(fault.Unavailable, "the database does not answer")
	case broken:
		return fault.Wrap(fault.Internal, errors.New("migration 0002 failed at "+address), "the database cannot be migrated")
	}
	return nil
}

// Open implements Driver.
func (f *flaky) Open(ctx context.Context, name, address string) (tenant.Store, error) {
	n := f.opening.Add(1)
	defer f.opening.Add(-1)
	for {
		m := f.maxOpening.Load()
		if n <= m || f.maxOpening.CompareAndSwap(m, n) {
			break
		}
	}
	if err := f.behave(ctx, name, address); err != nil {
		return nil, err
	}
	s, err := f.sqlite.Open(ctx, name, "")
	if err != nil {
		return nil, err
	}
	f.opens.Add(1)
	return &flakyStore{Store: s, f: f, name: name, address: address}, nil
}

// Location implements Driver: host:port/database, with no user and no
// password, as a Postgres driver answers.
func (f *flaky) Location(name, address string) (string, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != flakyDriver {
		return "", fault.New(fault.Invalid, "not a flaky address")
	}
	return u.Host + u.Path, nil
}

// flakyStore is a test driver tenant's store.
type flakyStore struct {
	tenant.Store
	f       *flaky
	name    string
	address string
	closed  atomic.Bool
}

// Ping fails, hangs or answers as the tenant's mode says.
func (s *flakyStore) Ping(ctx context.Context) error {
	if err := s.f.behave(ctx, s.name, s.address); err != nil {
		return err
	}
	return s.Store.Ping(ctx)
}

// Close closes the database once and counts it.
func (s *flakyStore) Close() error {
	if !s.closed.Swap(true) {
		s.f.closes.Add(1)
	}
	return s.Store.Close()
}

// countingMaster counts every call made to the master it wraps, to show
// which paths reach the master and which do not.
type countingMaster struct {
	master.Store
	calls atomic.Int64
}

// count adds one call.
func (c *countingMaster) count() { c.calls.Add(1) }

// Each method below counts, then calls the wrapped master.

func (c *countingMaster) Migrate(ctx context.Context) error { c.count(); return c.Store.Migrate(ctx) }
func (c *countingMaster) Version(ctx context.Context) (int, error) {
	c.count()
	return c.Store.Version(ctx)
}
func (c *countingMaster) Ping(ctx context.Context) error { c.count(); return c.Store.Ping(ctx) }
func (c *countingMaster) Close() error                   { c.count(); return c.Store.Close() }
func (c *countingMaster) CreateTenant(ctx context.Context, t master.Tenant, a master.AuditEntry) error {
	c.count()
	return c.Store.CreateTenant(ctx, t, a)
}
func (c *countingMaster) Tenant(ctx context.Context, id string) (master.Tenant, error) {
	c.count()
	return c.Store.Tenant(ctx, id)
}
func (c *countingMaster) TenantByName(ctx context.Context, name string) (master.Tenant, error) {
	c.count()
	return c.Store.TenantByName(ctx, name)
}
func (c *countingMaster) ListTenants(ctx context.Context, p master.Page) ([]master.Tenant, string, error) {
	c.count()
	return c.Store.ListTenants(ctx, p)
}
func (c *countingMaster) SetTenantState(ctx context.Context, id string, s master.TenantState, reason string, a master.AuditEntry) error {
	c.count()
	return c.Store.SetTenantState(ctx, id, s, reason, a)
}
func (c *countingMaster) RemoveTenant(ctx context.Context, id string, a master.AuditEntry) error {
	c.count()
	return c.Store.RemoveTenant(ctx, id, a)
}
func (c *countingMaster) CreateKey(ctx context.Context, k master.Key, a master.AuditEntry) error {
	c.count()
	return c.Store.CreateKey(ctx, k, a)
}
func (c *countingMaster) Key(ctx context.Context, id string) (master.Key, error) {
	c.count()
	return c.Store.Key(ctx, id)
}
func (c *countingMaster) ListKeys(ctx context.Context, p master.Page) ([]master.Key, string, error) {
	c.count()
	return c.Store.ListKeys(ctx, p)
}
func (c *countingMaster) MarkKeyUsed(ctx context.Context, id string, at time.Time) (bool, error) {
	c.count()
	return c.Store.MarkKeyUsed(ctx, id, at)
}
func (c *countingMaster) RevokeKey(ctx context.Context, id string, at time.Time, a master.AuditEntry) (time.Time, error) {
	c.count()
	return c.Store.RevokeKey(ctx, id, at, a)
}
func (c *countingMaster) SetGrant(ctx context.Context, g master.Grant, a master.AuditEntry) (master.Role, error) {
	c.count()
	return c.Store.SetGrant(ctx, g, a)
}
func (c *countingMaster) GrantsByKey(ctx context.Context, id string, p master.Page) ([]master.Grant, string, error) {
	c.count()
	return c.Store.GrantsByKey(ctx, id, p)
}
func (c *countingMaster) GrantsByScope(ctx context.Context, s master.Scope, p master.Page) ([]master.Grant, string, error) {
	c.count()
	return c.Store.GrantsByScope(ctx, s, p)
}
func (c *countingMaster) RemoveGrant(ctx context.Context, id string, s master.Scope, a master.AuditEntry) (master.Role, error) {
	c.count()
	return c.Store.RemoveGrant(ctx, id, s, a)
}
func (c *countingMaster) Setting(ctx context.Context, name string) (string, error) {
	c.count()
	return c.Store.Setting(ctx, name)
}
func (c *countingMaster) SetSetting(ctx context.Context, name, value string, a master.AuditEntry) error {
	c.count()
	return c.Store.SetSetting(ctx, name, value, a)
}
func (c *countingMaster) UnsetSetting(ctx context.Context, name string, a master.AuditEntry) (bool, error) {
	c.count()
	return c.Store.UnsetSetting(ctx, name, a)
}
func (c *countingMaster) ListSettings(ctx context.Context, p master.Page) ([]master.Setting, string, error) {
	c.count()
	return c.Store.ListSettings(ctx, p)
}
func (c *countingMaster) AppendAudit(ctx context.Context, entries ...master.AuditEntry) error {
	c.count()
	return c.Store.AppendAudit(ctx, entries...)
}
func (c *countingMaster) ListAudit(ctx context.Context, f master.AuditFilter, p master.Page) ([]master.AuditEntry, string, error) {
	c.count()
	return c.Store.ListAudit(ctx, f, p)
}

// memMaster is a master held in memory, with only what the registry calls,
// for the test of 1,000 tenants: writing 1,000 rows to a SQLite master with
// a synced commit each would measure the master's disk, not the registry.
// The registry's behavior against a real master is tested everywhere else.
type memMaster struct {
	master.Store // nil: a method the registry should not call panics
	mu           sync.Mutex
	tenants      map[string]master.Tenant // by name
	writes       atomic.Int64
}

// newMemMaster returns an empty memMaster.
func newMemMaster() *memMaster { return &memMaster{tenants: map[string]master.Tenant{}} }

// ListTenants lists every tenant in one page, in no order.
func (m *memMaster) ListTenants(_ context.Context, _ master.Page) ([]master.Tenant, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]master.Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		out = append(out, t)
	}
	return out, "", nil
}

// SetTenantState sets a tenant's state.
func (m *memMaster) SetTenantState(_ context.Context, id string, s master.TenantState, reason string, _ master.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes.Add(1)
	for name, t := range m.tenants {
		if t.ID == id {
			t.State, t.StateReason = s, reason
			m.tenants[name] = t
			return nil
		}
	}
	return fault.New(fault.NotFound, "no such tenant")
}

// add adds a tenant row as Create would leave it, sealed with keys.
func (m *memMaster) add(t testing.TB, keys *secret.Keys, id, name, drv, address string, state master.TenantState) {
	t.Helper()
	sealed, err := keys.Seal(addressContext(id), []byte(address))
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tenants[name] = master.Tenant{ID: id, Name: name, Driver: drv, SealedAddress: sealed, State: state, Created: t0}
}

// mustNotContain fails if text holds any of the needles.
func mustNotContain(t testing.TB, what, text string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if n != "" && strings.Contains(text, n) {
			t.Errorf("%s holds %q: %s", what, n, text)
		}
	}
}

// waitFor polls cond every millisecond until it holds or 5 seconds pass.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// countingDriver wraps a driver and records the most opens that ran at
// once, to show the limit on opens holds.
type countingDriver struct {
	Driver
	opening, maxOpening atomic.Int64
}

// Open counts the open while it runs.
func (d *countingDriver) Open(ctx context.Context, name, address string) (tenant.Store, error) {
	n := d.opening.Add(1)
	defer d.opening.Add(-1)
	for {
		m := d.maxOpening.Load()
		if n <= m || d.maxOpening.CompareAndSwap(m, n) {
			break
		}
	}
	return d.Driver.Open(ctx, name, address)
}

// openFiles returns how many files the process has open, read from
// /dev/fd (Linux and macOS), or -1 where that cannot be read.
func openFiles() int {
	// Names only: on macOS a stat of each entry fails for some of them, so
	// os.ReadDir would give up on the whole directory.
	d, err := os.Open("/dev/fd")
	if err != nil {
		return -1
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return -1
	}
	return len(names) - 1 // less the descriptor reading it
}
