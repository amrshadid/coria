package sqlite

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
)

// micros returns t as microseconds since 1970 in UTC, the form every time is
// stored in. What is finer than a microsecond is dropped (master package
// documentation); master.ValidateTime keeps t after 1970, where dropping is
// the same as rounding down.
func micros(t time.Time) int64 {
	return t.UnixMicro()
}

// nullMicros is micros, with NULL for the zero time ("never").
func nullMicros(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMicro()
}

// fromMicros is the inverse of micros, in UTC.
func fromMicros(v int64) time.Time {
	return time.UnixMicro(v).UTC()
}

// fromNullMicros is fromMicros, with the zero time for NULL.
func fromNullMicros(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return fromMicros(v.Int64)
}

// nullText returns NULL for the empty string, for the grants' tenant_id.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// encodeDetails writes an audit entry's details as a JSON object. JSON is
// stored and returned whole and never searched inside, so it is the same in
// every SQL database, with no dialect's JSON functions to keep correct.
// encoding/json sorts the keys, so an entry's text does not depend on the
// map's order.
func encodeDetails(d map[string]string) (string, error) {
	if len(d) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", fault.Wrap(fault.Internal, err, "the audit entry's details cannot be written")
	}
	return string(b), nil
}

// decodeDetails reads what encodeDetails wrote; no details is nil.
func decodeDetails(s string) (map[string]string, error) {
	var d map[string]string
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return nil, fault.Wrap(fault.Internal, err, "an audit entry's details cannot be read")
	}
	if len(d) == 0 {
		return nil, nil
	}
	return d, nil
}

// oneRow returns NotFound with what unless the statement changed a row.
func oneRow(res sql.Result, err error, what string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fault.New(fault.NotFound, what)
	}
	return nil
}

// collect reads every row of a query with scan, closing the rows.
func collect[T any](rows *sql.Rows, err error, scan func(interface{ Scan(...any) error }) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // Errors are reported by rows.Err.
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
