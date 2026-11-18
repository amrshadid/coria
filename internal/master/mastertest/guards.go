package mastertest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// This file holds the tests that pin what a second driver could otherwise
// get subtly wrong and still pass: cursors bound to their list, error kinds,
// both or neither for failures, byte-exact text and ids, the edges of time
// and of every size bound, paging across deletes, ties, and concurrent calls
// on one row.

// wantDuplicateAudit fails unless err is ErrDuplicateAuditID of kind
// Internal.
func (e *env) wantDuplicateAudit(what string, err error) {
	e.Helper()
	if !errors.Is(err, master.ErrDuplicateAuditID) || fault.KindOf(err) != fault.Internal {
		e.Errorf("%s: %v (kind %v), want ErrDuplicateAuditID of kind Internal", what, err, fault.KindOf(err))
	}
}

// altered returns c with one character in its middle changed.
func altered(c string) string {
	i := len(c) / 2
	r := byte('A')
	if c[i] == 'A' {
		r = 'B'
	}
	return c[:i] + string(r) + c[i+1:]
}

// testCursorsBound guards that a cursor is accepted only by the list, and
// the arguments, it was made for: another list, another key or scope,
// another audit filter (any field), an altered cursor and a cut one are all
// Invalid (the API contract's page token rule).
func testCursorsBound(e *env) {
	lab, clinic := e.mustTenant("lab"), e.mustTenant("clinic")
	k1, k2 := e.mustKey(), e.mustKey()
	for _, k := range []master.Key{k1, k2} {
		e.mustGrant(k.ID, master.Installation, master.RoleReader)
		e.mustGrant(k.ID, master.TenantScope(lab.ID), master.RoleReader)
		e.mustGrant(k.ID, master.TenantScope(clinic.ID), master.RoleReader)
	}
	e.mustTenant("third")
	for i := range 3 {
		e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("s%d", i), "v", e.audit()))
	}
	filter := master.AuditFilter{TenantName: "lab", KeyID: k1.ID, Outcome: master.OutcomeDone, Since: T0, Until: T0.Add(time.Hour)}
	for range 3 {
		a := e.audit()
		a.TenantName, a.KeyID, a.LocalUser = "lab", k1.ID, ""
		e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a))
	}

	page := func(c string) master.Page { return master.Page{Limit: 1, Cursor: c} }
	type list struct {
		call func(cursor string) (string, error)
	}
	lists := map[string]list{
		"ListTenants": {func(c string) (string, error) { _, n, err := e.s.ListTenants(e.ctx, page(c)); return n, err }},
		"ListKeys":    {func(c string) (string, error) { _, n, err := e.s.ListKeys(e.ctx, page(c)); return n, err }},
		"GrantsByKey": {func(c string) (string, error) { _, n, err := e.s.GrantsByKey(e.ctx, k1.ID, page(c)); return n, err }},
		"GrantsByScope": {func(c string) (string, error) {
			_, n, err := e.s.GrantsByScope(e.ctx, master.TenantScope(lab.ID), page(c))
			return n, err
		}},
		"ListSettings": {func(c string) (string, error) { _, n, err := e.s.ListSettings(e.ctx, page(c)); return n, err }},
		"ListAudit":    {func(c string) (string, error) { _, n, err := e.s.ListAudit(e.ctx, filter, page(c)); return n, err }},
	}
	cursors := map[string]string{}
	for name, l := range lists {
		c, err := l.call("")
		e.ok(name, err)
		if c == "" {
			e.Fatalf("%s returned no cursor with more to come", name)
		}
		cursors[name] = c
		if _, err := l.call(c); err != nil {
			e.Errorf("%s refuses its own cursor: %v", name, err)
		}
		_, err = l.call(altered(c))
		e.wantKind(name+" with an altered cursor", err, fault.Invalid)
		_, err = l.call(c[:len(c)-1])
		e.wantKind(name+" with a cut cursor", err, fault.Invalid)
	}
	for name, l := range lists {
		for other, c := range cursors {
			if other == name {
				continue
			}
			_, err := l.call(c)
			e.wantKind(name+" with a cursor from "+other, err, fault.Invalid)
		}
	}

	// The same list with other arguments.
	_, _, err := e.s.GrantsByKey(e.ctx, k2.ID, page(cursors["GrantsByKey"]))
	e.wantKind("GrantsByKey cursor for another key", err, fault.Invalid)
	for name, scope := range map[string]master.Scope{"another tenant": master.TenantScope(clinic.ID), "the installation": master.Installation} {
		_, _, err := e.s.GrantsByScope(e.ctx, scope, page(cursors["GrantsByScope"]))
		e.wantKind("GrantsByScope cursor for "+name, err, fault.Invalid)
	}
	changed := map[string]master.AuditFilter{}
	f := filter
	f.TenantName = "clinic"
	changed["tenant"] = f
	f = filter
	f.KeyID = k2.ID
	changed["key"] = f
	f = filter
	f.Outcome = master.OutcomeFailed
	changed["outcome"] = f
	f = filter
	f.Since = T0.Add(-time.Second)
	changed["start time"] = f
	f = filter
	f.Until = T0.Add(2 * time.Hour)
	changed["end time"] = f
	changed["no filter"] = master.AuditFilter{}
	for name, f := range changed {
		_, _, err := e.s.ListAudit(e.ctx, f, page(cursors["ListAudit"]))
		e.wantKind("ListAudit cursor with another "+name, err, fault.Invalid)
	}
}

// testErrorKinds guards the kinds of the failures a caller acts on, and that
// a call that returned an error committed nothing: a context already
// canceled, a deadline already passed, and a context canceled while calls
// are in flight.
func testErrorKinds(e *env) {
	canceled, cancel := context.WithCancel(e.ctx)
	cancel()
	k, a := e.key(), e.audit()
	err := e.s.CreateKey(canceled, k, a)
	e.wantKind("CreateKey with a canceled context", err, fault.Unavailable)
	if !errors.Is(err, context.Canceled) {
		e.Errorf("CreateKey with a canceled context: %v does not keep context.Canceled", err)
	}
	_, err = e.s.Key(canceled, "anything")
	e.wantKind("Key with a canceled context", err, fault.Unavailable)
	_, _, err = e.s.ListAudit(canceled, master.AuditFilter{}, master.Page{Limit: 5})
	e.wantKind("ListAudit with a canceled context", err, fault.Unavailable)

	past, cancelPast := context.WithDeadline(e.ctx, time.Now().Add(-time.Second))
	defer cancelPast()
	a2 := e.audit()
	err = e.s.SetSetting(past, "late", "v", a2)
	e.wantKind("SetSetting past its deadline", err, fault.Unavailable)
	if !errors.Is(err, context.DeadlineExceeded) {
		e.Errorf("SetSetting past its deadline: %v does not keep context.DeadlineExceeded", err)
	}
	if _, err := e.s.Key(e.ctx, k.ID); fault.KindOf(err) != fault.NotFound {
		e.Errorf("the key of the canceled CreateKey exists (%v)", err)
	}
	if _, err := e.s.Setting(e.ctx, "late"); fault.KindOf(err) != fault.NotFound {
		e.Errorf("the setting of the late SetSetting exists (%v)", err)
	}
	ids := e.auditIDs()
	if ids[a.ID] || ids[a2.ID] {
		e.Error("an audit entry of a call that failed was written")
	}

	// Contexts canceled at varied moments: whatever each call returned,
	// it committed exactly when it returned nil.
	type result struct {
		key   master.Key
		entry master.AuditEntry
		err   error
	}
	results := make([]result, 60)
	var wg sync.WaitGroup
	for i := range results {
		results[i].key, results[i].entry = e.key(), e.audit()
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(e.ctx)
			go func() { time.Sleep(time.Duration(i%6) * 300 * time.Microsecond); cancel() }()
			results[i].err = e.s.CreateKey(ctx, results[i].key, results[i].entry)
			cancel()
		}()
	}
	wg.Wait()
	ids = e.auditIDs()
	failed := 0
	for _, r := range results {
		_, kerr := e.s.Key(e.ctx, r.key.ID)
		committed := kerr == nil
		if r.err != nil {
			failed++
			if k := fault.KindOf(r.err); k != fault.Unavailable {
				e.Errorf("CreateKey under a canceled context: kind %v (%v), want Unavailable", k, r.err)
			}
		}
		if committed != (r.err == nil) || ids[r.entry.ID] != (r.err == nil) {
			e.Errorf("CreateKey returned %v but the key present = %v, its audit entry present = %v", r.err, committed, ids[r.entry.ID])
		}
	}
	e.Logf("%d of %d calls failed under a canceled context", failed, len(results))
}

// testCloseInFlight guards that calls in flight while the store is closed
// fail as Unavailable, never with another kind or a panic.
func testCloseInFlight(e *env) {
	k := e.mustKey()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	stop := make(chan struct{})
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var err error
				if i%2 == 0 {
					_, err = e.s.Key(e.ctx, k.ID)
				} else {
					err = e.s.SetSetting(e.ctx, "busy", "v", e.auditAt(i))
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	e.ok("Close", e.s.Close())
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		if fault.KindOf(err) != fault.Unavailable {
			e.Errorf("a call in flight during Close: kind %v (%v), want Unavailable", fault.KindOf(err), err)
		}
	}
}

// auditMu guards env.seq for helpers called from several goroutines.
var auditMu sync.Mutex

// auditAt returns a new audit entry; safe to call from several goroutines.
func (e *env) auditAt(int) master.AuditEntry {
	auditMu.Lock()
	defer auditMu.Unlock()
	return e.audit()
}

// testRolesFor guards the access check's lookup: the installation's role and
// the named tenant's, each none when absent, for an unknown key, an unknown
// tenant and no tenant, and after the tenant's removal.
func testRolesFor(e *env) {
	lab, _ := e.mustTenant("lab"), e.mustTenant("clinic")
	both, tenantOnly, none := e.mustKey(), e.mustKey(), e.mustKey()
	e.mustGrant(both.ID, master.Installation, master.RoleReader)
	e.mustGrant(both.ID, master.TenantScope(lab.ID), master.RoleWriter)
	e.mustGrant(tenantOnly.ID, master.TenantScope(lab.ID), master.RoleAdmin)
	cases := []struct {
		key, tenant      string
		wantInst, wantTn master.Role
	}{
		{both.ID, "lab", master.RoleReader, master.RoleWriter},
		{both.ID, "clinic", master.RoleReader, ""},
		{both.ID, "", master.RoleReader, ""},
		{both.ID, "nope", master.RoleReader, ""},
		{tenantOnly.ID, "lab", "", master.RoleAdmin},
		{tenantOnly.ID, "", "", ""},
		{none.ID, "lab", "", ""},
		{"unknown", "lab", "", ""},
	}
	for _, c := range cases {
		inst, tn, err := e.s.RolesFor(e.ctx, c.key, c.tenant)
		if err != nil || inst != c.wantInst || tn != c.wantTn {
			e.Errorf("RolesFor(%s, %q) = %q, %q, %v; want %q, %q", c.key, c.tenant, inst, tn, err, c.wantInst, c.wantTn)
		}
	}
	_, _, err := e.s.RolesFor(e.ctx, both.ID, "Bad Name")
	e.wantKind("RolesFor, bad tenant name", err, fault.Invalid)
	_, _, err = e.s.RolesFor(e.ctx, "a b", "lab")
	e.wantKind("RolesFor, bad key id", err, fault.Invalid)
	e.ok("RemoveTenant", e.s.RemoveTenant(e.ctx, lab.ID, e.audit()))
	inst, tn, err := e.s.RolesFor(e.ctx, both.ID, "lab")
	if err != nil || inst != master.RoleReader || tn != "" {
		e.Errorf("RolesFor after the tenant's removal = %q, %q, %v; want reader and none", inst, tn, err)
	}
	// A tenant made again under the name is a new tenant with no grants.
	e.mustTenant("lab")
	if _, tn, _ := e.s.RolesFor(e.ctx, both.ID, "lab"); tn != "" {
		e.Errorf("a new tenant under a removed tenant's name inherited the role %q", tn)
	}
}

// testGrantTenantNames guards that the grant lists carry each tenant's name
// as it is at the time of the read, and none for the installation.
func testGrantTenantNames(e *env) {
	lab, clinic := e.mustTenant("lab"), e.mustTenant("clinic")
	k := e.mustKey()
	e.mustGrant(k.ID, master.Installation, master.RoleAdmin)
	e.mustGrant(k.ID, master.TenantScope(lab.ID), master.RoleReader)
	e.mustGrant(k.ID, master.TenantScope(clinic.ID), master.RoleWriter)
	grants, _, err := e.s.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 10})
	e.ok("GrantsByKey", err)
	want := map[string]string{"": "", lab.ID: "lab", clinic.ID: "clinic"}
	if len(grants) != 3 {
		e.Fatalf("%d grants, want 3", len(grants))
	}
	for _, g := range grants {
		if g.TenantName != want[g.Scope.TenantID] {
			e.Errorf("grant on %s carries the name %q, want %q", g.Scope, g.TenantName, want[g.Scope.TenantID])
		}
	}
	byScope, _, err := e.s.GrantsByScope(e.ctx, master.TenantScope(clinic.ID), master.Page{Limit: 10})
	e.ok("GrantsByScope", err)
	if len(byScope) != 1 || byScope[0].TenantName != "clinic" {
		e.Errorf("GrantsByScope = %+v, want one grant named clinic", byScope)
	}
	inst, _, err := e.s.GrantsByScope(e.ctx, master.Installation, master.Page{Limit: 10})
	e.ok("GrantsByScope(installation)", err)
	if len(inst) != 1 || inst[0].TenantName != "" {
		e.Errorf("installation grants = %+v, want one with no tenant name", inst)
	}
}

// testMarkKeyUsedEdges guards the throttle at its exact edges and that the
// stored time never moves backward.
func testMarkKeyUsedEdges(e *env) {
	k := e.mustKey()
	at := func(t time.Time) bool {
		e.Helper()
		w, err := e.s.MarkKeyUsed(e.ctx, k.ID, t)
		e.ok("MarkKeyUsed", err)
		return w
	}
	if !at(T0) {
		e.Fatal("the first use did not write")
	}
	if at(T0.Add(-time.Hour)) || at(T0.Add(-time.Microsecond)) {
		e.Error("a use older than the stored time wrote")
	}
	if at(T0) {
		e.Error("a use at the stored time wrote")
	}
	if at(T0.Add(time.Minute)) {
		e.Error("a use exactly one minute later wrote")
	}
	if !at(T0.Add(time.Minute + time.Microsecond)) {
		e.Error("a use one minute and one microsecond later did not write")
	}
	if at(T0) {
		e.Error("after a newer use, an older one wrote")
	}
	got, err := e.s.Key(e.ctx, k.ID)
	e.ok("Key", err)
	if !sameTime(got.LastUsed, T0.Add(time.Minute+time.Microsecond)) {
		e.Errorf("last used = %v, want the newest use", got.LastUsed)
	}
}

// testFailuresWriteNoAudit guards both or neither in the other direction:
// for every failure of every changing operation, the audit entry it was
// handed is absent afterwards.
func testFailuresWriteNoAudit(e *env) {
	t := e.mustTenant("lab")
	k := e.mustKey()
	gone := e.mustKey()
	_, err := e.s.RevokeKey(e.ctx, gone.ID, T0.Add(time.Hour), e.audit())
	e.ok("RevokeKey", err)
	e.ok("SetSetting", e.s.SetSetting(e.ctx, "kept", "v", e.audit()))

	type call struct {
		name string
		kind fault.Kind
		run  func(a master.AuditEntry) error
	}
	sameName := e.tenant("lab")
	sameID := e.tenant("other")
	sameID.ID = t.ID
	dupKey := e.key()
	dupKey.ID = k.ID
	badTenant := e.tenant("Bad")
	calls := []call{
		{"CreateTenant, name taken", fault.Conflict, func(a master.AuditEntry) error { return e.s.CreateTenant(e.ctx, sameName, a) }},
		{"CreateTenant, id taken", fault.Conflict, func(a master.AuditEntry) error { return e.s.CreateTenant(e.ctx, sameID, a) }},
		{"CreateTenant, invalid", fault.Invalid, func(a master.AuditEntry) error { return e.s.CreateTenant(e.ctx, badTenant, a) }},
		{"SetTenantState, unknown", fault.NotFound, func(a master.AuditEntry) error {
			return e.s.SetTenantState(e.ctx, "nope", master.StateFailed, "", a)
		}},
		{"SetTenantState, invalid", fault.Invalid, func(a master.AuditEntry) error { return e.s.SetTenantState(e.ctx, t.ID, "gone", "", a) }},
		{"RemoveTenant, unknown", fault.NotFound, func(a master.AuditEntry) error { return e.s.RemoveTenant(e.ctx, "nope", a) }},
		{"CreateKey, id taken", fault.Conflict, func(a master.AuditEntry) error { return e.s.CreateKey(e.ctx, dupKey, a) }},
		{"RevokeKey, unknown", fault.NotFound, func(a master.AuditEntry) error { _, err := e.s.RevokeKey(e.ctx, "nope", T0, a); return err }},
		{"SetGrant, unknown key", fault.NotFound, func(a master.AuditEntry) error {
			_, err := e.s.SetGrant(e.ctx, master.Grant{KeyID: "nope", Scope: master.Installation, Role: master.RoleReader}, a)
			return err
		}},
		{"SetGrant, unknown tenant", fault.NotFound, func(a master.AuditEntry) error {
			_, err := e.s.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: master.TenantScope("nope"), Role: master.RoleReader}, a)
			return err
		}},
		{"SetGrant, revoked key", fault.Conflict, func(a master.AuditEntry) error {
			_, err := e.s.SetGrant(e.ctx, master.Grant{KeyID: gone.ID, Scope: master.Installation, Role: master.RoleReader}, a)
			return err
		}},
		{"RemoveGrant, unknown key", fault.NotFound, func(a master.AuditEntry) error {
			_, err := e.s.RemoveGrant(e.ctx, "nope", master.Installation, a)
			return err
		}},
		{"RemoveGrant, unknown tenant", fault.NotFound, func(a master.AuditEntry) error {
			_, err := e.s.RemoveGrant(e.ctx, k.ID, master.TenantScope("nope"), a)
			return err
		}},
		{"SetSetting, invalid", fault.Invalid, func(a master.AuditEntry) error { return e.s.SetSetting(e.ctx, "Bad Name", "v", a) }},
		{"UnsetSetting, invalid", fault.Invalid, func(a master.AuditEntry) error { _, err := e.s.UnsetSetting(e.ctx, "Bad Name", a); return err }},
		{"AppendAudit, an invalid entry in the batch", fault.Invalid, func(a master.AuditEntry) error {
			bad := e.audit()
			bad.Outcome = "maybe"
			return e.s.AppendAudit(e.ctx, a, bad)
		}},
	}
	for _, c := range calls {
		a := e.audit()
		e.wantKind(c.name, c.run(a), c.kind)
		if e.auditIDs()[a.ID] {
			e.Errorf("%s failed but wrote its audit entry", c.name)
		}
	}
}

// testIDsCaseAndOrder guards that ids are compared whole and case-sensitively
// and that lists order text by its bytes, with mixed case, "-" and "_".
func testIDsCaseAndOrder(e *env) {
	upper, lower := e.key(), e.key()
	upper.ID, lower.ID = "CaseKey", "casekey"
	upper.Holder, lower.Holder = "upper", "lower"
	e.ok("CreateKey CaseKey", e.s.CreateKey(e.ctx, upper, e.audit()))
	e.ok("CreateKey casekey", e.s.CreateKey(e.ctx, lower, e.audit()))
	for _, k := range []master.Key{upper, lower} {
		got, err := e.s.Key(e.ctx, k.ID)
		if err != nil || got.Holder != k.Holder {
			e.Errorf("Key(%s) = %q, %v; want %q", k.ID, got.Holder, err, k.Holder)
		}
	}
	for _, id := range []string{"CASEKEY", "caseKey", "CaseKe", "CaseKeyX"} {
		if _, err := e.s.Key(e.ctx, id); fault.KindOf(err) != fault.NotFound {
			e.Errorf("Key(%s) = %v, want NotFound: ids are compared whole and by case", id, err)
		}
	}
	t1, t2 := e.tenant("upper"), e.tenant("lower")
	t1.ID, t2.ID = "Tid", "tid"
	e.ok("CreateTenant Tid", e.s.CreateTenant(e.ctx, t1, e.audit()))
	e.ok("CreateTenant tid", e.s.CreateTenant(e.ctx, t2, e.audit()))
	if got, err := e.s.Tenant(e.ctx, "tid"); err != nil || got.Name != "lower" {
		e.Errorf("Tenant(tid) = %q, %v; want lower", got.Name, err)
	}

	// Keys of one creation time, so the list orders them by id alone.
	ids := []string{"a_b", "a-b", "A-b", "B", "a", "_x", "-x", "Z", "z9", "Z_", "AB", "Ab"}
	later := T0.Add(24 * time.Hour)
	for _, id := range ids {
		k := e.key()
		k.ID, k.Created, k.Expires = id, later, later.Add(time.Hour)
		e.ok("CreateKey "+id, e.s.CreateKey(e.ctx, k, e.audit()))
	}
	want := slices.Clone(ids)
	sort.Strings(want) // byte order
	keys, _, err := e.s.ListKeys(e.ctx, master.Page{Limit: 1000})
	e.ok("ListKeys", err)
	var got []string
	for _, k := range keys {
		if k.Created.Equal(later) {
			got = append(got, k.ID)
		}
	}
	if !slices.Equal(got, want) {
		e.Errorf("keys of one time in order %q, want byte order %q", got, want)
	}
	scope := master.TenantScope(t1.ID)
	for _, id := range ids {
		e.mustGrant(id, scope, master.RoleReader)
	}
	grants, _, err := e.s.GrantsByScope(e.ctx, scope, master.Page{Limit: 1000})
	e.ok("GrantsByScope", err)
	got = got[:0]
	for _, g := range grants {
		got = append(got, g.KeyID)
	}
	if !slices.Equal(got, want) {
		e.Errorf("grants on one scope in order %q, want byte order %q", got, want)
	}

	// Tenant ids for the list by key, and names and setting names.
	tids := []string{"T_1", "T-1", "t1", "T1", "_t"}
	k := e.mustKey()
	for i, id := range tids {
		t := e.tenant(fmt.Sprintf("o%d", i))
		t.ID = id
		e.ok("CreateTenant "+id, e.s.CreateTenant(e.ctx, t, e.audit()))
		e.mustGrant(k.ID, master.TenantScope(id), master.RoleReader)
	}
	wantT := append([]string{""}, tids...)
	sort.Strings(wantT)
	byKey, _, err := e.s.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 1000})
	e.ok("GrantsByKey", err)
	got = got[:0]
	for _, g := range byKey {
		got = append(got, g.Scope.TenantID)
	}
	if !slices.Equal(got, wantT[1:]) { // no installation grant here
		e.Errorf("grants of one key in order %q, want byte order %q", got, wantT[1:])
	}
	names := []string{"n-b", "n0", "nb", "n", "n-"}
	for _, n := range names {
		e.mustTenant(n)
	}
	tenants, _, err := e.s.ListTenants(e.ctx, master.Page{Limit: 1000})
	e.ok("ListTenants", err)
	got = got[:0]
	for _, t := range tenants {
		if strings.HasPrefix(t.Name, "n") {
			got = append(got, t.Name)
		}
	}
	wantN := slices.Clone(names)
	sort.Strings(wantN)
	if !slices.Equal(got, wantN) {
		e.Errorf("tenant names in order %q, want byte order %q", got, wantN)
	}
	settings := []string{"a.b", "a-b", "a_b", "a0", "ab", "a"}
	for _, n := range settings {
		e.ok("SetSetting", e.s.SetSetting(e.ctx, n, "v", e.audit()))
	}
	list, _, err := e.s.ListSettings(e.ctx, master.Page{Limit: 1000})
	e.ok("ListSettings", err)
	got = got[:0]
	for _, s := range list {
		got = append(got, s.Name)
	}
	wantS := slices.Clone(settings)
	sort.Strings(wantS)
	if !slices.Equal(got, wantS) {
		e.Errorf("settings in order %q, want byte order %q", got, wantS)
	}
}

// testExactText guards that text is kept and matched byte for byte: the
// audit tenant-name filter is exact (case, spaces, Unicode normal forms),
// and a holder's bytes come back exactly, trailing spaces and NFC or NFD
// form included.
func testExactText(e *env) {
	const nfc, nfd = "läb", "läb" // the same text to a reader, different bytes
	names := []string{"lab", "Lab", "lab ", " lab", nfc, nfd}
	for _, n := range names {
		a := e.audit()
		a.TenantName = n
		e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a))
	}
	for _, n := range names {
		got := e.allAudit(master.AuditFilter{TenantName: n})
		if len(got) != 1 || got[0].TenantName != n {
			var seen []string
			for _, a := range got {
				seen = append(seen, a.TenantName)
			}
			e.Errorf("filter %q matched %q, want exactly that name once", n, seen)
		}
	}
	holders := []string{"name  ", "  name", "Zoé", "Zoé", "tab\there"}
	for _, h := range holders {
		k := e.key()
		k.Holder = h
		e.ok("CreateKey", e.s.CreateKey(e.ctx, k, e.audit()))
		got, err := e.s.Key(e.ctx, k.ID)
		e.ok("Key", err)
		if got.Holder != h {
			e.Errorf("holder %q came back as %q", h, got.Holder)
		}
	}
}

// testTimeEdges guards the first and the last time the store keeps, read back
// exactly, and their order in a list.
func testTimeEdges(e *env) {
	first := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
	k := e.key()
	k.Created, k.Expires = first, last
	e.ok("CreateKey", e.s.CreateKey(e.ctx, k, e.audit()))
	got, err := e.s.Key(e.ctx, k.ID)
	e.ok("Key", err)
	if !sameTime(got.Created, first) || !sameTime(got.Expires, last) {
		e.Errorf("created %v and expires %v, want %v and %v", got.Created, got.Expires, first, last)
	}
	w, err := e.s.MarkKeyUsed(e.ctx, k.ID, last)
	if err != nil || !w {
		e.Errorf("MarkKeyUsed at the last time = %v, %v", w, err)
	}
	keys, _, err := e.s.ListKeys(e.ctx, master.Page{Limit: 1})
	e.ok("ListKeys", err)
	if len(keys) != 1 || keys[0].ID != k.ID {
		e.Errorf("the key created at 1970 is not first in the list")
	}
	a1, a2 := e.audit(), e.audit()
	a1.Time, a2.Time = first, last
	e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a1, a2))
	all := e.allAudit(master.AuditFilter{})
	if !sameTime(all[0].Time, last) || !sameTime(all[len(all)-1].Time, first) {
		e.Errorf("audit newest %v and oldest %v, want %v and %v", all[0].Time, all[len(all)-1].Time, last, first)
	}
	since := e.allAudit(master.AuditFilter{Since: last})
	if len(since) != 1 || since[0].ID != a2.ID {
		e.Errorf("entries since the last time = %d, want the one at it", len(since))
	}
}

// checkChurn lists with list, limit at a time, calling mutate between pages,
// which may insert and delete and returns the keys it deleted. Each item
// present before and never deleted must come back exactly once; no item may
// come back twice; and the keys must increase.
func checkChurn[T any](e *env, what string, list func(master.Page) ([]T, string, error), key func(T) string, mutate func(page int, seen []string) []string, limit int) {
	e.Helper()
	all, _, err := list(master.Page{Limit: master.MaxPageLimit})
	e.ok(what, err)
	before := map[string]bool{}
	for _, it := range all {
		before[key(it)] = true
	}
	deleted := map[string]bool{}
	seen := map[string]int{}
	var order []string
	page := master.Page{Limit: limit}
	for n := 0; n < 1000; n++ {
		items, next, err := list(page)
		e.ok(what, err)
		for _, it := range items {
			seen[key(it)]++
			order = append(order, key(it))
		}
		if next == "" {
			break
		}
		page.Cursor = next
		for _, d := range mutate(n, order) {
			deleted[d] = true
		}
	}
	for k := range before {
		if !deleted[k] && seen[k] != 1 {
			e.Errorf("%s: %q, present throughout, came back %d times", what, k, seen[k])
		}
	}
	for k, n := range seen {
		if n > 1 {
			e.Errorf("%s: %q came back %d times", what, k, n)
		}
	}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			e.Errorf("%s: %q after %q, out of order", what, order[i], order[i-1])
		}
	}
	if len(deleted) == 0 {
		e.Errorf("%s: nothing was deleted, so the test tested nothing", what)
	}
}

// pick returns, ahead, the largest of keys not yet seen and not yet used, or
// otherwise the smallest seen and not yet used, so that deletes land on both
// sides of the position reached. It returns "" when none is left.
func pick(keys []string, seen []string, used map[string]bool, ahead bool) string {
	s := map[string]bool{}
	for _, k := range seen {
		s[k] = true
	}
	sorted := slices.Clone(keys)
	sort.Strings(sorted)
	if ahead {
		slices.Reverse(sorted)
	}
	for _, k := range sorted {
		if !used[k] && s[k] != ahead {
			used[k] = true
			return k
		}
	}
	return ""
}

// testPagingWithDeletes guards every list that has a delete (tenants, grants
// by key, grants by scope, settings) against items removed ahead of and
// behind the position reached between pages. Keys and audit entries are
// never deleted, so their lists are paged across inserts only (elsewhere).
func testPagingWithDeletes(e *env) {
	// Tenants.
	var names []string
	ids := map[string]string{}
	for i := range 30 {
		t := e.mustTenant(fmt.Sprintf("m%02d", i))
		names = append(names, t.Name)
		ids[t.Name] = t.ID
	}
	used := map[string]bool{}
	checkChurn(e, "ListTenants", func(p master.Page) ([]master.Tenant, string, error) { return e.s.ListTenants(e.ctx, p) },
		func(t master.Tenant) string { return t.Name },
		func(n int, seen []string) []string {
			var del []string
			for _, ahead := range []bool{true, false} {
				if name := pick(names, seen, used, ahead); name != "" {
					e.ok("RemoveTenant", e.s.RemoveTenant(e.ctx, ids[name], e.audit()))
					del = append(del, name)
				}
			}
			e.mustTenant(fmt.Sprintf("n%02d", n))
			return del
		}, 4)

	// Grants by key and by scope.
	k := e.mustKey()
	tenant := e.mustTenant("shared")
	var scopes, keyIDs []string
	for i := range 20 {
		t := e.mustTenant(fmt.Sprintf("g%02d", i))
		e.mustGrant(k.ID, master.TenantScope(t.ID), master.RoleReader)
		scopes = append(scopes, t.ID)
		other := e.mustKey()
		e.mustGrant(other.ID, master.TenantScope(tenant.ID), master.RoleReader)
		keyIDs = append(keyIDs, other.ID)
	}
	checkChurnGrants := func(what string, list func(master.Page) ([]master.Grant, string, error), key func(master.Grant) string,
		pool []string, remove func(id string)) {
		used := map[string]bool{}
		checkChurn(e, what, list, key, func(_ int, seen []string) []string {
			var del []string
			for _, ahead := range []bool{true, false} {
				if id := pick(pool, seen, used, ahead); id != "" {
					remove(id)
					del = append(del, id)
				}
			}
			return del
		}, 3)
	}
	checkChurnGrants("GrantsByKey", func(p master.Page) ([]master.Grant, string, error) { return e.s.GrantsByKey(e.ctx, k.ID, p) },
		func(g master.Grant) string { return g.Scope.TenantID }, scopes,
		func(id string) {
			_, err := e.s.RemoveGrant(e.ctx, k.ID, master.TenantScope(id), e.audit())
			e.ok("RemoveGrant", err)
		})
	checkChurnGrants("GrantsByScope", func(p master.Page) ([]master.Grant, string, error) {
		return e.s.GrantsByScope(e.ctx, master.TenantScope(tenant.ID), p)
	}, func(g master.Grant) string { return g.KeyID }, keyIDs,
		func(id string) {
			_, err := e.s.RemoveGrant(e.ctx, id, master.TenantScope(tenant.ID), e.audit())
			e.ok("RemoveGrant", err)
		})

	// Settings.
	var settings []string
	for i := range 30 {
		n := fmt.Sprintf("m.%02d", i)
		e.ok("SetSetting", e.s.SetSetting(e.ctx, n, "v", e.audit()))
		settings = append(settings, n)
	}
	used = map[string]bool{}
	checkChurn(e, "ListSettings", func(p master.Page) ([]master.Setting, string, error) { return e.s.ListSettings(e.ctx, p) },
		func(s master.Setting) string { return s.Name },
		func(n int, seen []string) []string {
			var del []string
			for _, ahead := range []bool{true, false} {
				if name := pick(settings, seen, used, ahead); name != "" {
					_, err := e.s.UnsetSetting(e.ctx, name, e.audit())
					e.ok("UnsetSetting", err)
					del = append(del, name)
				}
			}
			e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("n.%02d", n), "v", e.audit()))
			return del
		}, 4)
}

// testTies guards the tie-breaker of the two lists sorted by time: keys of
// one creation time come back by id, ascending, and audit entries of one
// time by id, descending, also when a page ends inside the tie.
func testTies(e *env) {
	ids := []string{"k5", "k1", "k4", "k2", "k3"}
	for _, id := range ids {
		k := e.key()
		k.ID = id
		e.ok("CreateKey", e.s.CreateKey(e.ctx, k, e.audit()))
	}
	var got []string
	page := master.Page{Limit: 2}
	for {
		keys, next, err := e.s.ListKeys(e.ctx, page)
		e.ok("ListKeys", err)
		for _, k := range keys {
			if strings.HasPrefix(k.ID, "k") && len(k.ID) == 2 {
				got = append(got, k.ID)
			}
		}
		if next == "" {
			break
		}
		page.Cursor = next
	}
	if want := []string{"k1", "k2", "k3", "k4", "k5"}; !slices.Equal(got, want) {
		e.Errorf("keys of one time in order %q, want %q", got, want)
	}
	tie := T0.Add(time.Hour)
	for _, id := range []string{"t3", "t1", "t5", "t2", "t4"} {
		a := e.audit()
		a.ID, a.Time = id, tie
		e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a))
	}
	got = got[:0]
	page = master.Page{Limit: 2}
	for {
		entries, next, err := e.s.ListAudit(e.ctx, master.AuditFilter{Since: tie}, page)
		e.ok("ListAudit", err)
		for _, a := range entries {
			got = append(got, a.ID)
		}
		if next == "" {
			break
		}
		page.Cursor = next
	}
	if want := []string{"t5", "t4", "t3", "t2", "t1"}; !slices.Equal(got, want) {
		e.Errorf("audit entries of one time in order %q, want %q", got, want)
	}
}

// roleDetail is a role as an audit entry's details write it.
func roleDetail(r master.Role) string {
	if r == "" {
		return master.NoRole
	}
	return string(r)
}

// race runs a and b at the same moment and waits for both.
func race(a, b func()) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, f := range []func(){a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f()
		}()
	}
	close(start)
	wg.Wait()
}

// detailsOf returns the details of the audit entry with id.
func (e *env) detailsOf(id string) map[string]string {
	e.Helper()
	for _, a := range e.allAudit(master.AuditFilter{}) {
		if a.ID == id {
			return a.Details
		}
	}
	e.Errorf("no audit entry %s", id)
	return nil
}

// testConcurrentSameRow guards that two concurrent calls on one row are
// ordered and that each one's audit entry records what that call saw:
// RemoveGrant twice (one removes the role, the other finds none), RevokeKey
// twice (one revokes, the other finds it revoked and returns the first
// time), SetTenantState twice (one replaces the first state, the other the
// first one's).
func testConcurrentSameRow(e *env) {
	t := e.mustTenant("lab")
	for round := range 15 {
		k := e.mustKey()
		e.mustGrant(k.ID, master.TenantScope(t.ID), master.RoleWriter)
		a, b := e.audit(), e.audit()
		var ra, rb master.Role
		var ea, eb error
		race(func() { ra, ea = e.s.RemoveGrant(e.ctx, k.ID, master.TenantScope(t.ID), a) },
			func() { rb, eb = e.s.RemoveGrant(e.ctx, k.ID, master.TenantScope(t.ID), b) })
		if ea != nil || eb != nil {
			e.Fatalf("round %d: RemoveGrant: %v, %v", round, ea, eb)
		}
		if (ra == master.RoleWriter) == (rb == master.RoleWriter) || ra+rb != master.RoleWriter {
			e.Errorf("round %d: RemoveGrant returned %q and %q, want writer once and none once", round, ra, rb)
		}
		da, db := e.detailsOf(a.ID), e.detailsOf(b.ID)
		if da[master.DetailOldRole] != roleDetail(ra) || db[master.DetailOldRole] != roleDetail(rb) {
			e.Errorf("round %d: removals' audit old roles %q and %q, returned %q and %q",
				round, da[master.DetailOldRole], db[master.DetailOldRole], ra, rb)
		}

		at1, at2 := T0.Add(time.Hour), T0.Add(2*time.Hour)
		a, b = e.audit(), e.audit()
		var ta, tb time.Time
		race(func() { ta, ea = e.s.RevokeKey(e.ctx, k.ID, at1, a) },
			func() { tb, eb = e.s.RevokeKey(e.ctx, k.ID, at2, b) })
		if ea != nil || eb != nil {
			e.Fatalf("round %d: RevokeKey: %v, %v", round, ea, eb)
		}
		if !ta.Equal(tb) || (!ta.Equal(at1) && !ta.Equal(at2)) {
			e.Errorf("round %d: RevokeKey returned %v and %v, want both the first one's time", round, ta, tb)
		}
		da, db = e.detailsOf(a.ID), e.detailsOf(b.ID)
		firstIsA := ta.Equal(at1)
		if (da[master.DetailAlreadyRevoked] == "false") != firstIsA || (db[master.DetailAlreadyRevoked] == "false") == firstIsA {
			e.Errorf("round %d: revocations' audit already_revoked %q and %q, first was a: %v",
				round, da[master.DetailAlreadyRevoked], db[master.DetailAlreadyRevoked], firstIsA)
		}

		e.ok("SetTenantState", e.s.SetTenantState(e.ctx, t.ID, master.StateReady, "", e.audit()))
		a, b = e.audit(), e.audit()
		race(func() { ea = e.s.SetTenantState(e.ctx, t.ID, master.StateFailed, "a", a) },
			func() { eb = e.s.SetTenantState(e.ctx, t.ID, master.StateUpgrading, "b", b) })
		if ea != nil || eb != nil {
			e.Fatalf("round %d: SetTenantState: %v, %v", round, ea, eb)
		}
		da, db = e.detailsOf(a.ID), e.detailsOf(b.ID)
		aFirst := da[master.DetailOldState] == "ready" && db[master.DetailOldState] == "failed"
		bFirst := db[master.DetailOldState] == "ready" && da[master.DetailOldState] == "upgrading"
		if aFirst == bFirst {
			e.Errorf("round %d: state changes' audit old states %q and %q, want one ready and the other the first one's new state",
				round, da[master.DetailOldState], db[master.DetailOldState])
		}
		got, err := e.s.Tenant(e.ctx, t.ID)
		e.ok("Tenant", err)
		if (aFirst && got.State != master.StateUpgrading) || (bFirst && got.State != master.StateFailed) {
			e.Errorf("round %d: the state is %s, not the last change's", round, got.State)
		}
	}
}

// testSizeBounds guards every size bound through the store: a value at
// exactly its bound is stored and read back, and one byte over is Invalid.
func testSizeBounds(e *env) {
	t := master.Tenant{
		ID:            strings.Repeat("T", master.MaxIDLength),
		Name:          "n" + strings.Repeat("x", master.MaxTenantNameLength-1),
		Driver:        strings.Repeat("d", master.MaxDriverLength),
		SealedAddress: []byte(strings.Repeat("s", master.MaxSealedAddressLength)),
		State:         master.StateFailed,
		StateReason:   strings.Repeat("r", master.MaxStateReasonLength),
		Created:       T0,
	}
	e.ok("CreateTenant at every bound", e.s.CreateTenant(e.ctx, t, e.audit()))
	got, err := e.s.Tenant(e.ctx, t.ID)
	e.ok("Tenant", err)
	if d := diffTenant(got, t); d != "" {
		e.Errorf("tenant at every bound: %s", d)
	}
	over := map[string]func(*master.Tenant){
		"id":             func(x *master.Tenant) { x.ID += "T" },
		"name":           func(x *master.Tenant) { x.Name += "x" },
		"driver":         func(x *master.Tenant) { x.Driver += "d" },
		"sealed address": func(x *master.Tenant) { x.SealedAddress = append(x.SealedAddress, 's') },
		"state reason":   func(x *master.Tenant) { x.StateReason += "r" },
	}
	for name, change := range over {
		x := t
		x.ID, x.Name = "other", "other"
		x.SealedAddress = slices.Clone(t.SealedAddress)
		if name == "id" {
			x.ID = t.ID
		}
		if name == "name" {
			x.Name = t.Name
		}
		change(&x)
		e.wantKind("CreateTenant, "+name+" one byte over", e.s.CreateTenant(e.ctx, x, e.audit()), fault.Invalid)
	}
	e.wantKind("SetTenantState, reason one byte over",
		e.s.SetTenantState(e.ctx, t.ID, master.StateFailed, strings.Repeat("r", master.MaxStateReasonLength+1), e.audit()), fault.Invalid)

	k := e.key()
	k.ID = strings.Repeat("K", master.MaxIDLength)
	e.ok("CreateKey, id at its bound", e.s.CreateKey(e.ctx, k, e.audit()))
	k2 := e.key()
	k2.ID = k.ID + "K"
	e.wantKind("CreateKey, id one byte over", e.s.CreateKey(e.ctx, k2, e.audit()), fault.Invalid)

	name := strings.Repeat("s", master.MaxSettingNameLength)
	e.ok("SetSetting, name at its bound", e.s.SetSetting(e.ctx, name, "v", e.audit()))
	e.wantKind("SetSetting, name one byte over", e.s.SetSetting(e.ctx, name+"s", "v", e.audit()), fault.Invalid)

	a := e.audit()
	a.ID = strings.Repeat("A", master.MaxIDLength)
	a.Method = strings.Repeat("m", master.MaxAuditFieldLength)
	a.Object = strings.Repeat("o", master.MaxAuditFieldLength)
	a.RemoteAddress = strings.Repeat("r", master.MaxAuditFieldLength)
	a.RequestID = strings.Repeat("q", master.MaxAuditFieldLength)
	a.TenantName = strings.Repeat("t", master.MaxAuditFieldLength)
	a.LocalUser = strings.Repeat("u", master.MaxAuditFieldLength)
	a.Reason = strings.Repeat("x", master.MaxAuditReasonLength)
	e.ok("AppendAudit at every bound", e.s.AppendAudit(e.ctx, a))
	all := e.allAudit(master.AuditFilter{TenantName: a.TenantName})
	if len(all) != 1 || diffAudit(all[0], a) != "" {
		e.Errorf("audit entry at every bound did not read back exactly")
	}
	fields := map[string]func(*master.AuditEntry){
		"id":             func(x *master.AuditEntry) { x.ID += "A" },
		"method":         func(x *master.AuditEntry) { x.Method += "m" },
		"object":         func(x *master.AuditEntry) { x.Object += "o" },
		"remote address": func(x *master.AuditEntry) { x.RemoteAddress += "r" },
		"request id":     func(x *master.AuditEntry) { x.RequestID += "q" },
		"tenant name":    func(x *master.AuditEntry) { x.TenantName += "t" },
		"local user":     func(x *master.AuditEntry) { x.LocalUser += "u" },
		"reason":         func(x *master.AuditEntry) { x.Reason += "x" },
	}
	for name, change := range fields {
		x := a
		if name != "id" {
			x.ID = e.id("a")
		}
		change(&x)
		e.wantKind("AppendAudit, "+name+" one byte over", e.s.AppendAudit(e.ctx, x), fault.Invalid)
	}
}

// testSetGrantRevokeRace guards that a grant is never written after its key
// is revoked: SetGrant racing RevokeKey on one key either succeeds, and the
// grant exists, or is Conflict, and there is no grant; and a SetGrant after
// the revocation is always Conflict.
func testSetGrantRevokeRace(e *env) {
	setGrantRevokeRace(e, e.s, e.s, 30)
}

// setGrantRevokeRace is testSetGrantRevokeRace on two stores, which may be
// two instances on one database.
func setGrantRevokeRace(e *env, s1, s2 master.Store, rounds int) {
	granted, refused := 0, 0
	for round := range rounds {
		k := e.key()
		e.ok("CreateKey", s1.CreateKey(e.ctx, k, e.audit()))
		ga, ra := e.audit(), e.audit()
		var gerr, rerr error
		// Each side in turn starts a moment late, so both orders happen
		// and both outcomes are checked, while the calls still overlap.
		lag := func(mine int) {
			if round%3 == mine {
				time.Sleep(200 * time.Microsecond)
			}
		}
		race(func() {
			lag(0)
			_, gerr = s1.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: master.Installation, Role: master.RoleAdmin}, ga)
		}, func() {
			lag(1)
			_, rerr = s2.RevokeKey(e.ctx, k.ID, T0.Add(time.Hour), ra)
		})
		e.ok("RevokeKey", rerr)
		grants, _, err := s1.GrantsByKey(e.ctx, k.ID, master.Page{Limit: 10})
		e.ok("GrantsByKey", err)
		switch {
		case gerr == nil:
			granted++
			if len(grants) != 1 {
				e.Errorf("round %d: SetGrant succeeded but the key has %d grants", round, len(grants))
			}
		case fault.KindOf(gerr) == fault.Conflict:
			refused++
			if len(grants) != 0 {
				e.Errorf("round %d: SetGrant was refused but the key has %d grants: a grant written after revocation", round, len(grants))
			}
			if e.auditIDs()[ga.ID] {
				e.Errorf("round %d: the refused SetGrant wrote its audit entry", round)
			}
		default:
			e.Errorf("round %d: SetGrant racing RevokeKey: %v, want success or Conflict", round, gerr)
		}
		_, err = s2.SetGrant(e.ctx, master.Grant{KeyID: k.ID, Scope: master.Installation, Role: master.RoleReader}, e.audit())
		e.wantKind(fmt.Sprintf("round %d: SetGrant after RevokeKey", round), err, fault.Conflict)
	}
	e.Logf("SetGrant racing RevokeKey: %d granted first, %d refused", granted, refused)
}

// OpenPair returns two stores on one fresh, empty, migrated database, as two
// processes would each open it, and closes both when tb ends.
type OpenPair func(tb testing.TB) (first, second master.Store)

// RunShared runs the tests that need two instances of the store on one
// database: what one process's guards cannot show, such as a tenant name
// kept unique between two servers. A driver's tests call it as well as Run.
func RunShared(t *testing.T, open OpenPair) {
	tests := []struct {
		name string
		fn   func(e *env, other master.Store)
	}{
		{"TenantNameRaceAcrossInstances", testTenantNameRaceShared},
		{"SetGrantRevokeRaceAcrossInstances", func(e *env, other master.Store) { setGrantRevokeRace(e, e.s, other, 20) }},
		{"WritesSeenAcrossInstances", testWritesSeenShared},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, second := open(t)
			tt.fn(&env{T: t, s: first, ctx: context.Background()}, second)
		})
	}
}

// testTenantNameRaceShared guards the unique tenant name between two
// instances: creations of one name split between them end with exactly one
// tenant and one audit entry, every other creation Conflict.
func testTenantNameRaceShared(e *env, other master.Store) {
	const rounds, racers = 20, 8
	for round := range rounds {
		name := fmt.Sprintf("race%02d", round)
		tenants := make([]master.Tenant, racers)
		entries := make([]master.AuditEntry, racers)
		for i := range racers {
			tenants[i], entries[i] = e.tenant(name), e.audit()
		}
		errs := make([]error, racers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range racers {
			s := e.s
			if i%2 == 1 {
				s = other
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = s.CreateTenant(e.ctx, tenants[i], entries[i])
			}()
		}
		close(start)
		wg.Wait()
		winners := 0
		for i, err := range errs {
			switch {
			case err == nil:
				winners++
			case fault.KindOf(err) != fault.Conflict:
				e.Errorf("round %d: creation %d: %v, want Conflict", round, i, err)
			}
		}
		if winners != 1 {
			e.Fatalf("round %d: %d creations of one name succeeded across two instances, want 1", round, winners)
		}
		ids := e.auditIDs()
		n := 0
		for _, a := range entries {
			if ids[a.ID] {
				n++
			}
		}
		if n != 1 {
			e.Errorf("round %d: %d audit entries for one creation", round, n)
		}
	}
}

// testWritesSeenShared guards that what one instance commits the other reads
// at once, and that a duplicate audit id is caught across instances.
func testWritesSeenShared(e *env, other master.Store) {
	k := e.mustKey()
	if _, err := other.Key(e.ctx, k.ID); err != nil {
		e.Errorf("the other instance does not see a committed key: %v", err)
	}
	a := e.audit()
	e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a))
	e.wantDuplicateAudit("AppendAudit of the same id on the other instance", other.AppendAudit(e.ctx, a))
	_, err := other.RevokeKey(e.ctx, k.ID, T0.Add(time.Hour), e.audit())
	e.ok("RevokeKey", err)
	got, err := e.s.Key(e.ctx, k.ID)
	if err != nil || got.Revoked.IsZero() {
		e.Errorf("a revocation on the other instance is not seen: %v, %v", got.Revoked, err)
	}
}
