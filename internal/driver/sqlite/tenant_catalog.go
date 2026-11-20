package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/driver/sqlkit"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/tenant"
)

// The catalog's reads (tenant.CatalogReads): the statements are sqlkit's,
// shared with every SQL driver; this file runs them on the read pool and
// reads their rows.

// catalogSQL writes the catalog's reads with SQLite's parameters and
// times.
var catalogSQL = sqlkit.CatalogSQL{
	Param: func(int) string { return "?" },
	Time:  func(t time.Time) any { return micros(t) },
}

// Statements of the catalog's reads that differ between the drivers: a
// list of ids given as one JSON array (json_each), each id a lookup of a
// primary key.
const (
	// sessionsByIDSQL reads the sessions of a page of a modality's.
	sessionsByIDSQL = `SELECT ` + sqlkit.SessionColumns + ` FROM sessions WHERE id IN (SELECT value FROM json_each(?))`
	// modalitiesSQL reads the modalities of a page's sessions:
	// session_modalities' primary key. They are put in order by the
	// reader, a few a session, rather than sorted by the database.
	modalitiesSQL = `SELECT session_id, modality FROM session_modalities WHERE session_id IN (SELECT value FROM json_each(?))`
)

// rowScanner is a row or rows being read.
type rowScanner = interface{ Scan(...any) error }

// subjectRow is a subject read with its key.
type subjectRow struct {
	key sqlkit.CatalogKey
	s   tenant.CatalogSubject
}

// scanSubject reads a row of sqlkit.SubjectColumns.
func scanSubject(row rowScanner) (subjectRow, error) {
	var r subjectRow
	var created, updated int64
	err := row.Scan(&r.key.ID, &created, &r.key.Folded, &r.s.Label, &r.s.Sessions, &r.s.Instances, &r.s.Bytes, &updated)
	r.key.Created, r.s.Created, r.s.Updated = fromMicros(created), fromMicros(created), fromMicros(updated)
	return r, err
}

// sessionRead is a session read with its key.
type sessionRead struct {
	key sqlkit.CatalogKey
	s   tenant.CatalogSession
}

// scanSessionRow reads a row of sqlkit.SessionColumns.
func scanSessionRow(row rowScanner) (sessionRead, error) {
	var r sessionRead
	var created, updated int64
	err := row.Scan(&r.key.ID, &created, &r.key.SubjectFolded, &r.key.Folded, &r.s.StudyDate, &r.s.SubjectLabel, &r.s.Label,
		&r.s.StudyUID, &r.s.Scans, &r.s.Instances, &r.s.Bytes, &r.s.Storage, &r.s.Receiver, &updated)
	r.key.StudyDate = r.s.StudyDate
	r.key.Created, r.s.Created, r.s.Updated = fromMicros(created), fromMicros(created), fromMicros(updated)
	return r, err
}

// scanRead is a scan read with its key.
type scanRead struct {
	key sqlkit.CatalogKey
	s   tenant.CatalogScan
}

// scanScanRow reads a row of sqlkit.ScanColumns.
func scanScanRow(row rowScanner) (scanRead, error) {
	var r scanRead
	var created, updated int64
	var number sql.NullInt32
	var manifest []byte
	err := row.Scan(&r.key.ID, &created, &r.key.SubjectFolded, &r.key.SessionFolded, &r.key.Folded, &r.key.DescriptionFolded,
		&r.s.SubjectLabel, &r.s.SessionLabel, &r.s.Label, &r.s.SeriesUID, &r.s.Values.Modality, &r.s.Values.SeriesDescription,
		&number, &r.s.Instances, &r.s.Bytes, &r.s.Storage, &manifest, &r.s.Receiver, &updated)
	r.s.Values.SeriesNumber, r.s.Values.HasSeriesNumber = number.Int32, number.Valid
	r.s.ManifestSHA256 = sqlkit.ToSHA256(manifest)
	r.key.Created, r.s.Created, r.s.Updated = fromMicros(created), fromMicros(created), fromMicros(updated)
	return r, err
}

// scanHeldInstance reads a row of sqlkit.HeldInstanceColumns.
func scanHeldInstance(row rowScanner) (tenant.ScanInstance, error) {
	var in tenant.ScanInstance
	var sum, archived []byte
	var held int64
	err := row.Scan(&in.SOPInstanceUID, &in.ReceptionID, &in.Storage, &in.Key, &in.Offset, &in.Length, &in.Size,
		&sum, &archived, &held)
	in.SHA256, in.ArchivedSHA256, in.Held = sqlkit.ToSHA256(sum), sqlkit.ToSHA256(archived), fromMicros(held)
	return in, err
}

// subjectByLabel reads the subject with this label, or NotFound.
func (t *Tenant) subjectByLabel(ctx context.Context, subjectLabel string) (subjectRow, error) {
	q := catalogSQL.SubjectByLabel(subjectLabel)
	return readOne(ctx, t, "the tenant has no subject with this label", "the subject cannot be read", q.SQL, q.Args, scanSubject)
}

// sessionByLabels reads the session with these labels, or NotFound.
func (t *Tenant) sessionByLabels(ctx context.Context, subjectLabel, sessionLabel string) (sessionRead, error) {
	q := catalogSQL.SessionByLabels(subjectLabel, sessionLabel)
	return readOne(ctx, t, "the tenant has no session with these labels", "the session cannot be read", q.SQL, q.Args, scanSessionRow)
}

// scanByLabels reads the scan with these labels, or NotFound.
func (t *Tenant) scanByLabels(ctx context.Context, p label.Path) (scanRead, error) {
	q := catalogSQL.ScanByLabels(p)
	return readOne(ctx, t, "the tenant has no scan with these labels", "the scan cannot be read", q.SQL, q.Args, scanScanRow)
}

// CatalogSubject implements tenant.SubjectReads.
func (t *Tenant) CatalogSubject(ctx context.Context, subjectLabel string) (tenant.CatalogSubject, error) {
	if err := tenant.ValidateLabel("subject", subjectLabel); err != nil {
		return tenant.CatalogSubject{}, err
	}
	r, err := t.subjectByLabel(ctx, subjectLabel)
	return r.s, err
}

// ListSubjects implements tenant.SubjectReads.
func (t *Tenant) ListSubjects(ctx context.Context, f tenant.SubjectFilter, page master.Page) (tenant.CatalogPage[tenant.CatalogSubject], error) {
	var out tenant.CatalogPage[tenant.CatalogSubject]
	if err := f.Validate(); err != nil {
		return out, err
	}
	binding := t.binding(sqlkit.SubjectFilterParts(f)...)
	after, err := sqlkit.CatalogAfter(page, sqlkit.ListCatalogSubjects, binding, f.Order, 1)
	if err != nil {
		return out, err
	}
	if f.Total() == tenant.TotalTenant {
		totals, err := t.CatalogTotals(ctx)
		if err != nil {
			return out, err
		}
		out.Total = &totals.Subjects
	}
	q := catalogSQL.Subjects(f, after, page.Limit+1)
	rows, err := readRows(ctx, t, "the subjects cannot be listed", q.SQL, q.Args, scanSubject)
	if err != nil {
		return out, err
	}
	rows, last, more := sqlkit.Cut(rows, page.Limit)
	for _, r := range rows {
		out.Items = append(out.Items, r.s)
	}
	if more {
		out.Next = sqlkit.CatalogCursor(sqlkit.ListCatalogSubjects, binding, f.Order, 1, last.key)
	}
	return out, nil
}

// CatalogSession implements tenant.SessionReads.
func (t *Tenant) CatalogSession(ctx context.Context, subjectLabel, sessionLabel string) (tenant.CatalogSession, error) {
	for _, l := range [][2]string{{"subject", subjectLabel}, {"session", sessionLabel}} {
		if err := tenant.ValidateLabel(l[0], l[1]); err != nil {
			return tenant.CatalogSession{}, err
		}
	}
	r, err := t.sessionByLabels(ctx, subjectLabel, sessionLabel)
	if err != nil {
		return tenant.CatalogSession{}, err
	}
	rows := []sessionRead{r}
	if err := t.addModalities(ctx, rows); err != nil {
		return tenant.CatalogSession{}, err
	}
	return rows[0].s, nil
}

// ListSessions implements tenant.SessionReads.
func (t *Tenant) ListSessions(ctx context.Context, f tenant.SessionFilter, page master.Page) (tenant.CatalogPage[tenant.CatalogSession], error) {
	var out tenant.CatalogPage[tenant.CatalogSession]
	if err := f.Validate(); err != nil {
		return out, err
	}
	binding := t.binding(sqlkit.SessionFilterParts(f)...)
	after, err := sqlkit.CatalogAfter(page, sqlkit.ListCatalogSessions, binding, f.Order, 2)
	if err != nil {
		return out, err
	}
	var subjectID int64
	if f.SubjectLabel != "" {
		sub, err := t.subjectByLabel(ctx, f.SubjectLabel)
		if err != nil {
			return out, err
		}
		subjectID = sub.key.ID
		if f.Total() == tenant.TotalParent {
			out.Total = &sub.s.Sessions
		}
	} else if f.Total() == tenant.TotalTenant {
		totals, err := t.CatalogTotals(ctx)
		if err != nil {
			return out, err
		}
		out.Total = &totals.Sessions
	}
	q := catalogSQL.Sessions(f, subjectID, after, page.Limit+1)
	var rows []sessionRead
	if sqlkit.SessionsOfModality(f) {
		rows, err = t.sessionsOfModality(ctx, q)
	} else {
		rows, err = readRows(ctx, t, "the sessions cannot be listed", q.SQL, q.Args, scanSessionRow)
	}
	if err != nil {
		return out, err
	}
	rows, last, more := sqlkit.Cut(rows, page.Limit)
	if err := t.addModalities(ctx, rows); err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Items = append(out.Items, r.s)
	}
	if more {
		out.Next = sqlkit.CatalogCursor(sqlkit.ListCatalogSessions, binding, f.Order, 2, last.key)
	}
	return out, nil
}

// sessionsOfModality reads a page of a modality's sessions: their ids in
// order from session_modalities, then the sessions by id.
func (t *Tenant) sessionsOfModality(ctx context.Context, q sqlkit.CatalogRead) ([]sessionRead, error) {
	ids, err := readRows(ctx, t, "the sessions cannot be listed", q.SQL, q.Args, func(row rowScanner) (int64, error) {
		var id, created int64
		err := row.Scan(&id, &created)
		return id, err
	})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	list, err := sqlkit.JSONText(ids)
	if err != nil {
		return nil, err
	}
	found, err := readRows(ctx, t, "the sessions cannot be listed", sessionsByIDSQL, []any{list}, scanSessionRow)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]sessionRead, len(found))
	for _, r := range found {
		byID[r.key.ID] = r
	}
	out := make([]sessionRead, 0, len(ids))
	for _, id := range ids {
		// A session is never removed, so each id has its row.
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// addModalities sets the modalities of each session read.
func (t *Tenant) addModalities(ctx context.Context, rows []sessionRead) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]int64, len(rows))
	at := make(map[int64]int, len(rows))
	for i, r := range rows {
		ids[i], at[r.key.ID] = r.key.ID, i
	}
	list, err := sqlkit.JSONText(ids)
	if err != nil {
		return err
	}
	type pair struct {
		id       int64
		modality string
	}
	pairs, err := readRows(ctx, t, "the sessions' modalities cannot be read", modalitiesSQL, []any{list}, func(row rowScanner) (pair, error) {
		var p pair
		err := row.Scan(&p.id, &p.modality)
		return p, err
	})
	if err != nil {
		return err
	}
	for _, p := range pairs {
		i := at[p.id]
		rows[i].s.Modalities = append(rows[i].s.Modalities, p.modality)
	}
	for i := range rows {
		slices.Sort(rows[i].s.Modalities)
	}
	return nil
}

// CatalogScan implements tenant.ScanReads.
func (t *Tenant) CatalogScan(ctx context.Context, p label.Path) (tenant.CatalogScan, error) {
	if err := tenant.ValidateScanPath(p); err != nil {
		return tenant.CatalogScan{}, err
	}
	r, err := t.scanByLabels(ctx, p)
	return r.s, err
}

// ListScans implements tenant.ScanReads.
func (t *Tenant) ListScans(ctx context.Context, f tenant.ScanFilter, page master.Page) (tenant.CatalogPage[tenant.CatalogScan], error) {
	var out tenant.CatalogPage[tenant.CatalogScan]
	if err := f.Validate(); err != nil {
		return out, err
	}
	binding := t.binding(sqlkit.ScanFilterParts(f)...)
	after, err := sqlkit.CatalogAfter(page, sqlkit.ListCatalogScans, binding, f.Order, 3)
	if err != nil {
		return out, err
	}
	var sessionID int64
	switch {
	case f.SessionLabel != "":
		se, err := t.sessionByLabels(ctx, f.SubjectLabel, f.SessionLabel)
		if err != nil {
			return out, err
		}
		sessionID = se.key.ID
		if f.Total() == tenant.TotalParent {
			out.Total = &se.s.Scans
		}
	case f.SubjectLabel != "":
		if _, err := t.subjectByLabel(ctx, f.SubjectLabel); err != nil {
			return out, err
		}
	case f.Total() == tenant.TotalTenant:
		totals, err := t.CatalogTotals(ctx)
		if err != nil {
			return out, err
		}
		out.Total = &totals.Scans
	}
	q := catalogSQL.Scans(f, sessionID, after, page.Limit+1)
	rows, err := readRows(ctx, t, "the scans cannot be listed", q.SQL, q.Args, scanScanRow)
	if err != nil {
		return out, err
	}
	rows, last, more := sqlkit.Cut(rows, page.Limit)
	for _, r := range rows {
		out.Items = append(out.Items, r.s)
	}
	if more {
		out.Next = sqlkit.CatalogCursor(sqlkit.ListCatalogScans, binding, f.Order, 3, last.key)
	}
	return out, nil
}

// ListHeldInstances implements tenant.InstanceReads.
func (t *Tenant) ListHeldInstances(ctx context.Context, p label.Path, page master.Page) ([]tenant.ScanInstance, string, error) {
	if err := tenant.ValidateScanPath(p); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	binding := t.binding(sqlkit.ListHeldInstances, p.Subject, p.Session, p.Scan)
	after := ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, sqlkit.ListHeldInstances, binding, 1)
		if err != nil {
			return nil, "", err
		}
		after = f[0]
	}
	scan, err := t.scanByLabels(ctx, p)
	if err != nil {
		return nil, "", err
	}
	q := catalogSQL.HeldInstances(scan.key.ID, after, page.Limit+1)
	out, err := readRows(ctx, t, "the scan's instances cannot be listed", q.SQL, q.Args, scanHeldInstance)
	if err != nil {
		return nil, "", err
	}
	out, last, more := sqlkit.Cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(sqlkit.ListHeldInstances, binding, last.SOPInstanceUID), nil
}

// HeldInstance implements tenant.InstanceReads.
func (t *Tenant) HeldInstance(ctx context.Context, p label.Path, uid string) (tenant.ScanInstance, error) {
	if err := tenant.ValidateScanPath(p); err != nil {
		return tenant.ScanInstance{}, err
	}
	if err := tenant.ValidateInstanceUID(uid); err != nil {
		return tenant.ScanInstance{}, err
	}
	scan, err := t.scanByLabels(ctx, p)
	if err != nil {
		return tenant.ScanInstance{}, err
	}
	q := catalogSQL.HeldInstance(scan.key.ID, uid)
	return readOne(ctx, t, "the scan holds no instance with this SOP Instance UID", "the instance cannot be read", q.SQL, q.Args, scanHeldInstance)
}
