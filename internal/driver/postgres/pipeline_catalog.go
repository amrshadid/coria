package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// claimTries bounds the rounds of one claim. A round whose insert of a UID
// does nothing met a row another transaction committed meanwhile (it waited
// for that transaction), and the next round reads it; a row is removed again
// only when its reception ends, so a UID settles within a few rounds.
const claimTries = 8

// lockArchiving locks the row of a reception that has been identified and
// not ended, in the statement that checks it, with the lock given, and
// returns its Study Instance UID as received. A step that ends it (and
// releases its claims) takes the row FOR NO KEY UPDATE, so it and a claim
// (FOR SHARE: claims of one reception do not wait for each other) or a
// commit (FOR NO KEY UPDATE: it adds to the reception's counts) are
// ordered; under read committed a lock that waited reads the row as the
// other left it and tests the condition again. It refuses a reception not
// there (NotFound) or not archiving (a precondition).
func lockArchiving(ctx context.Context, tx pgx.Tx, receptionID, lock string) (string, error) {
	var study string
	err := tx.QueryRow(ctx, `SELECT study_uid FROM receptions WHERE id = $1 AND NOT ended AND storage <> '' `+lock,
		receptionID).Scan(&study)
	if err == nil || !isNoRow(err) {
		return study, err
	}
	has, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = $1`, receptionID)
	if err != nil {
		return "", err
	}
	if !has {
		return "", errNoReception()
	}
	return "", tenant.Precondition("the reception is not archiving: it has not been identified, or it has ended")
}

// scanInstanceRow reads one row of instanceRowsSQL.
func scanInstanceRow(row scanner) (string, tenant.InstanceRow, error) {
	var uid string
	var r tenant.InstanceRow
	var sum []byte
	err := row.Scan(&uid, &r.StudyUID, &r.SeriesUID, &sum, &r.By, &r.Held)
	r.SHA256 = toSHA256(sum)
	return uid, r, err
}

// instanceRows reads the instance rows under uids, by UID.
func instanceRows(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, uids []string) (map[string]*tenant.InstanceRow, error) {
	type found struct {
		uid string
		row tenant.InstanceRow
	}
	rows, err := q.Query(ctx, instanceRowsSQL, uids)
	all, err := collect(rows, err, func(row scanner) (found, error) {
		uid, r, err := scanInstanceRow(row)
		return found{uid, r}, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]*tenant.InstanceRow, len(all))
	for i := range all {
		out[all[i].uid] = &all[i].row
	}
	return out, nil
}

// insertClaimsSQL writes a set of claims in one statement, in the order of
// the arrays, which the caller gives sorted: Postgres inserts the rows of an
// INSERT ... SELECT in the order the SELECT yields them, and a row that
// meets another transaction's row not yet committed waits for it there, so
// two claims that share UIDs wait for each other in UID order and never in a
// cycle. A UID that meets a row is skipped (ON CONFLICT DO NOTHING); the
// UIDs written are returned.
const insertClaimsSQL = `INSERT INTO instances (sop_instance_uid, study_uid, series_uid, state, sha256, reception_id, scan_id,
	storage, storage_key, byte_offset, byte_length, size, archived_sha256, claimed_at, held_at)
	SELECT u.uid, u.study, u.series, 'claimed', u.sha, $1, NULL, '', '', 0, 0, 0, NULL, $2, NULL
	FROM unnest($3::text[], $4::text[], $5::text[], $6::bytea[]) AS u(uid, study, series, sha)
	ON CONFLICT DO NOTHING RETURNING sop_instance_uid`

// ClaimInstances implements tenant.Pipeline. Every file must be one of the
// reception's received files, as received (one read of the received files'
// key for the batch). The instance rows of the batch are read, each file is
// decided by tenant.DecideClaim in sorted order of UID, then checksum, and
// the UIDs to claim are inserted as one sorted set. A UID whose insert met a
// row committed meanwhile is read again, in a statement of its own (one that
// began before the other transaction committed would not see its row), and
// decided again; the others are settled.
func (t *Tenant) ClaimInstances(ctx context.Context, receptionID string, files []tenant.InstanceClaim, now time.Time, fence *tenant.WorkFence) ([]tenant.Duplicate, error) {
	if err := tenant.ValidateClaimBatch(receptionID, files, now); err != nil {
		return nil, err
	}
	if fence != nil {
		if err := fence.Validate(); err != nil {
			return nil, err
		}
	}
	order := sortClaims(files)
	uids, sums := make([]string, len(files)), make([][]byte, len(files))
	var distinct []string
	for k, i := range order {
		uids[k], sums[k] = files[i].SOPInstanceUID, sumBytes(files[i].SHA256)
		if k == 0 || uids[k] != uids[k-1] {
			distinct = append(distinct, uids[k])
		}
	}
	var out []tenant.Duplicate
	err := t.pipe(ctx, "the instances cannot be claimed", func(tx pgx.Tx) error {
		if fence != nil {
			if err := checkFence(ctx, tx, *fence); err != nil {
				return err
			}
		}
		if _, err := lockArchiving(ctx, tx, receptionID, "FOR SHARE"); err != nil {
			return err
		}
		type asReceived struct{ study, series string }
		rows, err := tx.Query(ctx, receivedOfClaimSQL, receptionID, uids, sums)
		received, err := collect(rows, err, func(row scanner) (struct {
			k fileKey
			v asReceived
		}, error) {
			var r struct {
				k fileKey
				v asReceived
			}
			var sum []byte
			err := row.Scan(&r.k.uid, &sum, &r.v.study, &r.v.series)
			r.k.sum = toSHA256(sum)
			return r, err
		})
		if err != nil {
			return err
		}
		byKey := make(map[fileKey]asReceived, len(received))
		for _, r := range received {
			byKey[r.k] = r.v
		}
		for _, c := range files {
			got, ok := byKey[fileKey{c.SOPInstanceUID, c.SHA256}]
			if !ok || got.study != c.StudyUID || got.series != c.SeriesUID {
				return tenant.Precondition("a claim names a file the reception did not receive, or not as it received it")
			}
		}
		view, err := instanceRows(ctx, tx, distinct)
		if err != nil {
			return err
		}
		answers := make([]tenant.Duplicate, len(files))
		settled := make(map[string]bool, len(distinct))
		for try := 0; ; try++ {
			if try == claimTries {
				return fault.New(fault.Unavailable, "the instances' rows kept changing under the claim; try again")
			}
			var insUIDs, insStudies, insSeries []string
			var insSums [][]byte
			for _, i := range order {
				c := files[i]
				if settled[c.SOPInstanceUID] {
					continue
				}
				d, action := tenant.DecideClaim(receptionID, c, view[c.SOPInstanceUID])
				answers[i] = d
				if action == tenant.ClaimInsert {
					insUIDs, insStudies = append(insUIDs, c.SOPInstanceUID), append(insStudies, c.StudyUID)
					insSeries, insSums = append(insSeries, c.SeriesUID), append(insSums, sumBytes(c.SHA256))
					// A second file of the batch under this UID is decided
					// against this claim: the lower checksum is kept.
					view[c.SOPInstanceUID] = &tenant.InstanceRow{StudyUID: c.StudyUID, SeriesUID: c.SeriesUID, SHA256: c.SHA256, By: receptionID}
				}
			}
			var lost []string
			if len(insUIDs) > 0 {
				rows, err := tx.Query(ctx, insertClaimsSQL, receptionID, micros(now), insUIDs, insStudies, insSeries, insSums)
				written, err := collect(rows, err, func(row scanner) (string, error) {
					var u string
					err := row.Scan(&u)
					return u, err
				})
				if err != nil {
					return err
				}
				done := make(map[string]bool, len(written))
				for _, u := range written {
					done[u] = true
				}
				for _, u := range insUIDs {
					if !done[u] {
						lost = append(lost, u)
					}
				}
			}
			for _, u := range distinct {
				settled[u] = true
			}
			if len(lost) == 0 {
				break
			}
			again, err := instanceRows(ctx, tx, lost)
			if err != nil {
				return err
			}
			for _, u := range lost {
				settled[u] = false
				view[u] = again[u]
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

// CheckDuplicates implements tenant.Pipeline: one read of the batch's UIDs'
// instance rows, each judged by tenant.JudgeRow.
func (t *Tenant) CheckDuplicates(ctx context.Context, files []tenant.DuplicateQuery) ([]tenant.Duplicate, error) {
	if err := tenant.ValidateDuplicateBatch(files); err != nil {
		return nil, err
	}
	if err := t.open(); err != nil {
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
	byUID, err := instanceRows(ctx, t.pool, uids)
	if err != nil {
		return nil, t.fail(ctx, err, "the duplicate check cannot be read")
	}
	for i, f := range files {
		out[i] = tenant.JudgeRow(f, byUID[f.SOPInstanceUID])
	}
	return out, nil
}

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
			r.h.archived = toSHA256(archived)
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
		var sum []byte
		out = tenant.Scan{SubjectLabel: c.SubjectLabel, SessionLabel: c.SessionLabel, Label: c.ScanLabel,
			StudyUID: c.StudyUID, SeriesUID: c.SeriesUID, Storage: c.Storage}
		if err := tx.QueryRow(ctx, `SELECT instances, bytes, manifest_sha256 FROM scans WHERE id = $1`, scanID).
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
		func(row scanner) (tenant.SessionLabels, error) {
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
	type scanLabel struct{ series, label string }
	scans, err := readRows(ctx, t, "the catalog's labels cannot be read", scanLabelsSQL, []any{seriesUIDs},
		func(row scanner) (scanLabel, error) {
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
		`SELECT subjects, sessions, scans, instances FROM catalog_totals WHERE only_row`, nil,
		func(row scanner) (tenant.CatalogCounts, error) {
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
		[]any{receptionID, tenant.MaxReceptionScans + 1}, func(row scanner) (tenant.ReceptionScan, error) {
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
