package tenant

import (
	"context"
	"errors"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
)

// Events is a tenant's event feed: one row for each thing that happened in
// the tenant that a system outside wants to follow, written in the
// transaction of the change it reports and read back in the order of the
// commits that wrote them. A driver that keeps a feed implements it beside
// Pipeline and proves it with the contract suite's event groups
// (tenanttest.RunEvents); once every driver does, it becomes part of Store.
//
// # Where events are written
//
// An event is written in the same transaction as its change, or not at all:
// a change that is undone takes its event with it, and an event is never
// written for a change that did not commit. A driver that implements Events
// writes these itself, inside the operations that make the change, with
// the operation's own time:
//
//   - OpenReception, when it opens a reception, and ReleaseQuarantined, when
//     its release opens one: the reception reaching receiving;
//   - RetryReception: the new reception reaching complete, with RetryOf;
//   - FailWork and ClaimWork, when a unit held as failed fails its
//     reception: the reception reaching failed;
//   - HoldFiles: one event per new record, with the quarantine's totals
//     after it; the time is the record's quarantine time, since HoldFiles
//     takes no other;
//   - ReleaseQuarantined and RejectQuarantined: one event per decision, with
//     the totals after it. A file already decided changes nothing and makes
//     no event.
//
// A reception's other steps are written by the step's hook: StepEventHook
// returns the hook that writes the step's event, which the caller passes to
// StepReception (alone or with its own hooks, StepHooks). A driver that
// implements Events runs a step's hook with a context that carries the
// transaction's event writer (WithEventWriter); without one, the hook fails
// and the step is undone, so a step is never taken silently without its
// event. A batch run again runs the hook again, in the new transaction, and
// the event of the run that was undone is undone with it: a step is
// recorded once however many times its batch runs.
//
// DiscardReception writes no event: the contract has no event for a discard
// (the reception stays failed), so a client that follows a status learns of
// the incoming bytes it freed only from a new status.
//
// What the server reports that is not a row of the pipeline (a failed
// association, a receiver's health, a storage's space) it writes with
// AppendEvents, which refuses the pipeline's own types: those are written
// with their change, never apart from it.
//
// # Commit order
//
// ReadEvents answers events in an order such that a reader that resumes
// from the cursor of the last event it saw never misses one: every event
// after that cursor, once committed, is answered by a later read from it.
// An order of ids alone does not give that where several transactions write
// at once, since an id is taken when the row is written, not when its
// transaction commits: transaction A takes id 10, B takes 11, B commits, a
// reader answers 11 and moves its cursor past 10, then A commits and 10 is
// never answered.
//
// The SQLite driver has one writer, which runs one transaction at a time
// (sqlite.org/isolation.html), and takes ids that only grow (AUTOINCREMENT,
// sqlite.org/autoinc.html), so the order of ids is the order of commits and
// a read of the ids after a cursor is exact. A transaction undone and run
// again cannot break that: what it wrote, its ids included, is gone with it,
// no reader ever saw it, and the run again takes ids after every committed
// one.
//
// A driver with several writers must not let a reader pass an event whose
// transaction is still running. On Postgres: each event records its writer's
// transaction id (pg_current_xact_id(), of type xid8, which does not wrap
// around), and a read answers only events whose transaction id is below the
// oldest transaction still running when the read starts
// (pg_snapshot_xmin(pg_current_snapshot())), ordered by (transaction id, id),
// with the cursor the pair of the last one answered. Every transaction below
// that bound has committed or rolled back, so nothing can appear behind the
// cursor later (PostgreSQL documentation, "System Information Functions",
// postgresql.org/docs/current/functions-info.html). The price is stated: a
// long writing transaction holds every reader of the tenant's feed back
// until it ends, which is why every batch of the pipeline is bounded
// (MaxScanCommit). The suite's case LateCommit fails on a driver that reads
// by id alone.
//
// # Cursors and retention
//
// A cursor is opaque, bound to the tenant and to the feed, and resumes
// exactly: a read from it answers the events after it, none twice and none
// missed. A filtered read moves its cursor past the events its filter left
// out, so a filtered reader never reads them again, and every page carries
// the cursor to resume from, an empty one included. The type filter is not
// part of a cursor, so a reader may resume with another filter.
//
// Events are pruned by age, in bounded batches (PruneEvents). A cursor that
// points before the oldest event kept, so that events after it were pruned
// before it read them, is refused with ErrCursorExpired in the chain, never
// answered from a later point: a reader learns it missed events. The store
// knows by a mark of what was pruned, moved in the pruning's transaction.
type Events interface {
	// AppendEvents writes up to MaxEventBatch events the server reports,
	// all or none, and returns how many were new. Each has a key the
	// caller makes (master.ValidateID); an event whose key the feed holds
	// is skipped, so a batch written again after a failure writes nothing
	// twice while its events are kept. Only the server's types are
	// accepted (ServerEventType); a pipeline type is Invalid.
	AppendEvents(ctx context.Context, events []EventAppend) (int, error)
	// ReadEvents returns up to limit (1 to MaxEventPage) events after the
	// cursor, in commit order, of the types given (all types when none; at
	// most MaxEventTypes, each one EventTypes lists), with the cursor to
	// resume from. An empty after reads from the oldest event kept. A
	// cursor older than what is kept holds ErrCursorExpired; a cursor not
	// made by this tenant's feed is Invalid.
	ReadEvents(ctx context.Context, after string, types []EventType, limit int) (EventPage, error)
	// EventCursor returns the cursor after the newest event committed: a
	// read from it answers the events committed later, which is where a
	// reader that starts "from now" starts.
	EventCursor(ctx context.Context) (string, error)
	// StatusSnapshot reads, in one snapshot of the database, the cursor of
	// the feed and the counts a status reports: an event after the cursor
	// is a change the counts do not hold, and one at or before it is one
	// they do.
	StatusSnapshot(ctx context.Context) (StatusSnapshot, error)
	// PruneEvents removes up to limit (1 to MaxPruneBatch) of the oldest
	// events, in order, stopping at the first whose time is not before
	// before, and returns how many it removed and whether more are older
	// than before. It moves the feed's mark of what was pruned in the same
	// transaction, so a reader whose cursor it passed is answered
	// ErrCursorExpired on its next read, never a gap.
	PruneEvents(ctx context.Context, before time.Time, limit int) (removed int, more bool, err error)
}

// The bounds of the feed's operations.
const (
	// MaxEventBatch is the most events one AppendEvents writes.
	MaxEventBatch = 1000
	// MaxEventPage is the most events one ReadEvents answers.
	MaxEventPage = 1000
	// MaxEventTypes is the most types a read's filter names, the
	// contract's 16.
	MaxEventTypes = 16
	// MaxPruneBatch is the most events one PruneEvents removes, so a
	// pruning never holds the writer for long.
	MaxPruneBatch = 10000
	// DefaultEventRetention is how long events are kept when the setting
	// events.retention is not set, the contract's 90 days.
	DefaultEventRetention = 90 * 24 * time.Hour
)

// ErrCursorExpired is in the chain of a read whose cursor points before the
// oldest event kept: events after it were pruned, so the events after it can
// no longer all be answered. Its kind is Invalid, since sending the cursor
// again fails again; the API answers it as cursor expired, and the client
// starts over from a status.
var ErrCursorExpired = errors.New("the cursor points before the oldest event kept")

// CursorExpired returns the refusal of a read whose cursor is older than
// what the feed keeps.
func CursorExpired() error {
	return fault.Wrap(fault.Invalid, ErrCursorExpired,
		"the cursor points before the oldest event kept; read the status again and watch from its event cursor")
}

// EventAppend is one event the server reports, with the key that makes its
// append idempotent.
type EventAppend struct {
	// Key names the event among those the server appends, made by the
	// caller (master.ValidateID).
	Key string
	// Event is the event, made by NewEvent.
	Event EventRecord
}

// Event is one event as a read answers it.
type Event struct {
	// Cursor is its place in the feed: a read from it answers the events
	// after this one.
	Cursor string
	// Type is its type.
	Type EventType
	// Time is the time of the change it reports, as its writer gave it.
	Time time.Time
	// Payload is what happened, of the type Type names; nil for a type
	// this version does not know (written by a later one), which a reader
	// skips, taking its cursor.
	Payload EventPayload
}

// EventPage is a page of events and the cursor after it.
type EventPage struct {
	// Events are the events read, in commit order.
	Events []Event
	// Cursor is where the next read starts: after the last event answered,
	// or after the events the filter left out, whichever is later. It is
	// set whether or not the page has events.
	Cursor string
}

// StepCount is how many receptions are in one step.
type StepCount struct {
	// Step is the step.
	Step reception.State
	// Receptions counts the tenant's receptions in it.
	Receptions int64
}

// StatusSnapshot is what a status reads from the tenant's database, all of
// it in one snapshot.
type StatusSnapshot struct {
	// EventCursor is the feed's place in the snapshot: a read from it
	// answers exactly the events whose changes the counts do not hold.
	EventCursor string
	// Catalog are the catalog's running totals.
	Catalog CatalogCounts
	// QuarantinedFiles and QuarantinedBytes are the quarantine's totals:
	// files held, and the bytes kept for them.
	QuarantinedFiles, QuarantinedBytes int64
	// InProgress are the receptions that have not ended, one entry per
	// step that has any, in the order of reception.States.
	InProgress []StepCount
	// IncomingBytes are the bytes the tenant holds in the incoming area:
	// the received bytes of each reception that has not ended, or that
	// failed and was neither retried nor discarded, less what it
	// quarantined; and the quarantine's held bytes.
	IncomingBytes int64
}

// EventWriter writes an event in the transaction of the change that is
// running, so the two commit together or not at all.
type EventWriter interface {
	// WriteEvent writes the event, made by NewEvent, in the change's
	// transaction.
	WriteEvent(ctx context.Context, e EventRecord) error
}

// eventWriterKey is the context key of a transaction's EventWriter.
type eventWriterKey struct{}

// WithEventWriter returns ctx carrying w, the event writer of the
// transaction a hook runs in. A driver that implements Events runs a step's
// hook with it.
func WithEventWriter(ctx context.Context, w EventWriter) context.Context {
	return context.WithValue(ctx, eventWriterKey{}, w)
}

// EventWriterFrom returns the event writer ctx carries, if any.
func EventWriterFrom(ctx context.Context) (EventWriter, bool) {
	w, ok := ctx.Value(eventWriterKey{}).(EventWriter)
	return w, ok && w != nil
}

// StepEventHook returns the hook that writes a reception step's event in
// the step's transaction: the reception as it is after the step, with its
// counts and labels (ReceptionStepEvent). A store whose hook context carries
// no event writer fails it as Internal, which undoes the step: a step is
// never taken without its event when the caller asked for one.
func StepEventHook() StepHook {
	return func(ctx context.Context, _, after Reception) error {
		w, ok := EventWriterFrom(ctx)
		if !ok {
			return fault.New(fault.Internal, "this store cannot write a step's event in its transaction")
		}
		e, err := ReceptionStepEvent(after)
		if err != nil {
			return err
		}
		return w.WriteEvent(ctx, e)
	}
}

// StepHooks returns a hook that runs each hook in turn, stopping at the
// first error, which undoes the step. Nil hooks are skipped.
func StepHooks(hooks ...StepHook) StepHook {
	return func(ctx context.Context, before, after Reception) error {
		for _, h := range hooks {
			if h == nil {
				continue
			}
			if err := h(ctx, before, after); err != nil {
				return err
			}
		}
		return nil
	}
}
