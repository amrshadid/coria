package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

const receptionColumns = `id, receiver, study_uid, arrival, state, files_received, bytes_received, files_archived,
	bytes_archived, files_duplicate, bytes_duplicate, files_quarantined, bytes_quarantined, files_released,
	subject_label, session_label, storage, failure, started, last_file, step_at, ended_at, associations,
	calling_ae_titles, calling_truncated, retry_of, retry_reception, discard_reason, discard_at`

// scanReception reads one row of receptionColumns.
func scanReception(row interface{ Scan(...any) error }) (tenant.Reception, error) {
	var r tenant.Reception
	var state, titles string
	var started, lastFile, stepAt int64
	var endedAt, discardAt sql.NullInt64
	c := &r.Counts
	if err := row.Scan(&r.ID, &r.Receiver, &r.StudyUID, &r.Arrival, &state, &c.FilesReceived, &c.BytesReceived,
		&c.FilesArchived, &c.BytesArchived, &c.FilesDuplicate, &c.BytesDuplicate, &c.FilesQuarantined, &c.BytesQuarantined,
		&c.FilesReleased, &r.SubjectLabel, &r.SessionLabel, &r.Storage, &r.Failure, &started, &lastFile, &stepAt, &endedAt,
		&r.Associations, &titles, &r.CallingAETitlesTruncated, &r.RetryOf, &r.RetryReception, &r.DiscardReason, &discardAt); err != nil {
		return r, err
	}
	r.State = reception.State(state)
	r.Started, r.LastFile, r.StepTime = fromMicros(started), fromMicros(lastFile), fromMicros(stepAt)
	r.Ended, r.DiscardTime = fromNullMicros(endedAt), fromNullMicros(discardAt)
	if err := fromJSON(titles, &r.CallingAETitles); err != nil {
		return r, err
	}
	if len(r.CallingAETitles) == 0 {
		r.CallingAETitles = nil
	}
	return r, nil
}

// receptionInTx reads a reception inside a change's transaction, or
// NotFound.
func receptionInTx(ctx context.Context, tx *sql.Tx, id string) (tenant.Reception, error) {
	r, err := scanReception(tx.QueryRowContext(ctx, `SELECT `+receptionColumns+` FROM receptions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, fault.New(fault.NotFound, "the tenant has no reception with this id")
	}
	return r, err
}

// openReceptionInTx finds the reception of the study receiving at the
// receiver, or makes one with id at the next arrival of the study. Two opens
// at once meet at receptions_open, the partial unique index that holds one
// open reception per receiver and study, or at receptions_arrival: the
// insert that meets the other's row changes nothing (ON CONFLICT DO
// NOTHING), and the loop reads again, finding the reception that won, or
// the arrival number taken. On a database with several writers the second
// insert waits for the first's commit, then reads what it committed.
func openReceptionInTx(ctx context.Context, tx *sql.Tx, receiver, studyUID, id string, at time.Time) (tenant.Reception, bool, error) {
	for try := 0; try < 8; try++ {
		r, err := scanReception(tx.QueryRowContext(ctx, openReceptionSQL, receiver, studyUID))
		if err == nil {
			return r, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return r, false, err
		}
		if try == 0 {
			has, err := exists(ctx, tx, `SELECT 1 FROM receivers WHERE name = ?`, receiver)
			if err != nil {
				return r, false, err
			}
			if !has {
				return r, false, tenant.Precondition("the tenant has no receiver named " + receiver)
			}
		}
		inserted, err := insertReception(ctx, tx, id, receiver, studyUID, reception.Receiving, "", at)
		if err != nil {
			return r, false, err
		}
		if inserted {
			r, err = receptionInTx(ctx, tx, id)
			return r, true, err
		}
	}
	return tenant.Reception{}, false, fault.New(fault.Unavailable, "receptions of the study kept opening at the same moment; try again")
}

// insertReception writes a new reception in state at the next arrival of
// its study, and reports whether it did: a row it meets under one of the
// unique keys (its id, the open reception of its receiver and study, the
// arrival number another took meanwhile) leaves nothing written. An id
// taken by another reception is a Conflict, since ids are the caller's to
// make unique.
func insertReception(ctx context.Context, tx *sql.Tx, id, receiver, studyUID string, state reception.State, retryOf string, at time.Time) (bool, error) {
	var arrival int
	if err := tx.QueryRowContext(ctx, nextArrivalSQL, studyUID).Scan(&arrival); err != nil {
		return false, err
	}
	ts := micros(at)
	res, err := tx.ExecContext(ctx, `INSERT INTO receptions (id, receiver, study_uid, arrival, state, ended,
		files_received, bytes_received, files_archived, bytes_archived, files_duplicate, bytes_duplicate,
		files_quarantined, bytes_quarantined, files_released, subject_label, session_label, storage, failure,
		started, last_file, step_at, ended_at, associations, calling_ae_titles, calling_truncated,
		retry_of, retry_reception, discard_reason, discard_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, 0, 0, '', '', '', '', ?, ?, ?, NULL, 0, '[]', 0, ?, '', '', NULL)
		ON CONFLICT DO NOTHING`,
		id, receiver, studyUID, arrival, string(state), boolInt(state.Ended()), ts, ts, ts, retryOf)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 1 {
		return n == 1, err
	}
	taken, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	if taken {
		return false, fault.New(fault.Conflict, "a reception with this id exists")
	}
	return false, nil
}

// OpenReception implements tenant.Pipeline.
func (t *Tenant) OpenReception(ctx context.Context, receiver, studyUID, id string, at time.Time) (tenant.Reception, bool, error) {
	if err := tenant.ValidateOpen(receiver, studyUID, id, at); err != nil {
		return tenant.Reception{}, false, err
	}
	var out tenant.Reception
	var opened bool
	err := t.write(ctx, "the reception cannot be opened", func(tx *sql.Tx, _ *tenantStatements) error {
		var err error
		out, opened, err = openReceptionInTx(ctx, tx, receiver, studyUID, id, at)
		return err
	})
	if err != nil {
		return tenant.Reception{}, false, err
	}
	return out, opened, nil
}

// Reception implements tenant.Pipeline.
func (t *Tenant) Reception(ctx context.Context, id string) (tenant.Reception, []tenant.Arrival, error) {
	if err := master.ValidateID("reception id", id); err != nil {
		return tenant.Reception{}, nil, err
	}
	r, err := readOne(ctx, t, "the tenant has no reception with this id", "the reception cannot be read",
		`SELECT `+receptionColumns+` FROM receptions WHERE id = ?`, []any{id}, scanReception)
	if err != nil {
		return tenant.Reception{}, nil, err
	}
	// The latest arrivals, from receptions_arrival read backward, then put
	// in order of arrival number.
	arrivals, err := readRows(ctx, t, "the reception's arrivals cannot be read",
		arrivalsSQL,
		[]any{r.StudyUID, tenant.MaxArrivals}, func(row interface{ Scan(...any) error }) (tenant.Arrival, error) {
			var a tenant.Arrival
			var state string
			var started int64
			err := row.Scan(&a.ID, &a.Arrival, &state, &started)
			a.State, a.Started = reception.State(state), fromMicros(started)
			return a, err
		})
	if err != nil {
		return tenant.Reception{}, nil, err
	}
	slices.Reverse(arrivals)
	return r, arrivals, nil
}

// receptionBinding is what a ListReceptions cursor is bound to: the tenant
// and every field of the filter.
func (t *Tenant) receptionBinding(f tenant.ReceptionFilter) string {
	states := make([]string, len(f.States))
	for i, s := range f.States {
		states[i] = string(s)
	}
	return t.binding(f.Receiver, f.StudyUID, strings.Join(states, ","), strconv.FormatBool(f.InProgress),
		microsText(nullableMicros(f.Since)), microsText(nullableMicros(f.Until)))
}

// nullableMicros is micros, with 0 for the zero time.
func nullableMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return micros(t)
}

// ListReceptions implements tenant.Pipeline.
func (t *Tenant) ListReceptions(ctx context.Context, filter tenant.ReceptionFilter, page master.Page) ([]tenant.Reception, string, error) {
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}
	binding := t.receptionBinding(filter)
	after, err := pageAfterTime(page, listReceptions, binding)
	if err != nil {
		return nil, "", err
	}
	query, args := receptionsQuery(filter, after, page.Limit+1)
	if query == "" {
		if _, err := t.ready(ctx); err != nil {
			return nil, "", err
		}
		return nil, "", nil
	}
	out, err := readRows(ctx, t, "the receptions cannot be listed", query, args, scanReception)
	if err != nil {
		return nil, "", err
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, timeCursor(listReceptions, binding, micros(last.Started), last.ID), nil
}

// checkFence refuses a worker's write whose lease no longer holds: the unit
// is not leased, or its attempt is no longer the fence's, because a claim
// took it over. The check is an update of the unit's row with the attempt
// in its condition, so on a database with several writers the row stays
// locked to the end of the worker's transaction: a takeover waits for it,
// and a write after a takeover finds the condition false.
func checkFence(ctx context.Context, tx *sql.Tx, f tenant.WorkFence) error {
	res, err := tx.ExecContext(ctx, `UPDATE work SET updated = updated WHERE id = ? AND attempt = ? AND state = 'leased'`, f.ID, f.Attempt)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	has, err := exists(ctx, tx, `SELECT 1 FROM work WHERE id = ?`, f.ID)
	if err != nil {
		return err
	}
	if !has {
		return fault.New(fault.NotFound, "the tenant has no unit of work with this id")
	}
	return tenant.LeaseLost()
}

// errStateChanged is the refusal of a step whose reception moved meanwhile.
func errStateChanged() error {
	return fault.Wrap(fault.Conflict, tenant.ErrStateChanged, "the reception is no longer in the state the step started from")
}

// StepReception implements tenant.Pipeline.
func (t *Tenant) StepReception(ctx context.Context, step tenant.ReceptionStep, hook tenant.StepHook) (tenant.Reception, error) {
	if err := step.Validate(); err != nil {
		return tenant.Reception{}, err
	}
	var out tenant.Reception
	err := t.write(ctx, "the reception's step cannot be written", func(tx *sql.Tx, _ *tenantStatements) error {
		if step.Fence != nil {
			if err := checkFence(ctx, tx, *step.Fence); err != nil {
				return err
			}
		}
		before, err := receptionInTx(ctx, tx, step.ID)
		if err != nil {
			return err
		}
		next, err := reception.Step(step.From, step.Event)
		if err != nil {
			if before.State != step.From {
				return errStateChanged()
			}
			return fault.Wrap(fault.Conflict, err, "the reception cannot take this step: "+err.Error())
		}
		c := before.Counts
		if step.Counts != nil {
			c.FilesDuplicate, c.BytesDuplicate = step.Counts.FilesDuplicate, step.Counts.BytesDuplicate
		}
		subject, session, storage, failure := before.SubjectLabel, before.SessionLabel, before.Storage, before.Failure
		if step.Event == reception.Identify {
			has, err := exists(ctx, tx, `SELECT 1 FROM storages WHERE name = ?`, step.Storage)
			if err != nil {
				return err
			}
			if !has {
				return tenant.Precondition("the tenant has no storage named " + step.Storage)
			}
			subject, session, storage = step.SubjectLabel, step.SessionLabel, step.Storage
		}
		if step.Event == reception.Fail {
			failure = step.Failure
		}
		var endedAt any
		if next.Ended() {
			endedAt = micros(step.At)
		}
		// The compare of the compare-and-set is in the statement that
		// changes the row: the state, and the last file time when the step
		// was decided from it. On a database with several writers that is
		// what holds the rule against a batch or a step committed
		// meanwhile; here the one writer holds it as well.
		lastFile := int64(-1)
		if !step.LastFile.IsZero() {
			lastFile = micros(step.LastFile)
		}
		res, err := tx.ExecContext(ctx, `UPDATE receptions SET state = ?, ended = ?, step_at = ?, ended_at = ?,
			files_duplicate = ?, bytes_duplicate = ?, subject_label = ?, session_label = ?, storage = ?, failure = ?
			WHERE id = ? AND state = ? AND (? < 0 OR last_file = ?)`,
			string(next), boolInt(next.Ended()), micros(step.At), endedAt, c.FilesDuplicate, c.BytesDuplicate,
			subject, session, storage, failure, step.ID, string(step.From), lastFile, lastFile)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return errStateChanged()
		}
		if next.Ended() {
			if err := releaseClaims(ctx, tx, step.ID); err != nil {
				return err
			}
		}
		after, err := receptionInTx(ctx, tx, step.ID)
		if err != nil {
			return err
		}
		if hook != nil {
			if err := hook(ctx, before, after); err != nil {
				return err
			}
		}
		out = after
		return nil
	})
	if err != nil {
		return tenant.Reception{}, err
	}
	return out, nil
}

// RetryReception implements tenant.Pipeline.
func (t *Tenant) RetryReception(ctx context.Context, id, newID, receiver string, now time.Time, audit master.AuditEntry, guard tenant.Guard) (tenant.Reception, tenant.Reception, error) {
	for _, v := range []struct{ what, id string }{{"reception id", id}, {"new reception id", newID}} {
		if err := master.ValidateID(v.what, v.id); err != nil {
			return tenant.Reception{}, tenant.Reception{}, err
		}
	}
	if receiver != "" {
		if err := tenant.ValidateName("receiver", receiver); err != nil {
			return tenant.Reception{}, tenant.Reception{}, err
		}
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.Reception{}, tenant.Reception{}, err
	}
	if err := audit.ValidateWith(tenant.DetailRetryReception); err != nil {
		return tenant.Reception{}, tenant.Reception{}, err
	}
	var failed, retry tenant.Reception
	err := t.write(ctx, "the reception cannot be retried", func(tx *sql.Tx, st *tenantStatements) error {
		// The failed reception is marked first, in the statement that
		// checks it may be retried, so two retries at once make one new
		// reception: on a database with several writers the second waits
		// for the first and then finds it retried.
		res, err := tx.ExecContext(ctx, `UPDATE receptions SET retry_reception = ?
			WHERE id = ? AND state = 'failed' AND retry_reception = '' AND discard_reason = ''`, newID, id)
		if err != nil {
			return err
		}
		marked, err := res.RowsAffected()
		if err != nil {
			return err
		}
		f, err := receptionInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if marked == 0 {
			switch {
			case f.RetryReception != "":
				r, err := receptionInTx(ctx, tx, f.RetryReception)
				if err != nil {
					return err
				}
				a, err := addDetails(audit, tenant.DetailRetryReception, r.ID)
				if err != nil {
					return err
				}
				failed, retry = f, r
				return audited(ctx, tx, st, a, guard)
			case f.State != reception.Failed:
				return tenant.Precondition("the reception has not failed")
			default:
				return tenant.Precondition("the reception's files were discarded")
			}
		}
		via := receiver
		if via == "" {
			via = f.Receiver
		}
		has, err := exists(ctx, tx, `SELECT 1 FROM receivers WHERE name = ?`, via)
		if err != nil {
			return err
		}
		if !has && receiver == "" {
			return tenant.Precondition("the reception's receiver has been removed; name the receiver to run it by")
		}
		if !has {
			return tenant.Precondition("the tenant has no receiver named " + receiver)
		}
		if err := releaseClaims(ctx, tx, f.ID); err != nil {
			return err
		}
		inserted := false
		for try := 0; try < 8 && !inserted; try++ {
			if inserted, err = insertReception(ctx, tx, newID, via, f.StudyUID, reception.Complete, f.ID, now); err != nil {
				return err
			}
		}
		if !inserted {
			return fault.New(fault.Unavailable, "receptions of the study kept opening at the same moment; try again")
		}
		// The failed reception's files, as they are in the incoming area,
		// become the new one's; a file it confirmed is held, and the new
		// reception's claim answers it SameFile.
		if _, err := tx.ExecContext(ctx, `INSERT INTO received_files (`+receivedColumns+`)
			SELECT ?, sop_instance_uid, sha256, series_uid, study_uid, sop_class_uid, transfer_syntax_uid, size, incoming,
				calling_ae, called_ae, remote_addr, association, released, ?
			FROM received_files WHERE reception_id = ?`, newID, micros(now), f.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE receptions SET
			files_received = (SELECT count(*) FROM received_files WHERE reception_id = ?1),
			bytes_received = (SELECT coalesce(sum(size), 0) FROM received_files WHERE reception_id = ?1)
			WHERE id = ?1`, newID); err != nil {
			return err
		}
		if failed, err = receptionInTx(ctx, tx, f.ID); err != nil {
			return err
		}
		if retry, err = receptionInTx(ctx, tx, newID); err != nil {
			return err
		}
		a, err := addDetails(audit, tenant.DetailRetryReception, newID)
		if err != nil {
			return err
		}
		return audited(ctx, tx, st, a, guard)
	})
	if err != nil {
		return tenant.Reception{}, tenant.Reception{}, err
	}
	return failed, retry, nil
}

// DiscardReception implements tenant.Pipeline.
func (t *Tenant) DiscardReception(ctx context.Context, id, reason string, now time.Time, audit master.AuditEntry, guard tenant.Guard) (tenant.Reception, error) {
	if err := master.ValidateID("reception id", id); err != nil {
		return tenant.Reception{}, err
	}
	if err := tenant.ValidateDiscardReason(reason); err != nil {
		return tenant.Reception{}, err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.Reception{}, err
	}
	if err := audit.ValidateWith(tenant.DetailFiles, tenant.DetailBytes); err != nil {
		return tenant.Reception{}, err
	}
	var out tenant.Reception
	err := t.write(ctx, "the reception's files cannot be discarded", func(tx *sql.Tx, st *tenantStatements) error {
		// Marked in the statement that checks it may be discarded, so a
		// retry and a discard at once do not both happen.
		res, err := tx.ExecContext(ctx, `UPDATE receptions SET discard_reason = ?, discard_at = ?
			WHERE id = ? AND state = 'failed' AND retry_reception = '' AND discard_reason = ''`, reason, micros(now), id)
		if err != nil {
			return err
		}
		marked, err := res.RowsAffected()
		if err != nil {
			return err
		}
		r, err := receptionInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if marked == 0 {
			switch {
			case r.State != reception.Failed:
				return tenant.Precondition("the reception has not failed")
			case r.RetryReception != "":
				return tenant.Precondition("the reception was retried; its files belong to the reception that runs them")
			}
		}
		if err := releaseClaims(ctx, tx, id); err != nil {
			return err
		}
		var files, bytes int64
		if err := tx.QueryRowContext(ctx, discardCountSQL, id).Scan(&files, &bytes); err != nil {
			return err
		}
		a, err := addDetails(audit, tenant.DetailFiles, strconv.FormatInt(files, 10), tenant.DetailBytes, strconv.FormatInt(bytes, 10))
		if err != nil {
			return err
		}
		out = r
		return audited(ctx, tx, st, a, guard)
	})
	if err != nil {
		return tenant.Reception{}, err
	}
	return out, nil
}

// notKept is the condition, on a received file r, that no quarantine record
// keeps its bytes at the same place: the files a discard removes. The one
// condition serves the count in the audit entry and the list the caller
// removes, so the two agree. quarantine_kept answers it.
const notKept = `NOT EXISTS (SELECT 1 FROM quarantine AS q WHERE q.incoming = r.incoming AND q.content_kept = 1)`

// discardCountSQL counts the files and bytes a discard removes.
const discardCountSQL = `SELECT count(*), coalesce(sum(r.size), 0) FROM received_files AS r WHERE r.reception_id = ? AND ` + notKept

// discardedSQL pages the files a discard removes: the received files'
// primary key.
const discardedSQL = `SELECT ` + receivedColumns + ` FROM received_files AS r
	WHERE r.reception_id = ? AND (r.sop_instance_uid, r.sha256) > (?, ?) AND ` + notKept + `
	ORDER BY r.sop_instance_uid, r.sha256 LIMIT ?`

// DiscardedFiles implements tenant.Pipeline.
func (t *Tenant) DiscardedFiles(ctx context.Context, receptionID string, page master.Page) ([]tenant.ReceivedFile, string, error) {
	if err := master.ValidateID("reception id", receptionID); err != nil {
		return nil, "", err
	}
	r, _, err := t.Reception(ctx, receptionID)
	if err != nil {
		return nil, "", err
	}
	if r.DiscardReason == "" {
		return nil, "", tenant.Precondition("the reception's files were not discarded")
	}
	return t.pageReceived(ctx, listDiscarded, discardedSQL, receptionID, page)
}

// receptionsQuery returns the statement and arguments of one page of
// ListReceptions. It is put together from fixed fragments by which filters
// are set, so each reads the index named for it in 0002_pipeline.sql; every
// value is a parameter. after is the (started, id) of the last row
// returned, or nil. The empty statement means no reception can match.
func receptionsQuery(filter tenant.ReceptionFilter, after []any, limit int) (string, []any) {
	var where []string
	var args []any
	if filter.Receiver != "" {
		where, args = append(where, "receiver = ?"), append(args, filter.Receiver)
	}
	if filter.StudyUID != "" {
		where, args = append(where, "study_uid = ?"), append(args, filter.StudyUID)
	}
	var states []string
	for _, st := range filter.States {
		// With in progress, a step that is an end keeps nothing.
		if !slices.Contains(states, string(st)) && (!filter.InProgress || !st.Ended()) {
			states = append(states, string(st))
		}
	}
	if filter.InProgress && len(filter.States) > 0 && len(states) == 0 {
		// Only ended steps were asked for, with in progress: nothing can
		// match, and no statement is run.
		return "", nil
	}
	if filter.InProgress && len(states) == 0 {
		// The literal matches receptions_in_progress's condition. With
		// steps it is left out: the steps that remain have not ended.
		where = append(where, "ended = 0")
	}
	if !filter.Since.IsZero() {
		where, args = append(where, "started >= ?"), append(args, micros(filter.Since))
	}
	if !filter.Until.IsZero() {
		where, args = append(where, "started < ?"), append(args, micros(filter.Until))
	}
	if after != nil {
		where, args = append(where, "(started, id) < (?, ?)"), append(args, after...)
	}
	const order = " ORDER BY started DESC, id DESC LIMIT ?"
	page := func(where []string) string {
		query := "SELECT " + receptionColumns + " FROM receptions"
		if len(where) > 0 {
			query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- every fragment is a constant above; values are parameters.
		}
		return query + order
	}
	if len(states) <= 1 {
		if len(states) == 1 {
			where, args = append([]string{"state = ?"}, where...), append([]any{states[0]}, args...)
		}
		return page(where), append(args, limit)
	}
	// Several steps: one page per step, each read from receptions_by_state
	// and bounded by the limit, merged and cut to the limit. A single
	// "state IN (...)" would make SQLite walk the time index and test every
	// reception, which costs as much as the receptions are many when the
	// steps asked for are rare.
	parts := make([]string, len(states))
	var all []any
	for i, s := range states {
		parts[i] = "SELECT * FROM (" + page(append([]string{"state = ?"}, where...)) + ")"
		all = append(append(append(all, s), args...), limit)
	}
	return strings.Join(parts, " UNION ALL ") + order, append(all, limit)
}
