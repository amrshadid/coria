package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/sqlkit"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// instanceRowSQL reads the instance row under a UID. The primary key.
const instanceRowSQL = `SELECT study_uid, series_uid, sha256, reception_id, state = 'held' FROM instances WHERE sop_instance_uid = ?`

// scanInstanceRow reads one row of instanceRowSQL.
func scanInstanceRow(row interface{ Scan(...any) error }) (tenant.InstanceRow, error) {
	var r tenant.InstanceRow
	var sum []byte
	err := row.Scan(&r.StudyUID, &r.SeriesUID, &sum, &r.By, &r.Held)
	r.SHA256 = sqlkit.ToSHA256(sum)
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
// not ended, in the statement that checks it, and returns its Study Instance
// UID as received. A step that ends it (and releases its claims) is then
// ordered with a claim or a commit: on a database with several writers the
// second waits for the first. It refuses a reception not there (NotFound) or
// not archiving (a precondition).
func lockArchiving(ctx context.Context, tx *sql.Tx, receptionID string) (string, error) {
	var study string
	err := tx.QueryRowContext(ctx, `UPDATE receptions SET step_at = step_at WHERE id = ? AND ended = 0 AND storage <> ''
		RETURNING study_uid`, receptionID).Scan(&study)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return study, err
	}
	has, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = ?`, receptionID)
	if err != nil {
		return "", err
	}
	if !has {
		return "", fault.New(fault.NotFound, "the tenant has no reception with this id")
	}
	return "", tenant.Precondition("the reception is not archiving: it has not been identified, or it has ended")
}

// lockStorage takes a storage's row in the transaction, by an update that
// changes nothing, so a commit or an identify that names the storage and its
// removal or move are ordered: on a database with several writers the later
// waits for the earlier, then sees what it wrote. A storage not there is a
// precondition failure.
func lockStorage(ctx context.Context, tx *sql.Tx, name string) error {
	res, err := tx.ExecContext(ctx, `UPDATE storages SET updated = updated WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	return tenant.Precondition("the tenant has no storage named " + name)
}

// releaseClaims removes the claims a reception did not confirm. A held
// instance is never touched.
func releaseClaims(ctx context.Context, tx *sql.Tx, receptionID string) error {
	_, err := tx.ExecContext(ctx, releaseClaimedSQL, receptionID)
	return err
}

// ClaimInstances implements tenant.Pipeline. The files are claimed in sorted
// order of UID, then checksum, and answered in the caller's order. Each must
// be one of the reception's received files, as received. Each answer is
// tenant.DecideClaim's on the row read; an insert that meets a row made
// meanwhile (on a database with several writers, by a transaction it waited
// for) reads it again and decides again.
func (t *Tenant) ClaimInstances(ctx context.Context, receptionID string, files []tenant.InstanceClaim, now time.Time, fence *tenant.WorkFence) ([]tenant.Duplicate, error) {
	if err := tenant.ValidateClaimBatch(receptionID, files, now); err != nil {
		return nil, err
	}
	if fence != nil {
		if err := fence.Validate(); err != nil {
			return nil, err
		}
	}
	order := tenant.SortClaims(files)
	var out []tenant.Duplicate
	err := t.write(ctx, "the instances cannot be claimed", func(tx *sql.Tx, s *tenantStatements) error {
		if fence != nil {
			if err := checkFence(ctx, tx, *fence); err != nil {
				return err
			}
		}
		if _, err := lockArchiving(ctx, tx, receptionID); err != nil {
			return err
		}
		read, insert := tx.StmtContext(ctx, s.instanceRow), tx.StmtContext(ctx, s.insertClaim)
		received := tx.StmtContext(ctx, s.receivedFile)
		answers := make([]tenant.Duplicate, len(files))
		for _, i := range order {
			c := files[i]
			var study, series string
			err := received.QueryRowContext(ctx, receptionID, c.SOPInstanceUID, c.SHA256[:]).Scan(&study, &series)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && (study != c.StudyUID || series != c.SeriesUID)) {
				return tenant.Precondition("a claim names a file the reception did not receive, or not as it received it")
			}
			if err != nil {
				return err
			}
			for try := 0; ; try++ {
				row, err := instanceRowInTx(ctx, read, c.SOPInstanceUID)
				if err != nil {
					return err
				}
				d, action := tenant.DecideClaim(receptionID, c, row)
				if action == tenant.ClaimInsert {
					res, err := insert.ExecContext(ctx, c.SOPInstanceUID, c.StudyUID, c.SeriesUID, c.SHA256[:], receptionID, micros(now))
					if err != nil {
						return err
					}
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
	list, err := sqlkit.JSONText(uids)
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
		var uid string
		r, err := scanInstanceRow(scanPrefixed{row: row, first: &uid})
		f.uid, f.row = uid, r
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

// scanPrefixed reads a row whose first column goes to first and the rest to
// the destinations its caller gives, so a row with a key before the columns
// of another statement is read by that statement's scanner.
type scanPrefixed struct {
	row   interface{ Scan(...any) error }
	first *string
}

// Scan reads the row.
func (p scanPrefixed) Scan(dst ...any) error { return p.row.Scan(append([]any{p.first}, dst...)...) }

// duplicatesSQL reads the instance rows of a batch's SOP Instance UIDs,
// given as one JSON array (json_each), so one prepared statement serves a
// batch of any size; each UID is a lookup of the instances' primary key.
const duplicatesSQL = `SELECT sop_instance_uid, study_uid, series_uid, sha256, reception_id, state = 'held'
	FROM instances WHERE sop_instance_uid IN (SELECT value FROM json_each(?))`

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
	list, err := sqlkit.JSONText(seriesUIDs)
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

// scanInstancesSQL pages the instances held in the scan of a series: the
// scans' unique index on series_uid, then instances_by_scan, in order of
// SOP Instance UID.
const scanInstancesSQL = `SELECT i.sop_instance_uid, i.reception_id, i.storage, i.storage_key, i.byte_offset, i.byte_length,
	i.size, i.sha256, i.archived_sha256, i.held_at
	FROM scans AS s JOIN instances AS i ON i.scan_id = s.id AND i.state = 'held'
	WHERE s.series_uid = ? AND i.sop_instance_uid > ? ORDER BY i.sop_instance_uid LIMIT ?`

// ScanInstances implements tenant.ScanContents.
func (t *Tenant) ScanInstances(ctx context.Context, seriesUID string, page master.Page) ([]tenant.ScanInstance, string, error) {
	if err := tenant.ValidateSeriesUID(seriesUID); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	binding := t.binding(seriesUID)
	after := ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listScanInstances, binding, 1)
		if err != nil {
			return nil, "", err
		}
		after = f[0]
	}
	out, err := readRows(ctx, t, "the scan's instances cannot be listed", scanInstancesSQL,
		[]any{seriesUID, after, page.Limit + 1}, func(row interface{ Scan(...any) error }) (tenant.ScanInstance, error) {
			var in tenant.ScanInstance
			var sum, archived []byte
			var held int64
			err := row.Scan(&in.SOPInstanceUID, &in.ReceptionID, &in.Storage, &in.Key, &in.Offset, &in.Length, &in.Size,
				&sum, &archived, &held)
			in.SHA256, in.ArchivedSHA256, in.Held = sqlkit.ToSHA256(sum), sqlkit.ToSHA256(archived), fromMicros(held)
			return in, err
		})
	if err != nil {
		return nil, "", err
	}
	out, last, more := sqlkit.Cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listScanInstances, binding, last.SOPInstanceUID), nil
}
