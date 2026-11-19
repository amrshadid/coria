package receive

import (
	"context"
	"errors"

	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/receive/incoming"
	"github.com/amrshadid/coria/internal/tenant"
)

// The reasons an Outcome gives, fixed phrases safe to send and log.
const (
	reasonStopping    = "the archive is stopping"
	reasonOverLimit   = "the tenant holds as many incoming bytes as it may"
	reasonLowDisk     = "the incoming area's disk is below its reserve"
	reasonNoReceiver  = "the receiver does not exist or does not take files"
	reasonTooLarge    = "the file is larger than the receiver's largest file"
	reasonNotKept     = "the file could not be kept"
	reasonBadArrival  = "the file's sender is not described in a form the archive keeps"
	reasonUnavailable = "the tenant's database cannot be reached"
)

// Accept takes one file from a source. It answers Received only once the
// file is whole and synced in the incoming area (incoming.Area.Land); the
// file's row is written afterwards, in a batch, and a crash between the two
// leaves the file landed, which Recover records. It answers Refused, keeping
// nothing, while the pipeline is stopping, the tenant is over its incoming
// limit, the disk is below its reserve, the database cannot be reached, or
// the file could not be written; and Failed for a receiver that does not
// take files or a file too large for it, which is held in quarantine without
// its bytes so a person sees that it came.
func (p *Pipeline) Accept(ctx context.Context, a Arrival) Outcome {
	p.mu.Lock()
	if p.stopping || !p.running.Load() {
		p.mu.Unlock()
		p.stats.Refused.Add(1)
		return Outcome{Status: Refused, Reason: reasonStopping}
	}
	p.accepting.Add(1)
	p.mu.Unlock()
	defer p.accepting.Done()

	if err := validArrival(a); err != nil {
		return Outcome{Status: Failed, Reason: reasonBadArrival}
	}
	rcv, err := p.receivers.get(ctx, a.Receiver)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return Outcome{Status: Failed, Reason: reasonNoReceiver}
	case err != nil:
		p.stats.Refused.Add(1)
		return Outcome{Status: Refused, Reason: reasonUnavailable}
	case !rcv.Enabled:
		return Outcome{Status: Failed, Reason: reasonNoReceiver}
	}
	for _, c := range []struct {
		check  Check
		reason string
	}{{p.cfg.OverLimit, reasonOverLimit}, {p.cfg.LowDisk, reasonLowDisk}} {
		if c.check == nil {
			continue
		}
		refuse, err := c.check(ctx)
		if err != nil || refuse {
			p.stats.Refused.Add(1)
			return Outcome{Status: Refused, Reason: c.reason}
		}
	}
	f, err := p.area.Land(ctx, a.Receiver, a.Body, rcv.MaxFileBytes)
	var tl *incoming.TooLargeError
	if errors.As(err, &tl) {
		p.holdTooLarge(ctx, a, tl)
		return Outcome{Status: Failed, Reason: reasonTooLarge}
	}
	if err != nil {
		p.stats.Refused.Add(1)
		return Outcome{Status: Refused, Reason: reasonNotKept}
	}
	p.stats.Landed.Add(1)
	p.meter.received(p.cfg.Clock.Now(), f.Size)
	p.at("accept.landed")
	l := landed{file: f, receiver: a.Receiver, caller: a.Caller, association: a.Association, told: a}
	l.told.Body = nil
	p.intake(ctx, l)
	return Outcome{Status: Received}
}

// intake checks a landed file and hands it to the batcher; a file whose check
// could not decide (its header unreadable for now, the database down) waits
// on the retry list. Either way the file is durable and owed.
func (p *Pipeline) intake(ctx context.Context, l landed) {
	d, err := p.check(ctx, l)
	if err != nil {
		p.log.Warn("a received file could not be checked yet; it is kept and checked again", "reason", safe(err))
		p.batch.later(l)
		return
	}
	p.batch.submit(ctx, batchItem{l: l, d: d})
}

// holdTooLarge records a file refused for its size, without its bytes,
// which were never kept. Its record is written at once rather than in a
// batch: nothing on disk would bring it back after a crash.
func (p *Pipeline) holdTooLarge(ctx context.Context, a Arrival, tl *incoming.TooLargeError) {
	q := tenant.QuarantinedFile{
		ID: newID(), Reason: tenant.QuarantineTooLarge, Receiver: a.Receiver, Caller: a.Caller,
		Details: map[string]string{"limit": itoa(tl.Limit)}, Size: tl.Read, Quarantined: p.now(),
	}
	for _, s := range []struct {
		dst *string
		v   string
	}{
		{&q.SOPClassUID, a.SOPClassUID}, {&q.SOPInstanceUID, a.SOPInstanceUID}, {&q.TransferSyntaxUID, a.TransferSyntaxUID},
	} {
		if validUID(s.v) {
			*s.dst = s.v
		}
	}
	if _, err := p.store.HoldFiles(ctx, []tenant.QuarantinedFile{q}); err != nil {
		p.log.Warn("a file too large could not be recorded in quarantine", "reason", safe(err))
		return
	}
	p.stats.Held.Add(1)
	p.wake()
}

// validArrival checks what a source said about a file before anything of it
// is kept, so no row is refused later for its form.
func validArrival(a Arrival) error {
	if a.Body == nil {
		return fault.New(fault.Invalid, "a file has no bytes")
	}
	if err := tenant.ValidateName("receiver", a.Receiver); err != nil {
		return err
	}
	if err := a.Caller.Validate(); err != nil {
		return err
	}
	if a.Association != "" {
		return master.ValidateID("association id", a.Association)
	}
	return nil
}
