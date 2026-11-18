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
// receiver, or makes one with id at the next arrival of the study; the
// literal state matches receptions_open, the partial unique index that
// holds one open reception per receiver and study even across processes.
func openReceptionInTx(ctx context.Context, tx *sql.Tx, receiver, studyUID, id string, at time.Time) (tenant.Reception, bool, error) {
	r, err := scanReception(tx.QueryRowContext(ctx, openReceptionSQL, receiver, studyUID))
	if err == nil {
		return r, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return r, false, err
	}
	has, err := exists(ctx, tx, `SELECT 1 FROM receivers WHERE name = ?`, receiver)
	if err != nil {
		return r, false, err
	}
	if !has {
		return r, false, tenant.Precondition("the tenant has no receiver named " + receiver)
	}
	if err := insertReception(ctx, tx, id, receiver, studyUID, reception.Receiving, "", at); err != nil {
		return r, false, err
	}
	r, err = receptionInTx(ctx, tx, id)
	return r, true, err
}

// insertReception writes a new reception in state at the next arrival of
// its study. The next arrival is read from receptions_arrival, whose unique
// key also refuses two receptions given one number.
func insertReception(ctx context.Context, tx *sql.Tx, id, receiver, studyUID string, state reception.State, retryOf string, at time.Time) error {
	var arrival int
	if err := tx.QueryRowContext(ctx, nextArrivalSQL, studyUID).Scan(&arrival); err != nil {
		return err
	}
	ts := micros(at)
	_, err := tx.ExecContext(ctx, `INSERT INTO receptions (id, receiver, study_uid, arrival, state, ended,
		files_received, bytes_received, files_archived, bytes_archived, files_duplicate, bytes_duplicate,
		files_quarantined, bytes_quarantined, files_released, subject_label, session_label, storage, failure,
		started, last_file, step_at, ended_at, associations, calling_ae_titles, calling_truncated,
		retry_of, retry_reception, discard_reason, discard_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, 0, 0, '', '', '', '', ?, ?, ?, NULL, 0, '[]', 0, ?, '', '', NULL)`,
		id, receiver, studyUID, arrival, string(state), boolInt(state.Ended()), ts, ts, ts, retryOf)
	if isUniqueViolation(err) {
		// The id taken, or, on a database with several writers, another
		// reception of the study opened at the same moment.
		return fault.Wrap(fault.Conflict, err, "a reception with this id or this arrival of the study exists")
	}
	return err
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
// took it over.
func checkFence(ctx context.Context, tx *sql.Tx, f tenant.WorkFence) error {
	var state string
	var attempt int
	err := tx.QueryRowContext(ctx, `SELECT state, attempt FROM work WHERE id = ?`, f.ID).Scan(&state, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return fault.New(fault.NotFound, "the tenant has no unit of work with this id")
	}
	if err != nil {
		return err
	}
	if state != string(tenant.WorkLeased) || attempt != f.Attempt {
		return tenant.LeaseLost()
	}
	return nil
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
		before, err := receptionInTx(ctx, tx, step.ID)
		if err != nil {
			return err
		}
		if before.State != step.From {
			return errStateChanged()
		}
		if !step.LastFile.IsZero() && micros(before.LastFile) != micros(step.LastFile) {
			return errStateChanged()
		}
		next, err := reception.Step(step.From, step.Event)
		if err != nil {
			return fault.Wrap(fault.Conflict, err, "the reception cannot take this step: "+err.Error())
		}
		if step.Fence != nil {
			if err := checkFence(ctx, tx, *step.Fence); err != nil {
				return err
			}
		}
		c := before.Counts
		if step.Counts != nil {
			c.FilesArchived, c.BytesArchived = step.Counts.FilesArchived, step.Counts.BytesArchived
			c.FilesDuplicate, c.BytesDuplicate = step.Counts.FilesDuplicate, step.Counts.BytesDuplicate
		}
		subject, session, storage, failure := before.SubjectLabel, before.SessionLabel, before.Storage, before.Failure
		if step.Event == reception.Identify {
			subject, session, storage = step.SubjectLabel, step.SessionLabel, step.Storage
		}
		if step.Event == reception.Fail {
			failure = step.Failure
		}
		var endedAt any
		if next.Ended() {
			endedAt = micros(step.At)
		}
		// The state in the WHERE is the compare of the compare-and-set: on
		// a database with several writers it is what holds the rule; here
		// the one writer already does.
		res, err := tx.ExecContext(ctx, `UPDATE receptions SET state = ?, ended = ?, step_at = ?, ended_at = ?,
			files_archived = ?, bytes_archived = ?, files_duplicate = ?, bytes_duplicate = ?,
			subject_label = ?, session_label = ?, storage = ?, failure = ? WHERE id = ? AND state = ?`,
			string(next), boolInt(next.Ended()), micros(step.At), endedAt, c.FilesArchived, c.BytesArchived,
			c.FilesDuplicate, c.BytesDuplicate, subject, session, storage, failure, step.ID, string(step.From))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return errStateChanged()
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
		f, err := receptionInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if f.RetryReception != "" {
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
		}
		if f.State != reception.Failed {
			return tenant.Precondition("the reception has not failed")
		}
		if f.DiscardReason != "" {
			return tenant.Precondition("the reception's files were discarded")
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
		if err := insertReception(ctx, tx, newID, via, f.StudyUID, reception.Complete, f.ID, now); err != nil {
			return err
		}
		// The failed reception's files, as they are in the incoming area,
		// become the new one's; a file it archived is found held by the
		// duplicate check and ignored.
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
		if _, err := tx.ExecContext(ctx, `UPDATE receptions SET retry_reception = ? WHERE id = ?`, newID, f.ID); err != nil {
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
		r, err := receptionInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.State != reception.Failed {
			return tenant.Precondition("the reception has not failed")
		}
		if r.RetryReception != "" {
			return tenant.Precondition("the reception was retried; its files belong to the reception that runs them")
		}
		if r.DiscardReason == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE receptions SET discard_reason = ?, discard_at = ? WHERE id = ?`,
				reason, micros(now), id); err != nil {
				return err
			}
			if r, err = receptionInTx(ctx, tx, id); err != nil {
				return err
			}
		}
		c := r.Counts
		files := max(c.FilesReceived-c.FilesArchived-c.FilesDuplicate-c.FilesQuarantined, 0)
		bytes := max(c.BytesReceived-c.BytesArchived-c.BytesDuplicate-c.BytesQuarantined, 0)
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

// receptionsQuery returns the statement and arguments of one page of
// ListReceptions. It is put together from fixed fragments by which filters
// are set, so each reads the index named for it in 0002_pipeline.sql; every
// value is a parameter. after is the (started, id) of the last row
// returned, or nil.
func receptionsQuery(filter tenant.ReceptionFilter, after []any, limit int) (string, []any) {
	var where []string
	var args []any
	if filter.Receiver != "" {
		where, args = append(where, "receiver = ?"), append(args, filter.Receiver)
	}
	if filter.StudyUID != "" {
		where, args = append(where, "study_uid = ?"), append(args, filter.StudyUID)
	}
	if filter.InProgress {
		// The literal matches receptions_in_progress's condition.
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
	var states []string
	for _, s := range filter.States {
		if !slices.Contains(states, string(s)) {
			states = append(states, string(s))
		}
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
