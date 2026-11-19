package postgres

import (
	"context"
	"errors"

	"github.com/amrshadid/coria/internal/driver/sqlkit"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/tenant"
)

// A scan's commit into the catalog (CommitScan): the subject, session and
// scan found or made once whoever commits at once, the reception's claims
// confirmed, and the counts added.

// findOne finds a row by its key with query, or reports none.
func findOne(ctx context.Context, tx pgx.Tx, query string, args []any, dst ...any) (bool, error) {
	err := tx.QueryRow(ctx, query, args...).Scan(dst...)
	if isNoRow(err) {
		return false, nil
	}
	return err == nil, err
}

// findOrMake runs read, which finds a row by its key, and when it finds none
// runs insert, which does nothing when a row with its key is there, then
// reads again, until read finds one; it reports whether this call made it.
// An insert that meets a row another transaction is making waits for that
// transaction and then does nothing; the read after it is a statement of its
// own, so it sees the row that transaction committed. A row once made stays,
// so a few rounds suffice.
func findOrMake(ctx context.Context, tx pgx.Tx, insert string, insertArgs []any, read func() (bool, error)) (bool, error) {
	for range 4 {
		found, err := read()
		if err != nil || found {
			return false, err
		}
		n, err := rowsChanged(tx.Exec(ctx, insert, insertArgs...))
		if err != nil {
			return false, err
		}
		if n == 1 {
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
// Each row is found or made as findOrMake says, so two commits at once of
// two studies of one subject share the subject, and a label another row
// took meanwhile is classified as the conflict it is.
func commitSession(ctx context.Context, tx pgx.Tx, c tenant.ScanCommit) (int64, int64, int64, error) {
	var sessionID, subjectID int64
	var sessionLabel, storage, subjectLabel string
	readSession := func() (bool, error) {
		return findOne(ctx, tx, `SELECT s.id, s.label, s.storage, sub.label FROM sessions AS s
			JOIN subjects AS sub ON sub.id = s.subject_id WHERE s.study_uid = $1`, []any{c.StudyUID},
			&sessionID, &sessionLabel, &storage, &subjectLabel)
	}
	found, err := readSession()
	if err != nil {
		return 0, 0, 0, err
	}
	var newSubjects, newSessions int64
	if !found {
		var held string
		folded := label.Fold(c.SubjectLabel)
		made, err := findOrMake(ctx, tx, `INSERT INTO subjects (label, folded, created) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			[]any{c.SubjectLabel, folded, micros(c.At)}, func() (bool, error) {
				return findOne(ctx, tx, `SELECT id, label FROM subjects WHERE folded = $1`, []any{folded}, &subjectID, &held)
			})
		if err != nil {
			return 0, 0, 0, err
		}
		if held != c.SubjectLabel {
			return 0, 0, 0, tenant.LabelConflictError("subject", tenant.FaultCaseCollision)
		}
		if made {
			newSubjects = 1
		}
		// The session is made unless its study's row or its label is there;
		// which one, the reads after say.
		if newSessions, err = rowsChanged(tx.Exec(ctx, `INSERT INTO sessions (study_uid, subject_id, label, folded, storage, created)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, c.StudyUID, subjectID, c.SessionLabel, label.Fold(c.SessionLabel),
			c.Storage, micros(c.At))); err != nil {
			return 0, 0, 0, err
		}
		if found, err = readSession(); err != nil {
			return 0, 0, 0, err
		}
		if !found {
			// Not the study's row: its label is held at its subject.
			if _, err := findOne(ctx, tx, `SELECT label FROM sessions WHERE subject_id = $1 AND folded = $2`,
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
func commitScanRow(ctx context.Context, tx pgx.Tx, c tenant.ScanCommit, sessionID int64) (int64, bool, error) {
	var scanID, scanSession int64
	var held string
	n, err := rowsChanged(tx.Exec(ctx, `INSERT INTO scans (session_id, series_uid, label, folded, instances, bytes, manifest_sha256, created, updated)
		VALUES ($1, $2, $3, $4, 0, 0, NULL, $5, $5) ON CONFLICT DO NOTHING`, sessionID, c.SeriesUID, c.ScanLabel, label.Fold(c.ScanLabel),
		micros(c.At)))
	if err != nil {
		return 0, false, err
	}
	found, err := findOne(ctx, tx, `SELECT id, session_id, label FROM scans WHERE series_uid = $1`, []any{c.SeriesUID}, &scanID, &scanSession, &held)
	if err != nil {
		return 0, false, err
	}
	switch {
	case !found:
		// Not the series's row: its label is held in the session.
		if _, err := findOne(ctx, tx, `SELECT label FROM scans WHERE session_id = $1 AND folded = $2`,
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

// confirmSQL turns a set of the reception's claims into held instances in
// one statement, in the statement that checks each claim is still this
// reception's, in the commit's study and series; the UIDs confirmed are
// returned.
const confirmSQL = `UPDATE instances AS i SET state = 'held', scan_id = $1, storage = $2, storage_key = u.key,

	byte_offset = u.off, byte_length = u.len, size = u.size, archived_sha256 = u.sha, held_at = $3
	FROM unnest($4::text[], $5::text[], $6::bigint[], $7::bigint[], $8::bigint[], $9::bytea[]) AS u(uid, key, off, len, size, sha)
	WHERE i.sop_instance_uid = u.uid AND i.reception_id = $10 AND i.state = 'claimed' AND i.study_uid = $11 AND i.series_uid = $12
	RETURNING i.sop_instance_uid`

// CommitScan implements tenant.Pipeline. Its locks, in the package's order:
// the unit of work (the fence), the reception FOR NO KEY UPDATE, the
// storage FOR SHARE, the catalog's subject, session and scan as findOrMake
// meets them, the reception's own claims, and the catalog's totals last.
func (t *Tenant) CommitScan(ctx context.Context, c tenant.ScanCommit) (tenant.Scan, error) {
	if err := c.Validate(); err != nil {
		return tenant.Scan{}, err
	}
	uids := make([]string, len(c.Instances))
	for i, in := range c.Instances {
		uids[i] = in.SOPInstanceUID
	}
	var out tenant.Scan
	err := t.pipe(ctx, "the scan cannot be committed", func(tx pgx.Tx) error {
		if c.Fence != nil {
			if err := checkFence(ctx, tx, *c.Fence); err != nil {
				return err
			}
		}
		study, err := lockArchiving(ctx, tx, c.ReceptionID, "FOR NO KEY UPDATE")
		if err != nil {
			return err
		}
		if study != c.StudyUID {
			return tenant.Precondition("a commit's study is its reception's")
		}
		if err := lockStorageShared(ctx, tx, c.Storage); err != nil {
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
		// This reception's rows cannot change under the commit: a claim of
		// the reception holds its row FOR SHARE, and a step that releases
		// its claims FOR NO KEY UPDATE, both of which wait for this one.
		type held struct {
			state, by string
			archived  tenant.SHA256
		}
		rows, err := tx.Query(ctx, `SELECT sop_instance_uid, state, reception_id, archived_sha256 FROM instances
			WHERE sop_instance_uid = ANY($1::text[])`, uids)
		got, err := collect(rows, err, func(row scanner) (struct {
			uid string
			h   held
		}, error) {
			var r struct {
				uid string
				h   held
			}
			var archived []byte
			err := row.Scan(&r.uid, &r.h.state, &r.h.by, &archived)
			r.h.archived = sqlkit.ToSHA256(archived)
			return r, err
		})
		if err != nil {
			return err
		}
		byUID := make(map[string]held, len(got))
		for _, r := range got {
			byUID[r.uid] = r.h
		}
		var keys []string
		var offs, lens, sizes []int64
		var sums [][]byte
		var confirm []string
		var newBytes int64
		for _, in := range c.Instances {
			h, found := byUID[in.SOPInstanceUID]
			if found && h.state == "held" && h.by == c.ReceptionID && h.archived == in.ArchivedSHA256 {
				continue // confirmed by this reception before: a commit written again
			}
			if !found || h.state != "claimed" || h.by != c.ReceptionID {
				return tenant.Precondition("an instance confirmed is not claimed by this reception")
			}
			confirm, keys = append(confirm, in.SOPInstanceUID), append(keys, in.Key)
			offs, lens, sizes = append(offs, in.Offset), append(lens, in.Length), append(sizes, in.Size)
			sums = append(sums, sumBytes(in.ArchivedSHA256))
			newBytes += in.Size
		}
		var newInstances int64
		if len(confirm) > 0 {
			rows, err := tx.Query(ctx, confirmSQL, scanID, c.Storage, micros(c.At), confirm, keys, offs, lens, sizes, sums,
				c.ReceptionID, c.StudyUID, c.SeriesUID)
			done, err := collect(rows, err, func(row scanner) (string, error) {
				var u string
				err := row.Scan(&u)
				return u, err
			})
			if err != nil {
				return err
			}
			if len(done) != len(confirm) {
				return tenant.Precondition("an instance confirmed was claimed in another study or series")
			}
			newInstances = int64(len(done))
		}
		if _, err := tx.Exec(ctx, `UPDATE scans SET instances = instances + $1, bytes = bytes + $2,
			manifest_sha256 = coalesce($3, manifest_sha256), updated = $4 WHERE id = $5`,
			newInstances, newBytes, nullSum(c.ManifestSHA256), micros(c.At), scanID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE receptions SET files_archived = files_archived + $1, bytes_archived = bytes_archived + $2
			WHERE id = $3`, newInstances, newBytes, c.ReceptionID); err != nil {
			return err
		}
		scans := int64(0)
		if newScan {
			scans = 1
		}
		if newSubjects+newSessions+scans+newInstances > 0 {
			if _, err := tx.Exec(ctx, `UPDATE catalog_totals SET subjects = subjects + $1, sessions = sessions + $2,
				scans = scans + $3, instances = instances + $4 WHERE only_row`,
				newSubjects, newSessions, scans, newInstances); err != nil {
				return err
			}
		}
		// The storage's total, a row of its own (storage_totals), last
		// with the catalog's, among the totals the package locks last.
		if newBytes > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO storage_totals (storage, bytes) VALUES ($1, $2)
				ON CONFLICT (storage) DO UPDATE SET bytes = storage_totals.bytes + excluded.bytes`, c.Storage, newBytes); err != nil {
				return err
			}
		}
		var sum []byte
		out = tenant.Scan{SubjectLabel: c.SubjectLabel, SessionLabel: c.SessionLabel, Label: c.ScanLabel,
			StudyUID: c.StudyUID, SeriesUID: c.SeriesUID, Storage: c.Storage}
		if err := tx.QueryRow(ctx, `SELECT instances, bytes, manifest_sha256 FROM scans WHERE id = $1`, scanID).
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
