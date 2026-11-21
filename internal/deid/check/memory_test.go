package check

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amrshadid/coria/internal/deid/engine"
)

// fixedLimit is a Budget's bound read from v.
func fixedLimit(v *atomic.Int64) func(context.Context) (int64, error) {
	return func(context.Context) (int64, error) { return v.Load(), nil }
}

// TestBudgetBoundsWhatIsHeldAtOnce guards the server's bound on
// de-identification's memory: reservations within it are taken at once, one
// that does not fit beside them waits until bytes are given back, one past
// the whole bound is refused at once as check_too_large (it would wait for
// ever), a release given twice gives back once, and a reservation that
// waits ends with its context.
func TestBudgetBoundsWhatIsHeldAtOnce(t *testing.T) {
	var limit atomic.Int64
	limit.Store(100)
	b := NewBudget(fixedLimit(&limit), 0)
	ctx := context.Background()
	first, err := b.Reserve(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Reserve(ctx, 40)
	if err != nil || b.Used() != 100 {
		t.Fatalf("two reservations within the bound: %v, used %d", err, b.Used())
	}
	if _, err := b.Reserve(ctx, 101); !isTooLarge(err) {
		t.Fatalf("a reservation past the whole bound answered %v, want check_too_large", err)
	}
	got := make(chan error, 1)
	go func() {
		release, err := b.Reserve(ctx, 50)
		if err == nil {
			defer release()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("a reservation with no room was let in (%v)", err)
	case <-time.After(50 * time.Millisecond):
	}
	first()
	first()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a reservation that waited was not let in once room was given back")
	}
	if u := b.Used(); u != 40 {
		t.Fatalf("used %d after a release given twice and one let in and out, want 40", u)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := b.Reserve(short, 61); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a reservation that waits past its context answered %v", err)
	}
	second()
	if u := b.Used(); u != 0 {
		t.Fatalf("used %d after every release", u)
	}
}

// TestBudgetReadsItsBoundLive guards a bound raised while a reservation
// waits: it is let in at the next reading of the bound, with no release by
// another, as an installation's raised deid.memory lets a waiting file in.
func TestBudgetReadsItsBoundLive(t *testing.T) {
	var limit atomic.Int64
	limit.Store(100)
	b := NewBudget(fixedLimit(&limit), 0)
	hold, err := b.Reserve(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	got := make(chan error, 1)
	go func() {
		release, err := b.Reserve(context.Background(), 50)
		if err == nil {
			release()
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	limit.Store(200)
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * recheck):
		t.Fatal("a raised bound did not let a waiting reservation in")
	}
}

// TestTheCheckTakesItsCopyFromTheBudget guards the check after under the
// server's bound: a file larger than the whole bound is refused as
// check_too_large before anything is read (a border: its bytes stay held
// and a release once the bound is raised checks it), a file within it is
// checked with its bytes taken from the bound while it is read and given
// back after, and a check that waits for room ends with its context, which
// is no verdict on the file.
func TestTheCheckTakesItsCopyFromTheBudget(t *testing.T) {
	p := writeFile(t, el{0x00204000, "LT", strings.Repeat("x", 3000)})
	n := needle(engine.NeedleText, "MRN-778812")
	var limit atomic.Int64
	limit.Store(1000)
	b := NewBudget(fixedLimit(&limit), 0)
	refusal(t, Options{Memory: b}, p, CauseTooLarge, n)
	limit.Store(1 << 20)
	var during int64
	c := New(Options{Memory: b})
	c.seams.afterParse = func() { during = b.Used() }
	if _, err := c.checkPath(t, context.Background(), p, engine.Needles{n}, nil); err != nil {
		t.Fatal(err)
	}
	if during == 0 || b.Used() != 0 {
		t.Fatalf("held %d while checking and %d after, want the file's size then nothing", during, b.Used())
	}
	hold, err := b.Reserve(context.Background(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = New(Options{Memory: b}).checkPath(t, ctx, p, engine.Needles{n}, nil)
	var r *Refusal
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &r) {
		t.Fatalf("a check waiting for room past its context answered %v, want the context's error", err)
	}
}

// isTooLarge reports whether err is a check_too_large refusal.
func isTooLarge(err error) bool {
	var r *Refusal
	return errors.As(err, &r) && r.Cause == CauseTooLarge
}

// TestBudgetServesFirstComeFirst guards a large reservation against a
// stream of small ones: with a bound of 100 and four loops reserving 30
// each without pause, a reservation of 90 that comes after them is let in
// within a bounded time (once the reservations taken before it are given
// back), because a newcomer waits behind it while it waits; and one that
// gives up leaves the line, so the small ones go on.
func TestBudgetServesFirstComeFirst(t *testing.T) {
	var limit atomic.Int64
	limit.Store(100)
	b := NewBudget(fixedLimit(&limit), 0)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var small atomic.Int64
	done := make(chan struct{})
	for range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for ctx.Err() == nil {
				release, err := b.Reserve(ctx, 30)
				if err != nil {
					return
				}
				small.Add(1)
				time.Sleep(time.Millisecond)
				release()
			}
		}()
	}
	for small.Load() < 50 {
		time.Sleep(time.Millisecond)
	}
	wait, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	began := time.Now()
	release, err := b.Reserve(wait, 90)
	if err != nil {
		t.Fatalf("a reservation of 90 among reservations of 30 waited %v and got %v (%d small ones let in)", time.Since(began), err, small.Load())
	}
	took := time.Since(began)
	if u := b.Used(); u != 90 {
		t.Errorf("used %d while the large reservation holds, want 90: a small one passed it", u)
	}
	release()
	t.Logf("the reservation of 90 was let in after %v", took)

	short, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	hold, err := b.Reserve(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(short, 90); err == nil {
		t.Fatal("a reservation with no room was let in")
	}
	hold()
	before := small.Load()
	deadline := time.Now().Add(2 * time.Second)
	for small.Load() < before+20 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if small.Load() < before+20 {
		t.Error("the small reservations stopped after a waiter ahead of them gave up")
	}
	stop()
	for range 4 {
		<-done
	}
}
