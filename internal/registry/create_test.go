package registry

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/driver"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// TestCreateWholeOrNothing guards D56: a creation whose database cannot be
// opened or upgraded leaves nothing behind (no row in the master, no entry in
// the registry, no open store) and answers the kind the API contract gives
// each failure; a system audit entry records the undoing, with no address.
func TestCreateWholeOrNothing(t *testing.T) {
	cases := []struct {
		name string
		mode mode
		kind fault.Kind
	}{
		{"does-not-answer", down, fault.Unavailable},
		{"migration-fails", broken, fault.Unavailable},
		{"address-refused", refused, fault.Invalid},
		{"plain-error", leaky, fault.Internal},
		{"driver-panics", panicky, fault.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			r := e.newRegistry(e.config())
			e.flaky.set(c.name, c.mode)
			_, err := r.Create(ctx, NewTenant{ID: "id-" + c.name, Name: c.name, Driver: flakyDriver, Address: flakyAddress(c.name)}, e.audit("CreateTenant"))
			e.wantNothingLeft(r, c.name, err, c.kind)
		})
	}
}

// TestCreateSQLiteRefusedFile guards the same for a SQLite tenant whose file
// the driver refuses: a loose file planted at its path, and a symbolic link.
// Both are Invalid, and the creation leaves nothing behind.
func TestCreateSQLiteRefusedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes and links are not used on Windows here")
	}
	for _, plant := range []string{"loose", "link"} {
		t.Run(plant, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			path, err := driver.SQLiteTenants{DataDir: e.data}.Path(plant)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			switch plant {
			case "loose":
				if err := os.WriteFile(path, nil, 0o644); err != nil { //nolint:gosec // G306: the loose mode is what is tested.
					t.Fatal(err)
				}
			case "link":
				target := filepath.Join(e.data, "elsewhere.db")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			r := e.newRegistry(e.config())
			_, err = r.Create(ctx, NewTenant{ID: "id-" + plant, Name: plant, Driver: driver.SQLiteDriver}, e.audit("CreateTenant"))
			e.wantNothingLeft(r, plant, err, fault.Invalid)
		})
	}
}

// wantNothingLeft fails unless err has the kind want and the tenant named
// name has no row, no entry and no open store, and the undoing was audited
// by the system without the address.
func (e *env) wantNothingLeft(r *Registry, name string, err error, want fault.Kind) {
	e.t.Helper()
	if fault.KindOf(err) != want {
		e.t.Errorf("Create = %v (kind %v), want %v", err, fault.KindOf(err), want)
	}
	if err != nil {
		mustNotContain(e.t, "the error", err.Error(), append(passwordForms, flakyHost)...)
	}
	if _, merr := e.master.TenantByName(context.Background(), name); fault.KindOf(merr) != fault.NotFound {
		e.t.Errorf("the master still has the tenant (%v)", merr)
	}
	if st, known := r.State(name); known {
		e.t.Errorf("the registry still has the tenant, %s", st.State)
	}
	if _, serr := r.Store(name); fault.KindOf(serr) != fault.NotFound {
		e.t.Errorf("Store after a failed creation = %v, want NotFound", serr)
	}
	if o, c := e.flaky.opens.Load(), e.flaky.closes.Load(); o != c {
		e.t.Errorf("%d stores opened and %d closed", o, c)
	}
	entries, _, lerr := e.master.ListAudit(context.Background(), master.AuditFilter{TenantName: name}, master.Page{Limit: 100})
	if lerr != nil {
		e.t.Fatal(lerr)
	}
	undone := false
	for _, a := range entries {
		if a.Method == UndoMethod {
			undone = true
			if a.Details[DetailActor] != ActorSystem || !strings.HasPrefix(a.Reason, "the creation was undone: ") {
				e.t.Errorf("the undoing's audit entry = %+v", a)
			}
			mustNotContain(e.t, "the undoing's audit entry", a.Reason, append(passwordForms, flakyHost)...)
		}
	}
	if !undone {
		e.t.Error("no audit entry says the creation was undone")
	}
}

// TestCreateRepeatComparesAddresses guards that a repeated creation with the
// same id, name and type is the idempotent repeat only with the same
// address: another address is Conflict and the stored one is kept.
func TestCreateRepeatComparesAddresses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.newRegistry(e.config())
	e.create(r, "id-lab", "lab", flakyDriver, flakyAddress("lab"))
	e.create(r, "id-lab", "lab", flakyDriver, flakyAddress("lab"))
	other := "flaky://admin:other@" + flakyHost + ":5432/lab"
	_, err := r.Create(ctx, NewTenant{ID: "id-lab", Name: "lab", Driver: flakyDriver, Address: other}, e.audit("CreateTenant"))
	if fault.KindOf(err) != fault.Conflict {
		t.Errorf("a repeat with another address = %v, want Conflict", err)
	}
	stored := e.masterTenant("lab")
	plain, err := e.keys.Open(addressContext(stored.ID), stored.SealedAddress)
	if err != nil || string(plain) != flakyAddress("lab") {
		t.Errorf("the stored address changed (%v)", err)
	}
	// With another key file the stored address does not open, so a repeat
	// cannot be told from another tenant: Internal, nothing changed.
	e.keys = newKeys(t)
	r2 := e.newRegistry(e.config())
	_, err = r2.Create(ctx, NewTenant{ID: "id-lab", Name: "lab", Driver: flakyDriver, Address: flakyAddress("lab")}, e.audit("CreateTenant"))
	if fault.KindOf(err) != fault.Internal || !strings.Contains(fault.Message(err), "does not open with this key file") {
		t.Errorf("a repeat under another key file = %v, want Internal saying the address does not open", err)
	}
}

// TestCreateWhoseRowCommittedThenFailed guards a creation whose row the
// master committed but whose call reported an error: the master is read
// again, the row is found as this call's, and the tenant is opened and ready
// rather than left upgrading with nobody to open it.
func TestCreateWhoseRowCommittedThenFailed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	tricky := &trickyMaster{Store: e.master}
	tricky.createCommitsThenFails.Store(true)
	cfg := e.config()
	cfg.Master = tricky
	r := e.newRegistry(cfg)
	tn, err := r.Create(ctx, NewTenant{ID: "id-lab", Name: "lab", Driver: driver.SQLiteDriver}, e.audit("CreateTenant"))
	if err != nil || tn.State != master.StateReady {
		t.Fatalf("Create after a lost answer = %s, %v; want ready", tn.State, err)
	}
	if _, err := r.Store("lab"); err != nil {
		t.Errorf("the tenant is not served: %v", err)
	}
	if got := e.masterTenant("lab"); got.State != master.StateReady {
		t.Errorf("the master holds %s, want ready", got.State)
	}
}

// TestRemoveWhoseCommitWasLost guards a removal that committed in the master
// and then reported an error: the tenant must not stay served. The retry
// finds no tenant, answers NotFound, and unregisters and closes it; and
// without a retry, the next round's reconciliation does.
func TestRemoveWhoseCommitWasLost(t *testing.T) {
	for _, fix := range []string{"retry", "round"} {
		t.Run(fix, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t)
			tricky := &trickyMaster{Store: e.master}
			cfg := e.config()
			cfg.Master = tricky
			r := e.newRegistry(cfg)
			e.create(r, "id-lab", "lab", flakyDriver, flakyAddress("lab"))
			tricky.removeCommitsThenFails.Store(true)
			if _, err := r.Remove(ctx, "lab", e.audit("RemoveTenant")); err == nil {
				t.Fatal("the lost answer was not reported")
			}
			switch fix {
			case "retry":
				_, err := r.Remove(ctx, "lab", e.audit("RemoveTenant"))
				if fault.KindOf(err) != fault.NotFound {
					t.Errorf("the retry = %v, want NotFound", err)
				}
			case "round":
				r.reconcile()
			}
			if _, err := r.Store("lab"); fault.KindOf(err) != fault.NotFound {
				t.Errorf("the removed tenant is still served (%v)", err)
			}
			if o, c := e.flaky.opens.Load(), e.flaky.closes.Load(); o != c {
				t.Errorf("%d stores opened and %d closed", o, c)
			}
		})
	}
}

// TestReconcileWithMaster guards that a round brings the registry to the
// master's list: a tenant another process added is registered and opened, a
// tenant another process removed is unregistered and closed, and a tenant
// whose state write finds no row is retired at once.
func TestReconcileWithMaster(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.seed("added", "removed", "gone-on-write")
	// The registry under test starts without "added", which another process
	// then adds back.
	added := e.masterTenant("added")
	e.ok(e.master.RemoveTenant(ctx, added.ID, e.audit("RemoveTenant")))
	r := e.newRegistry(e.config())
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	added.State = master.StateReady
	e.ok(e.master.CreateTenant(ctx, added, e.audit("CreateTenant")))
	e.ok(e.master.RemoveTenant(ctx, e.masterTenant("removed").ID, e.audit("RemoveTenant")))
	r.reconcile()
	waitFor(t, "the added tenant to be served", func() bool { _, err := r.Store("added"); return err == nil })
	if _, err := r.Store("removed"); fault.KindOf(err) != fault.NotFound {
		t.Errorf("a tenant the master no longer has is served (%v)", err)
	}

	// A state write that finds no row retires the tenant at once.
	e.ok(e.master.RemoveTenant(ctx, e.masterTenant("gone-on-write").ID, e.audit("RemoveTenant")))
	e.flaky.set("gone-on-write", down)
	checkNow(r)
	if _, err := r.Store("gone-on-write"); fault.KindOf(err) != fault.NotFound {
		t.Errorf("a tenant whose state write found no row = %v, want NotFound", err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if o, c := e.flaky.opens.Load(), e.flaky.closes.Load(); o != c {
		t.Errorf("%d stores opened and %d closed", o, c)
	}
}

// ok fails the test now on an error.
func (e *env) ok(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}
