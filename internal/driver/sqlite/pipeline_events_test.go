package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/tenant"
	"github.com/amrshadid/magnetar/internal/tenant/tenanttest"
)

// TestTenantEventsContract runs the contract suite's event groups on
// SQLite.
func TestTenantEventsContract(t *testing.T) {
	tenanttest.RunEvents(t, func(tb testing.TB, id string) tenanttest.EventStore {
		return openTenantTest(tb, id).(*Tenant)
	})
}

// TestTenantEventsContractShared runs the event tests that need two
// instances on one tenant database.
func TestTenantEventsContractShared(t *testing.T) {
	tenanttest.RunEventsShared(t, func(tb testing.TB, id string) (tenanttest.EventStore, tenanttest.EventStore) {
		a, b := openTenantPair(tb, id)
		return a.(*Tenant), b.(*Tenant)
	})
}

// countEvents returns how many events of a type the feed holds.
func countEvents(t *testing.T, p *Tenant, typ tenant.EventType) int {
	t.Helper()
	var n int
	if err := p.p.read.QueryRow(`SELECT count(*) FROM events WHERE type = ?`, string(typ)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestStepEventOnceWithItsBatch guards a step's event when the transaction
// the step was grouped into is undone by a neighbor (one whose statement is
// interrupted) and run again: the hook runs again and writes the event
// again in the new transaction, and the feed holds it once, since the first
// write was undone with its transaction.
func TestStepEventOnceWithItsBatch(t *testing.T) {
	ctx := context.Background()
	p := openTenantTest(t, tenanttest.SuiteTenant).(*Tenant)
	fixtureSetup(t, p, "a")
	r, _, err := p.OpenReception(ctx, "a", "2.25.1.1", fixtureID("rc"), fixtureAt(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ready(ctx); err != nil {
		t.Fatal(err)
	}
	before := countEvents(t, p, tenant.EventReceptionStep)
	h := holdWriter(t, &p.base)
	var calls atomic.Int32
	stepped := submit(t, &p.base, 1, func() error {
		_, err := p.StepReception(ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet, At: fixtureAt(70)},
			tenant.StepHooks(tenant.StepEventHook(), func(context.Context, tenant.Reception, tenant.Reception) error { calls.Add(1); return nil }))
		return err
	})
	stopped, cancel := context.WithCancel(ctx)
	defer cancel()
	interrupted := submit(t, &p.base, 2, func() error {
		return p.writeTx(stopped, "test", func(tx *sql.Tx) error {
			time.AfterFunc(50*time.Millisecond, cancel)
			_, err := tx.ExecContext(stopped, `INSERT INTO reception_associations (reception_id, association)
				WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n)
				SELECT 'x', 'a' || i FROM n`)
			return err
		})
	})
	h.letGo(t)
	if res := answer(t, "step", stepped); res.err != nil {
		t.Fatal(res.err)
	}
	answer(t, "interrupted", interrupted)
	t.Logf("the step's hooks ran %d times", calls.Load())
	if calls.Load() < 2 {
		t.Fatalf("the hook ran %d times; the batch was not run again, so this test showed nothing", calls.Load())
	}
	if got := countEvents(t, p, tenant.EventReceptionStep) - before; got != 1 {
		t.Errorf("a step run again with its batch left %d events, want 1", got)
	}
}

// TestEventsResumeAcrossRestart guards a cursor across a restart of the
// store: a reader that stops mid-way, with the store closed and opened
// again on the same file, resumes from its cursor with every event after
// it, once, in order.
func TestEventsResumeAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tenant.db")
	open := func() *Tenant {
		s, err := OpenTenant(ctx, path, "t1")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	}
	appendN := func(s *Tenant, from, n int) {
		batch := make([]tenant.EventAppend, n)
		for i := range batch {
			e, err := tenant.NewEvent(&tenant.StorageSpaceChanged{Storage: "store", State: tenant.StorageRecovered, UsedBytes: int64(from + i)},
				fixtureAt(from+i))
			if err != nil {
				t.Fatal(err)
			}
			batch[i] = tenant.EventAppend{Key: fmt.Sprintf("k%d", from+i), Event: e}
		}
		if _, err := s.AppendEvents(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	s := open()
	appendN(s, 0, 30)
	p, err := s.ReadEvents(ctx, "", nil, 12)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer func() { _ = s.Close() }()
	appendN(s, 30, 5)
	rest, err := s.ReadEvents(ctx, p.Cursor, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Events) != 23 {
		t.Fatalf("after a restart the reader read %d events, want 23", len(rest.Events))
	}
	for i, e := range rest.Events {
		if got := e.Payload.(*tenant.StorageSpaceChanged).UsedBytes; got != int64(12+i) {
			t.Fatalf("event %d after the restart is number %d, want %d", i, got, 12+i)
		}
	}
}
