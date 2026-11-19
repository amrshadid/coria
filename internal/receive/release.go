package receive

import (
	"context"
	"crypto/sha256"
	"io"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/receive/incoming"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// The operations below release, reject, retry and discard. Each is the
// store's operation, which records the decision and its reason with its
// audit entry, with what the pipeline does around it to the bytes in the
// incoming area and to the work queue. The store always records first: no
// byte is removed before the reason is committed.
//
// The API releases and retries through the tenant's running pipeline, so
// that the bytes are given their landed name before the store records a
// release and a retry's first unit is queued at once. It records a
// rejection or a discard through the store itself and then hands the
// removal of the bytes here (RemoveRejected, RemoveDiscarded); a removal
// that does not happen is done by Recover at the next start, which finds
// the decision recorded. A pipeline that stops between a step and the next
// loses nothing either: a released file that joined nothing is landed, and
// Recover checks it again as a release of its record; a reception with no
// unit gets one from the repair.

// Release releases a held file as it is (tenant.Pipeline.ReleaseQuarantined)
// and puts its bytes back in line.
//
// Order of effects:
//
//  1. Give the file's bytes their landed name (incoming.Area.Unrecord) and
//     make that durable. While the record is held, a restart that finds
//     them so gives them back their recorded name, since the record names
//     them.
//  2. Release in the store. A file with its UIDs and checksum joins a
//     reception of its study as a received file (the store's rule); one
//     without them joins nothing, and its record no longer keeps its bytes.
//  3. A file that joined a reception gets its recorded name back, and its
//     reception its quiet unit. One that joined nothing is checked again by
//     the receiver's rules now, as a file that just arrived, and recorded or
//     held again with ReleasedFrom naming this record. A crash between 2
//     and 3 leaves its bytes under the landed name with no record keeping
//     them, which a restart checks the same way.
func (p *Pipeline) Release(ctx context.Context, rel tenant.Release, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	if rel.ReceptionID == "" {
		rel.ReceptionID = newID()
	}
	if rel.Now.IsZero() {
		rel.Now = p.now()
	}
	q, err := p.store.QuarantinedFile(ctx, rel.ID)
	if err != nil {
		return tenant.QuarantinedFile{}, err
	}
	if q.State != tenant.QuarantineHeld {
		return p.store.ReleaseFile(ctx, rel, audit, guard)
	}
	if q.ContentKept {
		if err := p.area.Unrecord(q.Incoming); err != nil {
			return tenant.QuarantinedFile{}, err
		}
	}
	p.at("release.before-store")
	out, err := p.store.ReleaseFile(ctx, rel, audit, guard)
	if err != nil {
		if q.ContentKept {
			_ = p.area.Commit(q.Incoming)
		}
		return tenant.QuarantinedFile{}, err
	}
	p.at("release.after-store")
	return out, p.afterRelease(ctx, q, out)
}

// afterRelease does what follows a release the store recorded: before is
// the record as it was held, after as the release left it. A file that
// joined a reception gets its recorded name and its reception the unit of
// its state; one that joined nothing is given its landed name and checked
// again by the receiver's rules, as a file that just arrived, recorded or
// held again with ReleasedFrom naming the released record. A record that
// kept no bytes, or was not held, asks nothing more.
func (p *Pipeline) afterRelease(ctx context.Context, before, after tenant.QuarantinedFile) error {
	if before.State != tenant.QuarantineHeld || !before.ContentKept {
		return nil
	}
	p.wake()
	if after.ReleaseReception != "" {
		if err := p.area.Commit(before.Incoming); err != nil {
			p.log.Warn("a released file keeps its landed name until a restart", "reason", safe(err))
		}
		r, _, err := p.store.Reception(ctx, after.ReleaseReception)
		if err != nil {
			return err
		}
		return p.ensureNext(ctx, r)
	}
	if !p.running.Load() {
		return nil
	}
	f, err := p.landedFile(incoming.Entry{Receiver: before.Receiver, Path: before.Incoming, ID: pathID(before.Incoming)})
	if err != nil {
		return err
	}
	via := after.ReleaseReceiver
	if via == "" {
		via = before.Receiver
	}
	p.intake(ctx, landed{file: f, receiver: via, caller: before.Caller, releasedFrom: before.ID})
	return nil
}

// Reject rejects a held file (tenant.Pipeline.RejectQuarantined), then
// removes its bytes. A crash between the two leaves them under their
// recorded name with no record keeping them, which a restart removes.
func (p *Pipeline) Reject(ctx context.Context, id, reason string, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	before, err := p.store.QuarantinedFile(ctx, id)
	if err != nil {
		return tenant.QuarantinedFile{}, err
	}
	out, err := p.store.RejectQuarantined(ctx, id, reason, p.now(), audit, guard)
	if err != nil {
		return tenant.QuarantinedFile{}, err
	}
	p.wake()
	if before.ContentKept && before.State == tenant.QuarantineHeld {
		return out, p.RemoveRejected(before.Incoming)
	}
	return out, nil
}

// RemoveRejected removes the bytes of a held file at path in the incoming
// area, after the store recorded its rejection.
func (p *Pipeline) RemoveRejected(path string) error {
	p.at("reject.before-remove")
	return p.area.Remove(path)
}

// Retry makes a new reception of a failed one's files
// (tenant.Pipeline.RetryReception) and queues its first unit. A crash
// between the two leaves a reception past receiving with no unit, which
// Start's repair queues.
// newReceptionID, when not empty, is the id of the new reception.
func (p *Pipeline) Retry(ctx context.Context, id, newReceptionID, receiver string, audit master.AuditEntry, guard tenant.Guard) (failed, retry tenant.Reception, err error) {
	if newReceptionID == "" {
		newReceptionID = newID()
	}
	failed, retry, err = p.store.RetryReception(ctx, id, newReceptionID, receiver, p.now(), audit, guard)
	if err != nil {
		return failed, retry, err
	}
	p.wake()
	return failed, retry, p.ensureNext(ctx, retry)
}

// Discard records that a failed reception's files are let go
// (tenant.Pipeline.DiscardReception), then removes the bytes the store
// lists. A crash midway leaves recorded names no row keeps, which a restart
// removes.
func (p *Pipeline) Discard(ctx context.Context, id, reason string, audit master.AuditEntry, guard tenant.Guard) (tenant.Reception, error) {
	r, err := p.store.DiscardReception(ctx, id, reason, p.now(), audit, guard)
	if err != nil {
		return r, err
	}
	return r, p.RemoveDiscarded(ctx, id)
}

// RemoveDiscarded removes the bytes the store lists for a reception whose
// discard it recorded (tenant.Pipeline.DiscardedFiles).
func (p *Pipeline) RemoveDiscarded(ctx context.Context, receptionID string) error {
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		files, next, err := p.store.DiscardedFiles(ctx, receptionID, page)
		if err != nil {
			return err
		}
		for _, f := range files {
			p.at("discard.before-remove")
			if err := p.area.Remove(f.Incoming); err != nil && fault.KindOf(err) != fault.Invalid {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.Cursor = next
	}
}

// pathID returns the id of an incoming path, empty for one not of the form.
func pathID(path string) string {
	_, id, err := incoming.ParsePath(path)
	if err != nil {
		return ""
	}
	return id
}

// sumReader returns the SHA-256 of what r yields.
func sumReader(r io.Reader) (tenant.SHA256, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return tenant.SHA256{}, fault.Wrap(fault.Unavailable, err, "an incoming file cannot be read")
	}
	var out tenant.SHA256
	copy(out[:], h.Sum(nil))
	return out, nil
}
