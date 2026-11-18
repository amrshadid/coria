package access

import (
	"context"
	"strconv"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// auditTimeout bounds one append of the audit writer, so a master that
// hangs holds the writer this long at most, and the queue drops instead of
// growing.
const auditTimeout = 10 * time.Second

// dropNoticeEvery is how often at most the audit writer writes an entry
// saying entries were dropped. The first drop after this long without a
// notice is reported at the next batch; drops in between are counted into
// the next notice, so every drop is reported in the log, a minute late at
// most, and a flood of drops costs one entry a minute.
const dropNoticeEvery = time.Minute

// The method and the detail of the entry that reports dropped entries.
const (
	dropNoticeMethod = "access.AuditWriter"
	detailDropped    = "dropped"
)

// AuditStats counts what became of the audit entries of refusals and reads
// handed to the audit writer since New. The entries of changes are not here:
// each is written in its change's transaction.
type AuditStats struct {
	// Written is how many entries the master took.
	Written uint64
	// Dropped is how many entries were never tried: the queue was full,
	// they came after Close began, or Close's deadline passed before they
	// were written.
	Dropped uint64
	// Failed is how many entries the master refused or failed, one by one,
	// or that were not valid entries.
	Failed uint64
}

// AuditStats returns the counts of the audit writer, so a server can report
// entries it dropped or could not write.
func (a *Access) AuditStats() AuditStats {
	return AuditStats{Written: a.auditWritten.Load(), Dropped: a.auditDropped.Load(), Failed: a.auditFailed.Load()}
}

// Record hands the audit entry of an allowed read to the audit writer, with
// the read's outcome and reason. A change does not use it: it writes
// d.Entry with the change, in one transaction. Record never blocks and never
// fails; an entry it cannot queue is dropped and counted (AuditStats).
func (a *Access) Record(d Decision, outcome master.Outcome, reason string) {
	a.enqueueAudit(d.Entry(outcome, reason))
}

// FlushAudit waits until the audit writer has written, or failed, every
// entry handed to it before the call, or until ctx is done. A server calls
// it before a backup of the master; a test calls it to read what a refusal
// wrote. After Close it returns at once.
func (a *Access) FlushAudit(ctx context.Context) error {
	reply := make(chan struct{})
	select {
	case a.auditFlush <- reply:
	case <-a.auditDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-reply:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// enqueueAudit hands an entry to the audit writer without waiting. An entry
// the master would refuse is counted as failed here, so it cannot fail the
// batch it would have joined. When the queue is full, or Close has begun,
// the entry is dropped and counted.
func (a *Access) enqueueAudit(e master.AuditEntry) {
	if err := e.Validate(); err != nil {
		a.auditFailed.Add(1)
		a.log.Error("access: an audit entry is not valid and was not written", "audit_id", e.ID, "method", e.Method, "error", err)
		return
	}
	if a.closing.Load() {
		a.auditDropped.Add(1)
		return
	}
	select {
	case a.audit <- e:
	default:
		a.auditDropped.Add(1)
	}
}

// auditWriter is the audit writer: it gathers queued entries into a batch
// and appends the batch when it is full, when the interval ticks, when
// FlushAudit asks, and when Close stops it, after taking what is queued.
func (a *Access) auditWriter() {
	defer close(a.auditDone)
	w := &batchWriter{a: a, batch: make([]master.AuditEntry, 0, a.auditBatch)}
	tick := time.NewTicker(a.auditInterval)
	defer tick.Stop()
	for {
		select {
		case e := <-a.audit:
			w.add(e)
		case <-tick.C:
			w.flush()
		case reply := <-a.auditFlush:
			w.drain()
			w.flush()
			close(reply)
		case <-a.stop:
			w.drain()
			w.flush()
			return
		}
	}
}

// batchWriter is the audit writer's state, used by its goroutine only.
type batchWriter struct {
	a          *Access
	batch      []master.AuditEntry
	noticed    uint64
	lastNotice time.Time
}

// add puts e in the batch, and writes the batch when it is full.
func (w *batchWriter) add(e master.AuditEntry) {
	w.batch = append(w.batch, e)
	if len(w.batch) >= w.a.auditBatch {
		w.flush()
	}
}

// drain takes every entry queued now, writing each batch that fills.
func (w *batchWriter) drain() {
	for {
		select {
		case e := <-w.a.audit:
			w.add(e)
		default:
			return
		}
	}
}

// flush writes the notice of dropped entries when one is due, then the
// batch.
func (w *batchWriter) flush() {
	if dropped := w.a.auditDropped.Load(); dropped > w.noticed {
		now := w.a.clock()
		if w.lastNotice.IsZero() || now.Sub(w.lastNotice) >= dropNoticeEvery {
			if notice, ok := w.notice(dropped-w.noticed, now); ok {
				w.write([]master.AuditEntry{notice})
			}
			w.noticed, w.lastNotice = dropped, now
		}
	}
	if len(w.batch) == 0 {
		return
	}
	w.write(w.batch)
	clear(w.batch)
	w.batch = w.batch[:0]
}

// notice returns the entry that says n entries were dropped, or false when
// the id source gives an id the master would refuse.
func (w *batchWriter) notice(n uint64, now time.Time) (master.AuditEntry, bool) {
	id := w.a.newID()
	if master.ValidateID("audit entry id", id) != nil {
		return master.AuditEntry{}, false
	}
	count := strconv.FormatUint(n, 10)
	return master.AuditEntry{
		ID: id, Time: now, Method: dropNoticeMethod, Outcome: master.OutcomeFailed,
		Reason:  count + " audit entries of refusals or reads were dropped: the audit queue was full or the server was closing",
		Details: map[string]string{detailDropped: count},
	}, true
}

// write appends entries in one call. When the call fails, each entry is
// appended alone, once, so one entry the master refuses does not lose the
// others (the port appends all or none). An entry refused alone is counted
// as failed and not tried again: that covers master.ErrDuplicateAuditID, an
// id already taken, which would be refused again whatever the retry. Once Close's deadline has canceled the
// workers, what is left is dropped and counted without a call.
func (w *batchWriter) write(entries []master.AuditEntry) {
	a := w.a
	if a.workCtx.Err() != nil {
		a.auditDropped.Add(uint64(len(entries)))
		return
	}
	err := a.appendWithin(entries...)
	if err == nil {
		a.auditWritten.Add(uint64(len(entries)))
		return
	}
	if len(entries) == 1 {
		a.auditFailedEntry(entries[0], err)
		return
	}
	for i, e := range entries {
		if a.workCtx.Err() != nil {
			a.auditDropped.Add(uint64(len(entries[i:])))
			return
		}
		if err := a.appendWithin(e); err != nil {
			a.auditFailedEntry(e, err)
			continue
		}
		a.auditWritten.Add(1)
	}
}

// appendWithin appends entries, bounded by auditTimeout.
func (a *Access) appendWithin(entries ...master.AuditEntry) error {
	ctx, cancel := context.WithTimeout(a.workCtx, auditTimeout)
	defer cancel()
	return a.store.AppendAudit(ctx, entries...)
}

// auditFailedEntry counts and logs an entry the master did not take. The
// line names the entry and its cause, which carry no secret.
func (a *Access) auditFailedEntry(e master.AuditEntry, err error) {
	a.auditFailed.Add(1)
	a.log.Error("access: an audit entry could not be written", "audit_id", e.ID, "method", e.Method,
		"outcome", string(e.Outcome), "reason", e.Reason, "kind", fault.KindOf(err).String(), "error", err)
}
