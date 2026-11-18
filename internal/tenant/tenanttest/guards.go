package tenanttest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/master/mastertest"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// This file holds the tests that pin what a second driver could otherwise
// get subtly wrong and still pass, the tenant port's share of the master
// suite's guards (mastertest/guards.go): cursors bound to their list and
// filter, error kinds, both or neither for failures, byte-exact text, the
// edges of time and of every size bound, paging across deletes, ties, and
// two instances on one database.

// wantDuplicateAudit fails unless err is master.ErrDuplicateAuditID of kind
// Internal: a duplicate audit id is a caller's bug, not a Conflict.
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

// testFailuresWriteNoAudit guards both or neither in the other direction:
// for every failure of every changing operation, the audit entry it was
// handed is absent afterwards.
func testFailuresWriteNoAudit(e *env) {
	big := strings.Repeat("v", master.MaxSettingValueLength+1)
	calls := []struct {
		name string
		run  func(a master.AuditEntry) error
	}{
		{"SetSetting, bad name", func(a master.AuditEntry) error { return e.s.SetSetting(e.ctx, "Bad Name", "v", a) }},
		{"SetSetting, value too big", func(a master.AuditEntry) error { return e.s.SetSetting(e.ctx, "fine", big, a) }},
		{"SetSetting, value not UTF-8", func(a master.AuditEntry) error { return e.s.SetSetting(e.ctx, "fine", "\xff", a) }},
		{"UnsetSetting, bad name", func(a master.AuditEntry) error { _, err := e.s.UnsetSetting(e.ctx, "Bad Name", a); return err }},
		{"AppendAudit, an invalid entry in the batch", func(a master.AuditEntry) error {
			bad := e.audit()
			bad.Outcome = "maybe"
			return e.s.AppendAudit(e.ctx, a, bad)
		}},
	}
	for _, c := range calls {
		a := e.audit()
		e.wantKind(c.name, c.run(a), fault.Invalid)
		if e.auditIDs()[a.ID] {
			e.Errorf("%s failed but wrote its audit entry", c.name)
		}
	}
	if _, err := e.s.Setting(e.ctx, "fine"); fault.KindOf(err) != fault.NotFound {
		e.Errorf("a refused SetSetting left a value (%v)", err)
	}
}

// testCursorsBound guards that a cursor is accepted only by the list, and
// the filter, it was made for: the other list, an audit filter with any
// field changed, an altered cursor and a cut one are all Invalid (the API
// contract's page token rule).
func testCursorsBound(e *env) {
	for i := range 3 {
		e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("s%d", i), "v", e.audit()))
	}
	filter := master.AuditFilter{TenantName: "lab", KeyID: "KA", Outcome: master.OutcomeDone, Since: T0, Until: T0.Add(time.Hour)}
	for range 3 {
		a := e.audit()
		a.KeyID = "KA"
		e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a))
	}
	page := func(c string) master.Page { return master.Page{Limit: 1, Cursor: c} }
	lists := map[string]func(cursor string) (string, error){
		"ListSettings": func(c string) (string, error) { _, n, err := e.s.ListSettings(e.ctx, page(c)); return n, err },
		"ListAudit":    func(c string) (string, error) { _, n, err := e.s.ListAudit(e.ctx, filter, page(c)); return n, err },
	}
	cursors := map[string]string{}
	for name, call := range lists {
		c, err := call("")
		e.ok(name, err)
		if c == "" {
			e.Fatalf("%s returned no cursor with more to come", name)
		}
		cursors[name] = c
		if _, err := call(c); err != nil {
			e.Errorf("%s refuses its own cursor: %v", name, err)
		}
		_, err = call(altered(c))
		e.wantKind(name+" with an altered cursor", err, fault.Invalid)
		_, err = call(c[:len(c)-1])
		e.wantKind(name+" with a cut cursor", err, fault.Invalid)
	}
	_, err := lists["ListAudit"](cursors["ListSettings"])
	e.wantKind("ListAudit with the settings list's cursor", err, fault.Invalid)
	_, err = lists["ListSettings"](cursors["ListAudit"])
	e.wantKind("ListSettings with the audit list's cursor", err, fault.Invalid)

	changed := map[string]func(*master.AuditFilter){
		"tenant":     func(f *master.AuditFilter) { f.TenantName = "clinic" },
		"key":        func(f *master.AuditFilter) { f.KeyID = "KB" },
		"outcome":    func(f *master.AuditFilter) { f.Outcome = master.OutcomeFailed },
		"start time": func(f *master.AuditFilter) { f.Since = T0.Add(-time.Second) },
		"end time":   func(f *master.AuditFilter) { f.Until = T0.Add(2 * time.Hour) },
		"no filter":  func(f *master.AuditFilter) { *f = master.AuditFilter{} },
	}
	for name, change := range changed {
		f := filter
		change(&f)
		_, _, err := e.s.ListAudit(e.ctx, f, page(cursors["ListAudit"]))
		e.wantKind("ListAudit cursor with another "+name, err, fault.Invalid)
	}
}

// testErrorKinds guards the kinds of the failures a caller acts on, and that
// a call that returned an error committed nothing: a context already
// canceled, a deadline already passed, and contexts canceled while calls
// are in flight.
func testErrorKinds(e *env) {
	canceled, cancel := context.WithCancel(e.ctx)
	cancel()
	a := e.audit()
	err := e.s.SetSetting(canceled, "early", "v", a)
	e.wantKind("SetSetting with a canceled context", err, fault.Unavailable)
	if !errors.Is(err, context.Canceled) {
		e.Errorf("SetSetting with a canceled context: %v does not keep context.Canceled", err)
	}
	_, err = e.s.Setting(canceled, "anything")
	e.wantKind("Setting with a canceled context", err, fault.Unavailable)
	_, _, err = e.s.ListAudit(canceled, master.AuditFilter{}, master.Page{Limit: 5})
	e.wantKind("ListAudit with a canceled context", err, fault.Unavailable)
	_, _, err = e.s.ListSettings(canceled, master.Page{Limit: 5})
	e.wantKind("ListSettings with a canceled context", err, fault.Unavailable)

	past, cancelPast := context.WithDeadline(e.ctx, time.Now().Add(-time.Second))
	defer cancelPast()
	a2 := e.audit()
	err = e.s.AppendAudit(past, a2)
	e.wantKind("AppendAudit past its deadline", err, fault.Unavailable)
	if !errors.Is(err, context.DeadlineExceeded) {
		e.Errorf("AppendAudit past its deadline: %v does not keep context.DeadlineExceeded", err)
	}
	if _, err := e.s.Setting(e.ctx, "early"); fault.KindOf(err) != fault.NotFound {
		e.Errorf("the setting of the canceled SetSetting exists (%v)", err)
	}
	ids := e.auditIDs()
	if ids[a.ID] || ids[a2.ID] {
		e.Error("an audit entry of a call that failed was written")
	}

	// Contexts canceled at varied moments: whatever each call returned, it
	// committed exactly when it returned nil.
	type result struct {
		name  string
		entry master.AuditEntry
		err   error
	}
	results := make([]result, 60)
	var wg sync.WaitGroup
	for i := range results {
		results[i].name, results[i].entry = fmt.Sprintf("raced.%02d", i), e.audit()
		wg.Go(func() {
			ctx, cancel := context.WithCancel(e.ctx)
			go func() { time.Sleep(time.Duration(i%6) * 300 * time.Microsecond); cancel() }()
			results[i].err = e.s.SetSetting(ctx, results[i].name, "v", results[i].entry)
			cancel()
		})
	}
	wg.Wait()
	ids = e.auditIDs()
	for _, r := range results {
		_, serr := e.s.Setting(e.ctx, r.name)
		committed := serr == nil
		if r.err != nil && fault.KindOf(r.err) != fault.Unavailable {
			e.Errorf("SetSetting under a canceled context: kind %v (%v), want Unavailable", fault.KindOf(r.err), r.err)
		}
		if committed != (r.err == nil) || ids[r.entry.ID] != (r.err == nil) {
			e.Errorf("SetSetting returned %v but the setting present = %v, its audit entry present = %v", r.err, committed, ids[r.entry.ID])
		}
	}
}

// testCloseInFlight guards that calls in flight while the store is closed
// fail as Unavailable, never with another kind or a panic: the registry
// closes a tenant's store that requests may still be using.
func testCloseInFlight(e *env) {
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	stop := make(chan struct{})
	for i := range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				var err error
				if i%2 == 0 {
					_, _, err = e.s.ListSettings(e.ctx, master.Page{Limit: 10})
				} else {
					err = e.s.SetSetting(e.ctx, "busy", "v", e.audit())
				}
				if err != nil {
					errs <- err
					return
				}
			}
		})
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

// testExactText guards that text is kept and matched byte for byte: the
// audit tenant-name filter is exact (case, spaces, Unicode normal forms),
// and a setting's value comes back exactly.
func testExactText(e *env) {
	const nfc, nfd = "l\u00e4b", "la\u0308b" // the same text to a reader, different bytes
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
	for i, v := range []string{"value  ", "  value", "Zo\u00e9", "Zoe\u0301", "tab\there", "line\nbreak"} {
		name := fmt.Sprintf("exact.%d", i)
		e.ok("SetSetting", e.s.SetSetting(e.ctx, name, v, e.audit()))
		got, err := e.s.Setting(e.ctx, name)
		if err != nil || got != v {
			e.Errorf("value %q came back as %q (%v)", v, got, err)
		}
	}
}

// testTimeEdges guards the first and the last time the store keeps, read back
// exactly, and their order in the audit list.
func testTimeEdges(e *env) {
	first := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
	a1, a2 := e.audit(), e.audit()
	a1.Time, a2.Time = first, last
	e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a1, a2))
	all := e.allAudit(master.AuditFilter{})
	if len(all) != 2 || !mastertest.SameTime(all[0].Time, last) || !mastertest.SameTime(all[1].Time, first) {
		e.Fatalf("audit entries at the edges of time read back as %+v", all)
	}
	since := e.allAudit(master.AuditFilter{Since: last})
	if len(since) != 1 || since[0].ID != a2.ID {
		e.Errorf("entries since the last time = %d, want the one at it", len(since))
	}
}

// testPagingWithDeletes guards the settings list against settings removed
// ahead of and behind the position reached between pages, with others
// added: each setting present throughout comes back exactly once, none
// twice, in order. Audit entries are never deleted, so their list is paged
// across inserts only (testAuditPaging10000).
func testPagingWithDeletes(e *env) {
	var names []string
	for i := range 30 {
		n := fmt.Sprintf("m.%02d", i)
		e.ok("SetSetting", e.s.SetSetting(e.ctx, n, "v", e.audit()))
		names = append(names, n)
	}
	deleted := map[string]bool{}
	seen := map[string]int{}
	var order []string
	remove := func(n string) {
		_, err := e.s.UnsetSetting(e.ctx, n, e.audit())
		e.ok("UnsetSetting", err)
		deleted[n] = true
	}
	page := master.Page{Limit: 4}
	for round := 0; round < 100; round++ {
		items, next, err := e.s.ListSettings(e.ctx, page)
		e.ok("ListSettings", err)
		for _, it := range items {
			seen[it.Name]++
			order = append(order, it.Name)
		}
		if next == "" {
			break
		}
		page.Cursor = next
		// One behind the position reached and one ahead of it.
		for _, n := range order {
			if !deleted[n] {
				remove(n)
				break
			}
		}
		for i := len(names) - 1; i >= 0; i-- {
			if n := names[i]; !deleted[n] && seen[n] == 0 {
				remove(n)
				break
			}
		}
		e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("n.%02d", round), "v", e.audit()))
	}
	for _, n := range names {
		if !deleted[n] && seen[n] != 1 {
			e.Errorf("%q, present throughout, came back %d times", n, seen[n])
		}
	}
	for n, c := range seen {
		if c > 1 {
			e.Errorf("%q came back %d times", n, c)
		}
	}
	if !slices.IsSorted(order) {
		e.Errorf("settings out of order: %q", order)
	}
	if len(deleted) < 4 {
		e.Errorf("only %d settings were deleted, so the test tested little", len(deleted))
	}
}

// testTies guards the tie-breaker of the audit list: entries of one time
// come back by id, descending, also when a page ends inside the tie.
func testTies(e *env) {
	tie := T0.Add(time.Hour)
	for _, id := range []string{"t3", "t1", "t5", "t2", "t4"} {
		a := e.audit()
		a.ID, a.Time = id, tie
		e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a))
	}
	var got []string
	page := master.Page{Limit: 2}
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

// testSizeBounds guards every size bound through the store: a value at
// exactly its bound is stored and read back, and one byte over is Invalid.
func testSizeBounds(e *env) {
	name := strings.Repeat("s", master.MaxSettingNameLength)
	value := strings.Repeat("v", master.MaxSettingValueLength)
	e.ok("SetSetting at both bounds", e.s.SetSetting(e.ctx, name, value, e.audit()))
	if got, err := e.s.Setting(e.ctx, name); err != nil || got != value {
		e.Errorf("a setting at both bounds read back with %d bytes (%v)", len(got), err)
	}
	e.wantKind("SetSetting, name one byte over", e.s.SetSetting(e.ctx, name+"s", "v", e.audit()), fault.Invalid)
	e.wantKind("SetSetting, value one byte over", e.s.SetSetting(e.ctx, "x", value+"v", e.audit()), fault.Invalid)

	a := e.audit()
	a.ID = strings.Repeat("A", master.MaxIDLength)
	a.KeyID = ""
	a.Method = strings.Repeat("m", master.MaxAuditFieldLength)
	a.Object = strings.Repeat("o", master.MaxAuditFieldLength)
	a.RemoteAddress = strings.Repeat("r", master.MaxAuditFieldLength)
	a.RequestID = strings.Repeat("q", master.MaxAuditFieldLength)
	a.TenantName = strings.Repeat("t", master.MaxAuditFieldLength)
	a.LocalUser = strings.Repeat("u", master.MaxAuditFieldLength)
	a.Reason = strings.Repeat("x", master.MaxAuditReasonLength)
	e.ok("AppendAudit at every bound", e.s.AppendAudit(e.ctx, a))
	all := e.allAudit(master.AuditFilter{TenantName: a.TenantName})
	if len(all) != 1 || mastertest.DiffAudit(all[0], a) != "" {
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

// OpenPair returns two stores for the tenant whose id is tenantID on one
// fresh, empty, migrated database, as two processes would each open it, and
// closes both when tb ends.
type OpenPair func(tb testing.TB, tenantID string) (first, second tenant.Store)

// RunShared runs the tests that need two instances of the store on one
// database. A driver's tests call it as well as Run.
func RunShared(t *testing.T, open OpenPair) {
	tests := []struct {
		name string
		fn   func(e *env, other tenant.Store)
	}{
		{"WritesSeenAcrossInstances", testWritesSeenShared},
		{"ConcurrentChangesAcrossInstances", testConcurrentShared},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, second := open(t, SuiteTenant)
			tt.fn(&env{T: t, s: first, ctx: context.Background()}, second)
		})
	}
}

// testWritesSeenShared guards that what one instance commits the other reads
// at once, and that a duplicate audit id is caught across instances.
func testWritesSeenShared(e *env, other tenant.Store) {
	e.ok("SetSetting", e.s.SetSetting(e.ctx, "seen", "yes", e.audit()))
	if v, err := other.Setting(e.ctx, "seen"); err != nil || v != "yes" {
		e.Errorf("the other instance reads %q, %v; want the committed value", v, err)
	}
	a := e.audit()
	e.ok("AppendAudit", e.s.AppendAudit(e.ctx, a))
	e.wantDuplicateAudit("AppendAudit of the same id on the other instance", other.AppendAudit(e.ctx, a))
	removed, err := other.UnsetSetting(e.ctx, "seen", e.audit())
	if err != nil || !removed {
		e.Errorf("UnsetSetting on the other instance = %v, %v", removed, err)
	}
	if _, err := e.s.Setting(e.ctx, "seen"); fault.KindOf(err) != fault.NotFound {
		e.Errorf("a removal on the other instance is not seen (%v)", err)
	}
}

// testConcurrentShared guards that changes made at once through two
// instances, as two processes would, are each written with their entry,
// none lost to a lock error.
func testConcurrentShared(e *env, other tenant.Store) {
	const each = 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*each)
	for i, s := range []tenant.Store{e.s, other} {
		wg.Go(func() {
			for j := range each {
				errs <- s.SetSetting(e.ctx, fmt.Sprintf("i%d.%02d", i, j), "v", e.audit())
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			e.Errorf("a change across two instances: %v", err)
		}
	}
	all, _, err := other.ListSettings(e.ctx, master.Page{Limit: master.MaxPageLimit})
	e.ok("ListSettings", err)
	if len(all) != 2*each || len(e.auditIDs()) != 2*each {
		e.Errorf("%d settings and %d entries after %d changes across two instances", len(all), len(e.auditIDs()), 2*each)
	}
}

// testTenantsApart guards that two tenants' databases stay apart: each reads
// only its own settings and audit entries, and a page token from one
// tenant's list is refused by the other's, for the settings list and for the
// audit list with the same filter (the API contract's page token rule, with
// the tenant as one of the list's arguments).
func testTenantsApart(e *env) {
	other := e.open(e.T, OtherTenant)
	for i := range 3 {
		e.ok("SetSetting", e.s.SetSetting(e.ctx, fmt.Sprintf("mine.%d", i), "v", e.audit()))
		e.ok("SetSetting", other.SetSetting(e.ctx, fmt.Sprintf("theirs.%d", i), "v", e.audit()))
	}
	mine, _, err := e.s.ListSettings(e.ctx, master.Page{Limit: master.MaxPageLimit})
	e.ok("ListSettings", err)
	for _, s := range mine {
		if !strings.HasPrefix(s.Name, "mine.") {
			e.Errorf("a tenant reads another tenant's setting %q", s.Name)
		}
	}
	_, settingsCursor, err := e.s.ListSettings(e.ctx, master.Page{Limit: 1})
	e.ok("ListSettings", err)
	_, _, err = other.ListSettings(e.ctx, master.Page{Limit: 1, Cursor: settingsCursor})
	e.wantKind("another tenant's ListSettings with this tenant's cursor", err, fault.Invalid)

	_, auditCursor, err := e.s.ListAudit(e.ctx, master.AuditFilter{}, master.Page{Limit: 1})
	e.ok("ListAudit", err)
	if auditCursor == "" {
		e.Fatal("ListAudit returned no cursor with more to come")
	}
	_, _, err = other.ListAudit(e.ctx, master.AuditFilter{}, master.Page{Limit: 1, Cursor: auditCursor})
	e.wantKind("another tenant's ListAudit with this tenant's cursor", err, fault.Invalid)
	if _, _, err := e.s.ListAudit(e.ctx, master.AuditFilter{}, master.Page{Limit: 1, Cursor: auditCursor}); err != nil {
		e.Errorf("the tenant refuses its own cursor: %v", err)
	}
}
