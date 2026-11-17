package access

import (
	"context"
	"errors"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// markTimeout bounds one last-use write, so a master that hangs holds the
// worker for this long at most and the queue drops instead of growing.
const markTimeout = 5 * time.Second

// markEvery is how stale a key's last-use time must be before a check asks
// for a new one: the store's own throttle (master.Store.MarkKeyUsed). A
// check that skips the call spares the master's single write connection,
// which the call takes even when its throttle writes nothing.
const markEvery = time.Minute

// mark is one last-use write waiting for the worker.
type mark struct {
	keyID string
	at    time.Time
}

// MarkStats counts what became of the last-use marks since New. A check
// whose key was marked within the minute asks for none, and is not counted.
type MarkStats struct {
	// Written is how many marks the store took, whether or not its own
	// throttle let them write.
	Written uint64
	// Dropped is how many marks found the queue full, or came after Close
	// began, and were never tried.
	Dropped uint64
	// Failed is how many marks the store refused or failed.
	Failed uint64
}

// MarkStats returns the counts of last-use marks, so a server can report a
// worker that falls behind.
func (a *Access) MarkStats() MarkStats {
	return MarkStats{Written: a.markWritten.Load(), Dropped: a.markDropped.Load(), Failed: a.markFailed.Load()}
}

// needsMark reports whether a key last used at lastUsed should be marked
// used at now: never marked, or marked a minute or more before.
func needsMark(lastUsed, now time.Time) bool {
	return lastUsed.IsZero() || now.Sub(lastUsed) >= markEvery
}

// queueMark hands a last-use mark to the worker without waiting: when the
// queue is full, or Close has begun, the mark is dropped and counted. The
// mark is to within a minute anyway, and the next request with the key asks
// again, so a drop delays the mark and never the request.
func (a *Access) queueMark(k master.Key, at time.Time) {
	if !needsMark(k.LastUsed, at) {
		return
	}
	if a.closing.Load() {
		a.markDropped.Add(1)
		return
	}
	select {
	case a.marks <- mark{keyID: k.ID, at: at}:
	default:
		a.markDropped.Add(1)
	}
}

// markWorker writes queued marks one at a time until Close, then writes
// what is still queued until Close's deadline cancels workCtx. The channel
// is never closed, so a check racing Close cannot send on a closed channel.
func (a *Access) markWorker() {
	defer close(a.marksDone)
	for {
		select {
		case m := <-a.marks:
			a.writeMark(m)
		case <-a.stop:
			for {
				select {
				case m := <-a.marks:
					a.writeMark(m)
				default:
					return
				}
			}
		}
	}
}

// writeMark writes one mark, bounded by markTimeout. A failure is counted
// and logged with the key's id and the error's kind, never returned: no
// request waits for it. Once Close has canceled the workers, what is left
// is dropped without a call.
func (a *Access) writeMark(m mark) {
	if a.workCtx.Err() != nil {
		a.markDropped.Add(1)
		return
	}
	ctx, cancel := context.WithTimeout(a.workCtx, markTimeout)
	defer cancel()
	if _, err := a.store.MarkKeyUsed(ctx, m.keyID, m.at); err != nil {
		a.markFailed.Add(1)
		if !errors.Is(err, context.Canceled) {
			a.log.Warn("access: a key's last use was not recorded", "key_id", m.keyID, "kind", fault.KindOf(err).String(), "error", err)
		}
		return
	}
	a.markWritten.Add(1)
}
