package api_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/api"
	"github.com/amrshadid/magnetar/internal/api/codes"
	"github.com/amrshadid/magnetar/internal/driver"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/registry"
	"github.com/amrshadid/magnetar/internal/secret"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// TestMain runs the package's tests, then fails if goroutines are left
// running that were not before: this package starts none, and every
// goroutine of the packages below it has an owner that a test closes.
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
		}
	}
	os.Exit(code)
}

// t0 is the start of the test clock.
var t0 = time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)

// localHeader is the request header by which a test client says it is the
// local operator, with that user id. The real server learns this from its
// local socket; the stand-in reads a header.
const localHeader = "X-Test-Local-User"

// pgAddress is a made-up address for a tenant of the test's Postgres
// driver, which keeps the tenant in a SQLite file and opens nothing at it.
const pgAddress = "postgres://u:p@h:5432/d" //nolint:gosec // G101: made up; it reaches no database.

// syncBuffer is a buffer the logger writes to from several goroutines.
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

// String returns everything written.
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakePostgres is a registry driver registered as "postgres": it takes an
// address, as a Postgres driver would, and keeps the tenant's data in a
// SQLite file. When down, its open fails as a database that does not answer,
// with an error that repeats the address, as a careless driver might, so the
// tests show the address is kept out of everything all the same.
type fakePostgres struct {
	dir  string
	down atomic.Bool
}

// Location returns a location with no user, password or host of the
// address.
func (f *fakePostgres) Location(name, address string) (string, error) {
	if address == "" {
		return "", fault.New(fault.Invalid, "a Postgres tenant needs an address")
	}
	return "pg.location.test:5432/" + name, nil
}

// Open opens the tenant's file, or fails while down.
func (f *fakePostgres) Open(ctx context.Context, name, address string) (tenant.Store, error) {
	if f.down.Load() {
		return nil, fault.Wrap(fault.Unavailable, errors.New("dial "+address+": connection refused"), "cannot reach "+address)
	}
	return driver.SQLiteTenants{DataDir: f.dir}.Open(ctx, name, "")
}

// env is one test's installation: a SQLite master and SQLite tenants in a
// temporary directory, a real key file, the real access package and
// registry, the handlers, and an HTTP server whose only middleware is the
// stand-in for the server's path.
type env struct {
	t        testing.TB
	dir      string
	data     string
	master   master.Store
	keys     *secret.Keys
	access   *access.Access
	registry *registry.Registry
	pg       *fakePostgres
	log      *syncBuffer
	url      string
	client   *http.Client
	seq      atomic.Int64
	ticks    atomic.Int64
	requests atomic.Int64
	// decide, when false, makes the stand-in skip putting the Decision in
	// the context, as a server that forgot would.
	decide atomic.Bool
	// badTenantID, when true, makes the handlers' id source give a tenant id
	// the master refuses: a fault of the server.
	badTenantID atomic.Bool
}

// newEnv returns a running installation with no tenant and no key.
func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory needs its execute bit.
		t.Fatal(err)
	}
	e := &env{t: t, dir: dir, data: filepath.Join(dir, "data"), log: &syncBuffer{}}
	e.decide.Store(true)
	if err := os.Mkdir(e.data, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "key")
	if err := secret.CreateKeyFile(keyPath); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.LoadKeyFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	e.keys = keys
	m, err := driver.OpenMaster(ctx, driver.SQLiteScheme+filepath.Join(e.data, "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e.master = m
	logger := slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.access, err = access.New(access.Config{Store: m, Clock: e.now, NewID: e.newID, AuditInterval: 5 * time.Millisecond, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	e.pg = &fakePostgres{dir: filepath.Join(dir, "pg")}
	e.registry, err = registry.New(registry.Config{
		Master: m, Keys: keys,
		Drivers:  map[string]registry.Driver{api.DriverSQLite: driver.SQLiteTenants{DataDir: e.data}, api.DriverPostgres: e.pg},
		Now:      e.now,
		Interval: time.Hour, OpenTimeout: 10 * time.Second, MaxOpening: 4,
		Log: logger, NewID: e.newID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.registry.Start(ctx); err != nil {
		t.Fatal(err)
	}
	tenantID := func() string {
		if e.badTenantID.Load() {
			return "not an id"
		}
		return e.newID()
	}
	h, err := api.New(api.Config{Access: e.access, Registry: e.registry, Master: m, NewID: tenantID, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	srv := connect.NewServer(e.standIn)
	h.Register(srv)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	e.url, e.client = hs.URL, hs.Client()
	t.Cleanup(func() {
		hs.Close()
		_ = e.registry.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.access.Close(cctx)
		_ = m.Close()
	})
	return e
}

// now is the test clock: t0, a microsecond later at each reading, so audit
// entries have distinct times and list in the order they were made.
func (e *env) now() time.Time {
	return t0.Add(time.Duration(e.ticks.Add(1)) * time.Microsecond)
}

// newID returns a fresh id.
func (e *env) newID() string { return fmt.Sprintf("id-%07d", e.seq.Add(1)) }

// replay is the request stream after the stand-in has read its message:
// the handler receives that message.
type replay struct {
	connect.ServerStream
	msg  proto.Message
	read bool
}

// Receive gives the message the stand-in read, once.
func (r *replay) Receive(dst any) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	m, ok := dst.(proto.Message)
	if !ok {
		return errors.New("replay: not a message")
	}
	proto.Merge(m, r.msg)
	return nil
}

// standIn is the test's stand-in for the server's path, and the only
// middleware: it sets the request id, reads the credential, asks
// api.Methods for the requirement and the audit facts, calls the real
// access.Check, puts the Decision in the context with api.WithDecision, and
// answers a refusal with its code. It reads the request before the check
// only when the requirement depends on it, as the server does.
func (e *env) standIn(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		info, _ := connect.CallInfoForServerContext(ctx)
		id := fmt.Sprintf("req-%06d", e.requests.Add(1))
		info.ResponseHeader().Set(api.RequestIDHeader, id)

		var methods api.Methods
		msg, known := methods.RequestFor(spec.Procedure)
		if !known {
			return connect.NewError(connect.CodeUnimplemented, "unknown method")
		}
		var read proto.Message
		if msg != nil {
			if err := stream.Receive(msg); err != nil {
				return err
			}
			read = msg
		}
		need, facts := methods.Requirement(spec.Procedure, read)
		facts.Method, facts.RequestID, facts.RemoteAddress = spec.Procedure, id, "127.0.0.1:1"

		cred := access.APIKey(strings.TrimPrefix(info.RequestHeader().Get("Authorization"), "Bearer "))
		if u := info.RequestHeader().Get(localHeader); u != "" {
			cred = access.LocalOperator(u)
		}
		d, err := e.access.Check(ctx, cred, need, facts)
		if err != nil {
			return codes.ToConnect(err)
		}
		if e.decide.Load() {
			ctx = api.WithDecision(ctx, d)
		}
		if msg != nil {
			stream = &replay{ServerStream: stream, msg: msg}
		}
		return next(ctx, spec, stream)
	}
}

// caller is who a test client calls as: a presented key, the local
// operator, or nobody.
type caller struct {
	key   string
	local string
}

// local is the local operator.
var local = caller{local: "501"}

// nobody is a caller with no key.
var nobody = caller{}

// clients is the generated Connect clients of the four services, for one
// caller.
type clients struct {
	tenants  magnetarv1connect.TenantServiceClient
	access   magnetarv1connect.AccessServiceClient
	settings magnetarv1connect.SettingsServiceClient
	audit    magnetarv1connect.AuditServiceClient
}

// as returns clients that call as c.
func (e *env) as(c caller) clients {
	set := func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if info, ok := connect.CallInfoForClientContext(ctx); ok {
				if c.key != "" {
					info.RequestHeader().Set("Authorization", "Bearer "+c.key)
				}
				if c.local != "" {
					info.RequestHeader().Set(localHeader, c.local)
				}
			}
			return next(ctx, spec)
		}
	}
	cl := connect.NewClient(connecthttp.NewTransport(e.client, e.url), set)
	return clients{
		tenants:  magnetarv1connect.NewTenantServiceClient(cl),
		access:   magnetarv1connect.NewAccessServiceClient(cl),
		settings: magnetarv1connect.NewSettingsServiceClient(cl),
		audit:    magnetarv1connect.NewAuditServiceClient(cl),
	}
}

// outcome is what one call answered: its response, its error, and the
// request id the server returned.
type outcome struct {
	res       proto.Message
	err       error
	requestID string
}

// code returns the call's code, or 0 for success.
func (o outcome) code() connect.Code {
	if o.err == nil {
		return 0
	}
	return connect.CodeOf(o.err)
}

// message returns the error's message, or "".
func (o outcome) message() string {
	var ce *connect.Error
	if errors.As(o.err, &ce) {
		return ce.Message()
	}
	return ""
}

// call makes one call with a client context, so the response's request id
// is read.
func call[Req, Res proto.Message](f func(context.Context, Req) (Res, error), req Req) outcome {
	ctx, info := connect.NewClientContext(context.Background())
	res, err := f(ctx, req)
	o := outcome{err: err, requestID: info.ResponseHeader().Get(api.RequestIDHeader)}
	if err == nil {
		o.res = res
	}
	return o
}

// key makes a key in the master with the grants given and returns it as a
// caller, with its id.
func (e *env) key(grants ...master.Grant) (caller, string) {
	e.t.Helper()
	ctx := context.Background()
	made, err := secret.MakeAPIKey()
	if err != nil {
		e.t.Fatal(err)
	}
	k := master.Key{ID: made.ID, Hash: made.Hash[:], Holder: "test", Created: t0}
	if err := e.master.CreateKey(ctx, k, e.setupEntry()); err != nil {
		e.t.Fatal(err)
	}
	for _, g := range grants {
		g.KeyID = k.ID
		if _, err := e.master.SetGrant(ctx, g, e.setupEntry()); err != nil {
			e.t.Fatal(err)
		}
	}
	return caller{key: made.Key()}, k.ID
}

// setupEntry is the audit entry of a change a test makes directly.
func (e *env) setupEntry() master.AuditEntry {
	return master.AuditEntry{ID: e.newID(), Time: e.now(), Method: "test.setup", Outcome: master.OutcomeDone}
}

// onInstallation is a grant of role on the installation.
func onInstallation(role master.Role) master.Grant {
	return master.Grant{Scope: master.Installation, Role: role}
}

// onTenant is a grant of role on the tenant named name.
func (e *env) onTenant(name string, role master.Role) master.Grant {
	e.t.Helper()
	t, err := e.master.TenantByName(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	return master.Grant{Scope: master.TenantScope(t.ID), Role: role}
}

// tenant creates a SQLite tenant over the API, as the local operator, and
// fails the test unless it is ready.
func (e *env) tenant(name string) {
	e.t.Helper()
	o := call(e.as(local).tenants.CreateTenant, &v1.CreateTenantRequest{Name: name, DatabaseType: v1.DatabaseType_DATABASE_TYPE_SQLITE})
	if o.err != nil {
		e.t.Fatalf("creating tenant %s: %v", name, o.err)
	}
	if s := o.res.(*v1.CreateTenantResponse).GetTenant().GetState(); s != v1.TenantState_TENANT_STATE_READY {
		e.t.Fatalf("tenant %s is %v", name, s)
	}
}

// setDefault sets an installation default directly in the master.
func (e *env) setDefault(name, value string) {
	e.t.Helper()
	if err := e.master.SetSetting(context.Background(), name, value, e.setupEntry()); err != nil {
		e.t.Fatal(err)
	}
}

// flush waits until the access package's audit writer has written what it
// holds.
func (e *env) flush() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.access.FlushAudit(ctx); err != nil {
		e.t.Fatal(err)
	}
}

// masterLog returns every entry of the installation's log, after a flush.
func (e *env) masterLog() []master.AuditEntry {
	e.t.Helper()
	e.flush()
	var out []master.AuditEntry
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		entries, next, err := e.master.ListAudit(context.Background(), master.AuditFilter{}, page)
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, entries...)
		if next == "" {
			return out
		}
		page.Cursor = next
	}
}

// tenantLog returns every entry of a tenant's log.
func (e *env) tenantLog(name string) []master.AuditEntry {
	e.t.Helper()
	st, err := e.registry.Store(name)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []master.AuditEntry
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		entries, next, err := st.ListAudit(context.Background(), master.AuditFilter{}, page)
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, entries...)
		if next == "" {
			return out
		}
		page.Cursor = next
	}
}

// entriesOf returns the entries of a log with this request id.
func entriesOf(log []master.AuditEntry, requestID string) []master.AuditEntry {
	var out []master.AuditEntry
	for _, en := range log {
		if en.RequestID == requestID {
			out = append(out, en)
		}
	}
	return out
}

// installation is the installation scope as a request names it.
func installation() *v1.Scope {
	return &v1.Scope{Kind: &v1.Scope_Installation{Installation: &v1.Installation{}}}
}

// tenantScope is a tenant scope as a request names it.
func tenantScope(name string) *v1.Scope {
	return &v1.Scope{Kind: &v1.Scope_Tenant{Tenant: name}}
}
