package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/sqlkit"
	"github.com/amrshadid/magnetar/internal/tenant"
)

const receivedColumns = `reception_id, sop_instance_uid, sha256, series_uid, study_uid, sop_class_uid, transfer_syntax_uid,
	size, incoming, calling_ae, called_ae, remote_addr, association, released, received`

// scanReceived reads one row of receivedColumns.
func scanReceived(row scanner) (tenant.ReceivedFile, error) {
	var f tenant.ReceivedFile
	var sum []byte
	var received time.Time
	err := row.Scan(&f.ReceptionID, &f.SOPInstanceUID, &sum, &f.SeriesUID, &f.StudyUID, &f.SOPClassUID, &f.TransferSyntaxUID,
		&f.Size, &f.Incoming, &f.Caller.CallingAETitle, &f.Caller.CalledAETitle, &f.Caller.Address, &f.Association,
		&f.Released, &received)
	f.SHA256, f.Received = toSHA256(sum), received.UTC()
	return f, err
}

// errReceptionClosed is the refusal of files for a reception that is no
// longer receiving: a precondition, and ErrReceptionClosed for the receiver
// that opens the next arrival.
func errReceptionClosed() error {
	return fault.Wrap(fault.Conflict, errors.Join(tenant.ErrPrecondition, tenant.ErrReceptionClosed),
		"the reception is no longer receiving; its files belong to the next arrival")
}

// fileKey is what makes two received files of one reception the same file.
type fileKey struct {
	uid string
	sum tenant.SHA256
}

// insertReceivedSQL writes a set of received files in one statement, one
// array for each column (unnest), so a batch of any size is fifteen
// parameters and one round trip, far below Postgres's 65535; a file already
// recorded is skipped, and the files written are returned.
const insertReceivedSQL = `INSERT INTO received_files (` + receivedColumns + `)
	SELECT * FROM unnest($1::text[], $2::text[], $3::bytea[], $4::text[], $5::text[], $6::text[], $7::text[], $8::bigint[],
		$9::text[], $10::text[], $11::text[], $12::text[], $13::text[], $14::boolean[], $15::timestamptz[])
	ON CONFLICT DO NOTHING RETURNING sop_instance_uid, sha256`

// insertReceived writes files (all of one reception, no key twice) with
// insertReceivedSQL, at the time given, and returns the keys of those that
// were new.
func insertReceived(ctx context.Context, tx pgx.Tx, files []tenant.ReceivedFile, at time.Time) (map[fileKey]bool, error) {
	n := len(files)
	text := func() []string { return make([]string, 0, n) }
	rids, uids, series, studies, classes, syntaxes, incoming, calling, called, addrs, assocs :=
		text(), text(), text(), text(), text(), text(), text(), text(), text(), text(), text()
	sums := make([][]byte, 0, n)
	sizes := make([]int64, 0, n)
	released := make([]bool, 0, n)
	times := make([]time.Time, 0, n)
	for _, f := range files {
		rids, uids, sums = append(rids, f.ReceptionID), append(uids, f.SOPInstanceUID), append(sums, sumBytes(f.SHA256))
		series, studies, classes = append(series, f.SeriesUID), append(studies, f.StudyUID), append(classes, f.SOPClassUID)
		syntaxes, sizes, incoming = append(syntaxes, f.TransferSyntaxUID), append(sizes, f.Size), append(incoming, f.Incoming)
		calling, called, addrs = append(calling, f.Caller.CallingAETitle), append(called, f.Caller.CalledAETitle), append(addrs, f.Caller.Address)
		assocs, released, times = append(assocs, f.Association), append(released, f.Released), append(times, micros(at))
	}
	rows, err := tx.Query(ctx, insertReceivedSQL, rids, uids, sums, series, studies, classes, syntaxes, sizes, incoming,
		calling, called, addrs, assocs, released, times)
	keys, err := collect(rows, err, func(row scanner) (fileKey, error) {
		var k fileKey
		var sum []byte
		err := row.Scan(&k.uid, &sum)
		k.sum = toSHA256(sum)
		return k, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[fileKey]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out, nil
}

// AddReceivedFiles implements tenant.Pipeline. Each reception the batch
// names is locked first, FOR NO KEY UPDATE (the lock its UPDATE of counts
// takes), in order of id, in the statement that checks it is receiving: a
// step and a batch of one reception are then ordered, and two batches naming
// two receptions take them in one order. Its files are then written as one
// set, and its counts added by one UPDATE on the row it holds.
func (t *Tenant) AddReceivedFiles(ctx context.Context, files []tenant.ReceivedFile, at time.Time) (int, error) {
	if err := tenant.ValidateReceivedBatch(files, at); err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, t.open()
	}
	// The files of each reception, in the batch's order, each key once (the
	// first kept, as a row-by-row insert would keep it).
	byReception := map[string][]tenant.ReceivedFile{}
	seen := map[string]map[fileKey]bool{}
	for _, f := range files {
		if seen[f.ReceptionID] == nil {
			seen[f.ReceptionID] = map[fileKey]bool{}
		}
		k := fileKey{f.SOPInstanceUID, f.SHA256}
		if seen[f.ReceptionID][k] {
			continue
		}
		seen[f.ReceptionID][k] = true
		byReception[f.ReceptionID] = append(byReception[f.ReceptionID], f)
	}
	order := make([]string, 0, len(byReception))
	for id := range byReception {
		order = append(order, id)
	}
	slices.Sort(order)
	var inserted int
	err := t.pipe(ctx, "the received files cannot be recorded", func(tx pgx.Tx) error {
		total := 0
		for _, id := range order {
			var study, titlesText string
			var truncated bool
			err := tx.QueryRow(ctx, `SELECT study_uid, calling_ae_titles, calling_truncated FROM receptions
				WHERE id = $1 AND state = 'receiving' FOR NO KEY UPDATE`, id).Scan(&study, &titlesText, &truncated)
			if isNoRow(err) {
				has, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = $1`, id)
				if err != nil {
					return err
				}
				if !has {
					return errNoReception()
				}
				return errReceptionClosed()
			}
			if err != nil {
				return err
			}
			batch := byReception[id]
			for _, f := range batch {
				if f.StudyUID != study {
					return fault.New(fault.Invalid, "a received file's Study Instance UID is its reception's")
				}
			}
			var titles []string
			if err := fromJSON(titlesText, &titles); err != nil {
				return err
			}
			added, err := insertReceived(ctx, tx, batch, at)
			if err != nil {
				return err
			}
			if len(added) == 0 {
				continue
			}
			var nFiles, nBytes, nReleased int64
			var assocs []string
			for _, f := range batch {
				if !added[fileKey{f.SOPInstanceUID, f.SHA256}] {
					continue
				}
				nFiles++
				nBytes += f.Size
				if f.Released {
					nReleased++
				}
				if f.Association != "" && !slices.Contains(assocs, f.Association) {
					assocs = append(assocs, f.Association)
				}
			}
			// The associations of the new files that the reception sees for
			// the first time, each with the calling AE title of the first new
			// file it brought, in the order the batch first names them.
			newAssoc := map[string]bool{}
			if len(assocs) > 0 {
				rows, err := tx.Query(ctx, `INSERT INTO reception_associations (reception_id, association)
					SELECT $1, a FROM unnest($2::text[]) AS a ON CONFLICT DO NOTHING RETURNING association`, id, assocs)
				names, err := collect(rows, err, func(row scanner) (string, error) {
					var a string
					err := row.Scan(&a)
					return a, err
				})
				if err != nil {
					return err
				}
				for _, a := range names {
					newAssoc[a] = true
				}
			}
			nAssoc := int64(len(newAssoc))
			for _, f := range batch {
				if newAssoc[f.Association] && added[fileKey{f.SOPInstanceUID, f.SHA256}] {
					delete(newAssoc, f.Association)
					titles, truncated = addTitle(titles, truncated, f.Caller.CallingAETitle)
				}
			}
			if titles == nil {
				titles = []string{}
			}
			titlesText, err = jsonText(titles)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE receptions SET files_received = files_received + $1, bytes_received = bytes_received + $2,
				files_released = files_released + $3, associations = associations + $4, calling_ae_titles = $5, calling_truncated = $6,
				last_file = greatest(last_file, $7) WHERE id = $8`,
				nFiles, nBytes, nReleased, nAssoc, titlesText, truncated, micros(at), id); err != nil {
				return err
			}
			total += int(nFiles)
		}
		inserted = total
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// addTitle adds a calling AE title to a reception's list when it is not in
// it, keeping at most tenant.MaxCallingAETitles and saying when there were
// more. It returns a new slice, never changing the one given, so a change
// that runs again starts from what it read.
func addTitle(titles []string, truncated bool, title string) ([]string, bool) {
	if title == "" || slices.Contains(titles, title) {
		return titles, truncated
	}
	if len(titles) >= tenant.MaxCallingAETitles {
		return titles, true
	}
	return append(slices.Clip(titles), title), truncated
}

// ReceivedFiles implements tenant.Pipeline.
func (t *Tenant) ReceivedFiles(ctx context.Context, receptionID string, page master.Page) ([]tenant.ReceivedFile, string, error) {
	if err := master.ValidateID("reception id", receptionID); err != nil {
		return nil, "", err
	}
	return t.pageReceived(ctx, listReceivedFiles, listReceivedSQL, receptionID, page)
}

// pageReceived pages a reception's received files with query, which takes
// the reception, the (SOP Instance UID, checksum) after which to start, and
// the limit, in the files' primary key order.
func (t *Tenant) pageReceived(ctx context.Context, list, query, receptionID string, page master.Page) ([]tenant.ReceivedFile, string, error) {
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	binding := t.binding(receptionID)
	afterUID, afterSum := "", []byte{}
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, list, binding, 2)
		if err != nil {
			return nil, "", err
		}
		sum, err := hex.DecodeString(f[1])
		if err != nil {
			return nil, "", fault.New(fault.Invalid, "the page token is not one this list returned with these arguments")
		}
		afterUID, afterSum = f[0], sum
	}
	out, err := readRows(ctx, t, "the received files cannot be listed", query,
		[]any{receptionID, afterUID, afterSum, page.Limit + 1}, scanReceived)
	if err != nil {
		return nil, "", err
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(list, binding, last.SOPInstanceUID, hex.EncodeToString(last.SHA256[:])), nil
}

// sortClaims returns the indexes of files in sorted order of SOP Instance
// UID, then checksum, byte by byte, the order every claim takes its rows in.
func sortClaims(files []tenant.InstanceClaim) []int {
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		if c := strings.Compare(files[a].SOPInstanceUID, files[b].SOPInstanceUID); c != 0 {
			return c
		}
		return bytes.Compare(files[a].SHA256[:], files[b].SHA256[:])
	})
	return order
}
