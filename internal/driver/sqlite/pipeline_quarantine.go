package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/amrshadid/magnetar/internal/core/uid"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

const quarantineColumns = `id, reason, details, state, receiver, reception_id, calling_ae, called_ae, remote_addr, size,
	sha256, kept_sha256, sop_class_uid, sop_instance_uid, study_uid, series_uid, transfer_syntax_uid, incoming,
	content_kept, at, decided_at, release_mode, release_reception, release_receiver, reject_reason, released_from`

// scanQuarantined reads one row of quarantineColumns.
func scanQuarantined(row interface{ Scan(...any) error }) (tenant.QuarantinedFile, error) {
	var q tenant.QuarantinedFile
	var reason, details, state, mode string
	var sum, kept []byte
	var at int64
	var decided sql.NullInt64
	if err := row.Scan(&q.ID, &reason, &details, &state, &q.Receiver, &q.ReceptionID, &q.Caller.CallingAETitle,
		&q.Caller.CalledAETitle, &q.Caller.Address, &q.Size, &sum, &kept, &q.SOPClassUID, &q.SOPInstanceUID, &q.StudyUID,
		&q.SeriesUID, &q.TransferSyntaxUID, &q.Incoming, &q.ContentKept, &at, &decided, &mode, &q.ReleaseReception,
		&q.ReleaseReceiver, &q.RejectReason, &q.ReleasedFrom); err != nil {
		return q, err
	}
	q.Reason, q.State, q.ReleaseMode = tenant.QuarantineReason(reason), tenant.QuarantineState(state), tenant.ReleaseMode(mode)
	q.SHA256, q.KeptSHA256 = toSHA256(sum), toSHA256(kept)
	q.Quarantined, q.Decided = fromMicros(at), fromNullMicros(decided)
	var err error
	q.Details, err = decodeDetails(details)
	return q, err
}

// quarantinedInTx reads a record inside a change's transaction, or NotFound.
func quarantinedInTx(ctx context.Context, tx *sql.Tx, id string) (tenant.QuarantinedFile, error) {
	q, err := scanQuarantined(tx.QueryRowContext(ctx, `SELECT `+quarantineColumns+` FROM quarantine WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return q, fault.New(fault.NotFound, "the tenant has no quarantined file with this id")
	}
	return q, err
}

// addTotals changes the quarantine's running totals in the transaction of
// the change that moved them.
func addTotals(ctx context.Context, tx *sql.Tx, files, bytes int64) error {
	if files == 0 && bytes == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE quarantine_totals SET files = files + ?, bytes = bytes + ? WHERE one = 1`, files, bytes)
	return err
}

// HoldFiles implements tenant.Pipeline.
func (t *Tenant) HoldFiles(ctx context.Context, files []tenant.QuarantinedFile) (int, error) {
	if err := tenant.ValidateQuarantineBatch(files); err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, t.open()
	}
	var inserted int
	err := t.write(ctx, "the quarantined files cannot be recorded", func(tx *sql.Tx, _ *tenantStatements) error {
		n := 0
		var heldFiles, heldBytes int64
		type delta struct{ files, bytes, receivedFiles, receivedBytes int64 }
		var order []string
		perReception := map[string]delta{}
		for _, q := range files {
			if q.ReceptionID != "" {
				if _, seen := perReception[q.ReceptionID]; !seen {
					has, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = ?`, q.ReceptionID)
					if err != nil {
						return err
					}
					if !has {
						return tenant.Precondition("a quarantined file names a reception the tenant does not have")
					}
					order = append(order, q.ReceptionID)
					perReception[q.ReceptionID] = delta{}
				}
			}
			q.State = tenant.QuarantineHeld
			details, err := encodeDetails(q.Details)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO quarantine (`+quarantineColumns+`)
				VALUES (?, ?, ?, 'held', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '', '', '', '', ?)
				ON CONFLICT (id) DO NOTHING`,
				q.ID, string(q.Reason), details, q.Receiver, q.ReceptionID, q.Caller.CallingAETitle, q.Caller.CalledAETitle,
				q.Caller.Address, q.Size, nullBlob(q.SHA256), nullBlob(q.KeptSHA256), q.SOPClassUID, q.SOPInstanceUID,
				q.StudyUID, q.SeriesUID, q.TransferSyntaxUID, q.Incoming, boolInt(q.ContentKept), micros(q.Quarantined),
				q.ReleasedFrom)
			if err != nil {
				return err
			}
			added, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if added == 0 {
				continue
			}
			n++
			heldFiles++
			heldBytes += q.HeldBytes()
			if q.ReceptionID != "" {
				d := perReception[q.ReceptionID]
				d.files, d.bytes = d.files+1, d.bytes+q.Size
				// A file that arrived and has no received file in its
				// reception (too large, not DICOM, no UIDs) is counted
				// received here, once, as the record is made once.
				recorded := false
				if !q.SHA256.IsZero() && q.SOPInstanceUID != "" {
					if recorded, err = exists(ctx, tx, `SELECT 1 FROM received_files
						WHERE reception_id = ? AND sop_instance_uid = ? AND sha256 = ?`, q.ReceptionID, q.SOPInstanceUID, q.SHA256[:]); err != nil {
						return err
					}
				}
				if !recorded {
					d.receivedFiles, d.receivedBytes = d.receivedFiles+1, d.receivedBytes+q.Size
				}
				perReception[q.ReceptionID] = d
			}
		}
		if err := addTotals(ctx, tx, heldFiles, heldBytes); err != nil {
			return err
		}
		for _, id := range order {
			d := perReception[id]
			if d.files == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE receptions SET files_quarantined = files_quarantined + ?,
				bytes_quarantined = bytes_quarantined + ?, files_received = files_received + ?, bytes_received = bytes_received + ?
				WHERE id = ?`, d.files, d.bytes, d.receivedFiles, d.receivedBytes, id); err != nil {
				return err
			}
		}
		inserted = n
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
		`SELECT `+quarantineColumns+` FROM quarantine WHERE id = ?`, []any{id}, scanQuarantined)
}

// ListQuarantined implements tenant.Pipeline.
func (t *Tenant) ListQuarantined(ctx context.Context, filter tenant.QuarantineFilter, page master.Page) ([]tenant.QuarantinedFile, string, error) {
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}
	binding := t.binding(string(filter.State), string(filter.Reason), filter.Receiver, filter.ReceptionID, filter.StudyUID,
		filter.SOPInstanceUID, microsText(nullableMicros(filter.Since)), microsText(nullableMicros(filter.Until)))
	after, err := pageAfterTime(page, listQuarantine, binding)
	if err != nil {
		return nil, "", err
	}
	query, args := quarantineQuery(filter, after, page.Limit+1)
	out, err := readRows(ctx, t, "the quarantined files cannot be listed", query, args, scanQuarantined)
	if err != nil {
		return nil, "", err
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, timeCursor(listQuarantine, binding, micros(last.Quarantined), last.ID), nil
}

// receivable reports whether a quarantined file carries what a received
// file needs, so a release can put it into a reception.
func receivable(q tenant.QuarantinedFile) bool {
	for _, u := range []string{q.SOPInstanceUID, q.SeriesUID, q.StudyUID, q.SOPClassUID, q.TransferSyntaxUID} {
		if !uid.Valid(u) {
			return false
		}
	}
	return !q.SHA256.IsZero() && q.Incoming != ""
}

// ReleaseQuarantined implements tenant.Pipeline.
func (t *Tenant) ReleaseQuarantined(ctx context.Context, id string, mode tenant.ReleaseMode, receiver, receptionID string, now time.Time, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	if err := tenant.ValidateRelease(id, mode, receiver, receptionID, now); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	if err := audit.Validate(); err != nil {
		return tenant.QuarantinedFile{}, err
	}
	var out tenant.QuarantinedFile
	err := t.write(ctx, "the quarantined file cannot be released", func(tx *sql.Tx, st *tenantStatements) error {
		// The record's row is locked before it is read, so on a database
		// with several writers a decision made meanwhile is seen.
		if _, err := tx.ExecContext(ctx, `UPDATE quarantine SET decided_at = decided_at WHERE id = ?`, id); err != nil {
			return err
		}
		q, err := quarantinedInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		switch q.State {
		case tenant.QuarantineRejected:
			return tenant.Precondition("the file was rejected")
		case tenant.QuarantineReleased:
			if q.ReleaseMode != mode {
				return tenant.Precondition("the file was released with another mode")
			}
			out = q
			return audited(ctx, tx, st, audit, guard)
		}
		if mode == tenant.ReleaseOverwrite && q.Reason != tenant.QuarantineDifferentFile {
			return fault.New(fault.Invalid, "only a different file under a held SOP Instance UID is released to overwrite")
		}
		if !q.ContentKept {
			return tenant.Precondition("the file's content was never kept")
		}
		via := receiver
		if via == "" {
			via = q.Receiver
		}
		has, err := exists(ctx, tx, `SELECT 1 FROM receivers WHERE name = ?`, via)
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
		if receivable(q) {
			r, _, err := openReceptionInTx(ctx, tx, via, q.StudyUID, receptionID, now)
			if err != nil {
				return err
			}
			joined = r.ID
			res, err := tx.StmtContext(ctx, st.insertReceived).ExecContext(ctx, r.ID, q.SOPInstanceUID, q.SHA256[:], q.SeriesUID,
				q.StudyUID, q.SOPClassUID, q.TransferSyntaxUID, q.Size, q.Incoming, q.Caller.CallingAETitle,
				q.Caller.CalledAETitle, q.Caller.Address, "", 1, micros(now))
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			switch {
			case r.ID == q.ReceptionID:
				// Back into the reception it was quarantined from, which
				// counted it received already (by its received file or by
				// its hold): released there, and no longer quarantined.
				if _, err := tx.ExecContext(ctx, `UPDATE received_files SET released = 1
					WHERE reception_id = ? AND sop_instance_uid = ? AND sha256 = ?`, r.ID, q.SOPInstanceUID, q.SHA256[:]); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE receptions SET files_released = files_released + 1,
					files_quarantined = files_quarantined - 1, bytes_quarantined = bytes_quarantined - ?, last_file = max(last_file, ?)
					WHERE id = ?`, q.Size, micros(now), r.ID); err != nil {
					return err
				}
			case n == 1:
				if _, err := tx.ExecContext(ctx, `UPDATE receptions SET files_received = files_received + 1,
					bytes_received = bytes_received + ?, files_released = files_released + 1, last_file = max(last_file, ?)
					WHERE id = ?`, q.Size, micros(now), r.ID); err != nil {
					return err
				}
			}
		}
		// The record's state is in the statement that changes it, so two
		// decisions at once make one.
		res, err := tx.ExecContext(ctx, `UPDATE quarantine SET state = 'released', content_kept = 0, decided_at = ?,
			release_mode = ?, release_reception = ?, release_receiver = ? WHERE id = ? AND state = 'held'`,
			micros(now), string(mode), joined, via, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return fault.New(fault.Unavailable, "the quarantined file was decided at the same moment; try again")
		}
		if err := addTotals(ctx, tx, -1, -q.HeldBytes()); err != nil {
			return err
		}
		if out, err = quarantinedInTx(ctx, tx, id); err != nil {
			return err
		}
		return audited(ctx, tx, st, audit, guard)
	})
	if err != nil {
		return tenant.QuarantinedFile{}, err
	}
	return out, nil
}

// RejectQuarantined implements tenant.Pipeline.
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
	err := t.write(ctx, "the quarantined file cannot be rejected", func(tx *sql.Tx, st *tenantStatements) error {
		// The record's row is locked before it is read, so on a database
		// with several writers a decision made meanwhile is seen.
		if _, err := tx.ExecContext(ctx, `UPDATE quarantine SET decided_at = decided_at WHERE id = ?`, id); err != nil {
			return err
		}
		q, err := quarantinedInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		switch q.State {
		case tenant.QuarantineReleased:
			return tenant.Precondition("the file was released")
		case tenant.QuarantineHeld:
			res, err := tx.ExecContext(ctx, `UPDATE quarantine SET state = 'rejected', content_kept = 0, decided_at = ?,
				reject_reason = ? WHERE id = ? AND state = 'held'`, micros(now), reason, id)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				if err != nil {
					return err
				}
				return fault.New(fault.Unavailable, "the quarantined file was decided at the same moment; try again")
			}
			if err := addTotals(ctx, tx, -1, -q.HeldBytes()); err != nil {
				return err
			}
			if q, err = quarantinedInTx(ctx, tx, id); err != nil {
				return err
			}
		}
		a, err := addDetails(audit, tenant.DetailReason, string(q.Reason), tenant.DetailSize, strconv.FormatInt(q.Size, 10),
			tenant.DetailSHA256, q.SHA256.String())
		if err != nil {
			return err
		}
		out = q
		return audited(ctx, tx, st, a, guard)
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
		`SELECT files, bytes FROM quarantine_totals WHERE one = 1`, nil, func(row interface{ Scan(...any) error }) (totals, error) {
			var v totals
			err := row.Scan(&v.files, &v.bytes)
			return v, err
		})
	return v.files, v.bytes, err
}

// quarantineQuery returns the statement and arguments of one page of
// ListQuarantined, put together as receptionsQuery's is, so each equality
// filter reads its index in 0002_pipeline.sql.
func quarantineQuery(filter tenant.QuarantineFilter, after []any, limit int) (string, []any) {
	var where []string
	var args []any
	for _, eq := range []struct{ column, value string }{
		{"state", string(filter.State)}, {"reason", string(filter.Reason)}, {"receiver", filter.Receiver},
		{"reception_id", filter.ReceptionID}, {"study_uid", filter.StudyUID}, {"sop_instance_uid", filter.SOPInstanceUID},
	} {
		if eq.value != "" {
			where, args = append(where, eq.column+" = ?"), append(args, eq.value)
		}
	}
	if !filter.Since.IsZero() {
		where, args = append(where, "at >= ?"), append(args, micros(filter.Since))
	}
	if !filter.Until.IsZero() {
		where, args = append(where, "at < ?"), append(args, micros(filter.Until))
	}
	if after != nil {
		where, args = append(where, "(at, id) < (?, ?)"), append(args, after...)
	}
	query := "SELECT " + quarantineColumns + " FROM quarantine"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- every fragment is a constant above; values are parameters.
	}
	query += " ORDER BY at DESC, id DESC LIMIT ?"
	return query, append(args, limit)
}
