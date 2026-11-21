package sqlkit

import (
	"context"
	"time"

	"github.com/amrshadid/coria/internal/core/importpath"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/tenant"
)

// The work of an import job's units (tenant.ImportWork). Every write checks
// its fence first, then locks the unit's row, the import job's, and last
// the job's, the order every transaction of an import keeps.

// fencedImportUnit checks the fence and reads the unit, locked.
func (k Sending) fencedImportUnit(ctx context.Context, tx SendingTx, fence tenant.WorkFence) (importUnitRow, error) {
	if err := tx.CheckFence(ctx, fence); err != nil {
		return importUnitRow{}, err
	}
	u, found, err := k.importUnitOf(ctx, tx, fence.ID, k.D.lockOf("u"))
	if err != nil {
		return importUnitRow{}, err
	}
	if !found {
		return importUnitRow{}, errNotImportUnit()
	}
	return u, nil
}

// StartImportUnit implements tenant.ImportWork.
func (k Sending) StartImportUnit(ctx context.Context, fence tenant.WorkFence, now time.Time) (tenant.ImportUnit, tenant.Job, error) {
	if err := fence.Validate(); err != nil {
		return tenant.ImportUnit{}, tenant.Job{}, err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.ImportUnit{}, tenant.Job{}, err
	}
	var unit tenant.ImportUnit
	var job tenant.Job
	err := k.Host.Write(ctx, "the import unit cannot be started", func(tx SendingTx) error {
		u, err := k.fencedImportUnit(ctx, tx, fence)
		if err != nil {
			return err
		}
		r, err := jobInTx(ctx, tx, u.jobID, k.D.Lock)
		if err != nil {
			return err
		}
		job = r.job
		if !r.job.State.Ended() && u.unit.State == tenant.ImportUnitQueued {
			if _, err := tx.Exec(ctx, `UPDATE import_units SET state = 'walking' WHERE job_id = ? AND unit_id = ?`,
				u.jobID, u.unit.ID); err != nil {
				return err
			}
			u.unit.State = tenant.ImportUnitWalking
		}
		if err := withTallies(ctx, tx, &u); err != nil {
			return err
		}
		unit = u.unit
		if r.job.State != tenant.JobQueued {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = 'running', started = ?, state_at = ? WHERE id = ? AND state = 'queued'`,
			k.D.Time(now), k.D.Time(now), u.jobID); err != nil {
			return err
		}
		after, err := jobInTx(ctx, tx, u.jobID, "")
		if err != nil {
			return err
		}
		job = after.job
		e, err := tenant.JobStepEvent(tenant.JobQueued, after.job, now)
		if err != nil {
			return err
		}
		return tx.WriteEvents(ctx, e)
	})
	if err != nil {
		return tenant.ImportUnit{}, tenant.Job{}, err
	}
	return unit, job, nil
}

// AddImportUnits implements tenant.ImportWork.
func (k Sending) AddImportUnits(ctx context.Context, fence tenant.WorkFence, folders []string, now time.Time) (tenant.Job, error) {
	if err := fence.Validate(); err != nil {
		return tenant.Job{}, err
	}
	if err := tenant.ValidateImportFolders(folders); err != nil {
		return tenant.Job{}, err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.Job{}, err
	}
	var out tenant.Job
	err := k.Host.Write(ctx, "the import units cannot be queued", func(tx SendingTx) error {
		u, err := k.fencedImportUnit(ctx, tx, fence)
		if err != nil {
			return err
		}
		if !u.unit.Listing {
			return tenant.Precondition("only an import job's listing unit queues units")
		}
		ij, _, err := importJobInTx(ctx, tx, u.jobID, k.D.Lock)
		if err != nil {
			return err
		}
		r, err := jobInTx(ctx, tx, u.jobID, "")
		if err != nil {
			return err
		}
		out = r.job
		if r.job.State.Ended() {
			return nil
		}
		var added int64
		for _, f := range folders {
			if !tenant.ImportPathUnder(u.unit.Folder, f) {
				return tenant.Precondition("a folder is not under the job's path")
			}
			key := tenant.ImportPathKey(f)
			queued, err := exists(ctx, tx, `SELECT 1 FROM import_units WHERE job_id = ? AND folder_key = ?`, u.jobID, key)
			if err != nil {
				return err
			}
			if queued {
				continue // a page written again after a crash
			}
			if u.cursorSet && importpath.Compare(f, u.unit.Cursor) <= 0 {
				return tenant.Precondition("a folder is at or before the listing unit's cursor")
			}
			if err := k.insertImportUnit(ctx, tx, u.jobID, ij.nextUnit, f, false, now); err != nil {
				return err
			}
			ij.nextUnit++
			added++
		}
		last := folders[len(folders)-1]
		if !u.cursorSet || importpath.Compare(last, u.unit.Cursor) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE import_units SET cursor_key = ? WHERE job_id = ? AND unit_id = ?`,
				tenant.ImportPathKey(last), u.jobID, u.unit.ID); err != nil {
				return err
			}
		}
		if added == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE import_jobs SET units = units + ?, next_unit = ? WHERE job_id = ?`,
			added, ij.nextUnit, u.jobID); err != nil {
			return err
		}
		after, err := jobInTx(ctx, tx, u.jobID, "")
		out = after.job
		return err
	})
	if err != nil {
		return tenant.Job{}, err
	}
	return out, nil
}

// RecordImportSkip implements tenant.ImportWork.
func (k Sending) RecordImportSkip(ctx context.Context, s tenant.ImportSkip) (tenant.Job, error) {
	if err := s.Validate(); err != nil {
		return tenant.Job{}, err
	}
	var out tenant.Job
	err := k.Host.Write(ctx, "the skipped file cannot be recorded", func(tx SendingTx) error {
		u, err := k.fencedImportUnit(ctx, tx, s.Fence)
		if err != nil {
			return err
		}
		fresh, err := judgeImportPath(u, s.Path)
		if err != nil {
			return err
		}
		if fresh {
			ij, _, err := importJobInTx(ctx, tx, u.jobID, k.D.Lock)
			if err != nil {
				return err
			}
			key := tenant.ImportPathKey(s.Path)
			if _, err := k.listImportFile(ctx, tx, &ij, importListed{pathKey: key, kind: tenant.ImportFileSkipped,
				unitID: u.unit.ID, skipCode: s.Code, size: s.Size, detail: s.Detail, at: s.Now}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE import_units SET cursor_key = ? WHERE job_id = ? AND unit_id = ?`,
				key, u.jobID, u.unit.ID); err != nil {
				return err
			}
			d := importDelta{found: 1, skipped: 1}
			if err := k.addToUnit(ctx, tx, u.jobID, u.unit.ID, d); err != nil {
				return err
			}
			if err := k.saveImportJob(ctx, tx, &ij, d); err != nil {
				return err
			}
			if err := addImportTally(ctx, tx, u.jobID, u.unit.ID, tallySkip, string(s.Code), 1); err != nil {
				return err
			}
		}
		r, err := jobInTx(ctx, tx, u.jobID, "")
		out = r.job
		return err
	})
	if err != nil {
		return tenant.Job{}, err
	}
	return out, nil
}

// EndImportWalk implements tenant.ImportWork.
func (k Sending) EndImportWalk(ctx context.Context, fence tenant.WorkFence, now time.Time) (tenant.Job, error) {
	return k.endWalk(ctx, fence, "", now)
}

// FailImportUnit implements tenant.ImportWork.
func (k Sending) FailImportUnit(ctx context.Context, fence tenant.WorkFence, reason string, now time.Time) (tenant.Job, error) {
	if reason != tenant.ImportReasonRootUnavailable {
		return tenant.Job{}, fault.New(fault.Invalid, "an import unit fails with the reason \"root unavailable\"")
	}
	return k.endWalk(ctx, fence, reason, now)
}

// endWalk ends a unit's walk, failed with reason when it is not empty, and
// completes its unit of work.
func (k Sending) endWalk(ctx context.Context, fence tenant.WorkFence, reason string, now time.Time) (tenant.Job, error) {
	if err := fence.Validate(); err != nil {
		return tenant.Job{}, err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.Job{}, err
	}
	var out tenant.Job
	err := k.Host.Write(ctx, "the import unit's walk cannot be ended", func(tx SendingTx) error {
		u, err := k.fencedImportUnit(ctx, tx, fence)
		if err != nil {
			return err
		}
		events, err := k.endWalkInTx(ctx, tx, u, reason, now)
		if err != nil {
			return err
		}
		if err := k.completeUnit(ctx, tx, fence, now); err != nil {
			return err
		}
		r, err := jobInTx(ctx, tx, u.jobID, "")
		if err != nil {
			return err
		}
		out = r.job
		return tx.WriteEvents(ctx, events...)
	})
	if err != nil {
		return tenant.Job{}, err
	}
	return out, nil
}

// endWalkInTx ends the walk of the unit u (its row locked): failed with
// reason when it is not empty; canceled when its job has ended; otherwise
// walked, or done when nothing it handed in is processing. The listing
// unit's end lists the job. It returns the events of the job's end. A walk
// ended already changes nothing.
func (k Sending) endWalkInTx(ctx context.Context, tx SendingTx, u importUnitRow, reason string, now time.Time) ([]tenant.EventRecord, error) {
	if u.unit.State.WalkEnded() {
		return nil, nil
	}
	ij, _, err := importJobInTx(ctx, tx, u.jobID, k.D.Lock)
	if err != nil {
		return nil, err
	}
	r, err := jobInTx(ctx, tx, u.jobID, "")
	if err != nil {
		return nil, err
	}
	state := tenant.ImportUnitWalked
	switch {
	case reason != "":
		state = tenant.ImportUnitFailed
	case r.job.State.Ended():
		state = tenant.ImportUnitCanceled
	case u.unit.Counts.Processing == 0:
		state = tenant.ImportUnitDone
	}
	if _, err := tx.Exec(ctx, `UPDATE import_units SET state = ?, reason = ?, walk_ended = ? WHERE job_id = ? AND unit_id = ?`,
		string(state), reason, k.D.Time(now), u.jobID, u.unit.ID); err != nil {
		return nil, err
	}
	failReason := ij.failReason
	if reason != "" && failReason == "" {
		failReason = reason
	}
	listed := ij.spec.Listed || (u.unit.Listing && reason == "")
	if _, err := tx.Exec(ctx, `UPDATE import_jobs SET units_walked = units_walked + 1, listed = ?, fail_reason = ? WHERE job_id = ?`,
		k.D.Bool(listed), failReason, u.jobID); err != nil {
		return nil, err
	}
	return k.settleImportJob(ctx, tx, u.jobID, now)
}

// settleImportJob ends an import job whose walks have all ended (it is
// listed, or a unit failed, so no unit will be queued) once nothing is
// processing, or at once when a unit failed (tenant.ImportEndState); while
// files are still processing it stays running with the reason "settling",
// from the time it first was. It returns the end's event; a job ended, or
// with walks still to end, is left as it is.
func (k Sending) settleImportJob(ctx context.Context, tx SendingTx, jobID string, now time.Time) ([]tenant.EventRecord, error) {
	ij, found, err := importJobInTx(ctx, tx, jobID, k.D.Lock)
	if err != nil || !found {
		return nil, err
	}
	c := ij.spec.Counts
	if (!ij.spec.Listed && ij.failReason == "") || c.UnitsWalked < c.Units {
		return nil, nil
	}
	r, err := jobInTx(ctx, tx, jobID, k.D.Lock)
	if err != nil || r.job.State.Ended() {
		return nil, err
	}
	if ij.failReason == "" && c.Processing > 0 {
		if ij.spec.SettlingSince.IsZero() {
			if _, err := tx.Exec(ctx, `UPDATE import_jobs SET settling_since = ? WHERE job_id = ?`, k.D.Time(now), jobID); err != nil {
				return nil, err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE jobs SET reason = ? WHERE id = ? AND reason <> ?`,
			tenant.ImportReasonSettling, jobID, tenant.ImportReasonSettling)
		return nil, err
	}
	state, reason := tenant.ImportEndState(c, ij.failReason)
	_, events, err := k.endJob(ctx, tx, r.job, state, reason, now)
	return events, err
}

// TimeOutImportSettling implements tenant.ImportWork.
func (k Sending) TimeOutImportSettling(ctx context.Context, jobID string, now time.Time) (tenant.Job, error) {
	if err := master.ValidateID("job id", jobID); err != nil {
		return tenant.Job{}, err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.Job{}, err
	}
	var out tenant.Job
	err := k.Host.Write(ctx, "the import job's settling cannot be timed out", func(tx SendingTx) error {
		first, err := importJobOf(ctx, tx, jobID)
		if err != nil {
			return err
		}
		out = first
		ij, _, err := importJobInTx(ctx, tx, jobID, k.D.Lock)
		if err != nil {
			return err
		}
		r, err := jobInTx(ctx, tx, jobID, k.D.Lock)
		if err != nil {
			return err
		}
		c := ij.spec.Counts
		if r.job.State.Ended() || !ij.spec.Listed || c.UnitsWalked < c.Units || c.Processing == 0 {
			out = r.job
			return nil
		}
		job, events, err := k.endJob(ctx, tx, r.job, tenant.JobPartial, tenant.ImportReasonSettlingTimedOut, now)
		if err != nil {
			return err
		}
		out = job
		return tx.WriteEvents(ctx, events...)
	})
	if err != nil {
		return tenant.Job{}, err
	}
	return out, nil
}

// ReleaseImportUnit implements tenant.ImportWork. As ReleaseJobUnit, the
// attempt the claim counted is given back by allowing one more, never by
// lowering the attempt, which fences every write.
func (k Sending) ReleaseImportUnit(ctx context.Context, fence tenant.WorkFence, due time.Time, reason string, now time.Time) error {
	if err := fence.Validate(); err != nil {
		return err
	}
	if err := tenant.ValidateWorkReason(reason); err != nil {
		return err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return err
	}
	if err := master.ValidateTime("due time", due); err != nil {
		return err
	}
	if due.Before(now) {
		return fault.New(fault.Invalid, "a unit is given back until a time not before now")
	}
	return k.Host.Write(ctx, "the import unit cannot be given back", func(tx SendingTx) error {
		u, err := k.fencedImportUnit(ctx, tx, fence)
		if err != nil {
			return err
		}
		r, err := jobInTx(ctx, tx, u.jobID, "")
		if err != nil {
			return err
		}
		if r.job.State.Ended() {
			events, err := k.endWalkInTx(ctx, tx, u, "", now)
			if err != nil {
				return err
			}
			if err := k.completeUnit(ctx, tx, fence, now); err != nil {
				return err
			}
			return tx.WriteEvents(ctx, events...)
		}
		_, err = tx.Exec(ctx, `UPDATE work SET state = 'queued', owner = '', due = ?, last_error = ?,
			max_attempts = max_attempts + 1, updated = ? WHERE id = ? AND attempt = ?`,
			k.D.Time(due), reason, k.D.Time(now), fence.ID, fence.Attempt)
		return err
	})
}

// cancelImportInTx ends an import job canceled, and with it the units of
// it that are queued and no transaction holds, whose walks end canceled; a
// unit a worker holds learns of the cancel from the job's state its next
// write answers. A job canceled already is returned as it stands with no
// event; one ended otherwise is a precondition failure. Its units' rows
// are locked before the import job's and the job's, the order of every
// import transaction.
func (k Sending) cancelImportInTx(ctx context.Context, tx SendingTx, id, reason string, now time.Time) (tenant.Job, *tenant.EventRecord, error) {
	first, err := jobInTx(ctx, tx, id, "")
	if err != nil {
		return tenant.Job{}, nil, err
	}
	if first.job.State == tenant.JobCanceled {
		return first.job, nil, nil
	}
	if first.job.State.Ended() {
		return tenant.Job{}, nil, tenant.Precondition("the job has ended " + string(first.job.State) + "; only a job queued or running is canceled")
	}
	var queued []string
	if err := tx.Query(ctx, `SELECT w.id FROM work AS w JOIN import_units AS u ON u.work_id = w.id
		WHERE u.job_id = ? AND w.state = 'queued' ORDER BY w.id`+k.D.lockOf("w")+skipLocked(k.D), []any{id}, func(r Row) error {
		var w string
		err := r.Scan(&w)
		queued = append(queued, w)
		return err
	}); err != nil {
		return tenant.Job{}, nil, err
	}
	var ended int64
	for _, w := range queued {
		if _, err := tx.Exec(ctx, `UPDATE work SET state = 'done', owner = '', last_error = ?, updated = ?
			WHERE id = ? AND state = 'queued'`, tenant.WorkReasonJobCanceled, k.D.Time(now), w); err != nil {
			return tenant.Job{}, nil, err
		}
		n, err := tx.Exec(ctx, `UPDATE import_units SET state = 'canceled', walk_ended = ? WHERE work_id = ?
			AND state IN ('queued', 'walking')`, k.D.Time(now), w)
		if err != nil {
			return tenant.Job{}, nil, err
		}
		ended += n
	}
	if _, _, err := importJobInTx(ctx, tx, id, k.D.Lock); err != nil {
		return tenant.Job{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE import_jobs SET units_walked = units_walked + ? WHERE job_id = ?`, ended, id); err != nil {
		return tenant.Job{}, nil, err
	}
	r, err := jobInTx(ctx, tx, id, k.D.Lock)
	if err != nil {
		return tenant.Job{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET state = 'canceled', ended = ?, reason = ?, state_at = ?, ended_at = ? WHERE id = ?`,
		k.D.Bool(true), reason, k.D.Time(now), k.D.Time(now), id); err != nil {
		return tenant.Job{}, nil, err
	}
	after, err := jobInTx(ctx, tx, id, "")
	if err != nil {
		return tenant.Job{}, nil, err
	}
	e, err := tenant.JobStepEvent(r.job.State, after.job, now)
	if err != nil {
		return tenant.Job{}, nil, err
	}
	return after.job, &e, nil
}

// skipLocked returns the clause that skips rows another transaction
// holds, written after a lock clause: " SKIP LOCKED" where rows are
// locked, "" where one writer runs at a time.
func skipLocked(d SendingDialect) string {
	if d.Lock == "" {
		return ""
	}
	return " SKIP LOCKED"
}

// giveUpImportUnit ends the walk of an import unit whose unit of work is
// held as failed, its attempts run out, in the transaction that holds it:
// failed, with the job ending failed once every walk has ended. It reports
// false for a unit of work that is no import unit's.
func (k Sending) giveUpImportUnit(ctx context.Context, tx SendingTx, workID string, now time.Time) (bool, []tenant.EventRecord, error) {
	u, found, err := k.importUnitOf(ctx, tx, workID, k.D.lockOf("u"))
	if err != nil || !found {
		return false, nil, err
	}
	events, err := k.endWalkInTx(ctx, tx, u, tenant.JobReasonAttemptsRanOut, now)
	return true, events, err
}
