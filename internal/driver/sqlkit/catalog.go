package sqlkit

import (
	"strconv"
	"strings"
	"time"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/tenant"
)

// The catalog's reads (tenant.CatalogReads) as SQL both drivers run, so the
// statements, their indexes and their cursors are one implementation. Each
// list reads one table through the one index its filter and order name,
// with no join: a page is a range of that index, read in its order, cut to
// its limit, so neither database sorts or hashes rows, whatever the size of
// the tables. That is why a session and a scan keep the labels of their
// parents beside their own (both are never changed once given), and a scan
// its session's storage. A session's modalities are read by a second
// statement, for the page's sessions only.
//
// Every statement is built from the constant fragments below; values are
// always parameters.

// The catalog lists' names, the first field of their cursors.
const (
	ListCatalogSubjects = "tenant-catalog-subjects"
	ListCatalogSessions = "tenant-catalog-sessions"
	ListCatalogScans    = "tenant-catalog-scans"
	ListHeldInstances   = "tenant-catalog-instances"
)

// The columns each catalog row is read with, in this order: its key (id,
// created and the texts its orders sort by), then what the reads answer.
const (
	SubjectColumns = `id, created, folded, label, session_count, instance_count, bytes, updated`
	SessionColumns = `id, created, subject_folded, folded, study_date, subject_label, label, study_uid, scan_count,
		instance_count, bytes, storage, receiver, updated`
	ScanColumns = `id, created, subject_folded, session_folded, folded, description_folded, subject_label, session_label, label,
		series_uid, modality, series_description, series_number, instances, bytes, storage, manifest_sha256, receiver, updated`
	// HeldInstanceColumns are tenant.ScanInstance's columns, as the
	// pipeline's ScanInstances reads them.
	HeldInstanceColumns = `sop_instance_uid, reception_id, storage, storage_key, byte_offset, byte_length, size, sha256,
		archived_sha256, held_at`
)

// CatalogKey is the key of one catalog row, read with it, from which the
// cursor after it is made.
type CatalogKey struct {
	// ID is the row's id in its table.
	ID int64
	// Created is when the row was first archived.
	Created time.Time
	// SubjectFolded, SessionFolded and Folded are the folded labels of its
	// subject, its session and its own, down to its level.
	SubjectFolded, SessionFolded, Folded string
	// StudyDate is a session's study date; DescriptionFolded a scan's
	// folded series description.
	StudyDate, DescriptionFolded string
}

// CatalogSQL writes the catalog's reads in one driver's terms.
type CatalogSQL struct {
	// Param returns the placeholder of the nth parameter, from 1: "?" or
	// "$n".
	Param func(n int) string
	// Time returns a time as the driver's columns hold it.
	Time func(time.Time) any
}

// CatalogRead is one statement and its arguments.
type CatalogRead struct {
	SQL  string
	Args []any
}

// builder gathers a statement's conditions and their arguments.
type builder struct {
	c    CatalogSQL
	args []any
	cond []string
}

// arg adds a value and returns its placeholder.
func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return b.c.Param(len(b.args))
}

// where adds a condition; each %s in it is replaced by the next value's
// placeholder, in order.
func (b *builder) where(cond string, values ...any) {
	for _, v := range values {
		cond = strings.Replace(cond, "%s", b.arg(v), 1)
	}
	b.cond = append(b.cond, cond)
}

// read returns the statement over table with the conditions, order and
// limit. The limit is written into the text, not passed as a parameter:
// a plan made for any value of the parameters (the generic plan Postgres
// settles on for a prepared statement) then still knows a page is a few
// rows, and reads them from the index in its order rather than sorting
// what a filter keeps. It is an int a page has validated, so the text
// holds digits only.
func (b *builder) read(columns, table, order string, limit int) CatalogRead {
	q := "SELECT " + columns + " FROM " + table
	if len(b.cond) > 0 {
		q += " WHERE " + strings.Join(b.cond, " AND ")
	}
	q += " ORDER BY " + order + " LIMIT " + strconv.Itoa(limit)
	return CatalogRead{SQL: q, Args: b.args} // #nosec G202 -- every fragment is a constant of this file; values are parameters.
}

// newest adds a newest-first list's time range and cursor on the columns
// given. After a cursor the range's end is left out: the cursor's row was
// before it, so the cursor's condition is the tighter upper bound, and a
// planner given both estimates fewer rows than there are and may choose
// to sort them.
func (b *builder) newest(created, id string, r tenant.TimeRange, after *CatalogKey) {
	if !r.Start.IsZero() {
		b.where(created+" >= %s", b.c.Time(r.Start))
	}
	if !r.End.IsZero() && after == nil {
		b.where(created+" < %s", b.c.Time(r.End))
	}
	if after != nil {
		b.where("("+created+", "+id+") < (%s, %s)", b.c.Time(after.Created), after.ID)
	}
}

// prefix adds the range of the texts of column that start with p, folded
// already; nothing for an empty prefix. After a cursor (resumed) the
// cursor's own condition is the lower bound, past the prefix's start since
// the rows before it were kept by the prefix too, so only the end is
// added: a planner given two lower bounds on one column reads its index
// from one of them, and could choose the prefix's start, walking every row
// before the cursor again.
func (b *builder) prefix(column, p string, resumed bool) {
	if p == "" {
		return
	}
	if !resumed {
		b.where(column+" >= %s", p)
	}
	if end, ok := tenant.PrefixEnd(p); ok {
		b.where(column+" < %s", end)
	}
}

// Subjects returns the page of ListSubjects after the row after, nil for
// the first page, read with limit rows.
func (c CatalogSQL) Subjects(f tenant.SubjectFilter, after *CatalogKey, limit int) CatalogRead {
	b := &builder{c: c}
	if f.Order == tenant.OrderLabel {
		b.prefix("folded", label.Fold(f.LabelPrefix), after != nil)
		if after != nil {
			b.where("folded > %s", after.Folded)
		}
		return b.read(SubjectColumns, "subjects", "folded", limit)
	}
	b.newest("created", "id", f.Range, after)
	return b.read(SubjectColumns, "subjects", "created DESC, id DESC", limit)
}

// Sessions returns the page of ListSessions; subjectID is the id of the
// subject the filter names, zero for none. The modality filter reads the
// session_modalities index only: it returns the page's session ids and
// creation times (SessionsOfModality), and the sessions are read by id.
func (c CatalogSQL) Sessions(f tenant.SessionFilter, subjectID int64, after *CatalogKey, limit int) CatalogRead {
	b := &builder{c: c}
	switch f.Order {
	case tenant.OrderLabel:
		if f.SubjectLabel != "" {
			b.where("subject_id = %s", subjectID)
			b.prefix("folded", label.Fold(f.LabelPrefix), after != nil)
			if after != nil {
				b.where("folded > %s", after.Folded)
			}
			return b.read(SessionColumns, "sessions", "folded", limit)
		}
		if after != nil {
			b.where("(subject_folded, folded) > (%s, %s)", after.SubjectFolded, after.Folded)
		}
		return b.read(SessionColumns, "sessions", "subject_folded, folded", limit)
	case tenant.OrderStudyDate:
		if f.SubjectLabel != "" {
			b.where("subject_id = %s", subjectID)
		}
		// A session with no study date holds '', before every date, so
		// a lower bound of "0" leaves it out when no start is given.
		start := f.StudyDateStart
		if start == "" {
			start = "0"
		}
		b.where("study_date >= %s", start)
		if f.StudyDateEnd != "" && after == nil {
			// After a cursor its own condition is the upper bound (newest
			// says why the end is then left out).
			b.where("study_date <= %s", f.StudyDateEnd)
		}
		if after != nil {
			b.where("(study_date, id) < (%s, %s)", after.StudyDate, after.ID)
		}
		return b.read(SessionColumns, "sessions", "study_date DESC, id DESC", limit)
	}
	if f.Modality != "" {
		b.where("modality = %s", f.Modality)
		b.newest("created", "session_id", f.Range, after)
		return b.read("session_id, created", "session_modalities", "created DESC, session_id DESC", limit)
	}
	switch {
	case f.SubjectLabel != "":
		b.where("subject_id = %s", subjectID)
	case f.Receiver != "":
		b.where("receiver = %s", f.Receiver)
	}
	b.newest("created", "id", f.Range, after)
	return b.read(SessionColumns, "sessions", "created DESC, id DESC", limit)
}

// SessionsOfModality reports whether Sessions reads session_modalities for
// the filter, whose rows are a session's id and creation time.
func SessionsOfModality(f tenant.SessionFilter) bool {
	return f.Order == tenant.OrderNewest && f.Modality != ""
}

// Scans returns the page of ListScans; sessionID is the id of the session
// the filter names, zero for none.
func (c CatalogSQL) Scans(f tenant.ScanFilter, sessionID int64, after *CatalogKey, limit int) CatalogRead {
	b := &builder{c: c}
	switch f.Order {
	case tenant.OrderLabel:
		switch {
		case f.SessionLabel != "":
			b.where("session_id = %s", sessionID)
			b.prefix("folded", label.Fold(f.LabelPrefix), after != nil)
			if after != nil {
				b.where("folded > %s", after.Folded)
			}
			return b.read(ScanColumns, "scans", "folded", limit)
		case f.SubjectLabel != "":
			b.where("subject_folded = %s", label.Fold(f.SubjectLabel))
			if after != nil {
				b.where("(session_folded, folded) > (%s, %s)", after.SessionFolded, after.Folded)
			}
			return b.read(ScanColumns, "scans", "session_folded, folded", limit)
		}
		if after != nil {
			b.where("(subject_folded, session_folded, folded) > (%s, %s, %s)", after.SubjectFolded, after.SessionFolded, after.Folded)
		}
		return b.read(ScanColumns, "scans", "subject_folded, session_folded, folded", limit)
	case tenant.OrderSeriesDescription:
		// A scan with no description holds '', before every other text,
		// and a cursor's description is never empty, so the cursor is the
		// lower bound once there is one (prefix says why only one).
		if p := tenant.FoldDescription(f.DescriptionPrefix); p != "" {
			b.prefix("description_folded", p, after != nil)
		} else if after == nil {
			b.where("description_folded > %s", "")
		}
		if after != nil {
			b.where("(description_folded, id) > (%s, %s)", after.DescriptionFolded, after.ID)
		}
		return b.read(ScanColumns, "scans", "description_folded, id", limit)
	}
	switch {
	case f.SessionLabel != "":
		b.where("session_id = %s", sessionID)
	case f.Modality != "":
		b.where("modality = %s", f.Modality)
	case f.Receiver != "":
		b.where("receiver = %s", f.Receiver)
	}
	b.newest("created", "id", f.Range, after)
	return b.read(ScanColumns, "scans", "created DESC, id DESC", limit)
}

// SubjectByLabel returns the read of one subject by its label: the
// subjects' unique index on folded, the label then compared exactly.
func (c CatalogSQL) SubjectByLabel(subjectLabel string) CatalogRead {
	b := &builder{c: c}
	b.where("folded = %s", label.Fold(subjectLabel))
	b.where("label = %s", subjectLabel)
	return CatalogRead{SQL: "SELECT " + SubjectColumns + " FROM subjects WHERE " + strings.Join(b.cond, " AND "), Args: b.args}
}

// SessionByLabels returns the read of one session by its labels: the
// sessions' unique index on its subject's folded label and its own, the
// labels then compared exactly.
func (c CatalogSQL) SessionByLabels(subjectLabel, sessionLabel string) CatalogRead {
	b := &builder{c: c}
	b.where("subject_folded = %s", label.Fold(subjectLabel))
	b.where("folded = %s", label.Fold(sessionLabel))
	b.where("subject_label = %s", subjectLabel)
	b.where("label = %s", sessionLabel)
	return CatalogRead{SQL: "SELECT " + SessionColumns + " FROM sessions WHERE " + strings.Join(b.cond, " AND "), Args: b.args}
}

// ScanByLabels returns the read of one scan by its labels: the scans'
// unique index on the folded labels of its subject, its session and its
// own, the labels then compared exactly.
func (c CatalogSQL) ScanByLabels(p label.Path) CatalogRead {
	b := &builder{c: c}
	b.where("subject_folded = %s", label.Fold(p.Subject))
	b.where("session_folded = %s", label.Fold(p.Session))
	b.where("folded = %s", label.Fold(p.Scan))
	b.where("subject_label = %s", p.Subject)
	b.where("session_label = %s", p.Session)
	b.where("label = %s", p.Scan)
	return CatalogRead{SQL: "SELECT " + ScanColumns + " FROM scans WHERE " + strings.Join(b.cond, " AND "), Args: b.args}
}

// HeldInstances returns the page of a scan's held instances after the UID
// after: instances_by_scan, in order of SOP Instance UID.
func (c CatalogSQL) HeldInstances(scanID int64, after string, limit int) CatalogRead {
	b := &builder{c: c}
	b.where("scan_id = %s", scanID)
	b.where("state = 'held'")
	b.where("sop_instance_uid > %s", after)
	return b.read(HeldInstanceColumns, "instances", "sop_instance_uid", limit)
}

// HeldInstance returns the read of the instance held under uid in the scan
// of id scanID: the instances' primary key.
func (c CatalogSQL) HeldInstance(scanID int64, uid string) CatalogRead {
	b := &builder{c: c}
	b.where("sop_instance_uid = %s", uid)
	b.where("state = 'held'")
	b.where("scan_id = %s", scanID)
	return CatalogRead{SQL: "SELECT " + HeldInstanceColumns + " FROM instances WHERE " + strings.Join(b.cond, " AND "), Args: b.args}
}

// SubjectFilterParts returns what a subject list's cursor is bound to: its
// order and every field of its filter.
func SubjectFilterParts(f tenant.SubjectFilter) []string {
	return []string{ListCatalogSubjects, f.Order.String(), f.LabelPrefix, MicrosText(f.Range.Start), MicrosText(f.Range.End)}
}

// SessionFilterParts returns what a session list's cursor is bound to.
func SessionFilterParts(f tenant.SessionFilter) []string {
	return []string{ListCatalogSessions, f.Order.String(), f.SubjectLabel, f.LabelPrefix, f.StudyDateStart, f.StudyDateEnd,
		f.Modality, f.Receiver, MicrosText(f.Range.Start), MicrosText(f.Range.End)}
}

// ScanFilterParts returns what a scan list's cursor is bound to.
func ScanFilterParts(f tenant.ScanFilter) []string {
	return []string{ListCatalogScans, f.Order.String(), f.SubjectLabel, f.SessionLabel, f.LabelPrefix, f.Modality,
		f.DescriptionPrefix, f.Receiver, MicrosText(f.Range.Start), MicrosText(f.Range.End)}
}

// keyFields returns the fields of a cursor after k in a list of level (1
// subjects, 2 sessions, 3 scans) and order: the order's key, then the id
// where the key is not unique.
func keyFields(order tenant.CatalogOrder, level int, k CatalogKey) []string {
	id := strconv.FormatInt(k.ID, 10)
	switch order {
	case tenant.OrderLabel:
		switch level {
		case 1:
			return []string{k.Folded}
		case 2:
			return []string{k.SubjectFolded, k.Folded}
		}
		return []string{k.SubjectFolded, k.SessionFolded, k.Folded}
	case tenant.OrderStudyDate:
		return []string{k.StudyDate, id}
	case tenant.OrderSeriesDescription:
		return []string{k.DescriptionFolded, id}
	}
	return []string{strconv.FormatInt(k.Created.UnixMicro(), 10), id}
}

// CatalogCursor returns the cursor after the row of key k of a catalog
// list of level (1 subjects, 2 sessions, 3 scans), bound to binding.
func CatalogCursor(list, binding string, order tenant.CatalogOrder, level int, k CatalogKey) string {
	return EncodeCursor(list, binding, keyFields(order, level, k)...)
}

// CatalogAfter checks page and decodes its cursor, made by CatalogCursor
// for the same list, binding, order and level, or returns nil for the
// first page.
func CatalogAfter(page master.Page, list, binding string, order tenant.CatalogOrder, level int) (*CatalogKey, error) {
	if err := page.Validate(); err != nil {
		return nil, err
	}
	if page.Cursor == "" {
		return nil, nil
	}
	n := len(keyFields(order, level, CatalogKey{}))
	f, err := DecodeCursor(page.Cursor, list, binding, n)
	if err != nil {
		return nil, err
	}
	var k CatalogKey
	switch order {
	case tenant.OrderLabel:
		switch level {
		case 1:
			k.Folded = f[0]
		case 2:
			k.SubjectFolded, k.Folded = f[0], f[1]
		default:
			k.SubjectFolded, k.SessionFolded, k.Folded = f[0], f[1], f[2]
		}
		return &k, nil
	case tenant.OrderStudyDate:
		k.StudyDate = f[0]
	case tenant.OrderSeriesDescription:
		k.DescriptionFolded = f[0]
	default:
		at, err := DecodeCursorInt(f[0])
		if err != nil {
			return nil, err
		}
		k.Created = time.UnixMicro(at).UTC()
	}
	if k.ID, err = DecodeCursorInt(f[1]); err != nil {
		return nil, err
	}
	return &k, nil
}
