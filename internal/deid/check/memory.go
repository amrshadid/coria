package check

import (
	"context"
	"sync"
	"time"
)

// Memory is a bound on the bytes the server's de-identification work holds
// in memory at once, across every tenant and every worker: the check's copy
// of each file, and a trial's or a job's copies. Budget is one.
type Memory interface {
	// Reserve takes n bytes of the bound and returns the function that
	// gives them back, which may be called more than once. It waits while
	// the bytes taken by others leave no room, until ctx ends (ctx's
	// error). n past the whole bound is a *Refusal with CauseTooLarge,
	// answered at once: such a file would wait for ever.
	Reserve(ctx context.Context, n int64) (release func(), err error)
}

// recheck is how often a reservation that waits reads the bound again, so a
// bound raised while it waits lets it in without a release by another.
const recheck = time.Second

// BoundInterval is how long a Budget of the server keeps the bound it read
// (NewBudget's every): a setting changed takes effect within it, and a
// reception of many files reads the setting once a second, not once a file.
const BoundInterval = time.Second

// Budget is a Memory whose bound is read live (the installation's
// deid.memory), so a change takes effect for the next file without a
// restart. A bound lowered below what is taken now takes nothing back:
// reservations wait until enough is given back.
//
// Reservations are let in first come, first served (Reserve).
type Budget struct {
	limit func(ctx context.Context) (int64, error)
	every time.Duration

	mu   sync.Mutex
	used int64
	// changed is closed, and replaced, each time bytes are given back or
	// the line changes, so every waiter looks again.
	changed chan struct{}
	// line is the reservations that wait, in the order they came.
	line []*waiter
	// bound is the bound last read, at read.
	bound int64
	read  time.Time
}

// NewBudget returns a Budget whose bound is what limit answers, read again
// once what was read is older than every (zero reads it at every
// reservation); an error of limit is returned by Reserve, which then takes
// nothing.
func NewBudget(limit func(ctx context.Context) (int64, error), every time.Duration) *Budget {
	return &Budget{limit: limit, every: every, changed: make(chan struct{})}
}

// current returns the bound, read again when what was read is older than
// every.
func (b *Budget) current(ctx context.Context) (int64, error) {
	b.mu.Lock()
	if !b.read.IsZero() && time.Since(b.read) < b.every {
		defer b.mu.Unlock()
		return b.bound, nil
	}
	b.mu.Unlock()
	n, err := b.limit(ctx)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	b.bound, b.read = n, time.Now()
	b.mu.Unlock()
	return n, nil
}

// Reserve implements Memory. Reservations are let in first come, first
// served: one that does not fit joins the line, and while anyone waits in
// it a newcomer waits behind them, even one that would fit, so a large
// file is never starved by a stream of small ones (one tenant's small
// files or dry run cannot hold back another tenant's large file).
func (b *Budget) Reserve(ctx context.Context, n int64) (func(), error) {
	if n <= 0 {
		return func() {}, nil
	}
	var me *waiter
	defer func() {
		if me != nil {
			b.leave(me)
		}
	}()
	for {
		limit, err := b.current(ctx)
		if err != nil {
			return nil, err
		}
		if n > limit {
			return nil, &Refusal{Cause: CauseTooLarge}
		}
		b.mu.Lock()
		first := len(b.line) == 0 || (me != nil && b.line[0] == me)
		if first && b.used+n <= limit {
			b.used += n
			if me != nil {
				b.line = b.line[1:]
				me = nil
				// The next in line may fit beside this one.
				b.wakeLocked()
			}
			b.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { b.give(n) }) }, nil
		}
		if me == nil {
			me = &waiter{n: n}
			b.line = append(b.line, me)
		}
		changed := b.changed
		b.mu.Unlock()
		t := time.NewTimer(recheck)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-changed:
		case <-t.C:
		}
		t.Stop()
	}
}

// waiter is one reservation waiting in a Budget's line: n bytes. It has a
// field so each waiter is a distinct pointer (pointers to zero-size values
// may compare equal).
type waiter struct{ n int64 }

// leave takes w out of the line, when it gave up waiting, and wakes the
// others: the next may now be first.
func (b *Budget) leave(w *waiter) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, x := range b.line {
		if x == w {
			b.line = append(b.line[:i], b.line[i+1:]...)
			break
		}
	}
	b.wakeLocked()
}

// wakeLocked wakes every reservation that waits, to look again; b.mu is
// held.
func (b *Budget) wakeLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// Used returns the bytes taken now.
func (b *Budget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// give gives n bytes back and wakes every reservation that waits.
func (b *Budget) give(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
	b.wakeLocked()
}
