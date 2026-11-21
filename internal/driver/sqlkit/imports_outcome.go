package sqlkit

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/amrshadid/coria/internal/core/importpath"
	"github.com/amrshadid/coria/internal/tenant"
)

// What an import's files became, written in the pipeline's own
// transactions: a file's arrival with its unit's cursor (RecordImportArrival,
// ImportHeld for a file held as it arrived), and each outcome in the
// transaction that decides it (ImportArchived, ImportHeld, ImportEnded,
// ImportRetried, ImportDiscarded). A driver calls these from its pipeline
// methods with its transaction; each returns the events of the jobs it
// ended, which the driver writes with its own.

// importDelta is a change of an import unit's counts, and of its job's by
// the same amounts.
type importDelta struct {
	found, skipped, handedIn, archived, alreadyHeld, quarantined, lost, failed, discarded, processing, bytes int64
}

// add returns d plus e.
func (d importDelta) add(e importDelta) importDelta {
	return importDelta{d.found + e.found, d.skipped + e.skipped, d.handedIn + e.handedIn, d.archived + e.archived,
		d.alreadyHeld + e.alreadyHeld, d.quarantined + e.quarantined, d.lost + e.lost, d.failed + e.failed,
		d.discarded + e.discarded, d.processing + e.processing, d.bytes + e.bytes}
}

// args returns d's amounts in the order of importDeltaSet.
func (d importDelta) args() []any {
	return []any{d.found, d.skipped, d.handedIn, d.archived, d.alreadyHeld, d.quarantined, d.lost, d.failed, d.discarded,
		d.processing, d.bytes}
}

// importDeltaSet is the SET list that adds an importDelta to a row of
// import_units or import_jobs, whose CHECKs hold the identities after each
// statement.
const importDeltaSet = `found = found + ?, skipped = skipped + ?, handed_in = handed_in + ?, archived = archived + ?,
	already_held = already_held + ?, quarantined = quarantined + ?, lost = lost + ?, failed = failed + ?,
	discarded = discarded + ?, processing = processing + ?, bytes_handed_in = bytes_handed_in + ?`

// addToUnit adds d to a unit's counts and, when the unit's walk has ended
// and nothing it handed in is processing, makes it done.
func (k Sending) addToUnit(ctx context.Context, tx SendingTx, jobID, unitID string, d importDelta) error {
	if _, err := tx.Exec(ctx, `UPDATE import_units SET `+importDeltaSet+` WHERE job_id = ? AND unit_id = ?`,
		append(d.args(), jobID, unitID)...); err != nil {
		return err
	}
	if d.processing >= 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE import_units SET state = 'done' WHERE job_id = ? AND unit_id = ? AND state = 'walked'
		AND processing = 0`, jobID, unitID)
	return err
}

// saveImportJob adds d to a job's counts and writes how far its listing of
// files got, as ij holds it.
func (k Sending) saveImportJob(ctx context.Context, tx SendingTx, ij *importJobRow, d importDelta) error {
	_, err := tx.Exec(ctx, `UPDATE import_jobs SET `+importDeltaSet+`, next_file = ?, files_listed = ?, files_not_listed = ?
		WHERE job_id = ?`, append(d.args(), ij.nextFile, ij.filesListed, ij.spec.FilesNotListed, ij.jobID)...)
	return err
}

// addImportTally adds n to a unit's and its job's count of one code.
func addImportTally(ctx context.Context, tx SendingTx, jobID, unitID, kind, code string, n int64) error {
	for _, u := range []string{unitID, ""} {
		if _, err := tx.Exec(ctx, `INSERT INTO import_tallies (job_id, unit_id, kind, code, count) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (job_id, unit_id, kind, code) DO UPDATE SET count = import_tallies.count + excluded.count`,
			jobID, u, kind, code, n); err != nil {
			return err
		}
	}
	return nil
}

// importListed is a row of import_files to write.
type importListed struct {
	pathKey    []byte
	kind       tenant.ImportFileKind
	unitID     string
	skipCode   tenant.ImportSkipCode
	quarantine string
	reason     tenant.QuarantineReason
	reception  string
	size       int64
	detail     string
	at         time.Time
}

// listImportFile writes f while the job lists fewer than its bound, and
// counts it not listed past it; ij (locked) keeps the counts, which the
// caller saves (saveImportJob). A row hidden before (a failed file
// retried, failing again) is shown again with its number. It returns the
// row's number, 0 when it was not listed.
func (k Sending) listImportFile(ctx context.Context, tx SendingTx, ij *importJobRow, f importListed) (int64, error) {
	if ij.filesListed >= int64(ij.spec.FailuresKept) {
		ij.spec.FilesNotListed++
		return 0, nil
	}
	var seq int64
	found, err := tx.QueryRow(ctx, `SELECT seq FROM import_files WHERE job_id = ? AND path_key = ? AND kind = ? AND hidden = ?`,
		[]any{ij.jobID, f.pathKey, string(f.kind), k.D.Bool(true)}, &seq)
	if err != nil {
		return 0, err
	}
	if found {
		if _, err := tx.Exec(ctx, `UPDATE import_files SET hidden = ?, unit_id = ?, quarantine_id = ?, quarantine_reason = ?,
			reception_id = ?, size = ?, at = ? WHERE job_id = ? AND seq = ?`, k.D.Bool(false), f.unitID, f.quarantine,
			string(f.reason), f.reception, f.size, k.D.Time(f.at), ij.jobID, seq); err != nil {
			return 0, err
		}
		ij.filesListed++
		return seq, nil
	}
	seq = ij.nextFile
	n, err := tx.Exec(ctx, `INSERT INTO import_files (job_id, path_key, kind, seq, unit_id, skip_code, quarantine_id,
		quarantine_reason, reception_id, size, detail, hidden, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (job_id, path_key, kind) DO NOTHING`, ij.jobID, f.pathKey, string(f.kind), seq, f.unitID,
		string(f.skipCode), f.quarantine, string(f.reason), f.reception, f.size, f.detail, k.D.Bool(false), k.D.Time(f.at))
	if err != nil || n == 0 {
		return 0, err // listed already: the same entry, judged once
	}
	ij.nextFile++
	ij.filesListed++
	return seq, nil
}

// hideFailed hides a failed arrival's row (its reception retried or
// discarded), so the job lists it no more and lists another in its place;
// one that was not listed, past the job's bound, is no longer counted so.
func (k Sending) hideFailed(ctx context.Context, tx SendingTx, ij *importJobRow, a arrival) error {
	if a.listedSeq == 0 {
		ij.spec.FilesNotListed--
		return nil
	}
	n, err := tx.Exec(ctx, `UPDATE import_files SET hidden = ? WHERE job_id = ? AND seq = ? AND hidden = ?`,
		k.D.Bool(true), ij.jobID, a.listedSeq, k.D.Bool(false))
	if n == 1 {
		ij.filesListed--
	}
	return err
}

// errNotImportUnit is the refusal of a unit no import job queued.
func errNotImportUnit() error {
	return tenant.Precondition("the unit of work is not one of an import job's")
}

// judgeImportPath refuses a path outside the unit's folder, and one at or
// before its cursor; it reports false for the path its cursor holds, which
// was recorded before. No message names the path.
func judgeImportPath(u importUnitRow, p string) (bool, error) {
	if !tenant.ImportPathUnder(u.unit.Folder, p) {
		return false, tenant.Precondition("the path is not under the unit's folder")
	}
	if !u.cursorSet {
		return true, nil
	}
	switch c := importpath.Compare(p, u.unit.Cursor); {
	case c == 0:
		return false, nil
	case c < 0:
		return false, tenant.Precondition("the path is before the unit's cursor")
	}
	return true, nil
}

// CheckImportFences checks the fence of each arrival, in the order given,
// each once, locking each unit of work's row first: a driver calls it
// first in the transaction of a batch of received or held files, before
// any reception's row, as every fenced write locks its unit first. A fence
// that no longer holds refuses the whole batch with ErrLeaseLost.
func CheckImportFences(ctx context.Context, tx SendingTx, arrivals []*tenant.ImportArrival) error {
	var seen []tenant.WorkFence
	for _, a := range arrivals {
		if a == nil || slices.Contains(seen, a.Fence) {
			continue
		}
		seen = append(seen, a.Fence)
		if err := tx.CheckFence(ctx, a.Fence); err != nil {
			return err
		}
	}
	return nil
}

// ReceivedArrivals returns the import arrivals a batch of received files
// carries, for CheckImportFences; nil when it carries none.
func ReceivedArrivals(files []tenant.ReceivedFile) []*tenant.ImportArrival {
	var out []*tenant.ImportArrival
	for _, f := range files {
		if f.Import != nil {
			out = append(out, f.Import)
		}
	}
	return out
}

// HeldArrivals returns the import arrivals a batch of quarantine records
// carries, for CheckImportFences; nil when it carries none.
func HeldArrivals(files []tenant.QuarantinedFile) []*tenant.ImportArrival {
	var out []*tenant.ImportArrival
	for _, q := range files {
		if q.Import != nil {
			out = append(out, q.Import)
		}
	}
	return out
}

// importLink is what links an arrival to its received file.
type importLink struct {
	reception, uid string
	sum            tenant.SHA256
}

// RecordImportArrival records a file an import unit handed in, as a
// received file of the reception with the UID and received checksum
// given, in the transaction of the batch that records it (after
// CheckImportFences): the arrival, processing, the unit's cursor, and
// Found, HandedIn and the bytes on the unit and its job. A file at the
// unit's cursor was recorded before and changes nothing.
func (k Sending) RecordImportArrival(ctx context.Context, tx SendingTx, a tenant.ImportArrival, receptionID, uid string,
	sum tenant.SHA256, at time.Time) error {
	_, err := k.recordArrival(ctx, tx, a, &importLink{reception: receptionID, uid: uid, sum: sum}, nil, at)
	return err
}

// recordArrival records a file handed in: into processing, linked to its
// received file (link), or decided at once as held (held, the record of a
// file held as it arrived). It returns the unit, and false when the file
// was recorded before.
func (k Sending) recordArrival(ctx context.Context, tx SendingTx, a tenant.ImportArrival, link *importLink,
	held *tenant.QuarantinedFile, at time.Time) (bool, error) {
	u, found, err := k.importUnitOf(ctx, tx, a.Fence.ID, k.D.lockOf("u"))
	if err != nil {
		return false, err
	}
	if !found {
		return false, errNotImportUnit()
	}
	fresh, err := judgeImportPath(u, a.Path)
	if err != nil || !fresh {
		return false, err
	}
	ij, found, err := importJobInTx(ctx, tx, u.jobID, k.D.Lock)
	if err != nil {
		return false, err
	}
	if !found {
		return false, errNotImportUnit()
	}
	key := tenant.ImportPathKey(a.Path)
	d := importDelta{found: 1, handedIn: 1, bytes: a.Size}
	if held == nil {
		d.processing = 1
		if _, err := tx.Exec(ctx, `INSERT INTO import_arrivals (job_id, path_key, unit_id, reception_id, sop_instance_uid, sha256,
			size, outcome, listed_seq, at) VALUES (?, ?, ?, ?, ?, ?, ?, '', 0, ?)`, u.jobID, key, u.unit.ID, link.reception,
			link.uid, link.sum[:], a.Size, k.D.Time(at)); err != nil {
			return false, err
		}
	} else {
		kind, reason := heldOutcome(held.Reason)
		if kind == tenant.ImportFileLost {
			d.lost = 1
		} else {
			d.quarantined = 1
			if err := addImportTally(ctx, tx, u.jobID, u.unit.ID, tallyQuarantined, string(reason), 1); err != nil {
				return false, err
			}
		}
		if _, err := k.listImportFile(ctx, tx, &ij, importListed{pathKey: key, kind: kind, unitID: u.unit.ID,
			quarantine: held.ID, reason: reason, reception: held.ReceptionID, size: a.Size, at: at}); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE quarantine SET import_job = ?, import_path = ? WHERE id = ?`,
			u.jobID, a.Path, held.ID); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE import_units SET cursor_key = ? WHERE job_id = ? AND unit_id = ?`, key, u.jobID, u.unit.ID); err != nil {
		return false, err
	}
	if err := k.addToUnit(ctx, tx, u.jobID, u.unit.ID, d); err != nil {
		return false, err
	}
	return true, k.saveImportJob(ctx, tx, &ij, d)
}

// heldOutcome is the outcome a quarantine record gives an arrival: lost
// for a copy gone from the incoming area, quarantined by its reason
// otherwise.
func heldOutcome(r tenant.QuarantineReason) (tenant.ImportFileKind, tenant.QuarantineReason) {
	if r == tenant.QuarantineIncomingLost {
		return tenant.ImportFileLost, ""
	}
	return tenant.ImportFileQuarantined, r
}

// arrival is a row of import_arrivals.
type arrival struct {
	jobID, unitID string
	pathKey       []byte
	reception     string
	size          int64
	listedSeq     int64
}

// arrivalColumns are an arrival's columns as the deciding transactions read
// them.
const arrivalColumns = `job_id, unit_id, path_key, reception_id, size, listed_seq`

// readArrivals reads the arrivals query selects, in its order.
func readArrivals(ctx context.Context, tx SendingTx, query string, args ...any) ([]arrival, error) {
	var out []arrival
	err := tx.Query(ctx, query, args, func(r Row) error {
		var a arrival
		err := r.Scan(&a.jobID, &a.unitID, &a.pathKey, &a.reception, &a.size, &a.listedSeq)
		out = append(out, a)
		return err
	})
	return out, err
}

// hasArrivals reports whether a reception has an import's file at all: the
// one probe of a deciding transaction of a reception no import fed, on the
// index it would read anyway.
func hasArrivals(ctx context.Context, tx SendingTx, receptionID string) (bool, error) {
	return exists(ctx, tx, `SELECT 1 FROM import_arrivals WHERE reception_id = ? LIMIT 1`, receptionID)
}

// outcome is what a deciding transaction makes of an arrival.
type outcome struct {
	kind tenant.ImportFileKind // quarantined, lost, failed, discarded; "" for archived, already held, retried
	// held is an arrival archived (false) or already held (true), for kind
	// "" when retryInto is empty.
	held bool
	// retryInto names the reception a retried arrival moves into.
	retryInto  string
	quarantine string
	reason     tenant.QuarantineReason
}

// decide gives each arrival its outcome, in one pass per job and unit, in
// the order of their ids, so two deciding transactions lock the rows they
// share in one order (units, then the job), and returns the events of the
// jobs that ended.
func (k Sending) decide(ctx context.Context, tx SendingTx, arrivals []arrival, outs []outcome, now time.Time) ([]tenant.EventRecord, error) {
	if len(arrivals) == 0 {
		return nil, nil
	}
	type unitKey struct{ job, unit string }
	deltas := map[unitKey]importDelta{}
	var jobs []string
	for i, a := range arrivals {
		o := outs[i]
		var d importDelta
		switch {
		case o.retryInto != "":
			d = importDelta{failed: -1, processing: 1}
		case o.kind == tenant.ImportFileDiscarded:
			d = importDelta{failed: -1, discarded: 1}
		case o.kind == tenant.ImportFileFailed:
			d = importDelta{processing: -1, failed: 1}
		case o.kind == tenant.ImportFileQuarantined:
			d = importDelta{processing: -1, quarantined: 1}
		case o.kind == tenant.ImportFileLost:
			d = importDelta{processing: -1, lost: 1}
		case o.held:
			d = importDelta{processing: -1, alreadyHeld: 1}
		default:
			d = importDelta{processing: -1, archived: 1}
		}
		key := unitKey{a.jobID, a.unitID}
		deltas[key] = deltas[key].add(d)
		if !slices.Contains(jobs, a.jobID) {
			jobs = append(jobs, a.jobID)
		}
	}
	slices.Sort(jobs)
	keys := make([]unitKey, 0, len(deltas))
	for k := range deltas {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b unitKey) int {
		if c := strings.Compare(a.job, b.job); c != 0 {
			return c
		}
		return strings.Compare(a.unit, b.unit)
	})
	var events []tenant.EventRecord
	for _, jobID := range jobs {
		var total importDelta
		for _, key := range keys {
			if key.job != jobID {
				continue
			}
			if err := k.addToUnit(ctx, tx, key.job, key.unit, deltas[key]); err != nil {
				return nil, err
			}
			total = total.add(deltas[key])
		}
		ij, found, err := importJobInTx(ctx, tx, jobID, k.D.Lock)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		for i, a := range arrivals {
			if a.jobID != jobID {
				continue
			}
			if err := k.settleArrival(ctx, tx, &ij, a, outs[i], now); err != nil {
				return nil, err
			}
		}
		if err := k.saveImportJob(ctx, tx, &ij, total); err != nil {
			return nil, err
		}
		more, err := k.settleImportJob(ctx, tx, jobID, now)
		if err != nil {
			return nil, err
		}
		events = append(events, more...)
	}
	return events, nil
}

// settleArrival writes what an outcome leaves of an arrival: its row
// removed, kept failed, or moved into a retry's reception; its listed row;
// its count by quarantine reason.
func (k Sending) settleArrival(ctx context.Context, tx SendingTx, ij *importJobRow, a arrival, o outcome, now time.Time) error {
	switch {
	case o.retryInto != "":
		if err := k.hideFailed(ctx, tx, ij, a); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_arrivals SET reception_id = ?, outcome = '', listed_seq = 0 WHERE job_id = ? AND path_key = ?`,
			o.retryInto, a.jobID, a.pathKey)
		return err
	case o.kind == tenant.ImportFileFailed:
		seq, err := k.listImportFile(ctx, tx, ij, importListed{pathKey: a.pathKey, kind: o.kind, unitID: a.unitID,
			reception: a.reception, size: a.size, at: now})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE import_arrivals SET outcome = 'failed', listed_seq = ? WHERE job_id = ? AND path_key = ?`,
			seq, a.jobID, a.pathKey)
		return err
	case o.kind == tenant.ImportFileDiscarded:
		if err := k.hideFailed(ctx, tx, ij, a); err != nil {
			return err
		}
		fallthrough
	case o.kind == tenant.ImportFileQuarantined || o.kind == tenant.ImportFileLost:
		if _, err := k.listImportFile(ctx, tx, ij, importListed{pathKey: a.pathKey, kind: o.kind, unitID: a.unitID,
			quarantine: o.quarantine, reason: o.reason, reception: a.reception, size: a.size, at: now}); err != nil {
			return err
		}
		if o.kind == tenant.ImportFileQuarantined {
			if err := addImportTally(ctx, tx, a.jobID, a.unitID, tallyQuarantined, string(o.reason), 1); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM import_arrivals WHERE job_id = ? AND path_key = ?`, a.jobID, a.pathKey)
	return err
}

// ArchivedFile is an instance a commit confirmed: its SOP Instance UID and
// received checksum, the key of the received file it was archived from.
type ArchivedFile struct {
	UID string
	Sum tenant.SHA256
}

// ImportArchived gives the arrivals of the instances a commit of the
// reception confirmed their outcome, in the commit's transaction: archived
// for the first in path order of each instance, already held for any other
// arrival of the same file (one instance in two folders).
func (k Sending) ImportArchived(ctx context.Context, tx SendingTx, receptionID string, files []ArchivedFile, now time.Time) ([]tenant.EventRecord, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if has, err := hasArrivals(ctx, tx, receptionID); err != nil || !has {
		return nil, err
	}
	var arrivals []arrival
	var outs []outcome
	for _, f := range files {
		as, err := readArrivals(ctx, tx, `SELECT `+arrivalColumns+` FROM import_arrivals WHERE reception_id = ?
			AND sop_instance_uid = ? AND sha256 = ? AND outcome = '' ORDER BY path_key, job_id`, receptionID, f.UID, f.Sum[:])
		if err != nil {
			return nil, err
		}
		for i, a := range as {
			arrivals = append(arrivals, a)
			outs = append(outs, outcome{held: i > 0}) // one new instance, whatever job handed it in
		}
	}
	return k.decide(ctx, tx, arrivals, outs, now)
}

// ImportHeld writes, in the transaction of a quarantine record just made,
// what it means for an import: for a file held as it arrived (q.Import),
// its arrival decided at once (after CheckImportFences); for a file of a
// reception, the outcome of the arrivals of its received file
// (quarantined by its reason, or lost), the record naming the job and the
// path of the first of them in path order; for a record a release
// quarantined again, the job and the path of the record it came from.
func (k Sending) ImportHeld(ctx context.Context, tx SendingTx, q tenant.QuarantinedFile, now time.Time) ([]tenant.EventRecord, error) {
	if q.Import != nil {
		_, err := k.recordArrival(ctx, tx, *q.Import, nil, &q, now)
		return nil, err
	}
	if q.ReleasedFrom != "" {
		_, err := tx.Exec(ctx, `UPDATE quarantine SET import_job = (SELECT import_job FROM quarantine WHERE id = ?),
			import_path = (SELECT import_path FROM quarantine WHERE id = ?) WHERE id = ?`, q.ReleasedFrom, q.ReleasedFrom, q.ID)
		return nil, err
	}
	if q.ReceptionID == "" || q.SOPInstanceUID == "" || q.SHA256.IsZero() {
		return nil, nil
	}
	if has, err := hasArrivals(ctx, tx, q.ReceptionID); err != nil || !has {
		return nil, err
	}
	arrivals, err := readArrivals(ctx, tx, `SELECT `+arrivalColumns+` FROM import_arrivals WHERE reception_id = ?
		AND sop_instance_uid = ? AND sha256 = ? AND outcome = '' ORDER BY path_key, job_id`, q.ReceptionID, q.SOPInstanceUID, q.SHA256[:])
	if err != nil || len(arrivals) == 0 {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE quarantine SET import_job = ?, import_path = ? WHERE id = ?`,
		arrivals[0].jobID, tenant.ImportPathOfKey(arrivals[0].pathKey), q.ID); err != nil {
		return nil, err
	}
	kind, reason := heldOutcome(q.Reason)
	outs := make([]outcome, len(arrivals))
	for i := range outs {
		outs[i] = outcome{kind: kind, quarantine: q.ID, reason: reason}
	}
	return k.decide(ctx, tx, arrivals, outs, now)
}

// ImportEnded gives the arrivals of a reception that reached an end and
// still have no outcome theirs, in the step's transaction: failed when it
// failed, already held otherwise, since a reception that did not fail has
// archived, ignored as the same file, or quarantined each received file,
// and the first and last were decided where they happened.
func (k Sending) ImportEnded(ctx context.Context, tx SendingTx, receptionID string, failed bool, now time.Time) ([]tenant.EventRecord, error) {
	if has, err := hasArrivals(ctx, tx, receptionID); err != nil || !has {
		return nil, err
	}
	arrivals, err := readArrivals(ctx, tx, `SELECT `+arrivalColumns+` FROM import_arrivals WHERE reception_id = ? AND outcome = ''
		ORDER BY job_id, path_key`, receptionID)
	if err != nil {
		return nil, err
	}
	outs := make([]outcome, len(arrivals))
	for i := range outs {
		if failed {
			outs[i] = outcome{kind: tenant.ImportFileFailed}
		} else {
			outs[i] = outcome{held: true}
		}
	}
	return k.decide(ctx, tx, arrivals, outs, now)
}

// ImportRetried moves the failed arrivals of a failed reception back to
// processing, into the reception that runs its files again, in the retry's
// transaction.
func (k Sending) ImportRetried(ctx context.Context, tx SendingTx, failedID, retryID string, now time.Time) ([]tenant.EventRecord, error) {
	return k.fromFailed(ctx, tx, failedID, outcome{retryInto: retryID}, now)
}

// ImportDiscarded makes the failed arrivals of a reception whose files a
// person discarded discarded, in the discard's transaction.
func (k Sending) ImportDiscarded(ctx context.Context, tx SendingTx, receptionID string, now time.Time) ([]tenant.EventRecord, error) {
	return k.fromFailed(ctx, tx, receptionID, outcome{kind: tenant.ImportFileDiscarded}, now)
}

// fromFailed gives every failed arrival of a reception the outcome o.
func (k Sending) fromFailed(ctx context.Context, tx SendingTx, receptionID string, o outcome, now time.Time) ([]tenant.EventRecord, error) {
	if has, err := hasArrivals(ctx, tx, receptionID); err != nil || !has {
		return nil, err
	}
	arrivals, err := readArrivals(ctx, tx, `SELECT `+arrivalColumns+` FROM import_arrivals WHERE reception_id = ? AND outcome = 'failed'
		ORDER BY job_id, path_key`, receptionID)
	if err != nil {
		return nil, err
	}
	outs := make([]outcome, len(arrivals))
	for i := range outs {
		outs[i] = o
	}
	return k.decide(ctx, tx, arrivals, outs, now)
}
