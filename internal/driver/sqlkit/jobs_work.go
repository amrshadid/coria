package sqlkit

import (
	"context"
	"time"

	"github.com/amrshadid/coria/internal/core/reception"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/tenant"
)

// jobScanColumns are a job's scan's columns, with its scan's labels and
// storage, read from job_scans joined to scans.
const jobScanColumns = `js.job_id, js.scan_id, js.start_after, js.cursor, js.instances, js.bytes, js.sent, js.sent_bytes,
	js.warned, js.warned_bytes, js.refused, js.refused_bytes, js.refused_not_listed, js.ended,
	s.subject_label, s.session_label, s.label, s.storage`

// jobScanRow is a job's scan as its row holds it.
type jobScanRow struct {
	jobID            string
	scanID           int64
	startAfter       string
	refusedNotListed int64
	scan             tenant.JobScan
}

// scanJobScan reads one row of jobScanColumns.
func scanJobScan(row Row) (jobScanRow, error) {
	var r jobScanRow
	c := &r.scan.Counts
	err := row.Scan(&r.jobID, &r.scanID, &r.startAfter, &r.scan.Cursor, &c.Instances, &c.Bytes, &c.InstancesSent, &c.BytesSent,
		&c.InstancesWarned, &c.BytesWarned, &c.InstancesRefused, &c.BytesRefused, &r.refusedNotListed, &r.scan.Ended,
		&r.scan.Path.Subject, &r.scan.Path.Session, &r.scan.Path.Scan, &r.scan.Storage)
	c.Scans, c.Listed = 1, true
	if r.scan.Ended {
		c.ScansDone = 1
	}
	return r, err
}

// jobScanOf reads the scan a unit sends, with the lock given on its row;
// false when the unit sends none.
func jobScanOf(ctx context.Context, tx SendingTx, workID, lock string) (jobScanRow, bool, error) {
	var out jobScanRow
	found := false
	err := tx.Query(ctx, jobScanByWorkSQL+lock, []any{workID}, func(r Row) error {
		found = true
		var err error
		out, err = scanJobScan(r)
		return err
	})
	return out, found, err
}

// listingJobOf returns the id of the job whose listing unit is workID, or
// "": a listing unit's subject is its job's id.
func listingJobOf(ctx context.Context, tx SendingTx, workID string) (string, error) {
	var subject string
	found, err := tx.QueryRow(ctx, `SELECT subject FROM work WHERE id = ?`, []any{workID}, &subject)
	if err != nil || !found || master.ValidateID("job id", subject) != nil {
		return "", err
	}
	var id string
	found, err = tx.QueryRow(ctx, `SELECT id FROM jobs WHERE id = ? AND list_work = ?`, []any{subject, workID}, &id)
	if err != nil || !found {
		return "", err
	}
	return id, nil
}

// errNotJobUnit is the refusal of a unit no job queued.
func errNotJobUnit() error {
	return tenant.Precondition("the unit of work is not one of a job's")
}

// completeUnit marks the leased unit done, fenced by its attempt.
func (k Sending) completeUnit(ctx context.Context, tx SendingTx, f tenant.WorkFence, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE work SET state = 'done', owner = '', updated = ? WHERE id = ? AND attempt = ?`,
		k.D.Time(now), f.ID, f.Attempt)
	return err
}

// StartJobUnit implements tenant.JobWork.
func (k Sending) StartJobUnit(ctx context.Context, fence tenant.WorkFence, now time.Time) (tenant.JobUnit, error) {
	if err := fence.Validate(); err != nil {
		return tenant.JobUnit{}, err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.JobUnit{}, err
	}
	var out tenant.JobUnit
	err := k.Host.Write(ctx, "the job's unit cannot be started", func(tx SendingTx) error {
		out = tenant.JobUnit{}
		if err := tx.CheckFence(ctx, fence); err != nil {
			return err
		}
		scan, isScan, err := jobScanOf(ctx, tx, fence.ID, "")
		if err != nil {
			return err
		}
		jobID := scan.jobID
		if !isScan {
			if jobID, err = listingJobOf(ctx, tx, fence.ID); err != nil {
				return err
			}
			if jobID == "" {
				return errNotJobUnit()
			}
			out.Listing = true
		}
		r, err := jobInTx(ctx, tx, jobID, k.D.Lock)
		if err != nil {
			return err
		}
		out.Scan = scan.scan
		out.Job = r.job
		if r.job.State != tenant.JobQueued {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = 'running', started = ?, state_at = ? WHERE id = ? AND state = 'queued'`,
			k.D.Time(now), k.D.Time(now), jobID); err != nil {
			return err
		}
		after, err := jobInTx(ctx, tx, jobID, "")
		if err != nil {
			return err
		}
		out.Job = after.job
		e, err := tenant.JobStepEvent(tenant.JobQueued, after.job, now)
		if err != nil {
			return err
		}
		return tx.WriteEvents(ctx, e)
	})
	if err != nil {
		return tenant.JobUnit{}, err
	}
	return out, nil
}

// JobScanInstances implements tenant.JobWork.
func (k Sending) JobScanInstances(ctx context.Context, fence tenant.WorkFence, after string, limit int) ([]tenant.ScanInstance, error) {
	if err := fence.Validate(); err != nil {
		return nil, err
	}
	if after != "" {
		if err := tenant.ValidateInstanceUID(after); err != nil {
			return nil, err
		}
	}
	if limit < 1 || limit > tenant.MaxJobInstancePage {
		return nil, fault.New(fault.Invalid, "a page of a job's instances is 1 to 1000")
	}
	var out []tenant.ScanInstance
	err := k.Host.Read(ctx, "the job's instances cannot be read", func(tx SendingTx) error {
		out = nil
		s, found, err := jobScanOf(ctx, tx, fence.ID, "")
		if err != nil {
			return err
		}
		if !found {
			return errNotJobUnit()
		}
		r, err := jobInTx(ctx, tx, s.jobID, "")
		if err != nil {
			return err
		}
		query, args := k.D.StreamPage(r.job, r.bound, s.scanID, s.startAfter, after, limit)
		if err := tx.Query(ctx, query, args, func(row Row) error {
			var in tenant.ScanInstance
			var sum, archived []byte
			var held DBTime
			err := row.Scan(&in.SOPInstanceUID, &in.ReceptionID, &in.Key, &in.Offset, &in.Length, &in.Size, &sum, &archived, &held)
			in.SHA256, in.ArchivedSHA256, in.Held = ToSHA256(sum), ToSHA256(archived), held.T
			out = append(out, in)
			return err
		}); err != nil {
			return err
		}
		return placeHeld(tenant.CatalogScan{SubjectLabel: s.scan.Path.Subject, SessionLabel: s.scan.Path.Session,
			Label: s.scan.Path.Scan, Storage: s.scan.Storage}, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordJobProgress implements tenant.JobWork.
func (k Sending) RecordJobProgress(ctx context.Context, p tenant.JobProgress) (tenant.Job, error) {
	if err := p.Validate(); err != nil {
		return tenant.Job{}, err
	}
	var out tenant.Job
	err := k.Host.Write(ctx, "the job's progress cannot be recorded", func(tx SendingTx) error {
		if err := tx.CheckFence(ctx, p.Fence); err != nil {
			return err
		}
		job, _, events, err := k.recordInTx(ctx, tx, p)
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

// EndJobScan implements tenant.JobWork.
func (k Sending) EndJobScan(ctx context.Context, p tenant.JobProgress) (tenant.Job, error) {
	if err := p.Validate(); err != nil {
		return tenant.Job{}, err
	}
	var out tenant.Job
	err := k.Host.Write(ctx, "the job's scan cannot be ended", func(tx SendingTx) error {
		if err := tx.CheckFence(ctx, p.Fence); err != nil {
			return err
		}
		job, scan, events, err := k.recordInTx(ctx, tx, p)
		if err != nil {
			return err
		}
		out = job
		if err := k.completeUnit(ctx, tx, p.Fence, p.Now); err != nil {
			return err
		}
		if !scan.scan.Ended {
			if _, err := tx.Exec(ctx, `UPDATE job_scans SET ended = ? WHERE job_id = ? AND scan_id = ?`,
				k.D.Bool(true), scan.jobID, scan.scanID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE jobs SET scans_done = scans_done + 1 WHERE id = ?`, scan.jobID); err != nil {
				return err
			}
			ended, more, err := k.endIfDone(ctx, tx, scan.jobID, p.Now)
			if err != nil {
				return err
			}
			out, events = ended, append(events, more...)
		}
		return tx.WriteEvents(ctx, events...)
	})
	if err != nil {
		return tenant.Job{}, err
	}
	return out, nil
}

// recordInTx records p on the scan its unit sends and on its job, after the
// fence was checked, and returns the job and the scan as they are after,
// with the progress event when one is due.
func (k Sending) recordInTx(ctx context.Context, tx SendingTx, p tenant.JobProgress) (tenant.Job, jobScanRow, []tenant.EventRecord, error) {
	first, found, err := jobScanOf(ctx, tx, p.Fence.ID, "")
	if err != nil {
		return tenant.Job{}, jobScanRow{}, nil, err
	}
	if !found {
		return tenant.Job{}, jobScanRow{}, nil, errNotJobUnit()
	}
	// The job's row before its scan's, in the order of the locks.
	r, err := jobInTx(ctx, tx, first.jobID, k.D.Lock)
	if err != nil {
		return tenant.Job{}, jobScanRow{}, nil, err
	}
	s, _, err := jobScanOf(ctx, tx, p.Fence.ID, k.D.lockOf("js"))
	if err != nil {
		return tenant.Job{}, jobScanRow{}, nil, err
	}
	if s.scan.Cursor != p.From {
		if p.From != p.To && s.scan.Cursor == p.To {
			return r.job, s, nil, nil // recorded before: its answer was lost
		}
		return tenant.Job{}, jobScanRow{}, nil, tenant.Precondition("the unit's cursor is not the one the progress follows")
	}
	if p.From == p.To {
		return r.job, s, nil, nil
	}
	c := s.scan.Counts
	if c.InstancesSent+c.InstancesWarned+c.InstancesRefused+p.Sent+p.Warned+p.Refused > c.Instances ||
		c.BytesSent+c.BytesWarned+c.BytesRefused+p.SentBytes+p.WarnedBytes+p.RefusedBytes > c.Bytes {
		return tenant.Job{}, jobScanRow{}, nil, tenant.Precondition("the progress counts more than the scan's instances hold")
	}
	listed, notListed, refusedNotListed := r.listed, int64(0), int64(0)
	for _, f := range p.Failures {
		if listed >= int64(p.FailuresKept) {
			notListed++
			if f.Code.Refused() {
				refusedNotListed++
			}
			continue
		}
		n, err := tx.Exec(ctx, `INSERT INTO job_failures (job_id, subject_label, session_label, scan_label, sop_instance_uid,
			scan_id, warning, code, peer_status, sop_class_uid, transfer_syntax_uid, detail, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, r.job.ID, s.scan.Path.Subject,
			s.scan.Path.Session, s.scan.Path.Scan, f.SOPInstanceUID, s.scanID, k.D.Bool(!f.Code.Refused()), string(f.Code),
			f.PeerStatus, f.SOPClassUID, f.TransferSyntaxUID, f.Detail, k.D.Time(f.Time))
		if err != nil {
			return tenant.Job{}, jobScanRow{}, nil, err
		}
		listed += n
	}
	if _, err := tx.Exec(ctx, `UPDATE job_scans SET cursor = ?, sent = sent + ?, sent_bytes = sent_bytes + ?, warned = warned + ?,
		warned_bytes = warned_bytes + ?, refused = refused + ?, refused_bytes = refused_bytes + ?,
		refused_not_listed = refused_not_listed + ? WHERE job_id = ? AND scan_id = ? AND cursor = ?`,
		p.To, p.Sent, p.SentBytes, p.Warned, p.WarnedBytes, p.Refused, p.RefusedBytes, refusedNotListed, s.jobID, s.scanID,
		p.From); err != nil {
		return tenant.Job{}, jobScanRow{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET sent = sent + ?, sent_bytes = sent_bytes + ?, warned = warned + ?,
		warned_bytes = warned_bytes + ?, refused = refused + ?, refused_bytes = refused_bytes + ?, failures_listed = ?,
		failures_not_listed = failures_not_listed + ? WHERE id = ?`, p.Sent, p.SentBytes, p.Warned, p.WarnedBytes,
		p.Refused, p.RefusedBytes, listed, notListed, r.job.ID); err != nil {
		return tenant.Job{}, jobScanRow{}, nil, err
	}
	after, err := jobInTx(ctx, tx, r.job.ID, "")
	if err != nil {
		return tenant.Job{}, jobScanRow{}, nil, err
	}
	s.scan.Cursor = p.To
	var events []tenant.EventRecord
	ac := after.job.Counts
	mark := ac.InstancesSent + ac.InstancesWarned + ac.InstancesRefused
	if after.job.State == tenant.JobRunning && mark != after.progressMark &&
		(after.progressAt.IsZero() || !p.Now.Before(after.progressAt.Add(tenant.ProgressEventInterval))) {
		e, err := tenant.JobProgressEvent(after.job, p.Now)
		if err != nil {
			return tenant.Job{}, jobScanRow{}, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET progress_at = ?, progress_mark = ? WHERE id = ?`,
			k.D.Time(p.Now), mark, r.job.ID); err != nil {
			return tenant.Job{}, jobScanRow{}, nil, err
		}
		events = append(events, e)
	}
	return after.job, s, events, nil
}

// lockOf returns the lock clause for the rows of one table of a join, so a
// join locks only the row it changes: " FOR NO KEY UPDATE OF js".
func (d SendingDialect) lockOf(table string) string {
	if d.Lock == "" {
		return ""
	}
	return d.Lock + " OF " + table
}

// endIfDone ends the job, listed, once every scan of it has ended, in the
// state EndState gives, and returns it with its event; a job ended already
// or with work left is returned as it is.
func (k Sending) endIfDone(ctx context.Context, tx SendingTx, jobID string, now time.Time) (tenant.Job, []tenant.EventRecord, error) {
	r, err := jobInTx(ctx, tx, jobID, "")
	if err != nil {
		return tenant.Job{}, nil, err
	}
	c := r.job.Counts
	if r.job.State.Ended() || !c.Listed || c.ScansDone < c.Scans {
		return r.job, nil, nil
	}
	state, reason := tenant.EndState(c)
	return k.endJob(ctx, tx, r.job, state, reason, now)
}

// endJob moves a job that has not ended to state with the reason.
func (k Sending) endJob(ctx context.Context, tx SendingTx, j tenant.Job, state tenant.JobState, reason string, now time.Time) (tenant.Job, []tenant.EventRecord, error) {
	if _, err := tx.Exec(ctx, `UPDATE jobs SET state = ?, ended = ?, reason = ?, state_at = ?, ended_at = ? WHERE id = ? AND state = ?`,
		string(state), k.D.Bool(true), reason, k.D.Time(now), k.D.Time(now), j.ID, string(j.State)); err != nil {
		return tenant.Job{}, nil, err
	}
	after, err := jobInTx(ctx, tx, j.ID, "")
	if err != nil {
		return tenant.Job{}, nil, err
	}
	e, err := tenant.JobStepEvent(j.State, after.job, now)
	if err != nil {
		return tenant.Job{}, nil, err
	}
	return after.job, []tenant.EventRecord{e}, nil
}

// GiveUpJobUnit ends what a unit of a job carried when the unit is held as
// failed, its attempts run out, in the transaction that holds it: a scan's
// unit ends its scan with what it did not send remaining, and ends the job
// (failed) when it was its last; a listing's unit ends its job failed, since
// what it did not list can no longer be sent. It returns the events, which
// the driver writes; a unit of no job changes nothing. A driver calls it
// where a unit is held as failed (FailWork, a claim).
func (k Sending) GiveUpJobUnit(ctx context.Context, tx SendingTx, workID string, now time.Time) ([]tenant.EventRecord, error) {
	s, found, err := jobScanOf(ctx, tx, workID, "")
	if err != nil {
		return nil, err
	}
	if found {
		r, err := jobInTx(ctx, tx, s.jobID, k.D.Lock)
		if err != nil {
			return nil, err
		}
		n, err := tx.Exec(ctx, `UPDATE job_scans SET ended = ? WHERE job_id = ? AND scan_id = ? AND ended = ?`,
			k.D.Bool(true), s.jobID, s.scanID, k.D.Bool(false))
		if err != nil || n == 0 {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET scans_done = scans_done + 1 WHERE id = ?`, r.job.ID); err != nil {
			return nil, err
		}
		_, events, err := k.endIfDone(ctx, tx, r.job.ID, now)
		return events, err
	}
	jobID, err := listingJobOf(ctx, tx, workID)
	if err != nil || jobID == "" {
		return nil, err
	}
	r, err := jobInTx(ctx, tx, jobID, k.D.Lock)
	if err != nil || r.job.State.Ended() {
		return nil, err
	}
	_, events, err := k.endJob(ctx, tx, r.job, tenant.JobFailed, tenant.JobReasonAttemptsRanOut, now)
	return events, err
}

// Forward makes, in the transaction of a reception's step that reached
// done, one job per sender its receiver forwards to (tenant.Sending), and
// returns their events, which the driver writes with the step's. A sender
// removed meanwhile is skipped; one forwarded to already is not made twice
// (tenant.ForwardKey). A reception not done makes nothing.
func (k Sending) Forward(ctx context.Context, tx SendingTx, r tenant.Reception) ([]tenant.EventRecord, error) {
	if r.State != reception.Done {
		return nil, nil
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

// JobsInProgressSQL counts the jobs not ended by state, from
// jobs_in_progress, for a status.
func (d SendingDialect) JobsInProgressSQL() string {
	return `SELECT state, count(*) FROM jobs WHERE ` + d.NotEnded + ` GROUP BY state`
}
