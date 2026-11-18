package sqlite

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/sqlkit"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// The tenant's event feed (tenant.Events), on the events and event_marks
// tables of migrations/tenant/0002_pipeline.sql.
//
// Commit order. Every change runs on the one write connection, one
// transaction at a time (group.go), and an event's id is taken by
// AUTOINCREMENT inside that transaction, so ids are in the order of commits
// and only grow. A reader on the read pool sees a snapshot that holds every
// commit up to one point and none after it (sqlite.org/wal.html,
// sqlite.org/isolation.html), so the events it sees are every committed
// event up to some id: a read of the ids after a cursor never passes an
// event that commits later. A grouped transaction that is undone and run
// again takes its ids with it (the counter AUTOINCREMENT keeps is a row of
// sqlite_sequence, written in the same transaction), and no reader saw
// them.

var _ tenant.Events = (*Tenant)(nil)

// listEvents names the feed in its cursors, apart from every other list.
const listEvents = "tenant-events"

// The feed's statements, named so the plan test reads the very text the
// driver runs.
const (
	// insertEventSQL writes one event; ON CONFLICT DO NOTHING skips an
	// appended event whose key the feed holds (events_by_key).
	insertEventSQL = `INSERT INTO events (type, at, payload, key) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`
	// eventsAfterSQL reads the events after an id: the table's own key.
	eventsAfterSQL = `SELECT id, type, at, payload FROM events WHERE id > ? ORDER BY id LIMIT ?`
	// eventsOfTypeSQL reads the events of one type after an id:
	// events_by_type.
	eventsOfTypeSQL = `SELECT id, type, at, payload FROM events WHERE type = ? AND id > ? ORDER BY id LIMIT ?`
	// eventHighSQL reads the feed's place: the newest id committed, or the
	// mark of what was pruned when every event is gone. max(id) is the end
	// of the table's key.
	eventHighSQL = `SELECT max(coalesce((SELECT max(id) FROM events), 0), pruned_through), pruned_through FROM event_marks WHERE one = 1`
	// pruneCandidatesSQL reads the oldest events, in order: the table's
	// own key.
	pruneCandidatesSQL = `SELECT id, at FROM events ORDER BY id LIMIT ?`
	// pruneSQL removes every event up to an id. NOT INDEXED keeps the
	// planner on the table's own key, a range from its start: with the
	// statistics of a full feed it would otherwise walk events_by_type once
	// per type.
	pruneSQL = `DELETE FROM events NOT INDEXED WHERE id <= ?`
	// incomingBytesSQL sums the bytes of the receptions whose files are in
	// the incoming area; the condition is receptions_holding's, written the
	// same so that SQLite's planner reads that index alone.
	incomingBytesSQL = `SELECT coalesce(sum(bytes_received - bytes_quarantined), 0) FROM receptions
		WHERE ended = 0 OR (state = 'failed' AND retry_reception = '' AND discard_reason = '')`
	// stepCountsSQL reads the running totals of receptions per step.
	stepCountsSQL = `SELECT state, receptions FROM reception_steps`
)

// writeEvent writes e in tx, with key for an appended event or nil for one
// the store writes with its change, and reports whether it was new.
func writeEvent(ctx context.Context, tx *sql.Tx, e tenant.EventRecord, key any) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, insertEventSQL, string(e.Type()), micros(e.Time()), e.Payload(), key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// txEvents is the event writer of one change's transaction, handed to a
// step's hook (tenant.WithEventWriter).
type txEvents struct{ tx *sql.Tx }

// WriteEvent implements tenant.EventWriter.
func (w txEvents) WriteEvent(ctx context.Context, e tenant.EventRecord) error {
	_, err := writeEvent(ctx, w.tx, e, nil)
	return err
}

// writeStepEvent writes the event of a reception as it is in tx after a
// step the store took itself (an open, a retry, a unit giving up).
func writeStepEvent(ctx context.Context, tx *sql.Tx, id string) error {
	r, err := receptionInTx(ctx, tx, id)
	if err != nil {
		return err
	}
	e, err := tenant.ReceptionStepEvent(r)
	if err != nil {
		return err
	}
	_, err = writeEvent(ctx, tx, e, nil)
	return err
}

// quarantineTotalsInTx reads the quarantine's totals inside a change.
func quarantineTotalsInTx(ctx context.Context, tx *sql.Tx) (int64, int64, error) {
	var files, bytes int64
	err := tx.QueryRowContext(ctx, `SELECT files, bytes FROM quarantine_totals WHERE one = 1`).Scan(&files, &bytes)
	return files, bytes, err
}

// writeDecisionEvent writes the event of a record just decided, with the
// quarantine's totals after it.
func writeDecisionEvent(ctx context.Context, tx *sql.Tx, q tenant.QuarantinedFile) error {
	files, bytes, err := quarantineTotalsInTx(ctx, tx)
	if err != nil {
		return err
	}
	e, err := tenant.QuarantineDecidedEvent(q, files, bytes)
	if err != nil {
		return err
	}
	_, err = writeEvent(ctx, tx, e, nil)
	return err
}

// eventCursor returns the cursor after the event with this id.
func (t *Tenant) eventCursor(id int64) string {
	return sqlkit.EncodeCursor(listEvents, t.binding(), strconv.FormatInt(id, 10))
}

// eventPosition decodes a feed cursor into the id it is after, or -1 for
// the empty cursor, which reads from the oldest event kept.
func (t *Tenant) eventPosition(cursor string) (int64, error) {
	if cursor == "" {
		return -1, nil
	}
	f, err := sqlkit.DecodeCursor(cursor, listEvents, t.binding(), 1)
	if err != nil {
		return 0, err
	}
	id, err := sqlkit.DecodeCursorInt(f[0])
	if err != nil || id < 0 {
		return 0, tenantBadCursor()
	}
	return id, nil
}

// tenantBadCursor is the refusal of a cursor that decodes but cannot be one
// the feed made, in the words of every other refused cursor.
func tenantBadCursor() error {
	return fault.New(fault.Invalid, "the page token is not one this list returned with these arguments")
}

// AppendEvents implements tenant.Events.
func (t *Tenant) AppendEvents(ctx context.Context, events []tenant.EventAppend) (int, error) {
	if err := tenant.ValidateEventAppends(events); err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, t.open()
	}
	var added int
	err := t.write(ctx, "the events cannot be written", func(tx *sql.Tx, _ *tenantStatements) error {
		n := 0
		for _, e := range events {
			fresh, err := writeEvent(ctx, tx, e.Event, e.Key)
			if err != nil {
				return err
			}
			if fresh {
				n++
			}
		}
		added = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}

// eventsQuery returns the statement and arguments of a page of events after
// id, of the types given. Several types read one page per type, each from
// events_by_type and bounded by the limit, merged and cut to the limit: a
// single "type IN (...)" would walk every id and test each event, which
// costs as much as the events of the other types are many.
func eventsQuery(types []tenant.EventType, after int64, limit int) (string, []any) {
	switch len(types) {
	case 0:
		return eventsAfterSQL, []any{after, limit}
	case 1:
		return eventsOfTypeSQL, []any{string(types[0]), after, limit}
	}
	parts := make([]string, len(types))
	args := make([]any, 0, 3*len(types)+1)
	for i, typ := range types {
		parts[i] = "SELECT * FROM (" + eventsOfTypeSQL + ")"
		args = append(args, string(typ), after, limit)
	}
	return strings.Join(parts, " UNION ALL ") + " ORDER BY id LIMIT ?", append(args, limit)
}

// distinctTypes returns types without repeats, in their first order: a
// type named twice would be read twice.
func distinctTypes(types []tenant.EventType) []tenant.EventType {
	var out []tenant.EventType
	for _, typ := range types {
		seen := false
		for _, o := range out {
			seen = seen || o == typ
		}
		if !seen {
			out = append(out, typ)
		}
	}
	return out
}

// readTx runs fn in one read transaction on the read pool, so every query
// in it reads one snapshot of the database (sqlite.org/isolation.html), and
// ends the transaction before it returns: a reader never holds a snapshot
// between calls, which would keep the write-ahead log from being reset
// (sqlite.org/wal.html).
func (t *Tenant) readTx(ctx context.Context, message string, fn func(tx *sql.Tx) error) error {
	if _, err := t.ready(ctx); err != nil {
		return err
	}
	tx, err := t.p.read.BeginTx(ctx, nil)
	if err != nil {
		return t.fail(ctx, err, message)
	}
	defer func() { _ = tx.Rollback() }() // A read changes nothing; ending it is all that matters.
	if err := fn(tx); err != nil {
		return t.fail(ctx, err, message)
	}
	return nil
}

// ReadEvents implements tenant.Events.
func (t *Tenant) ReadEvents(ctx context.Context, after string, types []tenant.EventType, limit int) (tenant.EventPage, error) {
	if err := tenant.ValidateEventRead(types, limit); err != nil {
		return tenant.EventPage{}, err
	}
	pos, err := t.eventPosition(after)
	if err != nil {
		return tenant.EventPage{}, err
	}
	var page tenant.EventPage
	err = t.readTx(ctx, "the events cannot be read", func(tx *sql.Tx) error {
		var high, pruned int64
		if err := tx.QueryRowContext(ctx, eventHighSQL).Scan(&high, &pruned); err != nil {
			return err
		}
		from := pos
		switch {
		case pos < 0:
			from = pruned
		case pos < pruned:
			return tenant.CursorExpired()
		}
		query, args := eventsQuery(distinctTypes(types), from, limit)
		rows, err := tx.QueryContext(ctx, query, args...)
		var ids []int64
		events, err := collect(rows, err, func(row interface{ Scan(...any) error }) (tenant.Event, error) {
			var id, at int64
			var typ, payload string
			if err := row.Scan(&id, &typ, &at, &payload); err != nil {
				return tenant.Event{}, err
			}
			p, err := tenant.DecodeEvent(tenant.EventType(typ), payload)
			ids = append(ids, id)
			return tenant.Event{Cursor: t.eventCursor(id), Type: tenant.EventType(typ), Time: fromMicros(at), Payload: p}, err
		})
		if err != nil {
			return err
		}
		// A full page ends at its last event, and the events the filter
		// left out before it are behind that. A short page has every event
		// of the snapshot after the cursor that the filter keeps, so the
		// reader is at the snapshot's newest event, whatever the filter left
		// out after its last one.
		next := max(from, high)
		if len(events) == limit {
			next = ids[len(ids)-1]
		}
		page = tenant.EventPage{Events: events, Cursor: t.eventCursor(next)}
		return nil
	})
	if err != nil {
		return tenant.EventPage{}, err
	}
	return page, nil
}

// EventCursor implements tenant.Events.
func (t *Tenant) EventCursor(ctx context.Context) (string, error) {
	var cursor string
	err := t.readTx(ctx, "the event feed's place cannot be read", func(tx *sql.Tx) error {
		var high, pruned int64
		if err := tx.QueryRowContext(ctx, eventHighSQL).Scan(&high, &pruned); err != nil {
			return err
		}
		cursor = t.eventCursor(high)
		return nil
	})
	return cursor, err
}

// StatusSnapshot implements tenant.Events. Every query runs in one read
// transaction, so the cursor and the counts are of one commit.
func (t *Tenant) StatusSnapshot(ctx context.Context) (tenant.StatusSnapshot, error) {
	var s tenant.StatusSnapshot
	err := t.readTx(ctx, "the tenant's status cannot be read", func(tx *sql.Tx) error {
		var high, pruned int64
		if err := tx.QueryRowContext(ctx, eventHighSQL).Scan(&high, &pruned); err != nil {
			return err
		}
		c := &s.Catalog
		if err := tx.QueryRowContext(ctx, `SELECT subjects, sessions, scans, instances FROM catalog_totals WHERE one = 1`).
			Scan(&c.Subjects, &c.Sessions, &c.Scans, &c.Instances); err != nil {
			return err
		}
		files, bytes, err := quarantineTotalsInTx(ctx, tx)
		if err != nil {
			return err
		}
		var incoming int64
		if err := tx.QueryRowContext(ctx, incomingBytesSQL).Scan(&incoming); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, stepCountsSQL)
		type stepRow struct {
			state string
			n     int64
		}
		steps, err := collect(rows, err, func(row interface{ Scan(...any) error }) (stepRow, error) {
			var r stepRow
			err := row.Scan(&r.state, &r.n)
			return r, err
		})
		if err != nil {
			return err
		}
		counts := make(map[reception.State]int64, len(steps))
		for _, r := range steps {
			counts[reception.State(r.state)] = r.n
		}
		s = tenant.StatusSnapshot{EventCursor: t.eventCursor(high), Catalog: *c, QuarantinedFiles: files,
			QuarantinedBytes: bytes, InProgress: tenant.InProgressSteps(counts), IncomingBytes: incoming + bytes}
		return nil
	})
	if err != nil {
		return tenant.StatusSnapshot{}, err
	}
	return s, nil
}

// PruneEvents implements tenant.Events. The oldest events are read in order
// of id and removed up to the first that is not older than before, so what
// is removed is always every event up to one id, which the mark then names:
// an event above the mark is never missing.
func (t *Tenant) PruneEvents(ctx context.Context, before time.Time, limit int) (int, bool, error) {
	if err := tenant.ValidatePrune(before, limit); err != nil {
		return 0, false, err
	}
	var removed int
	var more bool
	cut := micros(before)
	err := t.write(ctx, "the events cannot be pruned", func(tx *sql.Tx, _ *tenantStatements) error {
		removed, more = 0, false
		rows, err := tx.QueryContext(ctx, pruneCandidatesSQL, limit+1)
		type candidate struct{ id, at int64 }
		oldest, err := collect(rows, err, func(row interface{ Scan(...any) error }) (candidate, error) {
			var c candidate
			err := row.Scan(&c.id, &c.at)
			return c, err
		})
		if err != nil {
			return err
		}
		n := 0
		for n < len(oldest) && n < limit && oldest[n].at < cut {
			n++
		}
		if n == 0 {
			return nil
		}
		through := oldest[n-1].id
		if _, err := tx.ExecContext(ctx, pruneSQL, through); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE event_marks SET pruned_through = max(pruned_through, ?) WHERE one = 1`, through); err != nil {
			return err
		}
		removed, more = n, n == limit && len(oldest) > limit && oldest[limit].at < cut
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return removed, more, nil
}
