package server_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	magnetarv1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/driver/sqlite"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/secret"
	"github.com/amrshadid/magnetar/internal/server"
)

// testClock is a clock a test moves by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the clock's time.
func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Add moves the clock forward by d.
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// adminKey makes, directly in the master, a key with admin on the
// installation that expires at expires (zero for never), and returns its id
// and the whole key.
func adminKey(t *testing.T, e *env, created, expires time.Time) (string, string) {
	t.Helper()
	made, err := secret.MakeAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	entry := func() master.AuditEntry {
		return master.AuditEntry{ID: server.NewID(), Time: created, Method: "test", Outcome: master.OutcomeDone}
	}
	if err := e.master.CreateKey(e.ctx, master.Key{ID: made.ID, Hash: made.Hash[:], Holder: "test", Created: created, Expires: expires}, entry(), master.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.master.SetGrant(e.ctx, master.Grant{KeyID: made.ID, Scope: master.Installation, Role: master.RoleAdmin}, entry(), master.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	return made.ID, made.Key()
}

// heldBody starts a Connect JSON call of procedure over HTTP/1.1 with
// authorization whose body stops after five bytes, waits until the front
// step has read the key (reads counts the reads), and returns a function
// that sends the rest of the body and returns the answer and its request id.
func heldBody(t *testing.T, e *env, reads *keyReads, procedure, authorization, body string) func() (*http.Response, string, string) {
	t.Helper()
	before := reads.n.Load()
	conn := rawTLS(t, e)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	head := "POST " + procedure + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\nAuthorization: " + authorization + "\r\n\r\n"
	if _, err := io.WriteString(conn, head+body[:5]); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return reads.n.Load() > before })
	return func() (*http.Response, string, string) {
		if _, err := io.WriteString(conn, body[5:]); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, resp.Header.Get(server.RequestIDHeader), string(b)
	}
}

// entriesOf returns the audit entries of the request with this id.
func entriesOf(t *testing.T, e *env, id string) []master.AuditEntry {
	t.Helper()
	var out []master.AuditEntry
	for _, en := range allAudit(t, e) {
		if en.RequestID == id {
			out = append(out, en)
		}
	}
	return out
}

// TestKeyRevokedWhileBodyArrives guards that the decision to allow is made on
// the key as it is when the check runs, not as it was when the headers
// arrived: a key revoked while a request's body is still arriving, on a
// method whose requirement reads the request (a read, GetSetting, and a
// change, SetSetting), is refused when the body completes, with one refused
// entry that says the key was revoked; and the handler never runs.
func TestKeyRevokedWhileBodyArrives(t *testing.T) {
	reads := &keyReads{}
	e := newEnv(t, options{store: func(s access.Store) access.Store { reads.Store = s; return reads }})
	for _, c := range []struct{ proc, body string }{
		{magnetarv1connect.SettingsServiceGetSettingProcedure, `{"name":"key.default-lifetime","scope":{"installation":{}}}`},
		{magnetarv1connect.SettingsServiceSetSettingProcedure, `{"setting":{"name":"key.default-lifetime","value":"1h","scope":{"installation":{}}}}`},
	} {
		id, key := adminKey(t, e, time.Now().UTC().Add(-time.Hour), time.Time{})
		ran := e.standins.Ran.Load()
		finish := heldBody(t, e, reads, c.proc, "Bearer "+key, c.body)
		if _, err := e.master.RevokeKey(e.ctx, id, time.Now().UTC(), master.AuditEntry{ID: server.NewID(), Time: time.Now(), Method: "test", Outcome: master.OutcomeDone}, master.LocalOperator()); err != nil {
			t.Fatal(err)
		}
		resp, rid, body := finish()
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, access.MessageUnauthenticated) {
			t.Errorf("%s with a key revoked while its body arrived: %d %s", c.proc, resp.StatusCode, body)
		}
		if e.standins.Ran.Load() != ran {
			t.Errorf("%s: the handler ran for a revoked key", c.proc)
		}
		got := entriesOf(t, e, rid)
		if len(got) != 1 || got[0].Outcome != master.OutcomeRefused || got[0].Reason != "key revoked" || got[0].KeyID != id {
			t.Errorf("%s: entries %+v, want one refused, key revoked", c.proc, got)
		}
	}
}

// TestKeyExpiredWhileBodyArrives guards the same for expiry, with the
// access package's clock moved past the key's expiry while the body is
// still arriving.
func TestKeyExpiredWhileBodyArrives(t *testing.T) {
	reads := &keyReads{}
	clock := &testClock{now: time.Now().UTC()}
	e := newEnv(t, options{clock: clock.Now, store: func(s access.Store) access.Store { reads.Store = s; return reads }})
	id, key := adminKey(t, e, clock.Now().Add(-time.Hour), clock.Now().Add(time.Hour))
	finish := heldBody(t, e, reads, magnetarv1connect.SettingsServiceGetSettingProcedure, "Bearer "+key,
		`{"name":"key.default-lifetime","scope":{"installation":{}}}`)
	clock.Add(2 * time.Hour)
	resp, rid, body := finish()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a key expired while its body arrived: %d %s", resp.StatusCode, body)
	}
	got := entriesOf(t, e, rid)
	if len(got) != 1 || got[0].Outcome != master.OutcomeRefused || got[0].Reason != "key expired" || got[0].KeyID != id {
		t.Errorf("entries %+v, want one refused, key expired", got)
	}
}

// TestAllowedRequestWritesOneEntry guards that a request the check allowed
// writes exactly one audit entry however it ends before its handler records
// one: a message that cannot be read (on methods whose requirement reads it,
// where the fallback check may write the entry itself, and on one whose
// handler reads it), a handler's panic, a deadline, a cancel
// by the caller, and a handler that succeeds without recording; each with
// its fixed reason.
func TestAllowedRequestWritesOneEntry(t *testing.T) {
	e := newEnv(t, options{noNetwork: true})
	post := func(proc, body string, header map[string]string) string {
		req, _ := http.NewRequest(http.MethodPost, "http://local"+proc, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := e.socketClient(false).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.Header.Get(server.RequestIDHeader)
	}
	type want struct {
		name, id string
		outcome  master.Outcome
		reason   string
	}
	const unreadable = "the request's message could not be read"
	wants := []want{
		// GetTenant's fallback requirement names the tenant "", which does
		// not exist: the check itself answers NOT_FOUND and writes the one
		// entry, before the message is found malformed.
		{"GetTenant, bad JSON", post(magnetarv1connect.TenantServiceGetTenantProcedure, "{bad", nil), master.OutcomeFailed, "no such tenant"},
		{"GetSetting, bad JSON", post(magnetarv1connect.SettingsServiceGetSettingProcedure, "{bad", nil), master.OutcomeFailed, unreadable},
		{"ListTenants, bad JSON", post(magnetarv1connect.TenantServiceListTenantsProcedure, "{bad", nil), master.OutcomeFailed, unreadable},
		{"panic", post(magnetarv1connect.TenantServiceListTenantsProcedure, `{"pageToken":"panic"}`, nil), master.OutcomeFailed, "the handler failed (a panic) before it recorded the request"},
		{"deadline", post(magnetarv1connect.TenantServiceListTenantsProcedure, `{"pageToken":"block"}`, map[string]string{"Connect-Timeout-Ms": "100"}), master.OutcomeFailed, "the request's deadline passed before it was recorded"},
		{"succeeded without recording", post(magnetarv1connect.TenantServiceListTenantsProcedure, `{}`, nil), master.OutcomeDone, ""},
	}
	// A cancel: the caller goes away while the handler waits.
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.local().tenants.ListTenants(ctx, &magnetarv1.ListTenantsRequest{PageToken: "block"})
	}()
	waitFor(t, func() bool { return strings.Contains(metricsText(e), "magnetar_requests_in_flight 1\n") })
	cancel()
	<-done
	waitFor(t, func() bool { return strings.Contains(metricsText(e), "magnetar_requests_in_flight 0\n") })

	entries := allAudit(t, e)
	for _, w := range wants {
		var got []master.AuditEntry
		for _, en := range entries {
			if en.RequestID == w.id {
				got = append(got, en)
			}
		}
		if w.id == "" || len(got) != 1 || got[0].Outcome != w.outcome || got[0].Reason != w.reason || got[0].LocalUser == "" {
			t.Errorf("%s: entries %+v, want one %s %q", w.name, got, w.outcome, w.reason)
		}
	}
	canceled := 0
	for _, en := range entries {
		if en.Reason == "the request was canceled before it was recorded" {
			canceled++
		}
	}
	if canceled != 1 {
		t.Errorf("the canceled request wrote %d entries with its reason, want 1", canceled)
	}
}

// TestStoppedRequestWritesOneEntry guards the last way a request can end
// before its handler records it: the server stops, the drain runs out and
// the request is canceled. It writes one entry, and Stop writes it before it
// closes the audit writer.
func TestStoppedRequestWritesOneEntry(t *testing.T) {
	e := newEnv(t, options{noNetwork: true, drain: 300 * time.Millisecond, grace: time.Second})
	cl := e.local()
	go func() { _, _ = cl.tenants.ListTenants(e.ctx, &magnetarv1.ListTenantsRequest{PageToken: "block"}) }()
	waitFor(t, func() bool { return strings.Contains(metricsText(e), "magnetar_requests_in_flight 1\n") })
	if err := e.srv.Stop(); err != nil {
		t.Fatal(err)
	}
	m, err := sqlite.OpenMaster(e.ctx, filepath.Join(e.dir, "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	entries, _, err := m.ListAudit(e.ctx, master.AuditFilter{}, master.Page{Limit: master.MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, en := range entries {
		if en.Method == magnetarv1connect.TenantServiceListTenantsProcedure {
			n++
			if en.Outcome != master.OutcomeFailed || en.Reason != "the request was canceled before it was recorded" {
				t.Errorf("the stopped request's entry: %+v", en)
			}
		}
	}
	if n != 1 {
		t.Fatalf("the request stopped by the server wrote %d entries, want 1", n)
	}
}

// blockingKeys is an access.Store whose read of one key waits until the
// request's context ends, then fails as a driver fails on a canceled
// context, so a test can cancel a request in the middle of its check.
type blockingKeys struct {
	access.Store
	id      string
	entered chan struct{}
	once    sync.Once
}

// Key waits for the context when id is the blocked key's.
func (b *blockingKeys) Key(ctx context.Context, id string) (master.Key, error) {
	if id != b.id {
		return b.Store.Key(ctx, id)
	}
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	return master.Key{}, fault.Wrap(fault.Unavailable, ctx.Err(), "the database did not answer")
}

// TestCanceledCheckIsNotAStoreFailure guards that a caller who goes away
// during the check cannot make the audit log say the master failed: the
// request canceled while its key is being read is recorded as failed
// because it ended, never as a failure of the store, and the server does
// not log a store failure for it.
func TestCanceledCheckIsNotAStoreFailure(t *testing.T) {
	made, err := secret.MakeAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingKeys{id: made.ID, entered: make(chan struct{})}
	e := newEnv(t, options{store: func(s access.Store) access.Store { blocking.Store = s; return blocking }})
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() {
		_, err := e.network("Bearer "+made.Key()).access.ListKeys(ctx, &magnetarv1.ListKeysRequest{})
		done <- err
	}()
	<-blocking.entered
	cancel()
	if err := <-done; connect.CodeOf(err) != connect.CodeCanceled && !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled call: %v", err)
	}
	waitFor(t, func() bool { return strings.Contains(metricsText(e), "magnetar_requests_in_flight 0\n") })
	var got []master.AuditEntry
	for _, en := range allAudit(t, e) {
		if en.KeyID == made.ID {
			got = append(got, en)
		}
	}
	if len(got) != 1 || got[0].Outcome != master.OutcomeFailed || strings.Contains(got[0].Reason, "master database") ||
		!strings.Contains(got[0].Reason, "canceled by the caller") {
		t.Fatalf("entries %+v, want one failed because the request ended", got)
	}
	if strings.Contains(e.log.String(), "does not answer") || strings.Contains(e.log.String(), `"code":"unavailable"`) {
		t.Fatalf("a canceled check was logged as a store failure:\n%s", e.log.String())
	}
}

// TestRefusalClosesConnectionAtOnce guards that a keyless caller's HTTP/1.1
// connection is closed as soon as its refusal is written, whatever its body:
// with a body that never ends, the server does not wait for it (net/http
// would otherwise read up to 256 KiB of it, until the read bound).
func TestRefusalClosesConnectionAtOnce(t *testing.T) {
	e := newEnv(t, options{bodyRead: 5 * time.Second})
	for _, length := range []string{"100", "1000000"} {
		conn := rawTLS(t, e)
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		head := "POST " + servedProcedure + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: " + length + "\r\n\r\n{"
		if _, err := io.WriteString(conn, head); err != nil {
			t.Fatal(err)
		}
		r := bufio.NewReader(conn)
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		start := time.Now()
		var closed atomic.Bool
		if _, err := r.ReadByte(); errors.Is(err, io.EOF) {
			closed.Store(true)
		}
		took := time.Since(start)
		t.Logf("Content-Length %s: closed %v, %v after the refusal was read", length, closed.Load(), took.Round(time.Millisecond))
		if resp.StatusCode != http.StatusUnauthorized || !closed.Load() || took > time.Second {
			t.Errorf("Content-Length %s: %d, closed %v after %v; want closed at once", length, resp.StatusCode, closed.Load(), took)
		}
	}
}

// TestPrefaceAsHTTP1RefusedAlike guards that the HTTP/2 preface sent as an
// HTTP/1 request ("PRI * HTTP/2.0" over HTTP/1.1, which net/http hands to
// the handler) gets the same bytes as any other keyless plain request, the
// date and the request id masked, its Connection header in the same place.
func TestPrefaceAsHTTP1RefusedAlike(t *testing.T) {
	e := newEnv(t, options{})
	raw := func(req string) string {
		conn := rawTLS(t, e)
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, req); err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(conn)
		var lines []string
		for _, l := range strings.Split(string(b), "\r\n") {
			if strings.HasPrefix(l, "Date: ") || strings.HasPrefix(l, server.RequestIDHeader+": ") {
				l = l[:strings.Index(l, ":")] + ": -"
			}
			lines = append(lines, l)
		}
		return strings.Join(lines, "\n")
	}
	pri := raw("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	plain := raw("GET /x HTTP/1.1\r\nHost: x\r\n\r\n")
	if pri != plain || !strings.Contains(plain, "401") {
		t.Fatalf("the preface as HTTP/1:\n%s\nanother plain request:\n%s", pri, plain)
	}
}
