package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/magnetar/internal/driver/sqlkit"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

const keyColumns = `id, hash, holder, created, expires, last_used, revoked`

// scanKey reads one row of keyColumns.
func scanKey(row scanner) (master.Key, error) {
	var k master.Key
	var created time.Time
	var expires, lastUsed, revoked *time.Time
	err := row.Scan(&k.ID, &k.Hash, &k.Holder, &created, &expires, &lastUsed, &revoked)
	k.Created = created.UTC()
	k.Expires = fromNullMicros(expires)
	k.LastUsed = fromNullMicros(lastUsed)
	k.Revoked = fromNullMicros(revoked)
	return k, err
}

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
	return m.write(ctx, "the key cannot be created", func(tx pgx.Tx) error {
		if err := checkActor(ctx, tx, by, tenantLock{}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO api_keys (id, hash, holder, created, expires) VALUES ($1, $2, $3, $4, $5)`,
			k.ID, k.Hash, k.Holder, micros(k.Created), nullMicros(k.Expires))
		if isUniqueViolation(err) {
			return fault.Wrap(fault.Conflict, m.red.scrub(err), "a key with this id exists")
		}
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, m.red, audit)
	})
}

// Key implements master.Store: one read of the primary key's index.
func (m *Master) Key(ctx context.Context, id string) (master.Key, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return master.Key{}, err
	}
	if err := m.open(); err != nil {
		return master.Key{}, err
	}
	k, err := scanKey(m.pool.QueryRow(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE id = $1`, id))
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
	if err := m.open(); err != nil {
		return nil, "", err
	}
	rows, err := m.pool.Query(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE (created, id) > ($1, $2) ORDER BY created, id LIMIT $3`,
		microsTime(afterCreated), afterID, page.Limit+1)
	out, err := collect(rows, err, scanKey)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the keys cannot be listed")
	}
	out, last, more := sqlkit.Cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listKeys, "", strconv.FormatInt(last.Created.UnixMicro(), 10), last.ID), nil
}

// MarkKeyUsed implements master.Store with one UPDATE whose WHERE holds the
// throttle, so a call within the minute matches no row, takes no row lock and
// writes nothing: no read first, and no commit to wait for. When two calls
// race on a row that does match, the second waits for the first's commit and
// then re-evaluates the WHERE against the row the first wrote (read
// committed), so it matches nothing: a busy key costs one write a minute.
func (m *Master) MarkKeyUsed(ctx context.Context, id string, at time.Time) (bool, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return false, err
	}
	if err := master.ValidateTime("time of use", at); err != nil {
		return false, err
	}
	if err := m.open(); err != nil {
		return false, err
	}
	tag, err := m.pool.Exec(ctx, `UPDATE api_keys SET last_used = $1 WHERE id = $2 AND (last_used IS NULL OR last_used < $3)`,
		micros(at), id, micros(at.Add(-time.Minute)))
	if err != nil {
		return false, m.fail(ctx, err, "the key's use cannot be recorded")
	}
	return tag.RowsAffected() == 1, nil
}

// RevokeKey implements master.Store. The key's row is locked as its revoked
// time is read (the lock SetGrant takes too), so the audit entry says whether
// this call revoked the key or found it revoked, and a SetGrant racing this
// call is ordered with it: it either finishes before the revocation or finds
// the key revoked.
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
	err := m.write(ctx, "the key cannot be revoked", func(tx pgx.Tx) error {
		if err := lockKeys(ctx, tx, by, id); err != nil {
			return err
		}
		if err := checkActor(ctx, tx, by, tenantLock{}); err != nil {
			return err
		}
		var before *time.Time
		if err := tx.QueryRow(ctx, `SELECT revoked FROM api_keys WHERE id = $1 FOR NO KEY UPDATE`, id).Scan(&before); err != nil {
			return notFoundIn(err, "no such key")
		}
		if before != nil {
			revoked = before.UTC()
			return insertAudit(ctx, tx, m.red, audit.WithRevocation(true))
		}
		if _, err := tx.Exec(ctx, `UPDATE api_keys SET revoked = $1 WHERE id = $2`, micros(at), id); err != nil {
			return err
		}
		revoked = micros(at)
		return insertAudit(ctx, tx, m.red, audit.WithRevocation(false))
	})
	if err != nil {
		return time.Time{}, err
	}
	return revoked, nil
}

// The row locks a change takes on a tenant's row, from the weakest. Each
// change takes the one it needs the first time its transaction touches the
// row, so no transaction holds a weaker lock on a row and then asks for a
// stronger one, which two transactions doing at once would deadlock on.
const (
	// lockKeyShare keeps the row and its key: what a grant on the tenant,
	// and the check of a role on it, need (RemoveTenant waits for it).
	lockKeyShare = "FOR KEY SHARE"
	// lockNoKeyUpdate is what a change of the row's state needs; it does not
	// wait for key share, so it does not wait for grants being set.
	lockNoKeyUpdate = "FOR NO KEY UPDATE"
	// lockUpdate is what removing the row needs.
	lockUpdate = "FOR UPDATE"
)

// lockKeys locks the acting key's row and the row of the key a change is
// about, FOR NO KEY UPDATE, in the order of their ids, before checkActor and
// the change take their own locks on them. Without it, a change by key A on
// key B (locking A for share, then B) and one by B on A (B, then A) could each
// hold what the other waits for; and two changes by A on A would each hold A
// for share and wait for the other to let go of it. Locked in one order, in
// one statement, the rows are taken one after the other by every such
// change. An actor that is not a key locks nothing here.
func lockKeys(ctx context.Context, tx pgx.Tx, by master.Actor, target string) error {
	if !by.IsKey() {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT id FROM api_keys WHERE id = ANY($1) ORDER BY id FOR NO KEY UPDATE`, []string{by.KeyID(), target})
	return err
}
