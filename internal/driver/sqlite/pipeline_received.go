package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/sqlkit"
	"github.com/amrshadid/magnetar/internal/tenant"
)

const receivedColumns = `reception_id, sop_instance_uid, sha256, series_uid, study_uid, sop_class_uid, transfer_syntax_uid,
	size, incoming, calling_ae, called_ae, remote_addr, association, released, received`

// scanReceived reads one row of receivedColumns.
func scanReceived(row interface{ Scan(...any) error }) (tenant.ReceivedFile, error) {
	var f tenant.ReceivedFile
	var sum []byte
	var received int64
	err := row.Scan(&f.ReceptionID, &f.SOPInstanceUID, &sum, &f.SeriesUID, &f.StudyUID, &f.SOPClassUID, &f.TransferSyntaxUID,
		&f.Size, &f.Incoming, &f.Caller.CallingAETitle, &f.Caller.CalledAETitle, &f.Caller.Address, &f.Association,
		&f.Released, &received)
	f.SHA256, f.Received = toSHA256(sum), fromMicros(received)
	return f, err
}

// errReceptionClosed is the refusal of files for a reception that is no
// longer receiving: a precondition, and ErrReceptionClosed for the receiver
// that opens the next arrival.
func errReceptionClosed() error {
	return fault.Wrap(fault.Conflict, errors.Join(tenant.ErrPrecondition, tenant.ErrReceptionClosed),
		"the reception is no longer receiving; its files belong to the next arrival")
}

// AddReceivedFiles implements tenant.Pipeline.
func (t *Tenant) AddReceivedFiles(ctx context.Context, files []tenant.ReceivedFile, at time.Time) (int, error) {
	if err := tenant.ValidateReceivedBatch(files, at); err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, t.open()
	}
	// The files of each reception, in the order the batch first names it.
	var order []string
	byReception := map[string][]tenant.ReceivedFile{}
	for _, f := range files {
		if _, ok := byReception[f.ReceptionID]; !ok {
			order = append(order, f.ReceptionID)
		}
		byReception[f.ReceptionID] = append(byReception[f.ReceptionID], f)
	}
	var inserted int
	err := t.write(ctx, "the received files cannot be recorded", func(tx *sql.Tx, s *tenantStatements) error {
		total := 0
		insert, insertAssoc := tx.StmtContext(ctx, s.insertReceived), tx.StmtContext(ctx, s.insertAssociation)
		for _, id := range order {
			var study, titlesText string
			var truncated bool
			err := tx.StmtContext(ctx, s.receptionForBatch).QueryRowContext(ctx, id).Scan(&study, &titlesText, &truncated)
			if errors.Is(err, sql.ErrNoRows) {
				has, err := exists(ctx, tx, `SELECT 1 FROM receptions WHERE id = ?`, id)
				if err != nil {
					return err
				}
				if !has {
					return fault.New(fault.NotFound, "the tenant has no reception with this id")
				}
				return errReceptionClosed()
			}
			if err != nil {
				return err
			}
			var titles []string
			if err := fromJSON(titlesText, &titles); err != nil {
				return err
			}
			var nFiles, nBytes, nReleased, nAssoc int64
			for _, f := range byReception[id] {
				if f.StudyUID != study {
					return fault.New(fault.Invalid, "a received file's Study Instance UID is its reception's")
				}
				res, err := insert.ExecContext(ctx, f.ReceptionID, f.SOPInstanceUID, f.SHA256[:], f.SeriesUID, f.StudyUID,
					f.SOPClassUID, f.TransferSyntaxUID, f.Size, f.Incoming, f.Caller.CallingAETitle, f.Caller.CalledAETitle,
					f.Caller.Address, f.Association, boolInt(f.Released), micros(at))
				if err != nil {
					return err
				}
				n, err := res.RowsAffected()
				if err != nil {
					return err
				}
				if n == 0 {
					continue
				}
				nFiles++
				nBytes += f.Size
				if f.Released {
					nReleased++
				}
				if f.Association == "" {
					continue
				}
				res, err = insertAssoc.ExecContext(ctx, id, f.Association)
				if err != nil {
					return err
				}
				if n, err = res.RowsAffected(); err != nil {
					return err
				}
				if n == 1 {
					nAssoc++
					titles, truncated = addTitle(titles, truncated, f.Caller.CallingAETitle)
				}
			}
			if nFiles == 0 {
				continue
			}
			titlesText, err = jsonText(titles)
			if err != nil {
				return err
			}
			if _, err := tx.StmtContext(ctx, s.addReceived).ExecContext(ctx, nFiles, nBytes, nReleased, nAssoc,
				titlesText, boolInt(truncated), micros(at), id); err != nil {
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
