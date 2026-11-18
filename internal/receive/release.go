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

// The operations below are what the API's handlers call to release, reject,
// retry and discard. Each is the store's operation, which records the
// decision and its reason with its audit entry, with what the pipeline does
// around it to the bytes in the incoming area and to the work queue. The
// store always records first: no byte is removed before the reason is
// committed.

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
func (p *Pipeline) Release(ctx context.Context, id, receiver string, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	q, err := p.store.QuarantinedFile(ctx, id)
	if err != nil {
		return tenant.QuarantinedFile{}, err
	}
	if q.State != tenant.QuarantineHeld {
		return p.store.ReleaseQuarantined(ctx, id, tenant.ReleaseAsIs, receiver, newID(), p.now(), audit, guard)
	}
	if q.ContentKept {
		if err := p.area.Unrecord(q.Incoming); err != nil {
			return tenant.QuarantinedFile{}, err
		}
	}
	out, err := p.store.ReleaseQuarantined(ctx, id, tenant.ReleaseAsIs, receiver, newID(), p.now(), audit, guard)
	if err != nil {
		if q.ContentKept {
			_ = p.area.Commit(q.Incoming)
		}
		return tenant.QuarantinedFile{}, err
	}
	if out.ReleaseReception != "" {
		if err := p.area.Commit(q.Incoming); err != nil {
			p.log.Warn("a released file keeps its landed name until a restart", "reason", safe(err))
		}
		r, _, err := p.store.Reception(ctx, out.ReleaseReception)
		if err != nil {
			return out, err
		}
		p.wake()
		return out, p.ensureNext(ctx, r)
	}
	p.wake()
	if q.ContentKept {
		f, err := p.landedFile(incoming.Entry{Receiver: q.Receiver, Path: q.Incoming, ID: pathID(q.Incoming)})
		if err != nil {
			return out, err
		}
		via := out.ReleaseReceiver
		if via == "" {
			via = q.Receiver
		}
		p.intake(ctx, landed{file: f, receiver: via, caller: q.Caller, releasedFrom: q.ID})
	}
	return out, nil
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
		p.at("reject.before-remove")
		if err := p.area.Remove(before.Incoming); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Retry makes a new reception of a failed one's files
// (tenant.Pipeline.RetryReception) and queues its first unit. A crash
// between the two leaves a reception past receiving with no unit, which
// Start's repair queues.
func (p *Pipeline) Retry(ctx context.Context, id, receiver string, audit master.AuditEntry, guard tenant.Guard) (failed, retry tenant.Reception, err error) {
	failed, retry, err = p.store.RetryReception(ctx, id, newID(), receiver, p.now(), audit, guard)
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
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		files, next, err := p.store.DiscardedFiles(ctx, id, page)
		if err != nil {
			return r, err
		}
		for _, f := range files {
			p.at("discard.before-remove")
			if err := p.area.Remove(f.Incoming); err != nil && fault.KindOf(err) != fault.Invalid {
				return r, err
			}
		}
		if next == "" {
			return r, nil
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
