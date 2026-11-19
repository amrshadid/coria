package supervise

import (
	"context"

	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/receive"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// Decisions is what a tenant's running pipeline does for a person's
// decision on its quarantine and its failed receptions: the release and the
// retry run through it whole, so the bytes in the incoming area and the work
// queue follow the record in the order that survives a crash; after a
// rejection or a discard the store recorded, it removes the bytes. The
// reception pipeline (receive.Pipeline) is one.
type Decisions interface {
	// Release releases a held file (receive.Pipeline.Release).
	Release(ctx context.Context, rel tenant.Release, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error)
	// Retry makes a new reception of a failed one's files and queues its
	// first unit (receive.Pipeline.Retry).
	Retry(ctx context.Context, id, newReceptionID, receiver string, audit master.AuditEntry, guard tenant.Guard) (failed, retry tenant.Reception, err error)
	// RemoveRejected removes a rejected file's bytes at path.
	RemoveRejected(path string) error
	// RemoveDiscarded removes the bytes of a discarded reception's files.
	RemoveDiscarded(ctx context.Context, receptionID string) error
}

// The reception pipeline takes decisions.
var _ Decisions = (*receive.Pipeline)(nil)

// Decisions returns the running pipeline of the tenant named name, for a
// decision, and whether one runs. It reads memory only: the pipeline the
// tenant's worker published, which it withdraws before it stops one and
// replaces as soon as the next has started. A removal handed over in the
// moment a pipeline starts is refused, and done by the start after
// (receive.Recover).
func (s *Supervisor) Decisions(name string) (Decisions, bool) {
	s.mu.Lock()
	t := s.tenants[name]
	s.mu.Unlock()
	if t == nil {
		return nil, false
	}
	v := t.view.Load()
	if v == nil || v.pipeline == nil {
		return nil, false
	}
	d, ok := v.pipeline.(Decisions)
	return d, ok
}
