package postgres

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amrshadid/magnetar/internal/sqlkit"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

const quarantineColumns = `id, reason, details, state, receiver, reception_id, calling_ae, called_ae, remote_addr, size,
	sha256, kept_sha256, sop_class_uid, sop_instance_uid, study_uid, series_uid, transfer_syntax_uid, incoming,
	content_kept, at, decided_at, release_mode, release_reception, release_receiver, reject_reason, released_from, release_reason`

// scanQuarantined reads one row of quarantineColumns.
func scanQuarantined(row scanner) (tenant.QuarantinedFile, error) {
	var q tenant.QuarantinedFile
	var reason, details, state, mode string
	var sum, kept []byte
	var at time.Time
	var decided *time.Time
	if err := row.Scan(&q.ID, &reason, &details, &state, &q.Receiver, &q.ReceptionID, &q.Caller.CallingAETitle,
		&q.Caller.CalledAETitle, &q.Caller.Address, &q.Size, &sum, &kept, &q.SOPClassUID, &q.SOPInstanceUID, &q.StudyUID,
		&q.SeriesUID, &q.TransferSyntaxUID, &q.Incoming, &q.ContentKept, &at, &decided, &mode, &q.ReleaseReception,
		&q.ReleaseReceiver, &q.RejectReason, &q.ReleasedFrom, &q.ReleaseReason); err != nil {
		return q, err
	}
	q.Reason, q.State, q.ReleaseMode = tenant.QuarantineReason(reason), tenant.QuarantineState(state), tenant.ReleaseMode(mode)
	q.SHA256, q.KeptSHA256 = sqlkit.ToSHA256(sum), sqlkit.ToSHA256(kept)
	q.Quarantined, q.Decided = at.UTC(), fromNullMicros(decided)
	var err error
	q.Details, err = decodeDetails(details)
	return q, err
}

// quarantinedInTx reads a record inside a change's transaction, with the
// lock given, or NotFound.
func quarantinedInTx(ctx context.Context, tx pgx.Tx, id, lock string) (tenant.QuarantinedFile, error) {
	q, err := scanQuarantined(tx.QueryRow(ctx, `SELECT `+quarantineColumns+` FROM quarantine WHERE id = $1 `+lock, id))
	if isNoRow(err) {
		return q, fault.New(fault.NotFound, "the tenant has no quarantined file with this id")
	}
	return q, err
}

// addTotals adds to the quarantine's running totals, by an UPDATE that adds
// on the row it locks, the last row a change locks (the package's order).
func addTotals(ctx context.Context, tx pgx.Tx, files, bytes int64) error {
	if files == 0 && bytes == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE quarantine_totals SET files = files + $1, bytes = bytes + $2 WHERE only_row`, files, bytes)
	return err
}

// insertQuarantineSQL writes a set of held records in one statement, one
// array for each column given (unnest): twenty parameters for a batch of
// any size. A record whose id is there is skipped; the ids written are
// returned.
const insertQuarantineSQL = `INSERT INTO quarantine (` + quarantineColumns + `)
	SELECT id, reason, details, 'held', receiver, reception_id, calling_ae, called_ae, remote_addr, size, sha256, kept_sha256,
		sop_class_uid, sop_instance_uid, study_uid, series_uid, transfer_syntax_uid, incoming, content_kept, at, NULL,
		'', '', '', '', released_from, ''
	FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::bigint[],
		$10::bytea[], $11::bytea[], $12::text[], $13::text[], $14::text[], $15::text[], $16::text[], $17::text[], $18::boolean[],
		$19::timestamptz[], $20::text[])
		AS u(id, reason, details, receiver, reception_id, calling_ae, called_ae, remote_addr, size, sha256, kept_sha256,
		sop_class_uid, sop_instance_uid, study_uid, series_uid, transfer_syntax_uid, incoming, content_kept, at, released_from)
	ON CONFLICT (id) DO NOTHING RETURNING id`

// HoldFiles implements tenant.Pipeline. It locks the receptions its records
// name FOR NO KEY UPDATE, in order of id,
// as a received batch does, so a file counted received here and a batch of
// the same file are ordered; then writes the records as one set, in order of
// id, and adds to each reception's counts and to the totals last.
func (t *Tenant) HoldFiles(ctx context.Context, files []tenant.QuarantinedFile) (int, error) {
	return t.HoldFilesFenced(ctx, files, nil)
}

// HoldFilesFenced implements tenant.FencedFiles, the unit's row locked
// first as AddReceivedFilesFenced locks it.
func (t *Tenant) HoldFilesFenced(ctx context.Context, files []tenant.QuarantinedFile, fence *tenant.WorkFence) (int, error) {
	if err := tenant.ValidateQuarantineBatch(files); err != nil {
		return 0, err
	}
	if fence != nil {
		if err := fence.Validate(); err != nil {
			return 0, err
		}
	}
	if len(files) == 0 {
		if fence == nil {
			return 0, t.open()
		}
		return 0, t.pipe(ctx, "the quarantined files cannot be recorded", func(tx pgx.Tx) error { return checkFence(ctx, tx, *fence) })
	}
	// Each id once, the first kept, in order of id.
	batch := make([]tenant.QuarantinedFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	var receptions []string
	for _, q := range files {
		if seen[q.ID] {
			continue
		}
		seen[q.ID] = true
		q.State = tenant.QuarantineHeld
		batch = append(batch, q)
		if q.ReceptionID != "" && !slices.Contains(receptions, q.ReceptionID) {
			receptions = append(receptions, q.ReceptionID)
		}
	}
	slices.SortFunc(batch, func(a, b tenant.QuarantinedFile) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(receptions)
	n := len(batch)
	text := func() []string { return make([]string, 0, n) }
	ids, reasons, details, receivers, rids, calling, called, addrs := text(), text(), text(), text(), text(), text(), text(), text()
	classes, uids, studies, series, syntaxes, incoming, from := text(), text(), text(), text(), text(), text(), text()
	sizes := make([]int64, 0, n)
	sums, kept := make([][]byte, 0, n), make([][]byte, 0, n)
	contentKept := make([]bool, 0, n)
	ats := make([]time.Time, 0, n)
	for _, q := range batch {
		d, err := encodeDetails(q.Details)
		if err != nil {
			return 0, err
		}
		var sum, keptSum []byte
		if !q.SHA256.IsZero() {
			sum = sumBytes(q.SHA256)
		}
		if !q.KeptSHA256.IsZero() {
			keptSum = sumBytes(q.KeptSHA256)
		}
		ids, reasons, details, receivers = append(ids, q.ID), append(reasons, string(q.Reason)), append(details, d), append(receivers, q.Receiver)
		rids, calling, called = append(rids, q.ReceptionID), append(calling, q.Caller.CallingAETitle), append(called, q.Caller.CalledAETitle)
		addrs, sizes, sums, kept = append(addrs, q.Caller.Address), append(sizes, q.Size), append(sums, sum), append(kept, keptSum)
		classes, uids, studies = append(classes, q.SOPClassUID), append(uids, q.SOPInstanceUID), append(studies, q.StudyUID)
		series, syntaxes, incoming = append(series, q.SeriesUID), append(syntaxes, q.TransferSyntaxUID), append(incoming, q.Incoming)
		contentKept, ats, from = append(contentKept, q.ContentKept), append(ats, micros(q.Quarantined)), append(from, q.ReleasedFrom)
	}
	var inserted int
	err := t.pipe(ctx, "the quarantined files cannot be recorded", func(tx pgx.Tx) error {
		if fence != nil {
			if err := checkFence(ctx, tx, *fence); err != nil {
				return err
			}
		}
		if len(receptions) > 0 {
			rows, err := tx.Query(ctx, `SELECT id FROM receptions WHERE id = ANY($1::text[]) ORDER BY id FOR NO KEY UPDATE`, receptions)
			locked, err := collect(rows, err, func(row scanner) (string, error) {
				var id string
				err := row.Scan(&id)
				return id, err
			})
			if err != nil {
				return err
			}
			if len(locked) != len(receptions) {
				return tenant.Precondition("a quarantined file names a reception the tenant does not have")
			}
		}
		rows, err := tx.Query(ctx, insertQuarantineSQL, ids, reasons, details, receivers, rids, calling, called, addrs, sizes,
			sums, kept, classes, uids, studies, series, syntaxes, incoming, contentKept, ats, from)
		written, err := collect(rows, err, func(row scanner) (string, error) {
			var id string
			err := row.Scan(&id)
			return id, err
		})
		if err != nil {
			return err
		}
		added := make(map[string]bool, len(written))
		for _, id := range written {
			added[id] = true
		}
		// The new records whose file may have a received file in its
		// reception, looked up as one set by the received files' key.
		var qr, qu []string
		var qs [][]byte
		for _, q := range batch {
			if added[q.ID] && q.ReceptionID != "" && !q.SHA256.IsZero() && q.SOPInstanceUID != "" {
				qr, qu, qs = append(qr, q.ReceptionID), append(qu, q.SOPInstanceUID), append(qs, sumBytes(q.SHA256))
			}
		}
		type recordedKey struct {
			reception string
			k         fileKey
		}
		recorded := map[recordedKey]bool{}
		if len(qr) > 0 {
			rows, err := tx.Query(ctx, `SELECT r.reception_id, r.sop_instance_uid, r.sha256 FROM received_files AS r
				JOIN unnest($1::text[], $2::text[], $3::bytea[]) AS c(rid, uid, sha)
				ON r.reception_id = c.rid AND r.sop_instance_uid = c.uid AND r.sha256 = c.sha`, qr, qu, qs)
			keys, err := collect(rows, err, func(row scanner) (recordedKey, error) {
				var k recordedKey
				var sum []byte
				err := row.Scan(&k.reception, &k.k.uid, &sum)
				k.k.sum = sqlkit.ToSHA256(sum)
				return k, err
			})
			if err != nil {
				return err
			}
			for _, k := range keys {
				recorded[k] = true
			}
		}
		type delta struct{ files, bytes, receivedFiles, receivedBytes int64 }
		perReception := map[string]delta{}
		var heldFiles, heldBytes int64
		for _, q := range batch {
			if !added[q.ID] {
				continue
			}
			heldFiles++
			heldBytes += q.HeldBytes()
			if q.ReceptionID == "" {
				continue
			}
			d := perReception[q.ReceptionID]
			d.files, d.bytes = d.files+1, d.bytes+q.Size
			// A file that arrived and has no received file in its reception
			// (too large, not DICOM, no UIDs) is counted received here, once,
			// as the record is made once.
			if !recorded[recordedKey{q.ReceptionID, fileKey{q.SOPInstanceUID, q.SHA256}}] {
				d.receivedFiles, d.receivedBytes = d.receivedFiles+1, d.receivedBytes+q.Size
			}
			perReception[q.ReceptionID] = d
		}
		for _, id := range receptions {
			d, ok := perReception[id]
			if !ok {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE receptions SET files_quarantined = files_quarantined + $1,
				bytes_quarantined = bytes_quarantined + $2, files_received = files_received + $3, bytes_received = bytes_received + $4
				WHERE id = $5`, d.files, d.bytes, d.receivedFiles, d.receivedBytes, id); err != nil {
				return err
			}
		}
		if err := addTotals(ctx, tx, heldFiles, heldBytes); err != nil {
			return err
		}
		inserted = len(written)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// QuarantinedFile implements tenant.Pipeline.
func (t *Tenant) QuarantinedFile(ctx context.Context, id string) (tenant.QuarantinedFile, error) {
	if err := master.ValidateID("quarantine id", id); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	return readOne(ctx, t, "the tenant has no quarantined file with this id", "the quarantined file cannot be read",
		`SELECT `+quarantineColumns+` FROM quarantine WHERE id = $1`, []any{id}, scanQuarantined)
}

// ListQuarantined implements tenant.Pipeline.
func (t *Tenant) ListQuarantined(ctx context.Context, filter tenant.QuarantineFilter, page master.Page) ([]tenant.QuarantinedFile, string, error) {
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}
	binding := t.binding(string(filter.State), string(filter.Reason), filter.Receiver, filter.ReceptionID, filter.StudyUID,
		filter.SOPInstanceUID, sqlkit.MicrosText(filter.Since), sqlkit.MicrosText(filter.Until))
	after, err := sqlkit.PageAfterTime(page, listQuarantine, binding)
	if err != nil {
		return nil, "", err
	}
	query, args := quarantineQuery(filter, after, page.Limit+1)
	out, err := readRows(ctx, t, "the quarantined files cannot be listed", query, args, scanQuarantined)
	if err != nil {
		return nil, "", err
	}
	out, last, more := sqlkit.Cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.TimeCursor(listQuarantine, binding, last.Quarantined, last.ID), nil
}

// ReleaseQuarantined implements tenant.Pipeline. Its locks, in the
// package's order: the record FOR NO KEY UPDATE, read before anything is
// decided, so a decision made meanwhile is seen; the reception the file
// joins, FOR NO KEY UPDATE, before its received file is written
// (joinReception); the totals last.
func (t *Tenant) ReleaseQuarantined(ctx context.Context, id string, mode tenant.ReleaseMode, receiver, receptionID string, now time.Time, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	return t.ReleaseFile(ctx, tenant.Release{ID: id, Mode: mode, Receiver: receiver, ReceptionID: receptionID, Now: now}, audit, guard)
}

// ReleaseFile implements tenant.QuarantineReleases, with ReleaseQuarantined's
// locks.
func (t *Tenant) ReleaseFile(ctx context.Context, rel tenant.Release, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	if err := rel.Validate(); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	if err := audit.ValidateWith(tenant.DetailReleaseReception); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	id, mode, receiver, receptionID, now := rel.ID, rel.Mode, rel.Receiver, rel.ReceptionID, rel.Now
	var out tenant.QuarantinedFile
	err := t.pipe(ctx, "the quarantined file cannot be released", func(tx pgx.Tx) error {
		q, err := quarantinedInTx(ctx, tx, id, "FOR NO KEY UPDATE")
		if err != nil {
			return err
		}
		switch q.State {
		case tenant.QuarantineRejected:
			return tenant.Precondition("the file was rejected")
		case tenant.QuarantineReleased:
			a, err := releaseEntry(audit, q.ReleaseReception)
			if err != nil {
				return err
			}
			out = q
			return t.audited(ctx, tx, a, guard)
		}
		if q.Reason == tenant.QuarantineDifferentFile && q.SOPInstanceUID != "" {
			// Released as it is, it would only be quarantined again while
			// the tenant holds an instance under its UID; replacing that
			// instance deletes archived data, which reception never does.
			// A held instance is never removed, so a read is enough.
			held, err := exists(ctx, tx, `SELECT 1 FROM instances WHERE sop_instance_uid = $1 AND state = 'held'`, q.SOPInstanceUID)
			if err != nil {
				return err
			}
			if held {
				return tenant.Precondition("the tenant holds an instance under this file's SOP Instance UID; in this version the file can only be rejected")
			}
		}
		if !q.ContentKept {
			return tenant.Precondition("the file's content was never kept")
		}
		via := receiver
		if via == "" {
			via = q.Receiver
		}
		has, err := receiverExists(ctx, tx, via)
		if err != nil {
			return err
		}
		if !has && receiver == "" {
			return tenant.Precondition("the receiver the file came to has been removed; name the receiver to run it by")
		}
		if !has {
			return tenant.Precondition("the tenant has no receiver named " + receiver)
		}
		joined := ""
		if q.Receivable() {
			r, err := joinReception(ctx, tx, via, q.StudyUID, receptionID, now)
			if err != nil {
				return err
			}
			joined = r.ID
			f := tenant.ReceivedFile{ReceptionID: r.ID, SOPInstanceUID: q.SOPInstanceUID, SHA256: q.SHA256, SeriesUID: q.SeriesUID,
				StudyUID: q.StudyUID, SOPClassUID: q.SOPClassUID, TransferSyntaxUID: q.TransferSyntaxUID, Size: q.Size,
				Incoming: q.Incoming, Caller: q.Caller, Released: true}
			added, err := insertReceived(ctx, tx, []tenant.ReceivedFile{f}, now)
			if err != nil {
				return err
			}
			switch {
			case r.ID == q.ReceptionID:
				// Back into the reception it was quarantined from, which
				// counted it received already (by its received file or by its
				// hold): released there, and no longer quarantined.
				if _, err := tx.Exec(ctx, `UPDATE received_files SET released = true
					WHERE reception_id = $1 AND sop_instance_uid = $2 AND sha256 = $3`, r.ID, q.SOPInstanceUID, sumBytes(q.SHA256)); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE receptions SET files_released = files_released + 1,
					files_quarantined = files_quarantined - 1, bytes_quarantined = bytes_quarantined - $1, last_file = greatest(last_file, $2)
					WHERE id = $3`, q.Size, micros(now), r.ID); err != nil {
					return err
				}
			case len(added) == 1:
				if _, err := tx.Exec(ctx, `UPDATE receptions SET files_received = files_received + 1,
					bytes_received = bytes_received + $1, files_released = files_released + 1, last_file = greatest(last_file, $2)
					WHERE id = $3`, q.Size, micros(now), r.ID); err != nil {
					return err
				}
			}
		}
		// The record's state is in the statement that changes it, as well
		// as locked above, so two decisions at once make one.
		n, err := rowsChanged(tx.Exec(ctx, `UPDATE quarantine SET state = 'released', content_kept = false, decided_at = $1,
			release_mode = $2, release_reception = $3, release_receiver = $4, release_reason = $5 WHERE id = $6 AND state = 'held'`,
			micros(now), string(mode), joined, via, rel.Reason, id))
		if err != nil {
			return err
		}
		if n != 1 {
			return fault.New(fault.Unavailable, "the quarantined file was decided at the same moment; try again")
		}
		if err := addTotals(ctx, tx, -1, -q.HeldBytes()); err != nil {
			return err
		}
		if out, err = quarantinedInTx(ctx, tx, id, ""); err != nil {
			return err
		}
		a, err := releaseEntry(audit, joined)
		if err != nil {
			return err
		}
		return t.audited(ctx, tx, a, guard)
	})
	if err != nil {
		return tenant.QuarantinedFile{}, err
	}
	return out, nil
}

// releaseEntry returns a release's audit entry with the reception the file
// joined, when it joined one.
func releaseEntry(audit master.AuditEntry, joined string) (master.AuditEntry, error) {
	if joined == "" {
		return audit, nil
	}
	return sqlkit.AddDetails(audit, tenant.DetailReleaseReception, joined)
}

// joinReception finds or opens the reception a released file joins
// (openReceptionInTx) and locks its row FOR NO KEY UPDATE in the statement
// that checks it is still receiving, as a received batch does. A reception
// found receiving whose quiet step committed while this change waited for
// its row is receiving no more, and the file belongs to the next arrival:
// the next round, a statement of its own, does not find it receiving and
// opens that arrival.
func joinReception(ctx context.Context, tx pgx.Tx, via, studyUID, receptionID string, now time.Time) (tenant.Reception, error) {
	for range openTries {
		r, _, err := openReceptionInTx(ctx, tx, via, studyUID, receptionID, now)
		if err != nil {
			return r, err
		}
		receiving, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = $1 AND state = 'receiving' FOR NO KEY UPDATE`, r.ID)
		if err != nil || receiving {
			return r, err
		}
	}
	return tenant.Reception{}, fault.New(fault.Unavailable, "receptions of the study kept closing at the same moment; try again")
}

// RejectQuarantined implements tenant.Pipeline. The record's row is read FOR
// NO KEY UPDATE before anything is decided, so a decision made meanwhile is
// seen.
func (t *Tenant) RejectQuarantined(ctx context.Context, id, reason string, now time.Time, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	if err := master.ValidateID("quarantine id", id); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	if err := tenant.ValidateDiscardReason(reason); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	if err := master.ValidateTime("time", now); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	if err := audit.ValidateWith(tenant.DetailReason, tenant.DetailSize, tenant.DetailSHA256); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	var out tenant.QuarantinedFile
	err := t.pipe(ctx, "the quarantined file cannot be rejected", func(tx pgx.Tx) error {
		q, err := quarantinedInTx(ctx, tx, id, "FOR NO KEY UPDATE")
		if err != nil {
			return err
		}
		switch q.State {
		case tenant.QuarantineReleased:
			return tenant.Precondition("the file was released")
		case tenant.QuarantineHeld:
			n, err := rowsChanged(tx.Exec(ctx, `UPDATE quarantine SET state = 'rejected', content_kept = false, decided_at = $1,
				reject_reason = $2 WHERE id = $3 AND state = 'held'`, micros(now), reason, id))
			if err != nil {
				return err
			}
			if n != 1 {
				return fault.New(fault.Unavailable, "the quarantined file was decided at the same moment; try again")
			}
			if err := addTotals(ctx, tx, -1, -q.HeldBytes()); err != nil {
				return err
			}
			if q, err = quarantinedInTx(ctx, tx, id, ""); err != nil {
				return err
			}
		}
		a, err := sqlkit.AddDetails(audit, tenant.DetailReason, string(q.Reason), tenant.DetailSize, strconv.FormatInt(q.Size, 10),
			tenant.DetailSHA256, q.SHA256.String())
		if err != nil {
			return err
		}
		out = q
		return t.audited(ctx, tx, a, guard)
	})
	if err != nil {
		return tenant.QuarantinedFile{}, err
	}
	return out, nil
}

// QuarantineTotals implements tenant.Pipeline.
func (t *Tenant) QuarantineTotals(ctx context.Context) (int64, int64, error) {
	type totals struct{ files, bytes int64 }
	v, err := readOne(ctx, t, "the quarantine has no totals", "the quarantine's totals cannot be read",
		`SELECT files, bytes FROM quarantine_totals WHERE only_row`, nil, func(row scanner) (totals, error) {
			var v totals
			err := row.Scan(&v.files, &v.bytes)
			return v, err
		})
	return v.files, v.bytes, err
}

// quarantineQuery returns the statement and arguments of one page of
// ListQuarantined, put together from fixed fragments by which filters are
// set, so each reads its index in 0002_pipeline.sql; every value is a
// parameter.
func quarantineQuery(filter tenant.QuarantineFilter, after *sqlkit.TimePosition, limit int) (string, []any) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	for _, eq := range []struct{ column, value string }{
		{"state", string(filter.State)}, {"reason", string(filter.Reason)}, {"receiver", filter.Receiver},
		{"reception_id", filter.ReceptionID}, {"study_uid", filter.StudyUID}, {"sop_instance_uid", filter.SOPInstanceUID},
	} {
		if eq.value != "" {
			where = append(where, eq.column+" = "+arg(eq.value))
		}
	}
	if !filter.Since.IsZero() {
		where = append(where, "at >= "+arg(micros(filter.Since)))
	}
	if !filter.Until.IsZero() {
		where = append(where, "at < "+arg(micros(filter.Until)))
	}
	if after != nil {
		where = append(where, "(at, id) < ("+arg(after.At)+", "+arg(after.ID)+")")
	}
	query := "SELECT " + quarantineColumns + " FROM quarantine"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- every fragment is a constant above; values are parameters.
	}
	return query + " ORDER BY at DESC, id DESC LIMIT " + arg(limit), args
}
