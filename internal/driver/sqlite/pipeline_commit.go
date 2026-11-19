package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/driver/sqlkit"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/tenant"
)

// catalogID finds a row by its key with query, or reports none.
func catalogID(ctx context.Context, tx *sql.Tx, query string, args []any, dst ...any) (bool, error) {
	err := tx.QueryRowContext(ctx, query, args...).Scan(dst...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// findOrMake runs insert, which does nothing when a row with its key is
// there, then read, which finds the row by its key, until read finds one;
// it reports whether this call made it. On a database with several writers
// an insert that meets a row being made waits for that transaction, so the
// read then finds the row it committed. A few rounds suffice: a row once
// made stays.
func findOrMake(ctx context.Context, tx *sql.Tx, insert string, insertArgs []any, read func() (bool, error)) (bool, error) {
	for range 4 {
		found, err := read()
		if err != nil || found {
			return false, err
		}
		res, err := tx.ExecContext(ctx, insert, insertArgs...)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil || n == 1 {
			if err != nil {
				return false, err
			}
			found, err := read()
			if err == nil && !found {
				err = errors.New("a catalog row just made cannot be read back")
			}
			return err == nil, err
		}
	}
	return false, fault.New(fault.Unavailable, "the catalog's row kept changing under the commit; try again")
}

// commitSession finds or makes the commit's session, and its subject, and
// returns the session's id and how many subjects and sessions it made.
// Each row is found or made by findOrMake, so two commits at once of two
// studies of one subject share the subject, and a label another row took
// meanwhile is classified as the conflict it is.
func commitSession(ctx context.Context, tx *sql.Tx, c tenant.ScanCommit) (int64, int64, int64, error) {
	var sessionID, subjectID int64
	var sessionLabel, storage, subjectLabel string
	readSession := func() (bool, error) {
		return catalogID(ctx, tx, `SELECT s.id, s.label, s.storage, sub.label FROM sessions AS s
			JOIN subjects AS sub ON sub.id = s.subject_id WHERE s.study_uid = ?`, []any{c.StudyUID},
			&sessionID, &sessionLabel, &storage, &subjectLabel)
	}
	found, err := readSession()
	if err != nil {
		return 0, 0, 0, err
	}
	var newSubjects, newSessions int64
	if !found {
		var held string
		made, err := findOrMake(ctx, tx, `INSERT INTO subjects (label, folded, created) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			[]any{c.SubjectLabel, label.Fold(c.SubjectLabel), micros(c.At)}, func() (bool, error) {
				return catalogID(ctx, tx, `SELECT id, label FROM subjects WHERE folded = ?`, []any{label.Fold(c.SubjectLabel)}, &subjectID, &held)
			})
		if err != nil {
			return 0, 0, 0, err
		}
		if held != c.SubjectLabel {
			return 0, 0, 0, tenant.LabelConflictError("subject", tenant.FaultCaseCollision)
		}
		newSubjects = int64(boolInt(made))
		// The session is made unless its study's row or its label is
		// there; which one, the reads after say.
		res, err := tx.ExecContext(ctx, `INSERT INTO sessions (study_uid, subject_id, label, folded, storage, created)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, c.StudyUID, subjectID, c.SessionLabel, label.Fold(c.SessionLabel),
			c.Storage, micros(c.At))
		if err != nil {
			return 0, 0, 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, 0, 0, err
		}
		newSessions = n
		if found, err = readSession(); err != nil {
			return 0, 0, 0, err
		}
		if !found {
			// Not the study's row: its label is held at its subject.
			if _, err := catalogID(ctx, tx, `SELECT label FROM sessions WHERE subject_id = ? AND folded = ?`,
				[]any{subjectID, label.Fold(c.SessionLabel)}, &held); err != nil {
				return 0, 0, 0, err
			}
			if held == c.SessionLabel {
				return 0, 0, 0, tenant.LabelConflictError("session", tenant.FaultHeldByAnotherSession)
			}
			return 0, 0, 0, tenant.LabelConflictError("session", tenant.FaultCaseCollision)
		}
	}
	switch {
	case subjectLabel != c.SubjectLabel:
		return 0, 0, 0, tenant.LabelConflictError("subject", tenant.FaultTwoSubjectsForOneStudy)
	case sessionLabel != c.SessionLabel:
		return 0, 0, 0, tenant.Precondition("the session already has its label; an existing session's labels are found with CatalogLabels")
	case storage != c.Storage:
		return 0, 0, 0, tenant.Precondition("the session's files are on another storage, where every arrival of it is archived")
	}
	return sessionID, newSubjects, newSessions, nil
}

// commitScanRow finds or makes the commit's scan under its session and
// returns its id and whether it made it, as commitSession does.
func commitScanRow(ctx context.Context, tx *sql.Tx, c tenant.ScanCommit, sessionID int64) (int64, bool, error) {
	var scanID, scanSession int64
	var held string
	readScan := func() (bool, error) {
		return catalogID(ctx, tx, `SELECT id, session_id, label FROM scans WHERE series_uid = ?`, []any{c.SeriesUID}, &scanID, &scanSession, &held)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO scans (session_id, series_uid, label, folded, instances, bytes, manifest_sha256, created, updated)
		VALUES (?, ?, ?, ?, 0, 0, NULL, ?, ?) ON CONFLICT DO NOTHING`, sessionID, c.SeriesUID, c.ScanLabel, label.Fold(c.ScanLabel),
		micros(c.At), micros(c.At))
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	found, err := readScan()
	if err != nil {
		return 0, false, err
	}
	switch {
	case !found:
		// Not the series's row: its label is held in the session.
		if _, err := catalogID(ctx, tx, `SELECT label FROM scans WHERE session_id = ? AND folded = ?`,
			[]any{sessionID, label.Fold(c.ScanLabel)}, &held); err != nil {
			return 0, false, err
		}
		if held == c.ScanLabel {
			return 0, false, tenant.LabelConflictError("scan", tenant.FaultHeldByAnotherScan)
		}
		return 0, false, tenant.LabelConflictError("scan", tenant.FaultCaseCollision)
	case scanSession != sessionID:
		return 0, false, fault.Wrap(fault.Conflict, tenant.ErrSeriesElsewhere, "the series belongs to a scan of another session")
	case held != c.ScanLabel:
		return 0, false, tenant.Precondition("the scan already has its label; an existing scan's label is found with CatalogLabels")
	}
	return scanID, n == 1, nil
}

// confirmInstanceSQL turns an instance's claim by the reception into the
// held instance, in the statement that checks the claim is still this
// reception's, in the commit's study and series.
const confirmInstanceSQL = `UPDATE instances SET state = 'held', scan_id = ?, storage = ?, storage_key = ?, byte_offset = ?,
	byte_length = ?, size = ?, archived_sha256 = ?, held_at = ?
	WHERE sop_instance_uid = ? AND reception_id = ? AND state = 'claimed' AND study_uid = ? AND series_uid = ?`

// CommitScan implements tenant.Pipeline.
func (t *Tenant) CommitScan(ctx context.Context, c tenant.ScanCommit) (tenant.Scan, error) {
	if err := c.Validate(); err != nil {
		return tenant.Scan{}, err
	}
	var out tenant.Scan
	err := t.write(ctx, "the scan cannot be committed", func(tx *sql.Tx, _ *tenantStatements) error {
		if c.Fence != nil {
			if err := checkFence(ctx, tx, *c.Fence); err != nil {
				return err
			}
		}
		study, err := lockArchiving(ctx, tx, c.ReceptionID)
		if err != nil {
			return err
		}
		if study != c.StudyUID {
			return tenant.Precondition("a commit's study is its reception's")
		}
		if err := lockStorage(ctx, tx, c.Storage); err != nil {
			return err
		}
		sessionID, newSubjects, newSessions, err := commitSession(ctx, tx, c)
		if err != nil {
			return err
		}
		scanID, newScan, err := commitScanRow(ctx, tx, c, sessionID)
		if err != nil {
			return err
		}
		var newInstances, newBytes int64
		for _, in := range c.Instances {
			var state, by string
			var archived []byte
			found, err := catalogID(ctx, tx, `SELECT state, reception_id, archived_sha256 FROM instances WHERE sop_instance_uid = ?`,
				[]any{in.SOPInstanceUID}, &state, &by, &archived)
			if err != nil {
				return err
			}
			if found && state == "held" && by == c.ReceptionID && sqlkit.ToSHA256(archived) == in.ArchivedSHA256 {
				continue // confirmed by this reception before: a commit written again
			}
			if !found || state != "claimed" || by != c.ReceptionID {
				return tenant.Precondition("an instance confirmed is not claimed by this reception")
			}
			res, err := tx.ExecContext(ctx, confirmInstanceSQL, scanID, c.Storage, in.Key, in.Offset, in.Length, in.Size,
				in.ArchivedSHA256[:], micros(c.At), in.SOPInstanceUID, c.ReceptionID, c.StudyUID, c.SeriesUID)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				if err != nil {
					return err
				}
				return tenant.Precondition("an instance confirmed was claimed in another study or series")
			}
			newInstances++
			newBytes += in.Size
		}
		var manifest any
		if !c.ManifestSHA256.IsZero() {
			manifest = c.ManifestSHA256[:]
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scans SET instances = instances + ?, bytes = bytes + ?,
			manifest_sha256 = coalesce(?, manifest_sha256), updated = ? WHERE id = ?`,
			newInstances, newBytes, manifest, micros(c.At), scanID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE receptions SET files_archived = files_archived + ?, bytes_archived = bytes_archived + ?
			WHERE id = ?`, newInstances, newBytes, c.ReceptionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_totals SET subjects = subjects + ?, sessions = sessions + ?,
			scans = scans + ?, instances = instances + ? WHERE one = 1`,
			newSubjects, newSessions, boolInt(newScan), newInstances); err != nil {
			return err
		}
		if newBytes > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO storage_totals (storage, bytes) VALUES (?, ?)
				ON CONFLICT (storage) DO UPDATE SET bytes = bytes + excluded.bytes`, c.Storage, newBytes); err != nil {
				return err
			}
		}
		var sum []byte
		out = tenant.Scan{SubjectLabel: c.SubjectLabel, SessionLabel: c.SessionLabel, Label: c.ScanLabel,
			StudyUID: c.StudyUID, SeriesUID: c.SeriesUID, Storage: c.Storage}
		if err := tx.QueryRowContext(ctx, `SELECT instances, bytes, manifest_sha256 FROM scans WHERE id = ?`, scanID).
			Scan(&out.Instances, &out.Bytes, &sum); err != nil {
			return err
		}
		out.ManifestSHA256 = sqlkit.ToSHA256(sum)
		return nil
	})
	if err != nil {
		return tenant.Scan{}, err
	}
	return out, nil
}
