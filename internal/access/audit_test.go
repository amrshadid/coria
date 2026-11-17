package access_test

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// refuse makes n requests with no key, each a refusal whose entry goes to
// the audit writer, with the method given.
func refuse(t testing.TB, a *access.Access, n int, method string) {
	t.Helper()
	for range n {
		_, err := a.Check(context.Background(), access.Credential{}, access.Authenticated(), facts(method))
		if fault.KindOf(err) != fault.Unauthenticated {
			t.Fatalf("refusal: %v", err)
		}
	}
}

// waitFor polls cond for up to two seconds, for what a worker does after
// the call that asked for it has returned.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// stored returns how many entries m kept, and its batch sizes.
func (m *memStore) stored() (int, []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.audit), append([]int(nil), m.batches...)
}

// TestAuditBatches guards batching: entries are appended in batches of the
// size given, a full batch is written without waiting for the interval, and
// FlushAudit writes the rest.
func TestAuditBatches(t *testing.T) {
	m, _ := newMem(t)
	a := newMemAccessWith(t, m, access.Config{AuditBatch: 3, AuditInterval: time.Hour})
	refuse(t, a, 7, "m")
	waitFor(t, "two full batches", func() bool { _, b := m.stored(); return len(b) == 2 })
	if err := a.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, batches := m.stored()
	if n != 7 || !reflect.DeepEqual(batches, []int{3, 3, 1}) {
		t.Fatalf("%d entries in batches %v, want 7 in [3 3 1]", n, batches)
	}
	if s := a.AuditStats(); s != (access.AuditStats{Written: 7}) {
		t.Fatalf("stats %+v", s)
	}
}

// TestAuditInterval guards the interval: a batch that does not fill is
// written when the interval passes, with no flush asked for.
func TestAuditInterval(t *testing.T) {
	m, _ := newMem(t)
	a := newMemAccessWith(t, m, access.Config{AuditBatch: 1000, AuditInterval: 10 * time.Millisecond})
	refuse(t, a, 2, "m")
	waitFor(t, "the interval's batch", func() bool { n, _ := m.stored(); return n == 2 })
}

// TestAuditFallback guards the fallback: when a batch fails, each entry is
// appended alone, so the one the master refuses is lost, counted and logged,
// and the others are kept.
func TestAuditFallback(t *testing.T) {
	m, _ := newMem(t)
	m.appendFn = func(_ context.Context, entries []master.AuditEntry) error {
		for _, e := range entries {
			if e.Method == "poison" {
				return fault.New(fault.Invalid, "test: poison")
			}
		}
		return nil
	}
	a := newMemAccessWith(t, m, access.Config{AuditInterval: time.Hour})
	refuse(t, a, 2, "a")
	refuse(t, a, 1, "poison")
	refuse(t, a, 2, "b")
	if err := a.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, batches := m.stored()
	if n != 4 || !reflect.DeepEqual(batches, []int{5, 1, 1, 1, 1, 1}) {
		t.Fatalf("%d entries kept in calls %v, want 4 in [5 1 1 1 1 1]", n, batches)
	}
	if s := a.AuditStats(); s != (access.AuditStats{Written: 4, Failed: 1}) {
		t.Fatalf("stats %+v", s)
	}
}

// TestAuditDropsWhenFull guards the full queue: with the master stuck,
// refusals still answer at once, the entries past the queue are dropped and
// counted, and once the master answers again one entry says how many were
// dropped; further drops within the minute add no second notice.
func TestAuditDropsWhenFull(t *testing.T) {
	m, _ := newMem(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	m.appendFn = func(ctx context.Context, _ []master.AuditEntry) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a := newMemAccessWith(t, m, access.Config{AuditQueue: 2, AuditBatch: 1, AuditInterval: time.Hour})
	refuse(t, a, 1, "m")
	<-entered // the worker holds the first entry, stuck in the append
	start := time.Now()
	refuse(t, a, 7, "m") // two fill the queue, five are dropped
	if d := time.Since(start); d > time.Second {
		t.Fatalf("7 refusals took %v with the master stuck", d)
	}
	if got := a.AuditStats().Dropped; got != 5 {
		t.Fatalf("%d dropped, want 5", got)
	}
	close(release)
	if err := a.FlushAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	var notices []master.AuditEntry
	for _, e := range m.audit {
		if e.Method == "access.AuditWriter" {
			notices = append(notices, e)
		}
	}
	kept := len(m.audit)
	m.mu.Unlock()
	if kept != 4 || len(notices) != 1 || notices[0].Details["dropped"] != "5" || notices[0].Outcome != master.OutcomeFailed {
		t.Fatalf("%d kept, notices %+v; want 3 refusals and one notice of 5", kept, notices)
	}
	t.Logf("notice: %s", notices[0].Reason)
	if s := a.AuditStats(); s != (access.AuditStats{Written: 4, Dropped: 5}) {
		t.Fatalf("stats %+v", s)
	}
}

// TestAuditDrainOnClose guards shutdown: Close writes every entry the writer
// holds before it returns.
func TestAuditDrainOnClose(t *testing.T) {
	m, _ := newMem(t)
	a := newMemAccessWith(t, m, access.Config{AuditInterval: time.Hour})
	refuse(t, a, 10, "m")
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := m.stored(); n != 10 {
		t.Fatalf("%d entries written by Close, want 10", n)
	}
	// After Close an entry is dropped and counted, never blocks.
	refuse(t, a, 1, "m")
	if s := a.AuditStats(); s != (access.AuditStats{Written: 10, Dropped: 1}) {
		t.Fatalf("stats %+v", s)
	}
}

// TestCloseDeadlineLeavesNoGoroutine guards a shutdown with the master
// stuck: Close returns at its deadline, the entries it could not write are
// counted as dropped, and neither worker is left running.
func TestCloseDeadlineLeavesNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	m, _ := newMem(t)
	m.appendFn = func(ctx context.Context, _ []master.AuditEntry) error {
		<-ctx.Done()
		return ctx.Err()
	}
	a, err := access.New(access.Config{Store: m, Clock: func() time.Time { return t0 }, NewID: ids("g"), AuditInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	refuse(t, a, 5, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: %v, want the deadline", err)
	}
	if s := a.AuditStats(); s.Dropped != 5 || s.Written != 0 {
		t.Fatalf("stats %+v", s)
	}
	waitFor(t, "the workers to end, "+strconv.Itoa(before)+" goroutines before New", func() bool {
		return runtime.NumGoroutine() <= before
	})
}

// TestRefusalOffPath guards that a refusal's entry is not written on the
// request's path: with every append taking 200 ms, five refusals still
// answer at once.
func TestRefusalOffPath(t *testing.T) {
	m, _ := newMem(t)
	m.appendFn = func(context.Context, []master.AuditEntry) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	}
	a := newMemAccessWith(t, m, access.Config{AuditBatch: 1, AuditInterval: time.Hour})
	start := time.Now()
	refuse(t, a, 5, "m")
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("5 refusals took %v with each append taking 200ms", d)
	}
}
