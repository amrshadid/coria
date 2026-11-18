package mastertest

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// Open returns a fresh, empty, migrated store that no other test uses, and
// closes it when tb ends (tb.Cleanup). Closing a store twice must be
// harmless, since a test may close it itself.
type Open func(tb testing.TB) master.Store

// T0 is the time the suite's data starts at: a time with microseconds, so a
// driver that drops them fails.
var T0 = time.Date(2026, 11, 18, 9, 30, 0, 123456000, time.UTC)

// Run runs the whole contract suite against the stores open makes. Each
// test has its own store and runs in parallel with the others.
func Run(t *testing.T, open Open) {
	tests := []struct {
		name string
		fn   func(*env)
	}{
		{"Schema", testSchema},
		{"Closed", testClosed},
		{"Times", testTimes},
		{"Tenants", testTenants},
		{"TenantsListed", testTenantsListed},
		{"TenantNameRace", testTenantNameRace},
		{"Keys", testKeys},
		{"KeysListed", testKeysListed},
		{"KeyHolderUnicode", testKeyHolderUnicode},
		{"MarkKeyUsed", testMarkKeyUsed},
		{"MarkKeyUsedConcurrent", testMarkKeyUsedConcurrent},
		{"Grants", testGrants},
		{"GrantsListed", testGrantsListed},
		{"SetGrantRace", testSetGrantRace},
		{"Settings", testSettings},
		{"SettingsListed", testSettingsListed},
		{"Audit", testAudit},
		{"AuditFilters", testAuditFilters},
		{"AuditPaging10000", testAuditPaging10000},
		{"PageLimits", testPageLimits},
		{"BothOrNeither", testBothOrNeither},
		{"FailuresWriteNoAudit", testFailuresWriteNoAudit},
		{"CursorsBound", testCursorsBound},
		{"ErrorKinds", testErrorKinds},
		{"CloseInFlight", testCloseInFlight},
		{"RolesFor", testRolesFor},
		{"GrantTenantNames", testGrantTenantNames},
		{"MarkKeyUsedEdges", testMarkKeyUsedEdges},
		{"IDsCaseAndOrder", testIDsCaseAndOrder},
		{"ExactText", testExactText},
		{"TimeEdges", testTimeEdges},
		{"PagingWithDeletes", testPagingWithDeletes},
		{"Ties", testTies},
		{"ConcurrentSameRow", testConcurrentSameRow},
		{"SizeBounds", testSizeBounds},
		{"SetGrantRevokeRace", testSetGrantRevokeRace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.fn(&env{T: t, s: open(t), ctx: context.Background()})
		})
	}
}

// env is one test's store and the helpers that make its data.
type env struct {
	*testing.T
	s   master.Store
	ctx context.Context
	seq int
}

// id returns a new id with the prefix, unique within the test.
func (e *env) id(prefix string) string {
	e.seq++
	return fmt.Sprintf("%s%06d", prefix, e.seq)
}

// audit returns a new audit entry with a unique id, for a change.
func (e *env) audit() master.AuditEntry {
	return master.AuditEntry{
		ID:            e.id("a"),
		Time:          T0,
		LocalUser:     "501",
		RemoteAddress: "local",
		Method:        "Test",
		Outcome:       master.OutcomeDone,
		RequestID:     "r1",
	}
}

// tenant returns a new valid tenant with the name.
func (e *env) tenant(name string) master.Tenant {
	return master.Tenant{
		ID:            e.id("t"),
		Name:          name,
		Driver:        "sqlite",
		SealedAddress: []byte("sealed:" + name),
		State:         master.StateReady,
		Created:       T0,
	}
}

// key returns a new valid key.
func (e *env) key() master.Key {
	id := e.id("k")
	hash := make([]byte, master.KeyHashLength)
	copy(hash, id)
	return master.Key{ID: id, Hash: hash, Holder: "holder of " + id, Created: T0, Expires: T0.Add(90 * 24 * time.Hour)}
}

// mustTenant creates a tenant named name and returns it.
func (e *env) mustTenant(name string) master.Tenant {
	e.Helper()
	t := e.tenant(name)
	if err := e.s.CreateTenant(e.ctx, t, e.audit()); err != nil {
		e.Fatalf("CreateTenant(%s): %v", name, err)
	}
	return t
}

// mustKey creates a key and returns it.
func (e *env) mustKey() master.Key {
	e.Helper()
	k := e.key()
	if err := e.s.CreateKey(e.ctx, k, e.audit()); err != nil {
		e.Fatalf("CreateKey: %v", err)
	}
	return k
}

// mustGrant sets a grant.
func (e *env) mustGrant(keyID string, scope master.Scope, role master.Role) {
	e.Helper()
	if _, err := e.s.SetGrant(e.ctx, master.Grant{KeyID: keyID, Scope: scope, Role: role}, e.audit()); err != nil {
		e.Fatalf("SetGrant: %v", err)
	}
}

// allAudit returns every audit entry, newest first, by paging through.
func (e *env) allAudit(filter master.AuditFilter) []master.AuditEntry {
	e.Helper()
	var out []master.AuditEntry
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		entries, next, err := e.s.ListAudit(e.ctx, filter, page)
		if err != nil {
			e.Fatalf("ListAudit: %v", err)
		}
		out = append(out, entries...)
		if next == "" {
			return out
		}
		page.Cursor = next
	}
}

// auditIDs returns the set of every audit entry's id.
func (e *env) auditIDs() map[string]bool {
	e.Helper()
	ids := map[string]bool{}
	for _, a := range e.allAudit(master.AuditFilter{}) {
		ids[a.ID] = true
	}
	return ids
}

// wantKind fails unless err has the kind want.
func (e *env) wantKind(what string, err error, want fault.Kind) {
	e.Helper()
	if err == nil {
		e.Errorf("%s: no error, want %v", what, want)
		return
	}
	if got := fault.KindOf(err); got != want {
		e.Errorf("%s: kind %v (%v), want %v", what, got, err, want)
	}
}

// ok fails the test now when err is not nil.
func (e *env) ok(what string, err error) {
	e.Helper()
	if err != nil {
		e.Fatalf("%s: %v", what, err)
	}
}

// SameTime reports whether a and b are the same instant, and a is in UTC
// unless it is the zero time.
func SameTime(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && b.IsZero()
	}
	return a.Equal(b) && a.Location() == time.UTC
}

// diffTenant describes how got differs from want, or returns "".
func diffTenant(got, want master.Tenant) string {
	var d []string
	if got.ID != want.ID || got.Name != want.Name || got.Driver != want.Driver {
		d = append(d, fmt.Sprintf("id/name/driver %s/%s/%s, want %s/%s/%s", got.ID, got.Name, got.Driver, want.ID, want.Name, want.Driver))
	}
	if string(got.SealedAddress) != string(want.SealedAddress) {
		d = append(d, "sealed address differs")
	}
	if got.State != want.State || got.StateReason != want.StateReason {
		d = append(d, fmt.Sprintf("state %s %q, want %s %q", got.State, got.StateReason, want.State, want.StateReason))
	}
	if !SameTime(got.Created, want.Created) {
		d = append(d, fmt.Sprintf("created %v, want %v", got.Created, want.Created))
	}
	return strings.Join(d, "; ")
}

// diffKey describes how got differs from want, or returns "".
func diffKey(got, want master.Key) string {
	var d []string
	if got.ID != want.ID || got.Holder != want.Holder {
		d = append(d, fmt.Sprintf("id/holder %s/%q, want %s/%q", got.ID, got.Holder, want.ID, want.Holder))
	}
	if string(got.Hash) != string(want.Hash) {
		d = append(d, "hash differs")
	}
	for _, f := range []struct {
		name      string
		got, want time.Time
	}{
		{"created", got.Created, want.Created},
		{"expires", got.Expires, want.Expires},
		{"last used", got.LastUsed, want.LastUsed},
		{"revoked", got.Revoked, want.Revoked},
	} {
		if !SameTime(f.got, f.want) {
			d = append(d, fmt.Sprintf("%s %v, want %v", f.name, f.got, f.want))
		}
	}
	return strings.Join(d, "; ")
}

// DiffAudit describes how got differs from want, or returns "". No details
// and empty details are the same.
func DiffAudit(got, want master.AuditEntry) string {
	var d []string
	gt, wt := got.Time, want.Time
	got.Time, want.Time = time.Time{}, time.Time{}
	gd, wd := got.Details, want.Details
	got.Details, want.Details = nil, nil
	if !reflect.DeepEqual(got, want) {
		d = append(d, fmt.Sprintf("%+v, want %+v", got, want))
	}
	if !SameTime(gt, wt) {
		d = append(d, fmt.Sprintf("time %v, want %v", gt, wt))
	}
	if len(gd) != 0 || len(wd) != 0 {
		if !maps.Equal(gd, wd) {
			d = append(d, fmt.Sprintf("details %v, want %v", gd, wd))
		}
	}
	return strings.Join(d, "; ")
}

// testSchema guards Migrate and Version: a migrated store is at a version
// above 0, and migrating again changes nothing.
func testSchema(e *env) {
	v, err := e.s.Version(e.ctx)
	e.ok("Version", err)
	if v < 1 {
		e.Errorf("Version = %d after Migrate, want at least 1", v)
	}
	e.ok("Migrate again", e.s.Migrate(e.ctx))
	again, err := e.s.Version(e.ctx)
	e.ok("Version", err)
	if again != v {
		e.Errorf("Version after a second Migrate = %d, want %d", again, v)
	}
	e.ok("Ping", e.s.Ping(e.ctx))
}

// testClosed guards that a closed store fails every call as Unavailable
// instead of panicking, and that Close may be called again.
func testClosed(e *env) {
	k := e.mustKey()
	e.ok("Close", e.s.Close())
	e.ok("second Close", e.s.Close())
	_, err := e.s.Key(e.ctx, k.ID)
	e.wantKind("Key after Close", err, fault.Unavailable)
	e.wantKind("CreateKey after Close", e.s.CreateKey(e.ctx, e.key(), e.audit()), fault.Unavailable)
	e.wantKind("Ping after Close", e.s.Ping(e.ctx), fault.Unavailable)
}

// testTimes guards that times come back in UTC with their microseconds,
// whatever zone they went in with, and that what is finer than a
// microsecond is dropped.
func testTimes(e *env) {
	zone := time.FixedZone("UTC+4", 4*60*60)
	in := time.Date(2026, 11, 18, 13, 30, 0, 123456789, zone)
	k := e.key()
	k.Created = in
	k.Expires = time.Date(2099, 12, 31, 23, 59, 59, 999999000, time.UTC)
	e.ok("CreateKey", e.s.CreateKey(e.ctx, k, e.audit()))
	got, err := e.s.Key(e.ctx, k.ID)
	e.ok("Key", err)
	want := time.Date(2026, 11, 18, 9, 30, 0, 123456000, time.UTC)
	if !got.Created.Equal(want) || got.Created.Location() != time.UTC || got.Created.Nanosecond() != 123456000 {
		t := got.Created
		e.Errorf("created = %v (%v), want %v in UTC", t, t.Location(), want)
	}
	if !SameTime(got.Expires, k.Expires) {
		e.Errorf("expires = %v, want %v", got.Expires, k.Expires)
	}
	if !got.LastUsed.IsZero() || !got.Revoked.IsZero() {
		e.Errorf("a new key has last used %v and revoked %v, want zero", got.LastUsed, got.Revoked)
	}
}

// testTenants guards each tenant operation: the normal case read back
// exactly, and each failure kind.
func testTenants(e *env) {
	t := e.tenant("lab")
	t.StateReason = "first"
	e.ok("CreateTenant", e.s.CreateTenant(e.ctx, t, e.audit()))
	got, err := e.s.Tenant(e.ctx, t.ID)
	e.ok("Tenant", err)
	if d := diffTenant(got, t); d != "" {
		e.Errorf("Tenant: %s", d)
	}
	got, err = e.s.TenantByName(e.ctx, "lab")
	e.ok("TenantByName", err)
	if d := diffTenant(got, t); d != "" {
		e.Errorf("TenantByName: %s", d)
	}

	sameName := e.tenant("lab")
	e.wantKind("CreateTenant, name taken", e.s.CreateTenant(e.ctx, sameName, e.audit()), fault.Conflict)
	sameID := e.tenant("other")
	sameID.ID = t.ID
	e.wantKind("CreateTenant, id taken", e.s.CreateTenant(e.ctx, sameID, e.audit()), fault.Conflict)
	bad := e.tenant("Bad Name")
	e.wantKind("CreateTenant, bad name", e.s.CreateTenant(e.ctx, bad, e.audit()), fault.Invalid)
	badAudit := e.audit()
	badAudit.Outcome = "maybe"
	e.wantKind("CreateTenant, bad audit entry", e.s.CreateTenant(e.ctx, e.tenant("fine"), badAudit), fault.Invalid)

	_, err = e.s.Tenant(e.ctx, "nope")
	e.wantKind("Tenant, unknown", err, fault.NotFound)
	_, err = e.s.TenantByName(e.ctx, "nope")
	e.wantKind("TenantByName, unknown", err, fault.NotFound)
	_, err = e.s.TenantByName(e.ctx, "No/Pe")
	e.wantKind("TenantByName, bad name", err, fault.Invalid)
	_, err = e.s.Tenant(e.ctx, "a/b")
	e.wantKind("Tenant, bad id", err, fault.Invalid)

	e.ok("SetTenantState", e.s.SetTenantState(e.ctx, t.ID, master.StateFailed, "upgrade to 2 failed: disk full", e.audit()))
	got, err = e.s.Tenant(e.ctx, t.ID)
	e.ok("Tenant", err)
	if got.State != master.StateFailed || got.StateReason != "upgrade to 2 failed: disk full" {
		e.Errorf("state = %s %q after SetTenantState", got.State, got.StateReason)
	}
	e.wantKind("SetTenantState, unknown", e.s.SetTenantState(e.ctx, "nope", master.StateReady, "", e.audit()), fault.NotFound)
	e.wantKind("SetTenantState, bad state", e.s.SetTenantState(e.ctx, t.ID, "gone", "", e.audit()), fault.Invalid)

	k := e.mustKey()
	e.mustGrant(k.ID, master.TenantScope(t.ID), master.RoleReader)
	e.mustGrant(k.ID, master.Installation, master.RoleReader)
	e.ok("RemoveTenant", e.s.RemoveTenant(e.ctx, t.ID, e.audit()))
	_, err = e.s.Tenant(e.ctx, t.ID)
	e.wantKind("Tenant after RemoveTenant", err, fault.NotFound)
	grants, _, err := e.s.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 10})
	e.ok("GrantsByKey", err)
	if len(grants) != 1 || !grants[0].Scope.IsInstallation() {
		e.Errorf("grants after the tenant's removal = %+v, want the installation grant only", grants)
	}
	e.wantKind("RemoveTenant, unknown", e.s.RemoveTenant(e.ctx, t.ID, e.audit()), fault.NotFound)
	// The name is free again once the tenant is gone.
	e.mustTenant("lab")
}

// testKeys guards each key operation: the normal case read back exactly,
// revocation once, and each failure kind.
func testKeys(e *env) {
	k := e.key()
	e.ok("CreateKey", e.s.CreateKey(e.ctx, k, e.audit()))
	got, err := e.s.Key(e.ctx, k.ID)
	e.ok("Key", err)
	if d := diffKey(got, k); d != "" {
		e.Errorf("Key: %s", d)
	}
	never := e.key()
	never.Expires = time.Time{}
	e.ok("CreateKey, no expiry", e.s.CreateKey(e.ctx, never, e.audit()))
	got, err = e.s.Key(e.ctx, never.ID)
	e.ok("Key", err)
	if !got.Expires.IsZero() {
		e.Errorf("a key with no expiry reads back expiring %v", got.Expires)
	}

	dup := e.key()
	dup.ID = k.ID
	e.wantKind("CreateKey, id taken", e.s.CreateKey(e.ctx, dup, e.audit()), fault.Conflict)
	short := e.key()
	short.Hash = short.Hash[:16]
	e.wantKind("CreateKey, short hash", e.s.CreateKey(e.ctx, short, e.audit()), fault.Invalid)
	_, err = e.s.Key(e.ctx, "nope")
	e.wantKind("Key, unknown", err, fault.NotFound)

	revoked := T0.Add(time.Hour)
	at, err := e.s.RevokeKey(e.ctx, k.ID, revoked, e.audit())
	e.ok("RevokeKey", err)
	if !SameTime(at, revoked) {
		e.Errorf("RevokeKey returned %v, want %v", at, revoked)
	}
	again := e.audit()
	at, err = e.s.RevokeKey(e.ctx, k.ID, revoked.Add(time.Hour), again)
	e.ok("RevokeKey again", err)
	if !SameTime(at, revoked) {
		e.Errorf("RevokeKey again returned %v, want the first revocation %v", at, revoked)
	}
	if !e.auditIDs()[again.ID] {
		e.Error("revoking a revoked key wrote no audit entry; the request is still recorded")
	}
	got, err = e.s.Key(e.ctx, k.ID)
	e.ok("Key", err)
	if !SameTime(got.Revoked, revoked) {
		e.Errorf("revoked = %v, want the first revocation %v", got.Revoked, revoked)
	}
	_, err = e.s.RevokeKey(e.ctx, "nope", revoked, e.audit())
	e.wantKind("RevokeKey, unknown", err, fault.NotFound)
	_, err = e.s.RevokeKey(e.ctx, never.ID, time.Time{}, e.audit())
	e.wantKind("RevokeKey, zero time", err, fault.Invalid)
}

// testKeyHolderUnicode guards that a holder's name is any UTF-8 up to 256
// bytes, kept exactly, and that longer or invalid UTF-8 is refused (D37).
func testKeyHolderUnicode(e *env) {
	holders := []string{
		"Zoë Ångström-Øster",
		"山田 太郎 (研究室)",
		"د. أمل",
		"🧠 imaging bot",
		strings.Repeat("ß", 128), // 256 bytes
	}
	for _, h := range holders {
		k := e.key()
		k.Holder = h
		e.ok("CreateKey "+h, e.s.CreateKey(e.ctx, k, e.audit()))
		got, err := e.s.Key(e.ctx, k.ID)
		e.ok("Key", err)
		if got.Holder != h {
			e.Errorf("holder = %q, want %q", got.Holder, h)
		}
	}
	long := e.key()
	long.Holder = strings.Repeat("ß", 128) + "x"
	e.wantKind("holder of 257 bytes", e.s.CreateKey(e.ctx, long, e.audit()), fault.Invalid)
	broken := e.key()
	broken.Holder = "caf\xe9"
	e.wantKind("holder not UTF-8", e.s.CreateKey(e.ctx, broken, e.audit()), fault.Invalid)
}

// testMarkKeyUsed guards the throttle: a thousand calls within one minute
// write once, the first; a call more than a minute after the stored time
// writes again; an unknown key writes nothing and is not an error.
func testMarkKeyUsed(e *env) {
	k := e.mustKey()
	writes := 0
	for i := range 1000 {
		at := T0.Add(time.Duration(i) * 59 * time.Millisecond) // the last is at 58.941 s
		wrote, err := e.s.MarkKeyUsed(e.ctx, k.ID, at)
		e.ok("MarkKeyUsed", err)
		if wrote {
			writes++
		}
	}
	if writes != 1 {
		e.Errorf("1000 calls within a minute wrote %d times, want 1", writes)
	}
	got, err := e.s.Key(e.ctx, k.ID)
	e.ok("Key", err)
	if !SameTime(got.LastUsed, T0) {
		e.Errorf("last used = %v, want the first call's %v", got.LastUsed, T0)
	}
	wrote, err := e.s.MarkKeyUsed(e.ctx, k.ID, T0.Add(time.Minute))
	e.ok("MarkKeyUsed", err)
	if wrote {
		e.Error("a call exactly one minute later wrote; the stored time must be more than a minute older")
	}
	later := T0.Add(time.Minute + time.Microsecond)
	wrote, err = e.s.MarkKeyUsed(e.ctx, k.ID, later)
	e.ok("MarkKeyUsed", err)
	if !wrote {
		e.Error("a call more than a minute later did not write")
	}
	got, err = e.s.Key(e.ctx, k.ID)
	e.ok("Key", err)
	if !SameTime(got.LastUsed, later) {
		e.Errorf("last used = %v, want %v", got.LastUsed, later)
	}
	wrote, err = e.s.MarkKeyUsed(e.ctx, "nope", T0)
	if err != nil || wrote {
		e.Errorf("MarkKeyUsed(unknown) = %v, %v; want false, nil", wrote, err)
	}
	_, err = e.s.MarkKeyUsed(e.ctx, k.ID, time.Time{})
	e.wantKind("MarkKeyUsed, zero time", err, fault.Invalid)
}

// testMarkKeyUsedConcurrent guards that many requests with the same key at
// the same moment write its last use once.
func testMarkKeyUsedConcurrent(e *env) {
	k := e.mustKey()
	const callers = 50
	results := make(chan error, callers)
	wrote := make(chan bool, callers)
	for range callers {
		go func() {
			w, err := e.s.MarkKeyUsed(e.ctx, k.ID, T0)
			wrote <- w
			results <- err
		}()
	}
	writes := 0
	for range callers {
		if err := <-results; err != nil {
			e.Errorf("MarkKeyUsed: %v", err)
		}
		if <-wrote {
			writes++
		}
	}
	if writes != 1 {
		e.Errorf("%d concurrent calls wrote %d times, want 1", callers, writes)
	}
}

// testGrants guards setting a grant (new, then replaced, returning the old
// role and auditing both), the lists by key and by scope, removal, and each
// failure kind.
func testGrants(e *env) {
	t := e.mustTenant("lab")
	k := e.mustKey()
	scope := master.TenantScope(t.ID)

	first := e.audit()
	first.Details = map[string]string{"scope": scope.String()}
	prev, err := e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: scope, Role: master.RoleReader}, first)
	e.ok("SetGrant", err)
	if prev != "" {
		e.Errorf("SetGrant on no grant returned %q, want the empty role", prev)
	}
	second := e.audit()
	prev, err = e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: scope, Role: master.RoleAdmin}, second)
	e.ok("SetGrant again", err)
	if prev != master.RoleReader {
		e.Errorf("SetGrant on a reader grant returned %q, want reader", prev)
	}
	grants, next, err := e.s.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 10})
	e.ok("GrantsByKey", err)
	if len(grants) != 1 || grants[0].Role != master.RoleAdmin || grants[0].Scope != scope || next != "" {
		e.Errorf("grants = %+v (next %q), want one admin grant on %s", grants, next, scope)
	}

	byID := map[string]master.AuditEntry{}
	for _, a := range e.allAudit(master.AuditFilter{}) {
		byID[a.ID] = a
	}
	if d := byID[first.ID].Details; d["old_role"] != "none" || d["new_role"] != "reader" || d["scope"] != scope.String() {
		e.Errorf("first set's audit details = %v, want old none, new reader and the caller's scope", d)
	}
	if d := byID[second.ID].Details; d["old_role"] != "reader" || d["new_role"] != "admin" {
		e.Errorf("second set's audit details = %v, want old reader, new admin", d)
	}

	_, err = e.s.SetGrant(e.ctx, master.Grant{KeyID: "nope", Scope: master.Installation, Role: master.RoleReader}, e.audit())
	e.wantKind("SetGrant, unknown key", err, fault.NotFound)
	_, err = e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: master.TenantScope("nope"), Role: master.RoleReader}, e.audit())
	e.wantKind("SetGrant, unknown tenant", err, fault.NotFound)
	_, err = e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: master.Installation, Role: "owner"}, e.audit())
	e.wantKind("SetGrant, bad role", err, fault.Invalid)

	removal := e.audit()
	prev, err = e.s.RemoveGrant(e.ctx, k.ID, scope, removal)
	e.ok("RemoveGrant", err)
	if prev != master.RoleAdmin {
		e.Errorf("RemoveGrant returned %q, want admin", prev)
	}
	grants, _, err = e.s.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 10})
	e.ok("GrantsByKey", err)
	if len(grants) != 0 {
		e.Errorf("grants after RemoveGrant = %+v, want none", grants)
	}
	again := e.audit()
	prev, err = e.s.RemoveGrant(e.ctx, k.ID, scope, again)
	if err != nil || prev != "" {
		e.Errorf("RemoveGrant of no grant = %q, %v; want the empty role and success", prev, err)
	}
	byID = map[string]master.AuditEntry{}
	for _, a := range e.allAudit(master.AuditFilter{}) {
		byID[a.ID] = a
	}
	if d := byID[removal.ID].Details; d["old_role"] != "admin" || d["new_role"] != "none" {
		e.Errorf("removal's audit details = %v, want old admin, new none", d)
	}
	if d := byID[again.ID].Details; d["old_role"] != "none" || d["new_role"] != "none" {
		e.Errorf("second removal's audit details = %v, want old none, new none", d)
	}
	_, err = e.s.RemoveGrant(e.ctx, "nope", scope, e.audit())
	e.wantKind("RemoveGrant, unknown key", err, fault.NotFound)
	_, err = e.s.RemoveGrant(e.ctx, k.ID, master.TenantScope("nope"), e.audit())
	e.wantKind("RemoveGrant, unknown tenant", err, fault.NotFound)
	grants, _, err = e.s.GrantsByKey(e.ctx, "nope", master.Page{Limit: 10})
	if err != nil || len(grants) != 0 {
		e.Errorf("GrantsByKey(unknown) = %v, %v; want none, nil", grants, err)
	}
}

// testGrantsListed guards both grant lists: by key, the installation first
// and then by tenant id; by scope, by key id; each paged across inserts.
func testGrantsListed(e *env) {
	k := e.mustKey()
	e.mustGrant(k.ID, master.Installation, master.RoleAdmin)
	var tenants []master.Tenant
	for i := range 23 {
		t := e.mustTenant(fmt.Sprintf("t%02d", i))
		tenants = append(tenants, t)
		e.mustGrant(k.ID, master.TenantScope(t.ID), master.RoleReader)
	}
	inserted := 0
	checkPaging(e, "GrantsByKey",
		func(p master.Page) ([]master.Grant, string, error) { return e.s.GrantsByKey(e.ctx, k.ID, p) },
		func(g master.Grant) string { return g.Scope.TenantID },
		func() {
			inserted++
			t := e.mustTenant(fmt.Sprintf("new%02d", inserted))
			e.mustGrant(k.ID, master.TenantScope(t.ID), master.RoleWriter)
		},
		24, 5)

	for range 21 {
		other := e.mustKey()
		e.mustGrant(other.ID, master.TenantScope(tenants[0].ID), master.RoleWriter)
	}
	checkPaging(e, "GrantsByScope",
		func(p master.Page) ([]master.Grant, string, error) {
			return e.s.GrantsByScope(e.ctx, master.TenantScope(tenants[0].ID), p)
		},
		func(g master.Grant) string { return g.KeyID },
		func() { e.mustGrant(e.mustKey().ID, master.TenantScope(tenants[0].ID), master.RoleReader) },
		22, 4)
	install, _, err := e.s.GrantsByScope(e.ctx, master.Installation, master.Page{Limit: 10})
	e.ok("GrantsByScope(installation)", err)
	if len(install) != 1 || install[0].KeyID != k.ID || !install[0].Scope.IsInstallation() {
		e.Errorf("installation grants = %+v, want the one admin grant", install)
	}
}

// testSetGrantRace guards that two concurrent sets on the same key and scope
// are ordered: exactly one sees no grant before it, the other sees the
// first one's role, and each one's audit entry says what it saw.
func testSetGrantRace(e *env) {
	t := e.mustTenant("lab")
	for round := range 25 {
		k := e.mustKey()
		scope := master.TenantScope(t.ID)
		if round%2 == 1 {
			scope = master.Installation
		}
		roles := []master.Role{master.RoleReader, master.RoleWriter}
		entries := []master.AuditEntry{e.audit(), e.audit()}
		prev := make([]master.Role, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		done := make(chan int, 2)
		for i := range 2 {
			go func() {
				<-start
				prev[i], errs[i] = e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: scope, Role: roles[i]}, entries[i])
				done <- i
			}()
		}
		close(start)
		<-done
		<-done
		if errs[0] != nil || errs[1] != nil {
			e.Fatalf("round %d: SetGrant: %v, %v", round, errs[0], errs[1])
		}
		var firstIdx int
		switch {
		case prev[0] == "" && prev[1] == roles[0]:
			firstIdx = 0
		case prev[1] == "" && prev[0] == roles[1]:
			firstIdx = 1
		default:
			e.Fatalf("round %d: previous roles %q and %q; want exactly one none and the other the first one's role", round, prev[0], prev[1])
		}
		lastIdx := 1 - firstIdx
		grants, _, err := e.s.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 10})
		e.ok("GrantsByKey", err)
		if len(grants) != 1 || grants[0].Role != roles[lastIdx] {
			e.Fatalf("round %d: grants = %+v, want one with the last set's role %s", round, grants, roles[lastIdx])
		}
		byID := map[string]master.AuditEntry{}
		for _, a := range e.allAudit(master.AuditFilter{}) {
			byID[a.ID] = a
		}
		if d := byID[entries[firstIdx].ID].Details; d["old_role"] != "none" || d["new_role"] != string(roles[firstIdx]) {
			e.Errorf("round %d: first set's audit = %v", round, d)
		}
		if d := byID[entries[lastIdx].ID].Details; d["old_role"] != string(roles[firstIdx]) || d["new_role"] != string(roles[lastIdx]) {
			e.Errorf("round %d: second set's audit = %v", round, d)
		}
	}
}

// testTenantNameRace guards the unique tenant name under concurrency: many
// creations of one name at once, with different ids, end with exactly one
// tenant, one success, every other Conflict, and an audit entry for the
// success only.
func testTenantNameRace(e *env) {
	const rounds, racers = 30, 8
	for round := range rounds {
		name := fmt.Sprintf("race%02d", round)
		tenants := make([]master.Tenant, racers)
		entries := make([]master.AuditEntry, racers)
		for i := range racers {
			tenants[i] = e.tenant(name)
			entries[i] = e.audit()
		}
		errs := make([]error, racers)
		start := make(chan struct{})
		done := make(chan struct{}, racers)
		for i := range racers {
			go func() {
				<-start
				errs[i] = e.s.CreateTenant(e.ctx, tenants[i], entries[i])
				done <- struct{}{}
			}()
		}
		close(start)
		for range racers {
			<-done
		}
		winners := 0
		var winner int
		for i, err := range errs {
			switch {
			case err == nil:
				winners++
				winner = i
			case fault.KindOf(err) != fault.Conflict:
				e.Errorf("round %d: creation %d failed with %v, want Conflict", round, i, err)
			}
		}
		if winners != 1 {
			e.Fatalf("round %d: %d creations of one name succeeded, want 1", round, winners)
		}
		got, err := e.s.TenantByName(e.ctx, name)
		e.ok("TenantByName", err)
		if got.ID != tenants[winner].ID {
			e.Errorf("round %d: the tenant is %s, want the winner %s", round, got.ID, tenants[winner].ID)
		}
		ids := e.auditIDs()
		for i := range racers {
			if ids[entries[i].ID] != (i == winner) {
				e.Errorf("round %d: audit entry of creation %d present = %v, want %v", round, i, ids[entries[i].ID], i == winner)
			}
		}
	}
}

// testSettings guards each setting operation and the bounds of a name and a
// value.
func testSettings(e *env) {
	_, err := e.s.Setting(e.ctx, "api.limit")
	e.wantKind("Setting, unset", err, fault.NotFound)
	e.ok("SetSetting", e.s.SetSetting(e.ctx, "api.limit", "4 MiB", e.audit()))
	v, err := e.s.Setting(e.ctx, "api.limit")
	if err != nil || v != "4 MiB" {
		e.Errorf("Setting = %q, %v; want 4 MiB", v, err)
	}
	e.ok("SetSetting again", e.s.SetSetting(e.ctx, "api.limit", "", e.audit()))
	v, err = e.s.Setting(e.ctx, "api.limit")
	if err != nil || v != "" {
		e.Errorf("Setting = %q, %v; want the empty value, which is a value", v, err)
	}
	big := strings.Repeat("v", master.MaxSettingValueLength-len("é")) + "é"
	e.ok("SetSetting 64 KiB", e.s.SetSetting(e.ctx, "big", big, e.audit()))
	v, err = e.s.Setting(e.ctx, "big")
	if err != nil || v != big {
		e.Errorf("a 64 KiB value read back with %d bytes, %v", len(v), err)
	}
	e.wantKind("SetSetting, value too big", e.s.SetSetting(e.ctx, "big", big+"x", e.audit()), fault.Invalid)
	e.wantKind("SetSetting, bad name", e.s.SetSetting(e.ctx, "API Limit", "x", e.audit()), fault.Invalid)
	removed, err := e.s.UnsetSetting(e.ctx, "api.limit", e.audit())
	if err != nil || !removed {
		e.Errorf("UnsetSetting = %v, %v; want true", removed, err)
	}
	_, err = e.s.Setting(e.ctx, "api.limit")
	e.wantKind("Setting after UnsetSetting", err, fault.NotFound)
	again := e.audit()
	removed, err = e.s.UnsetSetting(e.ctx, "api.limit", again)
	if err != nil || removed {
		e.Errorf("UnsetSetting of an unset name = %v, %v; want false and success", removed, err)
	}
	if !e.auditIDs()[again.ID] {
		e.Error("unsetting an unset name wrote no audit entry; the request is still recorded")
	}
	_, err = e.s.UnsetSetting(e.ctx, "Bad Name", e.audit())
	e.wantKind("UnsetSetting, bad name", err, fault.Invalid)
}

// testAudit guards that an entry reads back exactly, with Unicode and every
// field at its bound, that one may name a tenant that has no row, that an
// id is unique, and that a batch is written all or none.
func testAudit(e *env) {
	full := master.AuditEntry{
		ID:            e.id("a"),
		Time:          T0.Add(time.Second),
		KeyID:         "K1234567890a",
		RemoteAddress: "[2001:db8::1]:51234",
		Method:        "/magnetar.v1.AdminService/CreateTenant",
		TenantName:    "never-existed",
		Object:        "tenant/never-existed",
		Outcome:       master.OutcomeRefused,
		Reason:        strings.Repeat("ü", master.MaxAuditReasonLength/2),
		Details:       map[string]string{},
		RequestID:     "req-0001",
	}
	for i := range master.MaxAuditDetails {
		full.Details[fmt.Sprintf("key-%02d-%s", i, strings.Repeat("k", master.MaxAuditDetailKeyLength-7))] =
			strings.Repeat("値", master.MaxAuditDetailValueLength/3)
	}
	local := e.audit()
	local.Details = nil
	e.ok("AppendAudit", e.s.AppendAudit(e.ctx, full, local))
	got := e.allAudit(master.AuditFilter{})
	if len(got) != 2 {
		e.Fatalf("%d entries, want 2", len(got))
	}
	if d := DiffAudit(got[0], full); d != "" {
		e.Errorf("entry: %s", d)
	}
	if d := DiffAudit(got[1], local); d != "" {
		e.Errorf("entry: %s", d)
	}
	byTenant := e.allAudit(master.AuditFilter{TenantName: "never-existed"})
	if len(byTenant) != 1 || byTenant[0].ID != full.ID {
		e.Errorf("entries for a tenant with no row = %d, want the one", len(byTenant))
	}

	e.wantDuplicateAudit("AppendAudit, id taken", e.s.AppendAudit(e.ctx, full))
	fresh := e.audit()
	e.wantDuplicateAudit("AppendAudit, batch with an id taken", e.s.AppendAudit(e.ctx, fresh, full))
	if e.auditIDs()[fresh.ID] {
		e.Error("the batch's first entry was written although the batch failed")
	}
	tooMany := e.audit()
	tooMany.Details = map[string]string{}
	for i := range master.MaxAuditDetails + 1 {
		tooMany.Details[fmt.Sprint(i)] = "v"
	}
	e.wantKind("AppendAudit, 17 details", e.s.AppendAudit(e.ctx, tooMany), fault.Invalid)
	e.ok("AppendAudit, nothing", e.s.AppendAudit(e.ctx))
	batch := make([]master.AuditEntry, master.MaxAuditBatch+1)
	for i := range batch {
		batch[i] = e.audit()
	}
	e.wantKind("AppendAudit, too many", e.s.AppendAudit(e.ctx, batch...), fault.Invalid)
}

// testAuditFilters guards each filter of the audit listing, alone and
// together, and the order newest first with the id breaking ties.
func testAuditFilters(e *env) {
	var all []master.AuditEntry
	tenants := []string{"lab", "clinic", ""}
	keys := []string{"KA", "KB", ""}
	outcomes := []master.Outcome{master.OutcomeDone, master.OutcomeRefused, master.OutcomeFailed}
	for i := range 90 {
		a := e.audit()
		a.Time = T0.Add(time.Duration(i/2) * time.Second) // pairs share a time
		a.TenantName = tenants[i%3]
		a.KeyID, a.LocalUser = keys[(i/3)%3], ""
		a.Outcome = outcomes[(i/9)%3]
		all = append(all, a)
	}
	e.ok("AppendAudit", e.s.AppendAudit(e.ctx, all...))

	filters := []master.AuditFilter{
		{},
		{TenantName: "lab"},
		{KeyID: "KB"},
		{Outcome: master.OutcomeRefused},
		{Since: T0.Add(10 * time.Second)},
		{Until: T0.Add(20 * time.Second)},
		{Since: T0.Add(5 * time.Second), Until: T0.Add(25 * time.Second)},
		{TenantName: "clinic", KeyID: "KA"},
		{TenantName: "lab", Outcome: master.OutcomeFailed, Since: T0.Add(30 * time.Second)},
		{KeyID: "KA", Since: T0.Add(3 * time.Second), Until: T0.Add(40 * time.Second)},
		{TenantName: "nobody"},
	}
	for _, f := range filters {
		var want []master.AuditEntry
		for i := len(all) - 1; i >= 0; i-- {
			a := all[i]
			if (f.TenantName == "" || a.TenantName == f.TenantName) &&
				(f.KeyID == "" || a.KeyID == f.KeyID) &&
				(f.Outcome == "" || a.Outcome == f.Outcome) &&
				(f.Since.IsZero() || !a.Time.Before(f.Since)) &&
				(f.Until.IsZero() || a.Time.Before(f.Until)) {
				want = append(want, a)
			}
		}
		// Newest first; within a time, the larger id first. The ids grow
		// with i, so walking all backward is already that order.
		var got []master.AuditEntry
		page := master.Page{Limit: 7}
		for {
			entries, next, err := e.s.ListAudit(e.ctx, f, page)
			e.ok("ListAudit", err)
			got = append(got, entries...)
			if next == "" {
				break
			}
			page.Cursor = next
		}
		if len(got) != len(want) {
			e.Errorf("filter %+v: %d entries, want %d", f, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i].ID != want[i].ID {
				e.Errorf("filter %+v: entry %d is %s, want %s", f, i, got[i].ID, want[i].ID)
				break
			}
		}
	}
	_, _, err := e.s.ListAudit(e.ctx, master.AuditFilter{Since: T0, Until: T0}, master.Page{Limit: 5})
	e.wantKind("ListAudit, empty range", err, fault.Invalid)
}

// testAuditPaging10000 guards that a listing of 10,000 entries, paged, with
// entries inserted between pages before, after and at the position reached,
// returns each of the 10,000 exactly once, in order.
func testAuditPaging10000(e *env) {
	const n = 10000
	originals := make([]master.AuditEntry, n)
	for i := range originals {
		a := e.audit()
		a.ID = fmt.Sprintf("o%05d", i)
		a.Time = T0.Add(time.Duration(i/3) * time.Millisecond) // threes share a time
		originals[i] = a
	}
	e.ok("AppendAudit 10,000", e.s.AppendAudit(e.ctx, originals...))

	seen := map[string]int{}
	var order []master.AuditEntry
	page := master.Page{Limit: 97}
	pages := 0
	for {
		entries, next, err := e.s.ListAudit(e.ctx, master.AuditFilter{}, page)
		e.ok("ListAudit", err)
		pages++
		for _, a := range entries {
			seen[a.ID]++
		}
		order = append(order, entries...)
		if next == "" {
			break
		}
		page.Cursor = next
		// Between pages: one entry newer than all, one older than all, and
		// one at the exact time of the last entry returned, with ids on
		// both sides of it.
		last := entries[len(entries)-1]
		ins := []master.AuditEntry{e.audit(), e.audit(), e.audit(), e.audit()}
		ins[0].ID, ins[0].Time = fmt.Sprintf("new-%04d", pages), T0.Add(time.Hour)
		ins[1].ID, ins[1].Time = fmt.Sprintf("old-%04d", pages), T0.Add(-time.Hour)
		ins[2].ID, ins[2].Time = last.ID+"-after", last.Time
		ins[3].ID, ins[3].Time = "a"+fmt.Sprintf("%04d", pages), last.Time
		e.ok("AppendAudit between pages", e.s.AppendAudit(e.ctx, ins...))
		if pages > 2*n {
			e.Fatal("the listing does not end")
		}
	}
	for _, a := range originals {
		if seen[a.ID] != 1 {
			e.Fatalf("entry %s returned %d times, want once", a.ID, seen[a.ID])
		}
	}
	for id, count := range seen {
		if count != 1 {
			e.Errorf("entry %s returned %d times", id, count)
		}
	}
	for i := 1; i < len(order); i++ {
		p, c := order[i-1], order[i]
		if c.Time.After(p.Time) || (c.Time.Equal(p.Time) && c.ID >= p.ID) {
			e.Fatalf("entry %d (%s at %v) is not before entry %d (%s at %v)", i, c.ID, c.Time, i-1, p.ID, p.Time)
		}
	}
	e.Logf("10,000 entries in %d pages, %d entries returned with the ones inserted", pages, len(order))
}

// checkPaging lists with list, limit at a time, calling insert between
// pages, and checks that each of the first want items (those present
// before the listing) is returned exactly once and that the keys come in
// increasing order.
func checkPaging[T any](e *env, what string, list func(master.Page) ([]T, string, error), key func(T) string, insert func(), want, limit int) {
	e.Helper()
	before := map[string]bool{}
	page := master.Page{Limit: master.MaxPageLimit}
	items, _, err := list(page)
	e.ok(what, err)
	if len(items) != want {
		e.Fatalf("%s: %d items before paging, want %d", what, len(items), want)
	}
	for _, it := range items {
		before[key(it)] = true
	}
	seen := map[string]int{}
	var keys []string
	page = master.Page{Limit: limit}
	for range 1000 {
		items, next, err := list(page)
		e.ok(what, err)
		if len(items) > limit {
			e.Fatalf("%s: a page of %d items with limit %d", what, len(items), limit)
		}
		for _, it := range items {
			seen[key(it)]++
			keys = append(keys, key(it))
		}
		if next == "" {
			break
		}
		page.Cursor = next
		insert()
	}
	for k := range before {
		if seen[k] != 1 {
			e.Errorf("%s: item %q returned %d times, want once", what, k, seen[k])
		}
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] <= keys[i-1] {
			e.Errorf("%s: %q after %q, out of order", what, keys[i], keys[i-1])
		}
	}
}

// testTenantsListed guards the tenant list: by name, paged across inserts.
func testTenantsListed(e *env) {
	for i := range 40 {
		e.mustTenant(fmt.Sprintf("m%02d", i))
	}
	n := 0
	checkPaging(e, "ListTenants", func(p master.Page) ([]master.Tenant, string, error) { return e.s.ListTenants(e.ctx, p) },
		func(t master.Tenant) string { return t.Name },
		func() {
			n++
			e.mustTenant(fmt.Sprintf("a%02d", n)) // before the position reached
			e.mustTenant(fmt.Sprintf("z%02d", n)) // after it
		},
		40, 6)
}

// testKeysListed guards the key list: by creation then id, paged across
// inserts, with creation times that tie.
func testKeysListed(e *env) {
	for i := range 40 {
		k := e.key()
		k.Created = T0.Add(time.Duration(i/4) * time.Second)
		k.Expires = T0.Add(time.Hour)
		e.ok("CreateKey", e.s.CreateKey(e.ctx, k, e.audit()))
	}
	n := 0
	order := func(k master.Key) string {
		return fmt.Sprintf("%020d/%s", k.Created.UnixMicro(), k.ID)
	}
	checkPaging(e, "ListKeys", func(p master.Page) ([]master.Key, string, error) { return e.s.ListKeys(e.ctx, p) }, order,
		func() {
			n++
			k := e.key()
			k.Created = T0.Add(time.Duration(n%10) * time.Second)
			k.Expires = T0.Add(time.Hour)
			e.ok("CreateKey", e.s.CreateKey(e.ctx, k, e.audit()))
		},
		40, 7)
}

// testSettingsListed guards the settings list: by name, paged across
// inserts.
func testSettingsListed(e *env) {
	for i := range 30 {
		e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("m.%02d", i), "v", e.audit()))
	}
	n := 0
	checkPaging(e, "ListSettings", func(p master.Page) ([]master.Setting, string, error) { return e.s.ListSettings(e.ctx, p) },
		func(s master.Setting) string { return s.Name },
		func() {
			n++
			e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("a.%02d", n), "v", e.audit()))
			e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("z.%02d", n), "v", e.audit()))
		},
		30, 4)
}

// testPageLimits guards that every list refuses a limit of 0 or above 1000,
// and a cursor it did not make, as Invalid.
func testPageLimits(e *env) {
	k := e.mustKey()
	t := e.mustTenant("lab")
	lists := map[string]func(master.Page) error{
		"ListTenants": func(p master.Page) error { _, _, err := e.s.ListTenants(e.ctx, p); return err },
		"ListKeys":    func(p master.Page) error { _, _, err := e.s.ListKeys(e.ctx, p); return err },
		"GrantsByKey": func(p master.Page) error { _, _, err := e.s.GrantsByKey(e.ctx, k.ID, p); return err },
		"GrantsByScope": func(p master.Page) error {
			_, _, err := e.s.GrantsByScope(e.ctx, master.TenantScope(t.ID), p)
			return err
		},
		"ListSettings": func(p master.Page) error { _, _, err := e.s.ListSettings(e.ctx, p); return err },
		"ListAudit": func(p master.Page) error {
			_, _, err := e.s.ListAudit(e.ctx, master.AuditFilter{}, p)
			return err
		},
	}
	// A cursor some list really returned, to hand to the others.
	for i := range 3 {
		e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("s%d", i), "v", e.audit()))
	}
	_, settingsCursor, err := e.s.ListSettings(e.ctx, master.Page{Limit: 1})
	e.ok("ListSettings", err)
	if settingsCursor == "" {
		e.Fatal("ListSettings with more to come returned no cursor")
	}
	for name, list := range lists {
		e.ok(name+" limit 1", list(master.Page{Limit: 1}))
		e.ok(name+" limit 1000", list(master.Page{Limit: 1000}))
		e.wantKind(name+" limit 0", list(master.Page{Limit: 0}), fault.Invalid)
		e.wantKind(name+" limit 1001", list(master.Page{Limit: 1001}), fault.Invalid)
		e.wantKind(name+" garbage cursor", list(master.Page{Limit: 5, Cursor: "%%%not-a-cursor"}), fault.Invalid)
		if name != "ListSettings" {
			e.wantKind(name+" another list's cursor", list(master.Page{Limit: 5, Cursor: settingsCursor}), fault.Invalid)
		}
	}
}

// testBothOrNeither guards the rule that a change and its audit entry are
// written both or neither. For every changing operation, the audit entry is
// made to fail by giving it an id already taken; the operation must fail
// with ErrDuplicateAuditID and leave the change unmade. A driver that writes
// the change first and the entry second, outside one transaction, fails
// here.
func testBothOrNeither(e *env) {
	taken := e.audit()
	e.ok("AppendAudit", e.s.AppendAudit(e.ctx, taken))
	dup := func() master.AuditEntry {
		a := e.audit()
		a.ID = taken.ID
		return a
	}
	before := len(e.auditIDs())
	check := func(op string, err error, unchanged func() string) {
		e.Helper()
		e.wantDuplicateAudit(op+" with a taken audit id", err)
		if d := unchanged(); d != "" {
			e.Errorf("%s with a failed audit entry changed the master: %s", op, d)
		}
		if n := len(e.auditIDs()); n != before {
			e.Errorf("%s with a failed audit entry: %d audit entries, want %d", op, n, before)
		}
	}

	// CreateTenant.
	nt := e.tenant("never")
	check("CreateTenant", e.s.CreateTenant(e.ctx, nt, dup()), func() string {
		if _, err := e.s.TenantByName(e.ctx, "never"); fault.KindOf(err) != fault.NotFound {
			return fmt.Sprintf("the tenant exists (%v)", err)
		}
		return ""
	})

	t := e.mustTenant("lab")
	k := e.mustKey()
	e.mustGrant(k.ID, master.TenantScope(t.ID), master.RoleReader)
	e.ok("SetSetting", e.s.SetSetting(e.ctx, "kept", "old", e.audit()))
	before = len(e.auditIDs())

	// SetTenantState.
	check("SetTenantState", e.s.SetTenantState(e.ctx, t.ID, master.StateFailed, "x", dup()), func() string {
		got, err := e.s.Tenant(e.ctx, t.ID)
		if err != nil || got.State != master.StateReady {
			return fmt.Sprintf("state %s (%v)", got.State, err)
		}
		return ""
	})
	// RemoveTenant, which also removes grants.
	check("RemoveTenant", e.s.RemoveTenant(e.ctx, t.ID, dup()), func() string {
		if _, err := e.s.Tenant(e.ctx, t.ID); err != nil {
			return fmt.Sprintf("the tenant is gone (%v)", err)
		}
		g, _, err := e.s.GrantsByScope(e.ctx, master.TenantScope(t.ID), master.Page{Limit: 10})
		if err != nil || len(g) != 1 {
			return fmt.Sprintf("its grants are %v (%v)", g, err)
		}
		return ""
	})
	// CreateKey.
	nk := e.key()
	check("CreateKey", e.s.CreateKey(e.ctx, nk, dup()), func() string {
		if _, err := e.s.Key(e.ctx, nk.ID); fault.KindOf(err) != fault.NotFound {
			return fmt.Sprintf("the key exists (%v)", err)
		}
		return ""
	})
	// RevokeKey.
	_, err := e.s.RevokeKey(e.ctx, k.ID, T0.Add(time.Hour), dup())
	check("RevokeKey", err, func() string {
		got, err := e.s.Key(e.ctx, k.ID)
		if err != nil || !got.Revoked.IsZero() {
			return fmt.Sprintf("revoked %v (%v)", got.Revoked, err)
		}
		return ""
	})
	// SetGrant, new and replacing.
	grantIs := func(scope master.Scope, want master.Role) func() string {
		return func() string {
			g, _, err := e.s.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 10})
			if err != nil {
				return err.Error()
			}
			for _, x := range g {
				if x.Scope == scope {
					if x.Role != want {
						return fmt.Sprintf("role %s, want %s", x.Role, want)
					}
					return ""
				}
			}
			if want != "" {
				return "the grant is gone"
			}
			return ""
		}
	}
	_, err = e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: master.Installation, Role: master.RoleAdmin}, dup())
	check("SetGrant (new)", err, grantIs(master.Installation, ""))
	_, err = e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: master.TenantScope(t.ID), Role: master.RoleAdmin}, dup())
	check("SetGrant (replace)", err, grantIs(master.TenantScope(t.ID), master.RoleReader))
	// RemoveGrant.
	_, err = e.s.RemoveGrant(e.ctx, k.ID, master.TenantScope(t.ID), dup())
	check("RemoveGrant", err, grantIs(master.TenantScope(t.ID), master.RoleReader))
	// SetSetting, new and replacing.
	check("SetSetting (new)", e.s.SetSetting(e.ctx, "fresh", "v", dup()), func() string {
		if _, err := e.s.Setting(e.ctx, "fresh"); fault.KindOf(err) != fault.NotFound {
			return fmt.Sprintf("the setting exists (%v)", err)
		}
		return ""
	})
	settingIs := func() string {
		v, err := e.s.Setting(e.ctx, "kept")
		if err != nil || v != "old" {
			return fmt.Sprintf("value %q (%v)", v, err)
		}
		return ""
	}
	check("SetSetting (replace)", e.s.SetSetting(e.ctx, "kept", "new", dup()), settingIs)
	// UnsetSetting.
	_, err = e.s.UnsetSetting(e.ctx, "kept", dup())
	check("UnsetSetting", err, settingIs)
	// AppendAudit: a batch whose second entry fails writes neither.
	first := e.audit()
	check("AppendAudit (batch)", e.s.AppendAudit(e.ctx, first, dup()), func() string {
		if e.auditIDs()[first.ID] {
			return "the batch's first entry was written"
		}
		return ""
	})
}
