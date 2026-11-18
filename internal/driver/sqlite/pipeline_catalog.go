package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// instanceRowSQL reads the instance row under a UID: the instance held,
// the claim pending, or both. The primary key.
const instanceRowSQL = `SELECT study_uid, series_uid, held_sha256, held_by, claim_sha256, coalesce(claimed_by, '')
	FROM instances WHERE sop_instance_uid = ?`

// scanInstanceRow reads one row of instanceRowSQL.
func scanInstanceRow(row interface{ Scan(...any) error }) (tenant.InstanceRow, error) {
	var r tenant.InstanceRow
	var held, claim []byte
	err := row.Scan(&r.StudyUID, &r.SeriesUID, &held, &r.HeldBy, &claim, &r.ClaimedBy)
	r.HeldSHA256, r.ClaimSHA256 = toSHA256(held), toSHA256(claim)
	return r, err
}

// instanceRowInTx reads the instance row under uid with the transaction's
// statement, or nil when there is none.
func instanceRowInTx(ctx context.Context, stmt *sql.Stmt, uid string) (*tenant.InstanceRow, error) {
	r, err := scanInstanceRow(stmt.QueryRowContext(ctx, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// lockArchiving locks the row of a reception that has been identified and
// not ended, in the statement that checks it, so a step that ends it (and
// releases its claims) is ordered with a claim or a commit: on a database
// with several writers the second waits for the first. It refuses a
// reception not there (NotFound) or not archiving (a precondition).
func lockArchiving(ctx context.Context, tx *sql.Tx, receptionID string) error {
	res, err := tx.ExecContext(ctx, `UPDATE receptions SET step_at = step_at WHERE id = ? AND ended = 0 AND storage <> ''`, receptionID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	has, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = ?`, receptionID)
	if err != nil {
		return err
	}
	if !has {
		return fault.New(fault.NotFound, "the tenant has no reception with this id")
	}
	return tenant.Precondition("the reception is not archiving: it has not been identified, or it has ended")
}

// releaseClaims removes the claims of a reception it did not confirm: rows
// only claimed go, and a claim to overwrite a held instance is cleared,
// leaving the held instance as it was.
func releaseClaims(ctx context.Context, tx *sql.Tx, receptionID string) error {
	if _, err := tx.ExecContext(ctx, releaseClaimedSQL, receptionID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, releaseOverwriteSQL, receptionID)
	return err
}

// ClaimInstances implements tenant.Pipeline. Each file's answer is
// tenant.DecideClaim's on the row read; a write that finds the row changed
// meanwhile (an insert that meets another's row, a takeover that meets
// another's claim) reads it again and decides again, which on a database
// with several writers is the row the winner committed.
func (t *Tenant) ClaimInstances(ctx context.Context, receptionID string, files []tenant.InstanceClaim, now time.Time, fence *tenant.WorkFence) ([]tenant.Duplicate, error) {
	if err := tenant.ValidateClaimBatch(receptionID, files, now); err != nil {
		return nil, err
	}
	if fence != nil {
		if err := fence.Validate(); err != nil {
			return nil, err
		}
	}
	var out []tenant.Duplicate
	err := t.write(ctx, "the instances cannot be claimed", func(tx *sql.Tx, s *tenantStatements) error {
		if fence != nil {
			if err := checkFence(ctx, tx, *fence); err != nil {
				return err
			}
		}
		if err := lockArchiving(ctx, tx, receptionID); err != nil {
			return err
		}
		read, insert, takeOver := tx.StmtContext(ctx, s.instanceRow), tx.StmtContext(ctx, s.insertClaim), tx.StmtContext(ctx, s.takeOverClaim)
		answers := make([]tenant.Duplicate, len(files))
		for i, c := range files {
			for try := 0; ; try++ {
				row, err := instanceRowInTx(ctx, read, c.SOPInstanceUID)
				if err != nil {
					return err
				}
				d, action := tenant.DecideClaim(receptionID, c, row)
				var res sql.Result
				switch action {
				case tenant.ClaimInsert:
					res, err = insert.ExecContext(ctx, c.SOPInstanceUID, c.StudyUID, c.SeriesUID, c.SHA256[:], receptionID, micros(now))
				case tenant.ClaimTakeOver:
					res, err = takeOver.ExecContext(ctx, c.SHA256[:], receptionID, micros(now), c.SOPInstanceUID, d.HeldSHA256[:])
				}
				if err != nil {
					return err
				}
				if res != nil {
					n, err := res.RowsAffected()
					if err != nil {
						return err
					}
					if n == 0 {
						if try < 3 {
							continue
						}
						return fault.New(fault.Unavailable, "the instance's row kept changing under the claim; try again")
					}
				}
				answers[i] = d
				break
			}
		}
		out = answers
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CheckDuplicates implements tenant.Pipeline: a read of each UID's instance
// row, judged by tenant.JudgeRow.
func (t *Tenant) CheckDuplicates(ctx context.Context, files []tenant.DuplicateQuery) ([]tenant.Duplicate, error) {
	if err := tenant.ValidateDuplicateBatch(files); err != nil {
		return nil, err
	}
	s, err := t.ready(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tenant.Duplicate, len(files))
	if len(files) == 0 {
		return out, nil
	}
	uids := make([]string, len(files))
	for i, f := range files {
		uids[i] = f.SOPInstanceUID
	}
	list, err := jsonText(uids)
	if err != nil {
		return nil, err
	}
	type found struct {
		uid string
		row tenant.InstanceRow
	}
	rows, err := s.duplicates.QueryContext(ctx, list)
	all, err := collect(rows, err, func(row interface{ Scan(...any) error }) (found, error) {
		var f found
		var held, claim []byte
		err := row.Scan(&f.uid, &f.row.StudyUID, &f.row.SeriesUID, &held, &f.row.HeldBy, &claim, &f.row.ClaimedBy)
		f.row.HeldSHA256, f.row.ClaimSHA256 = toSHA256(held), toSHA256(claim)
		return f, err
	})
	if err != nil {
		return nil, t.fail(ctx, err, "the duplicate check cannot be read")
	}
	byUID := make(map[string]*tenant.InstanceRow, len(all))
	for i := range all {
		byUID[all[i].uid] = &all[i].row
	}
	for i, f := range files {
		out[i] = tenant.JudgeRow(f, byUID[f.SOPInstanceUID])
	}
	return out, nil
}

// duplicatesSQL reads the instance rows of a batch's SOP Instance UIDs,
// given as one JSON array (json_each), so one prepared statement serves a
// batch of any size; each UID is a lookup of the instances' primary key.
const duplicatesSQL = `SELECT sop_instance_uid, study_uid, series_uid, held_sha256, held_by, claim_sha256, coalesce(claimed_by, '')
	FROM instances WHERE sop_instance_uid IN (SELECT value FROM json_each(?))`

// catalogID finds a row by its key with query, or reports none.
func catalogID(ctx context.Context, tx *sql.Tx, query string, args []any, dst ...any) (bool, error) {
	err := tx.QueryRowContext(ctx, query, args...).Scan(dst...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// commitSession finds or makes the commit's session, and its subject, and
// returns the session's id and how many subjects and sessions it made.
func commitSession(ctx context.Context, tx *sql.Tx, c tenant.ScanCommit) (int64, int64, int64, error) {
	var sessionID int64
	var sessionLabel, storage, subjectLabel string
	found, err := catalogID(ctx, tx, `SELECT s.id, s.label, s.storage, sub.label FROM sessions AS s
		JOIN subjects AS sub ON sub.id = s.subject_id WHERE s.study_uid = ?`, []any{c.StudyUID},
		&sessionID, &sessionLabel, &storage, &subjectLabel)
	if err != nil {
		return 0, 0, 0, err
	}
	if found {
		switch {
		case subjectLabel != c.SubjectLabel:
			return 0, 0, 0, tenant.LabelConflictError("subject", tenant.FaultTwoSubjectsForOneStudy)
		case sessionLabel != c.SessionLabel:
			return 0, 0, 0, tenant.Precondition("the session already has its label; an existing session's labels are found with CatalogLabels")
		case storage != c.Storage:
			return 0, 0, 0, tenant.Precondition("the session's files are on another storage, where every arrival of it is archived")
		}
		return sessionID, 0, 0, nil
	}
	var subjectID int64
	var held string
	newSubjects := int64(0)
	found, err = catalogID(ctx, tx, `SELECT id, label FROM subjects WHERE folded = ?`, []any{label.Fold(c.SubjectLabel)}, &subjectID, &held)
	if err != nil {
		return 0, 0, 0, err
	}
	switch {
	case found && held != c.SubjectLabel:
		return 0, 0, 0, tenant.LabelConflictError("subject", tenant.FaultCaseCollision)
	case !found:
		err := tx.QueryRowContext(ctx, `INSERT INTO subjects (label, folded, created) VALUES (?, ?, ?) RETURNING id`,
			c.SubjectLabel, label.Fold(c.SubjectLabel), micros(c.At)).Scan(&subjectID)
		if err != nil {
			return 0, 0, 0, err
		}
		newSubjects = 1
	}
	found, err = catalogID(ctx, tx, `SELECT label FROM sessions WHERE subject_id = ? AND folded = ?`,
		[]any{subjectID, label.Fold(c.SessionLabel)}, &held)
	if err != nil {
		return 0, 0, 0, err
	}
	if found {
		if held == c.SessionLabel {
			return 0, 0, 0, tenant.LabelConflictError("session", tenant.FaultHeldByAnotherSession)
		}
		return 0, 0, 0, tenant.LabelConflictError("session", tenant.FaultCaseCollision)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO sessions (study_uid, subject_id, label, folded, storage, created)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, c.StudyUID, subjectID, c.SessionLabel, label.Fold(c.SessionLabel),
		c.Storage, micros(c.At)).Scan(&sessionID)
	if err != nil {
		return 0, 0, 0, err
	}
	return sessionID, newSubjects, 1, nil
}

// commitScanRow finds or makes the commit's scan under its session and
// returns its id and whether it made it.
func commitScanRow(ctx context.Context, tx *sql.Tx, c tenant.ScanCommit, sessionID int64) (int64, bool, error) {
	var scanID, scanSession int64
	var held string
	found, err := catalogID(ctx, tx, `SELECT id, session_id, label FROM scans WHERE series_uid = ?`, []any{c.SeriesUID}, &scanID, &scanSession, &held)
	if err != nil {
		return 0, false, err
	}
	if found {
		switch {
		case scanSession != sessionID:
			return 0, false, fault.Wrap(fault.Conflict, tenant.ErrSeriesElsewhere, "the series belongs to a scan of another session")
		case held != c.ScanLabel:
			return 0, false, tenant.Precondition("the scan already has its label; an existing scan's label is found with CatalogLabels")
		}
		return scanID, false, nil
	}
	found, err = catalogID(ctx, tx, `SELECT label FROM scans WHERE session_id = ? AND folded = ?`, []any{sessionID, label.Fold(c.ScanLabel)}, &held)
	if err != nil {
		return 0, false, err
	}
	if found {
		if held == c.ScanLabel {
			return 0, false, tenant.LabelConflictError("scan", tenant.FaultHeldByAnotherScan)
		}
		return 0, false, tenant.LabelConflictError("scan", tenant.FaultCaseCollision)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO scans (session_id, series_uid, label, folded, instances, bytes, manifest_sha256, created, updated)
		VALUES (?, ?, ?, ?, 0, 0, NULL, ?, ?) RETURNING id`, sessionID, c.SeriesUID, c.ScanLabel, label.Fold(c.ScanLabel),
		micros(c.At), micros(c.At)).Scan(&scanID)
	return scanID, err == nil, err
}

// confirmInstanceSQL turns an instance's claim by the reception into the
// held instance, in the statement that checks the claim is still this
// reception's.
const confirmInstanceSQL = `UPDATE instances SET held_sha256 = claim_sha256, held_by = claimed_by, scan_id = ?, storage = ?,
	storage_key = ?, byte_offset = ?, byte_length = ?, size = ?, archived_sha256 = ?, held_at = ?,
	claim_sha256 = NULL, claimed_by = NULL, claimed_at = NULL
	WHERE sop_instance_uid = ? AND claimed_by = ? AND series_uid = ?`

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
		if err := lockArchiving(ctx, tx, c.ReceptionID); err != nil {
			return err
		}
		has, err := exists(ctx, tx, `SELECT 1 FROM storages WHERE name = ?`, c.Storage)
		if err != nil {
			return err
		}
		if !has {
			return tenant.Precondition("the tenant has no storage named " + c.Storage)
		}
		sessionID, newSubjects, newSessions, err := commitSession(ctx, tx, c)
		if err != nil {
			return err
		}
		scanID, newScan, err := commitScanRow(ctx, tx, c, sessionID)
		if err != nil {
			return err
		}
		var newInstances, scanBytes, archivedFiles, archivedBytes int64
		for _, in := range c.Instances {
			var wasHeld bool
			var oldSize int64
			var claimedBy, heldBy string
			var archived []byte
			found, err := catalogID(ctx, tx, `SELECT held_sha256 IS NOT NULL, size, coalesce(claimed_by, ''), held_by, archived_sha256
				FROM instances WHERE sop_instance_uid = ?`, []any{in.SOPInstanceUID}, &wasHeld, &oldSize, &claimedBy, &heldBy, &archived)
			if err != nil {
				return err
			}
			if found && claimedBy == "" && heldBy == c.ReceptionID && toSHA256(archived) == in.ArchivedSHA256 {
				continue // confirmed by this reception before: a commit written again
			}
			if !found || claimedBy != c.ReceptionID {
				return tenant.Precondition("an instance confirmed is not claimed by this reception")
			}
			res, err := tx.ExecContext(ctx, confirmInstanceSQL, scanID, c.Storage, in.Key, in.Offset, in.Length, in.Size,
				in.ArchivedSHA256[:], micros(c.At), in.SOPInstanceUID, c.ReceptionID, c.SeriesUID)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				if err != nil {
					return err
				}
				return tenant.Precondition("an instance confirmed was claimed in another series")
			}
			if wasHeld {
				scanBytes += in.Size - oldSize
			} else {
				newInstances++
				scanBytes += in.Size
			}
			archivedFiles++
			archivedBytes += in.Size
		}
		var manifest any
		if !c.ManifestSHA256.IsZero() {
			manifest = c.ManifestSHA256[:]
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scans SET instances = instances + ?, bytes = bytes + ?,
			manifest_sha256 = coalesce(?, manifest_sha256), updated = ? WHERE id = ?`,
			newInstances, scanBytes, manifest, micros(c.At), scanID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE receptions SET files_archived = files_archived + ?, bytes_archived = bytes_archived + ?
			WHERE id = ?`, archivedFiles, archivedBytes, c.ReceptionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_totals SET subjects = subjects + ?, sessions = sessions + ?,
			scans = scans + ?, instances = instances + ? WHERE one = 1`,
			newSubjects, newSessions, boolInt(newScan), newInstances); err != nil {
			return err
		}
		var sum []byte
		out = tenant.Scan{SubjectLabel: c.SubjectLabel, SessionLabel: c.SessionLabel, Label: c.ScanLabel,
			StudyUID: c.StudyUID, SeriesUID: c.SeriesUID, Storage: c.Storage}
		if err := tx.QueryRowContext(ctx, `SELECT instances, bytes, manifest_sha256 FROM scans WHERE id = ?`, scanID).
			Scan(&out.Instances, &out.Bytes, &sum); err != nil {
			return err
		}
		out.ManifestSHA256 = toSHA256(sum)
		return nil
	})
	if err != nil {
		return tenant.Scan{}, err
	}
	return out, nil
}

// CatalogLabels implements tenant.Pipeline.
func (t *Tenant) CatalogLabels(ctx context.Context, studyUID string, seriesUIDs []string) (tenant.CatalogLabels, error) {
	if err := tenant.ValidateSeriesLookup(studyUID, seriesUIDs); err != nil {
		return tenant.CatalogLabels{}, err
	}
	var out tenant.CatalogLabels
	sessions, err := readRows(ctx, t, "the catalog's labels cannot be read", sessionLabelsSQL, []any{studyUID},
		func(row interface{ Scan(...any) error }) (tenant.SessionLabels, error) {
			var l tenant.SessionLabels
			err := row.Scan(&l.SubjectLabel, &l.SessionLabel, &l.Storage)
			return l, err
		})
	if err != nil {
		return out, err
	}
	if len(sessions) == 1 {
		out.Session = &sessions[0]
	}
	if len(seriesUIDs) == 0 {
		return out, nil
	}
	list, err := jsonText(seriesUIDs)
	if err != nil {
		return out, err
	}
	type scanLabel struct{ series, label string }
	scans, err := readRows(ctx, t, "the catalog's labels cannot be read", scanLabelsSQL, []any{list},
		func(row interface{ Scan(...any) error }) (scanLabel, error) {
			var l scanLabel
			err := row.Scan(&l.series, &l.label)
			return l, err
		})
	if err != nil {
		return out, err
	}
	out.Scans = make(map[string]string, len(scans))
	for _, s := range scans {
		out.Scans[s.series] = s.label
	}
	return out, nil
}

// CatalogTotals implements tenant.Pipeline.
func (t *Tenant) CatalogTotals(ctx context.Context) (tenant.CatalogCounts, error) {
	return readOne(ctx, t, "the catalog has no totals", "the catalog's totals cannot be read",
		`SELECT subjects, sessions, scans, instances FROM catalog_totals WHERE one = 1`, nil,
		func(row interface{ Scan(...any) error }) (tenant.CatalogCounts, error) {
			var c tenant.CatalogCounts
			err := row.Scan(&c.Subjects, &c.Sessions, &c.Scans, &c.Instances)
			return c, err
		})
}

// ReceptionScans implements tenant.Pipeline.
func (t *Tenant) ReceptionScans(ctx context.Context, receptionID string) ([]tenant.ReceptionScan, bool, error) {
	if err := master.ValidateID("reception id", receptionID); err != nil {
		return nil, false, err
	}
	out, err := readRows(ctx, t, "the reception's scans cannot be read", receptionScansSQL,
		[]any{receptionID, tenant.MaxReceptionScans + 1}, func(row interface{ Scan(...any) error }) (tenant.ReceptionScan, error) {
			var s tenant.ReceptionScan
			err := row.Scan(&s.Label, &s.SeriesUID, &s.Instances, &s.Bytes)
			return s, err
		})
	if err != nil {
		return nil, false, err
	}
	if len(out) > tenant.MaxReceptionScans {
		return out[:tenant.MaxReceptionScans], true, nil
	}
	return out, false, nil
}
