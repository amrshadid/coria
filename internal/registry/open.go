package registry

import (
	"context"
	"errors"
	"sync"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// The reasons the registry gives. A driver's own text, stripped, follows the
// first two.
const (
	reasonUnreachable = "the database does not answer"
	reasonNotOpened   = "the database cannot be opened or brought to this version's schema"
	reasonDeadline    = "the database did not answer within its deadline"
	reasonAddress     = "the database address did not open with this key file; is it the key file the tenant was created with?"
	reasonNoDriver    = "this server has no driver for the database type "
)

// addressContext is the context a tenant's address is sealed under (package
// secret), which binds it to the tenant's row: a sealed address copied into
// another tenant's row does not open there.
func addressContext(id string) string {
	return "tenant/" + id + "/database"
}

// refresh brings the entry's view, and the state the master holds, to what
// its database does now: it opens a tenant that has no store and pings one
// that has. On a timer check (first false) a failed tenant is left alone,
// since it waits for a person. The caller holds e.op.
func (r *Registry) refresh(e *entry, first bool) {
	if e.removed {
		return
	}
	cur := e.view.Load()
	if !first && cur.state == master.StateFailed {
		return
	}
	if e.opening.Load() {
		// An earlier open outlived its deadline and is still running; it
		// is not started twice.
		r.settle(e, master.StateUnreachable, reasonDeadline)
		return
	}
	if e.store != nil {
		err := r.ping(e)
		if err == nil {
			r.settle(e, master.StateReady, "")
			return
		}
		r.settle(e, master.StateUnreachable, r.because(e, reasonUnreachable, err))
		return
	}
	r.open(e)
}

// open opens, pings and migrates the entry's database under its deadline,
// and settles the entry in the state that follows. The caller holds e.op.
func (r *Registry) open(e *entry) {
	drv, ok := r.cfg.Drivers[e.driver]
	if !ok {
		r.settle(e, master.StateFailed, reasonNoDriver+e.driver)
		return
	}
	address, err := r.cfg.Keys.Open(addressContext(e.id), e.sealed)
	if err != nil {
		r.settle(e, master.StateFailed, reasonAddress)
		return
	}
	// The string is made here, before the open may outlive its deadline,
	// so clearing the bytes below cannot change what a late open reads.
	plain := string(address)
	clear(address)
	e.view.Store(&view{state: master.StateUpgrading, reason: ""})
	s, err := r.attempt(e, func(ctx context.Context) (tenant.Store, error) {
		s, err := drv.Open(ctx, e.name, plain)
		if err != nil {
			return nil, err
		}
		if err := s.Ping(ctx); err != nil {
			_ = s.Close() // The ping failure is the one reported.
			return nil, err
		}
		if err := s.Migrate(ctx); err != nil {
			_ = s.Close() // The migration failure is the one reported.
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		if unavailable(err) {
			r.settle(e, master.StateUnreachable, r.because(e, reasonUnreachable, err))
		} else {
			r.settle(e, master.StateFailed, r.because(e, reasonNotOpened, err))
		}
		return
	}
	e.store = s
	r.settle(e, master.StateReady, "")
}

// ping pings the entry's store under the deadline.
func (r *Registry) ping(e *entry) error {
	s := e.store
	_, err := r.attempt(e, func(ctx context.Context) (tenant.Store, error) {
		return nil, s.Ping(ctx)
	})
	return err
}

// errDeadline is the cause of an open or a ping that outlived its deadline.
var errDeadline = errors.New("deadline passed")

// attempt runs fn in a goroutine of its own under the entry's deadline and
// returns what it returned, or Unavailable when the deadline passes first.
// A driver that does not return by then is left running, still counted by
// the registry (Close waits for it), with e.opening set until it returns;
// a store it returns late is closed, since nobody holds it.
func (r *Registry) attempt(e *entry, fn func(ctx context.Context) (tenant.Store, error)) (tenant.Store, error) {
	ctx, cancel := context.WithTimeout(e.ctx, r.cfg.OpenTimeout)
	type result struct {
		s   tenant.Store
		err error
	}
	var mu sync.Mutex
	gaveUp := false
	done := make(chan result, 1)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		s, err := fn(ctx)
		mu.Lock()
		defer mu.Unlock()
		if gaveUp {
			if s != nil {
				_ = s.Close() // Nobody holds it; its error has no one to tell.
			}
			e.opening.Store(false)
			return
		}
		done <- result{s, err}
	}()
	select {
	case res := <-done:
		return res.s, res.err
	case <-ctx.Done():
	}
	mu.Lock()
	defer mu.Unlock()
	select {
	case res := <-done:
		// It returned as the deadline passed: what it returned is used.
		return res.s, res.err
	default:
	}
	gaveUp = true
	e.opening.Store(true)
	return nil, fault.Wrap(fault.Unavailable, errDeadline, reasonDeadline)
}

// unavailable reports whether err may pass if tried again: kind Unavailable
// or a context that ended.
func unavailable(err error) bool {
	return fault.KindOf(err) == fault.Unavailable ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// because returns a reason: what happened, then the driver's text stripped
// of every part of the tenant's address. The address is opened again here
// rather than kept from the open, so its plaintext lives only as long as one
// call.
func (r *Registry) because(e *entry, what string, err error) string {
	// The registry's own deadline, or a driver that gave up on it: the
	// same cause, said the same way.
	if errors.Is(err, errDeadline) || errors.Is(err, context.DeadlineExceeded) {
		return reasonDeadline
	}
	var secrets []string
	if address, oerr := r.cfg.Keys.Open(addressContext(e.id), e.sealed); oerr == nil {
		secrets = secretsOf(string(address))
		clear(address)
	}
	return scrub(what+": "+err.Error(), secrets)
}

// settle sets the entry's view to state, with the entry's store when ready,
// and writes the state to the master when it changed there. The caller
// holds e.op.
//
// A change between ready and unreachable is written at most once per
// StateChangeInterval: the view changes at once, and a change not written
// is written by a later check once the interval has passed, if it still
// holds. A change to or from upgrading or failed is written at once: those
// come from an open, not from a ping, and cannot flap.
func (r *Registry) settle(e *entry, state master.TenantState, reason string) {
	var s tenant.Store
	if state == master.StateReady {
		s = e.store
	}
	e.view.Store(&view{state: state, reason: reason, store: s})
	if e.logged.State != state || e.logged.Reason != reason {
		// Logged once per change, not once per check of a tenant that
		// stays down.
		r.log.Info("tenant state", "tenant", e.name, "state", string(state), "reason", reason)
		e.logged = State{State: state, Reason: reason}
	}
	if e.removed || state == e.written {
		return
	}
	now := r.cfg.Now()
	if flapping(e.written, state) && !e.lastWrite.IsZero() && now.Sub(e.lastWrite) < StateChangeInterval {
		return
	}
	audit := master.AuditEntry{
		ID:         r.cfg.NewID(),
		Time:       now,
		Method:     SystemMethod,
		TenantName: e.name,
		Object:     "tenant/" + e.name,
		Outcome:    master.OutcomeDone,
		Reason:     reason,
		Details: map[string]string{
			// The master adds the state before and after, read inside its
			// change (master.AuditEntry.WithStateChange).
			DetailActor: ActorSystem,
		},
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.cfg.OpenTimeout)
	defer cancel()
	err := r.cfg.Master.SetTenantState(ctx, e.id, state, reason, audit)
	switch {
	case err == nil:
		e.written, e.lastWrite = state, now
	case fault.KindOf(err) == fault.NotFound:
		// The row was removed meanwhile; Remove unregisters the entry.
	default:
		// The master did not take it; a later check tries again, since the
		// state it holds still differs.
		r.log.Warn("tenant state not recorded", "tenant", e.name, "state", string(state), "error", err.Error())
	}
}

// flapping reports whether a change from one state to another is between
// ready and unreachable, which a database that comes and goes repeats.
func flapping(from, to master.TenantState) bool {
	pair := func(s master.TenantState) bool { return s == master.StateReady || s == master.StateUnreachable }
	return pair(from) && pair(to)
}
