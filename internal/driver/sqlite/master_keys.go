package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/amrshadid/coria/internal/driver/sqlkit"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
)

// CreateKey implements master.Store.
func (m *Master) CreateKey(ctx context.Context, k master.Key, audit master.AuditEntry, by master.Actor) error {
	if err := k.Validate(); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	if err := by.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the key cannot be created", func(tx *sql.Tx, s *statements) error {
		if err := checkActor(ctx, tx, s, by); err != nil {
			return err
		}
		_, err := tx.StmtContext(ctx, s.insertKey).ExecContext(ctx,
			k.ID, k.Hash, k.Holder, micros(k.Created), nullMicros(k.Expires))
		if isUniqueViolation(err) {
			return fault.Wrap(fault.Conflict, err, "a key with this id exists")
		}
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, s, audit)
	})
}

const keyColumns = `id, hash, holder, created, expires, last_used, revoked`

// scanKey reads one row of keyColumns.
func scanKey(row interface{ Scan(...any) error }) (master.Key, error) {
	var k master.Key
	var created int64
	var expires, lastUsed, revoked sql.NullInt64
	err := row.Scan(&k.ID, &k.Hash, &k.Holder, &created, &expires, &lastUsed, &revoked)
	k.Created = fromMicros(created)
	k.Expires = fromNullMicros(expires)
	k.LastUsed = fromNullMicros(lastUsed)
	k.Revoked = fromNullMicros(revoked)
	return k, err
}

// Key implements master.Store: one read of the primary key's index.
func (m *Master) Key(ctx context.Context, id string) (master.Key, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return master.Key{}, err
	}
	s, err := m.ready(ctx)
	if err != nil {
		return master.Key{}, err
	}
	k, err := scanKey(s.keyByID.QueryRowContext(ctx, id))
	return k, m.notFound(ctx, err, "no such key", "the key cannot be read")
}

// ListKeys implements master.Store.
func (m *Master) ListKeys(ctx context.Context, page master.Page) ([]master.Key, string, error) {
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	// Creation times are never before 1970, so (-1, "") is before every key.
	afterCreated, afterID := int64(-1), ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listKeys, "", 2)
		if err != nil {
			return nil, "", err
		}
		if afterCreated, err = sqlkit.DecodeCursorInt(f[0]); err != nil {
			return nil, "", err
		}
		afterID = f[1]
	}
	s, err := m.ready(ctx)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.listKeys.QueryContext(ctx, afterCreated, afterID, page.Limit+1)
	out, err := collect(rows, err, scanKey)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the keys cannot be listed")
	}
	out, last, more := sqlkit.Cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listKeys, "", strconv.FormatInt(micros(last.Created), 10), last.ID), nil
}

// MarkKeyUsed implements master.Store with one UPDATE whose WHERE holds the
// throttle, so a call within the minute changes no row and writes nothing
// to the file: no read first, and no commit to sync. The same condition
// keeps the stored time from moving backward: an older at matches no row.
func (m *Master) MarkKeyUsed(ctx context.Context, id string, at time.Time) (bool, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return false, err
	}
	if err := master.ValidateTime("time of use", at); err != nil {
		return false, err
	}
	s, err := m.ready(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.markKeyUsed.ExecContext(ctx, micros(at), id, micros(at.Add(-time.Minute)))
	if err != nil {
		return false, m.fail(ctx, err, "the key's use cannot be recorded")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, m.fail(ctx, err, "the key's use cannot be recorded")
	}
	return n == 1, nil
}

// RevokeKey implements master.Store. The revoked time is read and, when it is
// unset, written in one write transaction, so the audit entry says whether
// this call revoked the key or found it revoked.
func (m *Master) RevokeKey(ctx context.Context, id string, at time.Time, audit master.AuditEntry, by master.Actor) (time.Time, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return time.Time{}, err
	}
	if err := master.ValidateTime("revocation time", at); err != nil {
		return time.Time{}, err
	}
	if err := audit.ValidateWith(master.DetailAlreadyRevoked); err != nil {
		return time.Time{}, err
	}
	if err := by.Validate(); err != nil {
		return time.Time{}, err
	}
	var revoked time.Time
	err := m.write(ctx, "the key cannot be revoked", func(tx *sql.Tx, s *statements) error {
		if err := checkActor(ctx, tx, s, by); err != nil {
			return err
		}
		var before sql.NullInt64
		err := tx.StmtContext(ctx, s.keyRevoked).QueryRowContext(ctx, id).Scan(&before)
		if errors.Is(err, sql.ErrNoRows) {
			return fault.New(fault.NotFound, "no such key")
		}
		if err != nil {
			return err
		}
		if before.Valid {
			revoked = fromMicros(before.Int64)
			return insertAudit(ctx, tx, s, audit.WithRevocation(true))
		}
		if _, err := tx.StmtContext(ctx, s.revokeKey).ExecContext(ctx, micros(at), id); err != nil {
			return err
		}
		revoked = fromMicros(micros(at))
		return insertAudit(ctx, tx, s, audit.WithRevocation(false))
	})
	if err != nil {
		return time.Time{}, err
	}
	return revoked, nil
}
