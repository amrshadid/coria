package events

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// Reader is one tenant's event feed as a watch reads it: the read and the
// current cursor of tenant.Events.
type Reader interface {
	// ReadEvents is tenant.Events.ReadEvents.
	ReadEvents(ctx context.Context, after string, types []tenant.EventType, limit int) (tenant.EventPage, error)
	// EventCursor is tenant.Events.EventCursor.
	EventCursor(ctx context.Context) (string, error)
}

// Tenants lists the tenants a stream of every tenant follows and gives each
// one's reader.
type Tenants interface {
	// TenantNames returns the names of the tenants now.
	TenantNames(ctx context.Context) ([]string, error)
	// Reader returns a tenant's reader, or an error when its database
	// does not answer or it is not ready; the stream skips it and tries
	// again on the next tick.
	Reader(ctx context.Context, name string) (Reader, error)
}

// Clock is where a Broadcaster takes time from: the time now, for
// keep-alives, and a ticker, for the timer that wakes every watch.
type Clock interface {
	// Now returns the time now.
	Now() time.Time
	// NewTicker returns a ticker that ticks every d until stopped.
	NewTicker(d time.Duration) Ticker
}

// Ticker ticks until stopped.
type Ticker interface {
	// C is where it ticks.
	C() <-chan time.Time
	// Stop stops it.
	Stop()
}

// The defaults of Config.
const (
	// DefaultTick is how often every watch is woken whatever is written.
	DefaultTick = time.Second
	// DefaultKeepAlive is the contract's 15 seconds with nothing sent
	// before a keep-alive.
	DefaultKeepAlive = 15 * time.Second
	// DefaultPageSize is how many events a watch reads at once.
	DefaultPageSize = 100
	// DefaultMaxPerOwner is the contract's most open streams of one
	// caller.
	DefaultMaxPerOwner = 16
	// DefaultMaxTotal is the contract's most open streams of the server.
	DefaultMaxTotal = 1024
	// DefaultMaxPerTenant is the most watches of one tenant.
	DefaultMaxPerTenant = 1024
)

// Config is how a Broadcaster runs. A zero field takes its default.
type Config struct {
	// Clock gives the time and the ticker; nil is the system's.
	Clock Clock
	// Tick is how often every watch is woken.
	Tick time.Duration
	// KeepAlive is how long a watch sends nothing before a keep-alive.
	KeepAlive time.Duration
	// PageSize is how many events a watch reads at once, 1 to
	// tenant.MaxEventPage.
	PageSize int
	// MaxPerOwner, MaxPerTenant and MaxTotal bound the watches open at
	// once: of one owner, of one tenant (a stream of every tenant is
	// counted by none), and in all.
	MaxPerOwner, MaxPerTenant, MaxTotal int
}

// ErrTooManyWatches is in the chain of a watch refused because a bound of
// open watches is reached. Its kind is Unavailable, since a watch may be
// opened once another ends; the API answers it as resource exhausted.
var ErrTooManyWatches = errors.New("too many watches are open")

// ErrClosed is in the chain of a watch ended or refused because its
// Broadcaster is closed: the server is stopping. Its kind is Unavailable.
var ErrClosed = errors.New("the event feed is closed")

// Message is one message of a stream: an event or a keep-alive.
type Message struct {
	// Tenant names the tenant an event happened in; empty for a
	// keep-alive of a stream of every tenant.
	Tenant string
	// Event is the event, nil for a keep-alive.
	Event *tenant.Event
	// KeepAlive is the keep-alive, nil for an event.
	KeepAlive *KeepAlive
}

// KeepAlive opens every stream and is sent again when nothing was sent for
// the keep-alive interval.
type KeepAlive struct {
	// Time is when it was sent.
	Time time.Time
	// Cursor is, on one tenant, where the stream has read up to, past the
	// events its filter left out.
	Cursor string
	// TenantCursors are, with every tenant, where the stream has read up
	// to in each tenant it has read; a copy, the caller's to keep.
	TenantCursors map[string]string
}

// Send delivers one message to a stream's client. It may block, which holds
// the watch until it returns; it returns when ctx ends. An error ends the
// watch with that error.
type Send func(ctx context.Context, m Message) error

// Watch is a stream of one tenant's events.
type Watch struct {
	// Tenant names the tenant.
	Tenant string
	// Owner names who opened it (a key, or the local operator), for the
	// bound of watches per owner.
	Owner string
	// Reader reads the tenant's feed.
	Reader Reader
	// Cursor is where to start: after this cursor, or from now when empty.
	Cursor string
	// Types keeps only events of these types; all when empty.
	Types []tenant.EventType
}

// WatchAll is a stream of every tenant's events.
type WatchAll struct {
	// Owner names who opened it.
	Owner string
	// Tenants lists the tenants and gives their readers.
	Tenants Tenants
	// Cursors are where to start in each tenant named; a tenant there at
	// the start and not named starts from now, and a tenant created later
	// from its first event.
	Cursors map[string]string
	// Types keeps only events of these types; all when empty.
	Types []tenant.EventType
}

// watcher is one open watch as the Broadcaster holds it: a wake signal of
// one slot, and for a stream of every tenant the tenants woken since it last
// looked. Nothing else: no event is ever held here.
type watcher struct {
	tenant string // empty for a stream of every tenant
	owner  string
	index  int // its place in its tenant's list, or in all
	wake   chan struct{}

	mu     sync.Mutex
	dirty  map[string]struct{} // every tenant: the tenants woken
	rescan bool                // every tenant: list the tenants again
}

// signal fills the watcher's slot, or finds it full: a wake folded into
// another costs nothing and is never queued.
func (w *watcher) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Broadcaster wakes the watches of a tenant after a commit that wrote an
// event, and runs each watch's loop. One per server; safe for concurrent
// use.
type Broadcaster struct {
	cfg    Config
	ctx    context.Context // ends with Close
	cancel context.CancelFunc
	wg     sync.WaitGroup // the timer's goroutine and every watch

	mu       sync.Mutex
	closed   bool
	byTenant map[string][]*watcher
	all      []*watcher
	owners   map[string]int
	total    int
}

// New returns a Broadcaster with cfg, its defaults filled in, and starts its
// timer, which runs until Close.
func New(cfg Config) (*Broadcaster, error) {
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	fill := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	fill(&cfg.PageSize, DefaultPageSize)
	fill(&cfg.MaxPerOwner, DefaultMaxPerOwner)
	fill(&cfg.MaxPerTenant, DefaultMaxPerTenant)
	fill(&cfg.MaxTotal, DefaultMaxTotal)
	if cfg.Tick == 0 {
		cfg.Tick = DefaultTick
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if cfg.Tick < 0 || cfg.KeepAlive < 0 || cfg.PageSize < 1 || cfg.PageSize > tenant.MaxEventPage ||
		cfg.MaxPerOwner < 1 || cfg.MaxPerTenant < 1 || cfg.MaxTotal < 1 {
		return nil, fault.New(fault.Invalid, "an event feed's tick, keep-alive, page size and bounds are positive, and a page at most 1000")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Broadcaster{cfg: cfg, ctx: ctx, cancel: cancel, byTenant: map[string][]*watcher{}, owners: map[string]int{}}
	t := cfg.Clock.NewTicker(cfg.Tick)
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer t.Stop()
		for {
			select {
			case <-t.C():
				b.wakeAll()
			case <-ctx.Done():
				return
			}
		}
	}()
	return b, nil
}

// Close ends every watch and the timer, and returns once they have ended.
// A watch opened after it is refused with ErrClosed. A second Close does
// nothing.
func (b *Broadcaster) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cancel()
	b.wg.Wait()
	return nil
}

// Wake wakes every watch of the tenant named, and every stream of every
// tenant. A writer calls it after a commit that wrote an event of that
// tenant. It never blocks on a watch: its cost is a few instructions per
// watch of the tenant.
func (b *Broadcaster) Wake(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, w := range b.byTenant[name] {
		w.signal()
	}
	for _, w := range b.all {
		w.mu.Lock()
		w.dirty[name] = struct{}{}
		w.mu.Unlock()
		w.signal()
	}
}

// wakeAll wakes every watch, on the timer's tick: a wake missed, and events
// another process wrote, are read at the latest one tick later. A stream of
// every tenant lists the tenants again, which is how one created or removed
// joins or leaves it.
func (b *Broadcaster) wakeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, list := range b.byTenant {
		for _, w := range list {
			w.signal()
		}
	}
	for _, w := range b.all {
		w.mu.Lock()
		w.rescan = true
		w.mu.Unlock()
		w.signal()
	}
}

// register admits a watch of the tenant named (empty for every tenant)
// within the bounds, or refuses it.
func (b *Broadcaster) register(name, owner string) (*watcher, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, fault.Wrap(fault.Unavailable, ErrClosed, "the server is stopping")
	}
	switch {
	case b.total >= b.cfg.MaxTotal:
		return nil, fault.Wrap(fault.Unavailable, ErrTooManyWatches, "the server holds as many open streams as it allows")
	case b.owners[owner] >= b.cfg.MaxPerOwner:
		return nil, fault.Wrap(fault.Unavailable, ErrTooManyWatches, "the caller holds as many open streams as one caller may")
	case name != "" && len(b.byTenant[name]) >= b.cfg.MaxPerTenant:
		return nil, fault.Wrap(fault.Unavailable, ErrTooManyWatches, "the tenant has as many open streams as it allows")
	}
	w := &watcher{tenant: name, owner: owner, wake: make(chan struct{}, 1)}
	if name == "" {
		w.dirty, w.rescan = map[string]struct{}{}, true
		w.index = len(b.all)
		b.all = append(b.all, w)
	} else {
		w.index = len(b.byTenant[name])
		b.byTenant[name] = append(b.byTenant[name], w)
	}
	b.owners[owner]++
	b.total++
	b.wg.Add(1)
	return w, nil
}

// unregister removes a watch, in constant time: the last of its list takes
// its place.
func (b *Broadcaster) unregister(w *watcher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remove := func(list []*watcher) []*watcher {
		last := list[len(list)-1]
		list[w.index], last.index = last, w.index
		list[len(list)-1] = nil
		return list[:len(list)-1]
	}
	if w.tenant == "" {
		b.all = remove(b.all)
	} else if list := remove(b.byTenant[w.tenant]); len(list) == 0 {
		delete(b.byTenant, w.tenant)
	} else {
		b.byTenant[w.tenant] = list
	}
	if b.owners[w.owner]--; b.owners[w.owner] == 0 {
		delete(b.owners, w.owner)
	}
	b.total--
	b.wg.Done()
}

// watchContext returns ctx, ended also by Close.
func (b *Broadcaster) watchContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(b.ctx, func() { cancel(ErrClosed) })
	return ctx, func() {
		stop()
		cancel(nil)
	}
}

// ended is the error of a watch whose context ended: ErrClosed when Close
// ended it, and the context's cause otherwise.
func ended(ctx context.Context) error {
	cause := context.Cause(ctx)
	if errors.Is(cause, ErrClosed) {
		return fault.Wrap(fault.Unavailable, ErrClosed, "the server is stopping")
	}
	return fault.Wrap(fault.Unavailable, cause, "the stream ended")
}

// Watch streams one tenant's events to send until ctx ends, Close is
// called, send fails, or a read fails, and returns why. The first message
// is a keep-alive with the cursor the stream starts from; then each event
// committed after it that the filter keeps, in commit order, once; and a
// keep-alive with the current cursor when nothing was sent for the
// keep-alive interval. A cursor older than what the tenant keeps ends it
// with tenant.ErrCursorExpired in the chain; a tenant whose database does
// not answer ends it with the read's error.
func (b *Broadcaster) Watch(ctx context.Context, w Watch, send Send) error {
	if err := tenant.ValidateName("tenant", w.Tenant); err != nil {
		return err
	}
	if w.Reader == nil || send == nil {
		return fault.New(fault.Invalid, "a watch has a reader and a send function")
	}
	if err := tenant.ValidateEventTypes(w.Types); err != nil {
		return err
	}
	me, err := b.register(w.Tenant, w.Owner)
	if err != nil {
		return err
	}
	defer b.unregister(me)
	ctx, cancel := b.watchContext(ctx)
	defer cancel()

	cursor := w.Cursor
	if cursor == "" {
		if cursor, err = w.Reader.EventCursor(ctx); err != nil {
			return b.readFailed(ctx, err)
		}
	}
	// The first page is read before the keep-alive that opens the stream,
	// so a cursor that is refused ends the stream before it opens.
	page, err := w.Reader.ReadEvents(ctx, cursor, w.Types, b.cfg.PageSize)
	if err != nil {
		return b.readFailed(ctx, err)
	}
	if err := send(ctx, Message{Tenant: w.Tenant, KeepAlive: &KeepAlive{Time: b.cfg.Clock.Now(), Cursor: cursor}}); err != nil {
		return sendFailed(ctx, err)
	}
	lastSent := b.cfg.Clock.Now()
	for {
		for i := range page.Events {
			if err := send(ctx, Message{Tenant: w.Tenant, Event: &page.Events[i]}); err != nil {
				return sendFailed(ctx, err)
			}
			lastSent = b.cfg.Clock.Now()
		}
		cursor = page.Cursor
		if len(page.Events) < b.cfg.PageSize {
			if now := b.cfg.Clock.Now(); now.Sub(lastSent) >= b.cfg.KeepAlive {
				if err := send(ctx, Message{Tenant: w.Tenant, KeepAlive: &KeepAlive{Time: now, Cursor: cursor}}); err != nil {
					return sendFailed(ctx, err)
				}
				lastSent = now
			}
			select {
			case <-me.wake:
			case <-ctx.Done():
				return ended(ctx)
			}
		}
		// The page is dropped before the next read: at most one page of
		// events is held by a watch at any moment.
		page = tenant.EventPage{}
		if page, err = w.Reader.ReadEvents(ctx, cursor, w.Types, b.cfg.PageSize); err != nil {
			return b.readFailed(ctx, err)
		}
	}
}

// sendFailed is the error that ends a watch whose send failed: the end of
// its context when that is why (a send returns when its context ends), and
// the send's error otherwise.
func sendFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ended(ctx)
	}
	return err
}

// readFailed is the error that ends a watch whose read failed: the end of
// its context when that is why, and the read's error otherwise.
func (b *Broadcaster) readFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ended(ctx)
	}
	return err
}

// pagesPerRound bounds the pages a stream of every tenant reads from one
// tenant before it looks at the others, so a tenant that writes without a
// pause does not keep the others waiting.
const pagesPerRound = 10

// follow is one tenant as a stream of every tenant follows it.
type follow struct {
	cursor  string
	fromNow bool // start from the tenant's newest event when first read
	known   bool // the cursor is set
}

// WatchAll streams every tenant's events to send, each tenant's in its
// commit order, until ctx ends, Close is called, send fails, or a tenant's
// cursor is older than what it keeps (tenant.ErrCursorExpired, with the
// tenant named in the message). A tenant whose database does not answer is
// skipped and tried again on the next tick; a tenant listed later joins
// from its first event; a tenant no longer listed leaves. The first message
// is a keep-alive with the cursors of the tenants read; a keep-alive with
// every tenant's cursor follows whenever nothing was sent for the
// keep-alive interval.
func (b *Broadcaster) WatchAll(ctx context.Context, w WatchAll, send Send) error {
	if w.Tenants == nil || send == nil {
		return fault.New(fault.Invalid, "a watch of every tenant has a list of tenants and a send function")
	}
	if err := tenant.ValidateEventTypes(w.Types); err != nil {
		return err
	}
	me, err := b.register("", w.Owner)
	if err != nil {
		return err
	}
	defer b.unregister(me)
	ctx, cancel := b.watchContext(ctx)
	defer cancel()

	follows := map[string]*follow{}
	listed := false
	// list reads the tenants: those there at the start begin from the
	// cursor given or from now, those listed later from their first event.
	list := func() {
		names, err := w.Tenants.TenantNames(ctx)
		if err != nil {
			return // tried again on the next tick
		}
		now := map[string]bool{}
		for _, n := range names {
			now[n] = true
			if _, ok := follows[n]; ok {
				continue
			}
			f := &follow{}
			switch c, given := w.Cursors[n]; {
			case !listed && given:
				f.cursor, f.known = c, true
			case !listed:
				f.fromNow = true
			default:
				f.known = true // from its first event: the empty cursor
			}
			follows[n] = f
		}
		for n := range follows {
			if !now[n] {
				delete(follows, n)
			}
		}
		listed = true
	}
	cursors := func() map[string]string {
		out := make(map[string]string, len(follows))
		for n, f := range follows {
			if f.known && f.cursor != "" {
				out[n] = f.cursor
			}
		}
		return out
	}
	// readTenant delivers what one tenant has after its cursor, a bounded
	// number of pages, and returns how many events it sent and whether the
	// tenant has more.
	readTenant := func(name string, f *follow) (int, bool, error) {
		r, err := w.Tenants.Reader(ctx, name)
		if err != nil {
			return 0, false, nil // skipped; tried again on the next tick
		}
		if !f.known {
			c, err := r.EventCursor(ctx)
			if err != nil {
				return 0, false, nil
			}
			f.cursor, f.known = c, true
		}
		sent := 0
		for range pagesPerRound {
			page, err := r.ReadEvents(ctx, f.cursor, w.Types, b.cfg.PageSize)
			switch {
			case ctx.Err() != nil:
				return sent, false, ended(ctx)
			case errors.Is(err, tenant.ErrCursorExpired):
				return sent, false, fault.Wrap(fault.KindOf(err), err, fault.Message(err)+" (tenant "+name+")")
			case err != nil:
				return sent, false, nil // skipped; tried again on the next tick
			}
			for i := range page.Events {
				if err := send(ctx, Message{Tenant: name, Event: &page.Events[i]}); err != nil {
					return sent, false, sendFailed(ctx, err)
				}
				sent++
			}
			f.cursor = page.Cursor
			if len(page.Events) < b.cfg.PageSize {
				return sent, false, nil
			}
		}
		return sent, true, nil
	}

	// The stream opens with a keep-alive that holds the cursor of every
	// tenant it can read now, those starting from now included.
	list()
	for n, f := range follows {
		if f.fromNow {
			if r, err := w.Tenants.Reader(ctx, n); err == nil {
				if c, err := r.EventCursor(ctx); err == nil {
					f.cursor, f.known = c, true
				}
			}
		}
	}
	if ctx.Err() != nil {
		return ended(ctx)
	}
	if err := send(ctx, Message{KeepAlive: &KeepAlive{Time: b.cfg.Clock.Now(), TenantCursors: cursors()}}); err != nil {
		return sendFailed(ctx, err)
	}
	lastSent := b.cfg.Clock.Now()
	for {
		me.mu.Lock()
		rescan, dirty := me.rescan, me.dirty
		me.rescan, me.dirty = false, map[string]struct{}{}
		me.mu.Unlock()
		if rescan {
			list()
		} else {
			for n := range dirty {
				if _, ok := follows[n]; !ok {
					// A tenant woken that the stream does not follow may be
					// new: list again now rather than at the next tick.
					list()
					rescan = true
					break
				}
			}
		}
		names := make([]string, 0, len(follows))
		for n := range follows {
			if _, woken := dirty[n]; rescan || woken {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		var again []string
		for _, n := range names {
			sent, more, err := readTenant(n, follows[n])
			if err != nil {
				return err
			}
			if sent > 0 {
				lastSent = b.cfg.Clock.Now()
			}
			if more {
				again = append(again, n)
			}
		}
		if now := b.cfg.Clock.Now(); now.Sub(lastSent) >= b.cfg.KeepAlive {
			if err := send(ctx, Message{KeepAlive: &KeepAlive{Time: now, TenantCursors: cursors()}}); err != nil {
				return sendFailed(ctx, err)
			}
			lastSent = now
		}
		if len(again) > 0 {
			me.mu.Lock()
			for _, n := range again {
				me.dirty[n] = struct{}{}
			}
			me.mu.Unlock()
			continue
		}
		select {
		case <-me.wake:
		case <-ctx.Done():
			return ended(ctx)
		}
	}
}

// systemClock is the system's clock.
type systemClock struct{}

// Now implements Clock.
func (systemClock) Now() time.Time { return time.Now() }

// NewTicker implements Clock.
func (systemClock) NewTicker(d time.Duration) Ticker { return systemTicker{time.NewTicker(d)} }

// systemTicker is a time.Ticker as a Ticker.
type systemTicker struct{ t *time.Ticker }

// C implements Ticker.
func (s systemTicker) C() <-chan time.Time { return s.t.C }

// Stop implements Ticker.
func (s systemTicker) Stop() { s.t.Stop() }
