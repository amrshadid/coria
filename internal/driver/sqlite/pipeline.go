package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/sqlkit"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// The pipeline's operations on a tenant's database (tenant.Pipeline), in
// pipeline_*.go, on the tables of migrations/tenant/0002_pipeline.sql.
//
// Every change goes through Tenant.write, so it is grouped with the changes
// waiting at the same moment like every other (group.go), and like every
// other it only reads, writes and assigns its results to variables it sets
// afresh on each run: a change may run again in a new transaction.
//
// The statements run per file or per claim, where their parsing would show,
// are prepared once (pipelineStatements); the others, run when a person
// changes a configuration or a step is taken, are written in their
// functions and parsed when they run.

var _ tenant.Pipeline = (*Tenant)(nil)

// The names of the pipeline's lists in their cursors, apart from each other
// and from the settings and audit lists.
const (
	listStorages      = "tenant-storages"
	listReceivers     = "tenant-receivers"
	listReceptions    = "tenant-receptions"
	listReceivedFiles = "tenant-received-files"
	listQuarantine    = "tenant-quarantine"
)

// pipelineStatements are the pipeline's prepared statements.
type pipelineStatements struct {
	// Write pool.
	insertReceived, insertAssociation, receptionForBatch, addReceived *sql.Stmt
	claimNext, leaseUnit                                              *sql.Stmt
	// Read pool.
	duplicates *sql.Stmt
}

// pipelineEntries returns the pipeline's statements to prepare into s.
func pipelineEntries(s *pipelineStatements, p *pools) []stmtEntry {
	r, w := p.read, p.write
	return []stmtEntry{
		{&s.insertReceived, w, `INSERT INTO received_files (` + receivedColumns + `)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`},
		{&s.insertAssociation, w, `INSERT INTO reception_associations (reception_id, association) VALUES (?, ?) ON CONFLICT DO NOTHING`},
		{&s.receptionForBatch, w, `SELECT state, study_uid, calling_ae_titles, calling_truncated FROM receptions WHERE id = ?`},
		{&s.addReceived, w, `UPDATE receptions SET files_received = files_received + ?, bytes_received = bytes_received + ?,
			files_released = files_released + ?, associations = associations + ?, calling_ae_titles = ?, calling_truncated = ?,
			last_file = max(last_file, ?) WHERE id = ?`},
		{&s.claimNext, w, claimNextSQL},
		{&s.leaseUnit, w, `UPDATE work SET state = 'leased', attempt = attempt + 1, owner = ?, due = ?, updated = ?
			WHERE id = ? AND attempt = ? AND state IN ('queued', 'leased')`},
		{&s.duplicates, r, duplicatesSQL},
	}
}

// write runs fn in one write transaction (base.writeTx) with the tenant's
// prepared statements. fn may run more than once and must only assign its
// results.
func (t *Tenant) write(ctx context.Context, message string, fn func(tx *sql.Tx, s *tenantStatements) error) error {
	s, err := t.ready(ctx)
	if err != nil {
		return err
	}
	return t.writeTx(ctx, message, func(tx *sql.Tx) error { return fn(tx, s) })
}

// audited ends a change a person or a key asked for: its audit entry is
// written in its transaction, then its Guard runs, last, as Store says.
func audited(ctx context.Context, tx *sql.Tx, s *tenantStatements, audit master.AuditEntry, guard tenant.Guard) error {
	if err := execAudit(ctx, tx.StmtContext(ctx, s.insertAudit), audit); err != nil {
		return err
	}
	return runLast(ctx, tx, guarded(guard))
}

// exists reports whether query, run in tx with args, returns a row.
func exists(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// readRows runs query on the read pool and scans every row.
func readRows[T any](ctx context.Context, t *Tenant, message, query string, args []any, scan func(interface{ Scan(...any) error }) (T, error)) ([]T, error) {
	if _, err := t.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := t.p.read.QueryContext(ctx, query, args...)
	out, err := collect(rows, err, scan)
	if err != nil {
		return nil, t.fail(ctx, err, message)
	}
	return out, nil
}

// readOne runs query on the read pool and scans its one row, or returns
// NotFound with what.
func readOne[T any](ctx context.Context, t *Tenant, what, message, query string, args []any, scan func(interface{ Scan(...any) error }) (T, error)) (T, error) {
	var zero T
	if _, err := t.ready(ctx); err != nil {
		return zero, err
	}
	v, err := scan(t.p.read.QueryRowContext(ctx, query, args...))
	if err != nil {
		return zero, t.notFound(ctx, err, what, message)
	}
	return v, nil
}

// binding is what a pipeline list's cursor is bound to: the tenant and the
// list's arguments.
func (t *Tenant) binding(parts ...string) string {
	return sqlkit.Binding(append([]string{t.lists.owner}, parts...)...)
}

// pageAfterName decodes a list's cursor that holds one name, or returns ""
// for the first page.
func (t *Tenant) pageAfterName(page master.Page, list string) (string, error) {
	if err := page.Validate(); err != nil {
		return "", err
	}
	if page.Cursor == "" {
		return "", nil
	}
	f, err := sqlkit.DecodeCursor(page.Cursor, list, t.binding(), 1)
	if err != nil {
		return "", err
	}
	return f[0], nil
}

// pageAfterTime decodes a newest-first list's cursor, which holds the (time,
// id) of the last row returned, or returns nil for the first page.
func pageAfterTime(page master.Page, list, binding string) ([]any, error) {
	if err := page.Validate(); err != nil {
		return nil, err
	}
	if page.Cursor == "" {
		return nil, nil
	}
	f, err := sqlkit.DecodeCursor(page.Cursor, list, binding, 2)
	if err != nil {
		return nil, err
	}
	at, err := sqlkit.DecodeCursorInt(f[0])
	if err != nil {
		return nil, err
	}
	return []any{at, f[1]}, nil
}

// timeCursor returns the cursor after a row of a newest-first list.
func timeCursor(list, binding string, at int64, id string) string {
	return sqlkit.EncodeCursor(list, binding, strconv.FormatInt(at, 10), id)
}

// microsText is a time's microseconds as text for a binding, or "" for the
// zero time.
func microsText(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

// nullBlob returns NULL for a zero checksum, its bytes otherwise.
func nullBlob(s tenant.SHA256) any {
	if s.IsZero() {
		return nil
	}
	return s[:]
}

// toSHA256 reads a checksum column; NULL or a wrong length is none.
func toSHA256(b []byte) tenant.SHA256 {
	var s tenant.SHA256
	if len(b) == len(s) {
		copy(s[:], b)
	}
	return s
}

// boolInt stores a bool as SQLite's 0 or 1.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// jsonText writes v as JSON for a column that is stored and returned whole.
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fault.Wrap(fault.Internal, err, "a value cannot be written as JSON")
	}
	return string(b), nil
}

// fromJSON reads a column jsonText wrote.
func fromJSON(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fault.Wrap(fault.Internal, err, "a stored value cannot be read")
	}
	return nil
}

// addDetails returns the audit entry with the details a store adds, checked
// with the rest of the entry: a caller's entry already at its bound of
// details is refused before anything is written.
func addDetails(audit master.AuditEntry, pairs ...string) (master.AuditEntry, error) {
	a := audit.WithDetails(pairs...)
	return a, a.Validate()
}
