package sqlkit

import (
	"context"

	"github.com/amrshadid/coria/internal/core/reception"
	"github.com/amrshadid/coria/internal/tenant"
)

// Forward makes, in the transaction of a reception's step that reached
// done, one job per sender its receiver forwards to (tenant.Sending), and
// returns their events, which the driver writes with the step's. A sender
// removed meanwhile is skipped; one forwarded to already is not made twice
// (tenant.ForwardKey). A reception not done, or one that archived nothing
// (every file a duplicate or quarantined), makes nothing: its job would
// send nothing.
func (k Sending) Forward(ctx context.Context, tx SendingTx, r tenant.Reception) ([]tenant.EventRecord, error) {
	if r.State != reception.Done {
		return nil, nil
	}
	if archived, err := exists(ctx, tx, receptionArchivedSQL, r.ID); err != nil || !archived {
		return nil, err
	}
	var config string
	found, err := tx.QueryRow(ctx, `SELECT config FROM receivers WHERE name = ?`, []any{r.Receiver}, &config)
	if err != nil || !found {
		return nil, err
	}
	var c dimseConfig
	if err := FromJSON(config, &c); err != nil {
		return nil, err
	}
	var events []tenant.EventRecord
	for _, sender := range c.ForwardTo {
		has, err := k.lockSender(ctx, tx, sender)
		if err != nil {
			return nil, err
		}
		if !has {
			continue
		}
		_, e, err := k.insertJob(ctx, tx, newJob{id: tenant.ForwardJobID(r.ID, sender), sender: sender,
			target: tenant.JobTarget{SubjectLabel: r.SubjectLabel, SessionLabel: r.SessionLabel}, receptionID: r.ID,
			receiver: r.Receiver, uniqueKey: tenant.ForwardKey(r.ID, sender), created: r.StepTime, bound: r.StepTime,
			retryListed: true})
		if err != nil {
			return nil, err
		}
		if e != nil {
			events = append(events, *e)
		}
	}
	return events, nil
}
