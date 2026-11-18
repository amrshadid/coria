package postgres

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/sqlkit"
	"github.com/amrshadid/magnetar/internal/tenant"
)

const receptionColumns = `id, receiver, study_uid, arrival, state, files_received, bytes_received, files_archived,
	bytes_archived, files_duplicate, bytes_duplicate, files_quarantined, bytes_quarantined, files_released,
	subject_label, session_label, storage, failure, started, last_file, step_at, ended_at, associations,
	calling_ae_titles, calling_truncated, retry_of, retry_reception, discard_reason, discard_at`

// scanReception reads one row of receptionColumns.
func scanReception(row scanner) (tenant.Reception, error) {
	var r tenant.Reception
	var state, titles string
	var started, lastFile, stepAt time.Time
	var endedAt, discardAt *time.Time
	c := &r.Counts
	if err := row.Scan(&r.ID, &r.Receiver, &r.StudyUID, &r.Arrival, &state, &c.FilesReceived, &c.BytesReceived,
		&c.FilesArchived, &c.BytesArchived, &c.FilesDuplicate, &c.BytesDuplicate, &c.FilesQuarantined, &c.BytesQuarantined,
		&c.FilesReleased, &r.SubjectLabel, &r.SessionLabel, &r.Storage, &r.Failure, &started, &lastFile, &stepAt, &endedAt,
		&r.Associations, &titles, &r.CallingAETitlesTruncated, &r.RetryOf, &r.RetryReception, &r.DiscardReason, &discardAt); err != nil {
		return r, err
	}
	r.State = reception.State(state)
	r.Started, r.LastFile, r.StepTime = started.UTC(), lastFile.UTC(), stepAt.UTC()
	r.Ended, r.DiscardTime = fromNullMicros(endedAt), fromNullMicros(discardAt)
	if err := fromJSON(titles, &r.CallingAETitles); err != nil {
		return r, err
	}
	if len(r.CallingAETitles) == 0 {
		r.CallingAETitles = nil
	}
	return r, nil
}

// errNoReception is the NotFound of a reception the tenant does not have.
func errNoReception() error {
	return fault.New(fault.NotFound, "the tenant has no reception with this id")
}

// receptionInTx reads a reception inside a change's transaction, with the
// lock given ("" for none), or NotFound.
func receptionInTx(ctx context.Context, tx pgx.Tx, id, lock string) (tenant.Reception, error) {
	r, err := scanReception(tx.QueryRow(ctx, `SELECT `+receptionColumns+` FROM receptions WHERE id = $1 `+lock, id))
	if isNoRow(err) {
		return r, errNoReception()
	}
	return r, err
}

// openTries bounds the rounds of openReceptionInTx and of a retry's insert.
// Each round that inserts nothing met a row another transaction committed
// meanwhile (the open reception, or the arrival number), so every round makes
// progress; the bound is generous for many receivers opening one study at
// once, and only keeps a broken table from looping forever.
const openTries = 64

// openReceptionInTx finds the reception of the study receiving at the
// receiver, or makes one with id at the next arrival of the study. Two opens at
// once meet at receptions_open, the partial unique index that holds one open
// reception per receiver and study, or at receptions_arrival: the second
// insert waits for the first's commit and then does nothing (ON CONFLICT DO
// NOTHING), and the next round, in statements of its own that see what was
// committed, finds the reception that won or the next free arrival number.
func openReceptionInTx(ctx context.Context, tx pgx.Tx, receiver, studyUID, id string, at time.Time) (tenant.Reception, bool, error) {
	has, err := receiverExists(ctx, tx, receiver)
	if err != nil {
		return tenant.Reception{}, false, err
	}
	if !has {
		return tenant.Reception{}, false, tenant.Precondition("the tenant has no receiver named " + receiver)
	}
	for range openTries {
		r, err := scanReception(tx.QueryRow(ctx, openReceptionSQL, receiver, studyUID))
		if err == nil {
			return r, false, nil
		}
		if !isNoRow(err) {
			return r, false, err
		}
		inserted, err := insertReception(ctx, tx, id, receiver, studyUID, reception.Receiving, "", at)
		if err != nil {
			return tenant.Reception{}, false, err
		}
		if inserted {
			r, err = receptionInTx(ctx, tx, id, "")
			return r, true, err
		}
	}
	return tenant.Reception{}, false, fault.New(fault.Unavailable, "receptions of the study kept opening at the same moment; try again")
}

// insertReception writes a new reception in state at the next arrival of
// its study, and reports whether it did: a row it meets under one of the
// unique keys (its id, the open reception of its receiver and study, the
// arrival number another took meanwhile) leaves nothing written. An id taken
// by another reception is a Conflict, since ids are the caller's to make
// unique.
func insertReception(ctx context.Context, tx pgx.Tx, id, receiver, studyUID string, state reception.State, retryOf string, at time.Time) (bool, error) {
	var arrival int
	if err := tx.QueryRow(ctx, nextArrivalSQL, studyUID).Scan(&arrival); err != nil {
		return false, err
	}
	var endedAt any
	if state.Ended() {
		endedAt = micros(at)
	}
	n, err := rowsChanged(tx.Exec(ctx, `INSERT INTO receptions (id, receiver, study_uid, arrival, state, ended,
		files_received, bytes_received, files_archived, bytes_archived, files_duplicate, bytes_duplicate,
		files_quarantined, bytes_quarantined, files_released, subject_label, session_label, storage, failure,
		started, last_file, step_at, ended_at, associations, calling_ae_titles, calling_truncated,
		retry_of, retry_reception, discard_reason, discard_at)
		VALUES ($1, $2, $3, $4, $5, $6, 0, 0, 0, 0, 0, 0, 0, 0, 0, '', '', '', '', $7, $7, $7, $8, 0, '[]', false, $9, '', '', NULL)
		ON CONFLICT DO NOTHING`,
		id, receiver, studyUID, arrival, string(state), state.Ended(), micros(at), endedAt, retryOf))
	if err != nil || n == 1 {
		return n == 1, err
	}
	taken, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = $1`, id)
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
	err := t.pipe(ctx, "the reception cannot be opened", func(tx pgx.Tx) error {
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
		`SELECT `+receptionColumns+` FROM receptions WHERE id = $1`, []any{id}, scanReception)
	if err != nil {
		return tenant.Reception{}, nil, err
	}
	arrivals, err := readRows(ctx, t, "the reception's arrivals cannot be read", arrivalsSQL,
		[]any{r.StudyUID, tenant.MaxArrivals}, func(row scanner) (tenant.Arrival, error) {
			var a tenant.Arrival
			var state string
			var started time.Time
			err := row.Scan(&a.ID, &a.Arrival, &state, &started)
			a.State, a.Started = reception.State(state), started.UTC()
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
		microsText(f.Since), microsText(f.Until))
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
		return nil, "", t.open()
	}
	out, err := readRows(ctx, t, "the receptions cannot be listed", query, args, scanReception)
	if err != nil {
		return nil, "", err
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, timeCursor(listReceptions, binding, last.Started, last.ID), nil
}

// checkFence refuses a worker's write whose lease no longer holds: the unit
// is not leased, or its attempt is no longer the fence's, because a claim
// took it over. The check is an UPDATE of the unit's row with the attempt in
// its condition, so the row stays locked (FOR NO KEY UPDATE) to the end of
// the worker's transaction: a takeover skips it while it is held (a claim
// takes rows with SKIP LOCKED) or comes after and changes the attempt, and a
// write after a takeover finds the condition false. A unit's row is the
// first row any transaction locks (the package documentation's order).
func checkFence(ctx context.Context, tx pgx.Tx, f tenant.WorkFence) error {
	n, err := rowsChanged(tx.Exec(ctx, `UPDATE work SET updated = updated WHERE id = $1 AND attempt = $2 AND state = 'leased'`, f.ID, f.Attempt))
	if err != nil || n == 1 {
		return err
	}
	has, err := exists(ctx, tx, `SELECT 1 FROM work WHERE id = $1`, f.ID)
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

// StepReception implements tenant.Pipeline. The reception's row is read FOR
// NO KEY UPDATE, the lock the step's UPDATE takes, before anything is
// decided, so the state and last file time compared are those no other step
// or batch can change until this one commits; the compare is also in the
// UPDATE's condition. An identify then holds its storage's row FOR SHARE
// (lockStorageShared), after the reception's, in the package's order.
func (t *Tenant) StepReception(ctx context.Context, step tenant.ReceptionStep, hook tenant.StepHook) (tenant.Reception, error) {
	if err := step.Validate(); err != nil {
		return tenant.Reception{}, err
	}
	var out tenant.Reception
	err := t.pipe(ctx, "the reception's step cannot be written", func(tx pgx.Tx) error {
		if step.Fence != nil {
			if err := checkFence(ctx, tx, *step.Fence); err != nil {
				return err
			}
		}
		before, err := receptionInTx(ctx, tx, step.ID, "FOR NO KEY UPDATE")
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
		if before.State != step.From || (!step.LastFile.IsZero() && !before.LastFile.Equal(micros(step.LastFile))) {
			return errStateChanged()
		}
		c := before.Counts
		if step.Counts != nil {
			c.FilesDuplicate, c.BytesDuplicate = step.Counts.FilesDuplicate, step.Counts.BytesDuplicate
		}
		subject, session, storage, failure := before.SubjectLabel, before.SessionLabel, before.Storage, before.Failure
		if step.Event == reception.Identify {
			if err := lockStorageShared(ctx, tx, step.Storage); err != nil {
				return err
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
		n, err := rowsChanged(tx.Exec(ctx, `UPDATE receptions SET state = $1, ended = $2, step_at = $3, ended_at = $4,
			files_duplicate = $5, bytes_duplicate = $6, subject_label = $7, session_label = $8, storage = $9, failure = $10
			WHERE id = $11 AND state = $12`,
			string(next), next.Ended(), micros(step.At), endedAt, c.FilesDuplicate, c.BytesDuplicate,
			subject, session, storage, failure, step.ID, string(step.From)))
		if err != nil {
			return err
		}
		if n != 1 {
			return errStateChanged()
		}
		if next.Ended() {
			if err := releaseClaims(ctx, tx, step.ID); err != nil {
				return err
			}
		}
		after, err := receptionInTx(ctx, tx, step.ID, "")
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

// RetryReception implements tenant.Pipeline. The failed reception is marked
// in the statement that checks it may be retried, so two retries at once
// make one new reception: the second waits for the first's row lock and then
// finds it retried.
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
	err := t.pipe(ctx, "the reception cannot be retried", func(tx pgx.Tx) error {
		// A reception's receiver never changes, so it is read before the
		// row is marked.
		var original string
		if err := tx.QueryRow(ctx, `SELECT receiver FROM receptions WHERE id = $1`, id).Scan(&original); err != nil {
			if isNoRow(err) {
				return errNoReception()
			}
			return err
		}
		via := receiver
		if via == "" {
			via = original
		}
		has, err := receiverExists(ctx, tx, via)
		if err != nil {
			return err
		}
		marked, err := rowsChanged(tx.Exec(ctx, `UPDATE receptions SET retry_reception = $1
			WHERE id = $2 AND state = 'failed' AND retry_reception = '' AND discard_reason = ''`, newID, id))
		if err != nil {
			return err
		}
		f, err := receptionInTx(ctx, tx, id, "")
		if err != nil {
			return err
		}
		if marked == 0 {
			switch {
			case f.RetryReception != "":
				r, err := receptionInTx(ctx, tx, f.RetryReception, "")
				if err != nil {
					return err
				}
				a, err := addDetails(audit, tenant.DetailRetryReception, r.ID)
				if err != nil {
					return err
				}
				failed, retry = f, r
				return t.audited(ctx, tx, a, guard)
			case f.State != reception.Failed:
				return tenant.Precondition("the reception has not failed")
			default:
				return tenant.Precondition("the reception's files were discarded")
			}
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
		for try := 0; try < openTries && !inserted; try++ {
			if inserted, err = insertReception(ctx, tx, newID, via, f.StudyUID, reception.Complete, f.ID, now); err != nil {
				return err
			}
		}
		if !inserted {
			return fault.New(fault.Unavailable, "receptions of the study kept opening at the same moment; try again")
		}
		// The failed reception's files, as they are in the incoming area,
		// become the new one's, with the counts in the same statement's
		// transaction; a file it confirmed is held, and the new reception's
		// claim answers it SameFile.
		if _, err := tx.Exec(ctx, `INSERT INTO received_files (`+receivedColumns+`)
			SELECT $1, sop_instance_uid, sha256, series_uid, study_uid, sop_class_uid, transfer_syntax_uid, size, incoming,
				calling_ae, called_ae, remote_addr, association, released, $2
			FROM received_files WHERE reception_id = $3`, newID, micros(now), f.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE receptions AS p SET files_received = c.n, bytes_received = c.b
			FROM (SELECT count(*) AS n, coalesce(sum(size), 0)::bigint AS b FROM received_files WHERE reception_id = $1) AS c
			WHERE p.id = $1`, newID); err != nil {
			return err
		}
		if failed, err = receptionInTx(ctx, tx, f.ID, ""); err != nil {
			return err
		}
		if retry, err = receptionInTx(ctx, tx, newID, ""); err != nil {
			return err
		}
		a, err := addDetails(audit, tenant.DetailRetryReception, newID)
		if err != nil {
			return err
		}
		return t.audited(ctx, tx, a, guard)
	})
	if err != nil {
		return tenant.Reception{}, tenant.Reception{}, err
	}
	return failed, retry, nil
}

// DiscardReception implements tenant.Pipeline. The reception is marked in
// the statement that checks it may be discarded, so a retry and a discard at
// once do not both happen: the second waits for the first's row lock and
// then finds its condition false.
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
	err := t.pipe(ctx, "the reception's files cannot be discarded", func(tx pgx.Tx) error {
		marked, err := rowsChanged(tx.Exec(ctx, `UPDATE receptions SET discard_reason = $1, discard_at = $2
			WHERE id = $3 AND state = 'failed' AND retry_reception = '' AND discard_reason = ''`, reason, micros(now), id))
		if err != nil {
			return err
		}
		r, err := receptionInTx(ctx, tx, id, "")
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
		if err := tx.QueryRow(ctx, discardCountSQL, id).Scan(&files, &bytes); err != nil {
			return err
		}
		a, err := addDetails(audit, tenant.DetailFiles, strconv.FormatInt(files, 10), tenant.DetailBytes, strconv.FormatInt(bytes, 10))
		if err != nil {
			return err
		}
		out = r
		return t.audited(ctx, tx, a, guard)
	})
	if err != nil {
		return tenant.Reception{}, err
	}
	return out, nil
}

// giveUp takes a reception whose unit of work was held as failed through the
// Fail step, in the transaction that held the unit, with
// tenant.FailureWorkGaveUp, and releases its claims, so no claim outlives
// every worker that could end its reception. The reception's row is read
// FOR NO KEY UPDATE first, after the unit's (the package's order). A
// reception that has ended, or is not there, is left as it is. The step is
// the core's (reception.Step).
func giveUp(ctx context.Context, tx pgx.Tx, receptionID string, now time.Time) error {
	if receptionID == "" {
		return nil
	}
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM receptions WHERE id = $1 FOR NO KEY UPDATE`, receptionID).Scan(&state)
	if isNoRow(err) {
		return nil
	}
	if err != nil {
		return err
	}
	next, refused := reception.Step(reception.State(state), reception.Fail)
	if refused != nil {
		// Ended already: nothing is held for it, and the unit is still held
		// as failed.
		return nil
	}
	n, err := rowsChanged(tx.Exec(ctx, `UPDATE receptions SET state = $1, ended = $2, step_at = $3, ended_at = $3, failure = $4
		WHERE id = $5 AND state = $6`, string(next), next.Ended(), micros(now), tenant.FailureWorkGaveUp, receptionID, state))
	if err != nil || n != 1 {
		return err
	}
	return releaseClaims(ctx, tx, receptionID)
}

// ReceptionsWithoutWork implements tenant.Pipeline.
func (t *Tenant) ReceptionsWithoutWork(ctx context.Context, page master.Page) ([]tenant.Reception, string, error) {
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	binding := t.binding()
	afterAt, afterID := time.UnixMicro(0).UTC(), ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listWithoutWork, binding, 2)
		if err != nil {
			return nil, "", err
		}
		at, err := sqlkit.DecodeCursorInt(f[0])
		if err != nil {
			return nil, "", err
		}
		afterAt, afterID = microsTime(at), f[1]
	} else {
		// Before every reception: a start time is after 1970
		// (master.ValidateTime), so the first page starts below it.
		afterAt = afterAt.Add(-time.Microsecond)
	}
	out, err := readRows(ctx, t, "the receptions without work cannot be listed", receptionsWithoutWorkSQL,
		[]any{afterAt, afterID, page.Limit + 1}, scanReception)
	if err != nil {
		return nil, "", err
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, timeCursor(listWithoutWork, binding, last.Started, last.ID), nil
}

// notKept is the condition, on a received file r, that no quarantine record
// keeps its bytes at the same place: the files a discard removes. The one
// condition serves the count in the audit entry and the list the caller
// removes, so the two agree. quarantine_kept answers it.
const notKept = `NOT EXISTS (SELECT 1 FROM quarantine AS q WHERE q.incoming = r.incoming AND q.content_kept)`

// discardCountSQL counts the files and bytes a discard removes.
const discardCountSQL = `SELECT count(*), coalesce(sum(r.size), 0)::bigint FROM received_files AS r WHERE r.reception_id = $1 AND ` + notKept

// discardedSQL pages the files a discard removes: the received files'
// primary key.
const discardedSQL = `SELECT ` + receivedColumns + ` FROM received_files AS r
	WHERE r.reception_id = $1 AND (r.sop_instance_uid, r.sha256) > ($2, $3) AND ` + notKept + `
	ORDER BY r.sop_instance_uid, r.sha256 LIMIT $4`

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
func receptionsQuery(filter tenant.ReceptionFilter, after *timePosition, limit int) (string, []any) {
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
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	// where returns the conditions of one part, with the state given ("" for
	// none). Each part numbers its own parameters, so the parts of a union
	// share none and each reads its index with its own values.
	where := func(state string) string {
		var w []string
		if state != "" {
			w = append(w, "state = "+arg(state))
		}
		if filter.Receiver != "" {
			w = append(w, "receiver = "+arg(filter.Receiver))
		}
		if filter.StudyUID != "" {
			w = append(w, "study_uid = "+arg(filter.StudyUID))
		}
		if filter.InProgress && len(states) == 0 {
			// The literal is receptions_in_progress's own condition. With
			// steps it is left out: the steps that remain have not ended.
			w = append(w, "NOT ended")
		}
		if !filter.Since.IsZero() {
			w = append(w, "started >= "+arg(micros(filter.Since)))
		}
		if !filter.Until.IsZero() {
			w = append(w, "started < "+arg(micros(filter.Until)))
		}
		if after != nil {
			w = append(w, "(started, id) < ("+arg(after.at)+", "+arg(after.id)+")")
		}
		if len(w) == 0 {
			return ""
		}
		return " WHERE " + strings.Join(w, " AND ") // #nosec G202 -- every fragment is a constant above; values are parameters.
	}
	const order = " ORDER BY started DESC, id DESC LIMIT "
	if len(states) <= 1 {
		state := ""
		if len(states) == 1 {
			state = states[0]
		}
		q := "SELECT " + receptionColumns + " FROM receptions" + where(state)
		return q + order + arg(limit), args
	}
	// Several steps: one part per step, each read from its index in order
	// and cut to the limit, then merged and cut again. A single
	// "state IN (...)" reads one index in state order, and the rows of all
	// the steps asked for are then sorted whole, which costs as much as
	// those receptions are many.
	parts := make([]string, len(states))
	for i, s := range states {
		parts[i] = "(SELECT " + receptionColumns + " FROM receptions" + where(s) + order + arg(limit) + ")"
	}
	return strings.Join(parts, " UNION ALL ") + order + arg(limit), args
}

// releaseClaims removes the claims a reception did not confirm. A held
// instance is never touched.
func releaseClaims(ctx context.Context, tx pgx.Tx, receptionID string) error {
	_, err := tx.Exec(ctx, releaseClaimedSQL, receptionID)
	return err
}
