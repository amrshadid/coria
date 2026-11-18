package postgres

import (
	"context"
	"embed"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/sqlkit"
)

// masterFiles holds the master database's migrations, embedded so the binary
// upgrades its own database with no file beside it.
//
//go:embed migrations/master/*.sql
var masterFiles embed.FS

// masterMigrations are the master's migrations, read once. A broken embedded
// directory is a build mistake that every test of this package shows.
var masterMigrations, masterMigrationsErr = sqlkit.Load(masterFiles, "migrations/master")

// masterLists names the master's settings and audit lists in their cursors,
// as they always were; a tenant's have names of their own (tenantLists).
var masterLists = lists{settings: listSettings, audit: listAudit, missing: "no such setting"}

// Master is the master database on Postgres. It shares its pool handling,
// its failure classification and its write transactions with Tenant (base),
// and its settings and audit log with it (shared.go). It is safe for
// concurrent use.
type Master struct {
	base
}

var _ master.Store = (*Master)(nil)

// OpenMaster opens the master database at address, a postgres:// URL. It does
// not migrate: the caller calls Migrate, so that opening a database never
// changes it unasked. It does reach the server, so an address that cannot be
// used fails here, as Unavailable, with the address not repeated.
func OpenMaster(ctx context.Context, address string) (*Master, error) {
	if masterMigrationsErr != nil {
		return nil, fault.Wrap(fault.Internal, masterMigrationsErr, "the master's migrations cannot be read")
	}
	m := &Master{}
	if err := m.connect(ctx, address, "master database", "master", poolOptions{}); err != nil {
		return nil, err
	}
	return m, nil
}

// Migrate implements master.Store.
func (m *Master) Migrate(ctx context.Context) error {
	_, err := m.migrate(ctx, masterMigrations)
	return err
}

// Version implements master.Store.
func (m *Master) Version(ctx context.Context) (int, error) { return m.version(ctx) }

// Ping implements master.Store.
func (m *Master) Ping(ctx context.Context) error { return m.ping(ctx) }

// Close implements master.Store. It waits for the calls in progress to give
// their connections back, then closes every connection.
func (m *Master) Close() error { return m.shutdown() }

// Setting implements master.Store.
func (m *Master) Setting(ctx context.Context, name string) (string, error) {
	return getSetting(ctx, &m.base, masterLists, name)
}

// SetSetting implements master.Store.
func (m *Master) SetSetting(ctx context.Context, name, value string, audit master.AuditEntry, by master.Actor) error {
	if err := by.Validate(); err != nil {
		return err
	}
	return setSetting(ctx, &m.base, name, value, audit, actorCheck(by))
}

// UnsetSetting implements master.Store.
func (m *Master) UnsetSetting(ctx context.Context, name string, audit master.AuditEntry, by master.Actor) (bool, error) {
	if err := by.Validate(); err != nil {
		return false, err
	}
	return unsetSetting(ctx, &m.base, name, audit, actorCheck(by))
}

// ListSettings implements master.Store.
func (m *Master) ListSettings(ctx context.Context, page master.Page) ([]master.Setting, string, error) {
	return listSettingsPage(ctx, &m.base, masterLists, page)
}

// AppendAudit implements master.Store.
func (m *Master) AppendAudit(ctx context.Context, entries ...master.AuditEntry) error {
	return appendAudit(ctx, &m.base, entries)
}

// ListAudit implements master.Store. The cursor is bound to the whole filter.
func (m *Master) ListAudit(ctx context.Context, filter master.AuditFilter, page master.Page) ([]master.AuditEntry, string, error) {
	return listAuditPage(ctx, &m.base, masterLists, filter, page)
}

// The names of the lists, as their cursors carry them, so a cursor from one
// list is refused by every other.
const (
	listTenants       = "tenants"
	listKeys          = "keys"
	listGrantsByKey   = "grants-by-key"
	listGrantsByScope = "grants-by-scope"
	listSettings      = "settings"
	listAudit         = "audit"
)

// --- Tenants ---

const tenantColumns = `id, name, driver, address, state, state_reason, created`

// scanner is what pgx.Row and pgx.CollectableRow share.
type scanner interface{ Scan(...any) error }

// scanTenant reads one row of tenantColumns.
func scanTenant(row scanner) (master.Tenant, error) {
	var t master.Tenant
	var state string
	var created time.Time
	err := row.Scan(&t.ID, &t.Name, &t.Driver, &t.SealedAddress, &state, &t.StateReason, &created)
	t.State = master.TenantState(state)
	t.Created = created.UTC()
	return t, err
}

// CreateTenant implements master.Store.
func (m *Master) CreateTenant(ctx context.Context, t master.Tenant, audit master.AuditEntry, by master.Actor) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	if err := by.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the tenant cannot be created", func(tx pgx.Tx) error {
		if err := checkActor(ctx, tx, by); err != nil {
			return err
		}
		// No look-up first: the unique constraints decide, so two creations
		// racing in two processes cannot both succeed, and the constraint
		// that failed says whether the name or the id was taken.
		_, err := tx.Exec(ctx, `INSERT INTO tenants (`+tenantColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			t.ID, t.Name, t.Driver, t.SealedAddress, string(t.State), t.StateReason, micros(t.Created))
		switch {
		case constraintViolation(err, "tenants_name_key"):
			return fault.Wrap(fault.Conflict, m.red.scrub(err), "a tenant named "+t.Name+" exists")
		case isUniqueViolation(err):
			return fault.Wrap(fault.Conflict, m.red.scrub(err), "a tenant with this id exists")
		case err != nil:
			return err
		}
		return insertAudit(ctx, tx, m.red, audit)
	})
}

// Tenant implements master.Store.
func (m *Master) Tenant(ctx context.Context, id string) (master.Tenant, error) {
	if err := master.ValidateID("tenant id", id); err != nil {
		return master.Tenant{}, err
	}
	if err := m.open(); err != nil {
		return master.Tenant{}, err
	}
	t, err := scanTenant(m.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE id = $1`, id))
	return t, m.notFound(ctx, err, "no such tenant", "the tenant cannot be read")
}

// TenantByName implements master.Store: one read of the name's unique index.
func (m *Master) TenantByName(ctx context.Context, name string) (master.Tenant, error) {
	if err := master.ValidateTenantName(name); err != nil {
		return master.Tenant{}, err
	}
	if err := m.open(); err != nil {
		return master.Tenant{}, err
	}
	t, err := scanTenant(m.pool.QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE name = $1`, name))
	return t, m.notFound(ctx, err, "no such tenant", "the tenant cannot be read")
}

// ListTenants implements master.Store.
func (m *Master) ListTenants(ctx context.Context, page master.Page) ([]master.Tenant, string, error) {
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	after := ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listTenants, "", 1)
		if err != nil {
			return nil, "", err
		}
		after = f[0]
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	// Names are never empty, so "after the empty name" is the first page.
	rows, err := m.pool.Query(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE name > $1 ORDER BY name LIMIT $2`, after, page.Limit+1)
	out, err := collect(rows, err, scanTenant)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the tenants cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listTenants, "", last.Name), nil
}

// SetTenantState implements master.Store. The old state is read under a row
// lock and the new one written in one transaction.
func (m *Master) SetTenantState(ctx context.Context, id string, state master.TenantState, reason string, audit master.AuditEntry, by master.Actor) error {
	if err := master.ValidateID("tenant id", id); err != nil {
		return err
	}
	if err := master.ValidateTenantState(state, reason); err != nil {
		return err
	}
	if err := audit.ValidateWith(master.DetailOldState, master.DetailNewState); err != nil {
		return err
	}
	if err := by.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the tenant's state cannot be set", func(tx pgx.Tx) error {
		if err := checkActor(ctx, tx, by); err != nil {
			return err
		}
		// The row is locked as it is read, so the state in the audit entry
		// is the one this call replaced even when another call changes the
		// same tenant at once. The state is not a key, so the lock is FOR NO
		// KEY UPDATE, which an actor's check holding the tenant for key
		// share (checkActor) does not wait for: two such changes of one
		// tenant are ordered by this lock instead of each waiting for the
		// other's share.
		var old string
		if err := tx.QueryRow(ctx, `SELECT state FROM tenants WHERE id = $1 FOR NO KEY UPDATE`, id).Scan(&old); err != nil {
			return notFoundIn(err, "no such tenant")
		}
		tag, err := tx.Exec(ctx, `UPDATE tenants SET state = $1, state_reason = $2 WHERE id = $3`, string(state), reason, id)
		if err := oneRow(tag, err, "no such tenant"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, m.red, audit.WithStateChange(master.TenantState(old), state))
	})
}

// RemoveTenant implements master.Store.
func (m *Master) RemoveTenant(ctx context.Context, id string, audit master.AuditEntry, by master.Actor) error {
	if err := master.ValidateID("tenant id", id); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	if err := by.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the tenant cannot be removed", func(tx pgx.Tx) error {
		if err := checkActor(ctx, tx, by); err != nil {
			return err
		}
		// The tenant's row is locked first. A SetGrant on this tenant holds
		// a share lock on it until it commits, so this waits for that grant
		// to exist and then removes it with the others; one that starts
		// after this finds no tenant. Without the lock a grant added between
		// the two deletes would make the second fail on the foreign key.
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = $1 FOR UPDATE`, id).Scan(&one); err != nil {
			return notFoundIn(err, "no such tenant")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM grants WHERE scope = $1`, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
		if err := oneRow(tag, err, "no such tenant"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, m.red, audit)
	})
}

// --- Keys ---

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
		if err := checkActor(ctx, tx, by); err != nil {
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
	out, last, more := cut(out, page.Limit)
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
		if err := checkActor(ctx, tx, by); err != nil {
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

// --- Grants ---

// SetGrant implements master.Store. A key's grants are changed one writer at
// a time: the key's row is locked first (FOR NO KEY UPDATE, which the foreign
// key checks of other grants do not conflict with), so two concurrent sets on
// one key are ordered, and the one statement below, which reads the role
// there was and writes the new one, sees what the other left even under read
// committed. Without the lock two sets of a grant that does not exist yet
// would both read "none". The key's lock also answers whether it exists.
func (m *Master) SetGrant(ctx context.Context, g master.Grant, audit master.AuditEntry, by master.Actor) (master.Role, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	if err := audit.ValidateRoleChange(); err != nil {
		return "", err
	}
	if err := by.Validate(); err != nil {
		return "", err
	}
	var previous master.Role
	err := m.write(ctx, "the grant cannot be set", func(tx pgx.Tx) error {
		if err := lockKeys(ctx, tx, by, g.KeyID); err != nil {
			return err
		}
		if err := checkActor(ctx, tx, by); err != nil {
			return err
		}
		revoked, err := lockGrantTargets(ctx, tx, g.KeyID, g.Scope)
		if err != nil {
			return err
		}
		if revoked {
			// Decided under the key's row lock, which RevokeKey also takes,
			// so no grant is written after the key's revocation.
			return fault.New(fault.Conflict, "the key is revoked")
		}
		var old *string
		err = tx.QueryRow(ctx, `WITH previous AS (SELECT role FROM grants WHERE key_id = $1 AND scope = $2)
			INSERT INTO grants (key_id, scope, tenant_id, role) VALUES ($1, $2, $3, $4)
			ON CONFLICT (key_id, scope) DO UPDATE SET role = EXCLUDED.role
			RETURNING (SELECT role FROM previous)`,
			g.KeyID, g.Scope.TenantID, nullText(g.Scope.TenantID), string(g.Role)).Scan(&old)
		if isForeignKeyViolation(err) {
			// The tenant was removed between the check and the insert.
			return fault.Wrap(fault.NotFound, m.red.scrub(err), "no such tenant")
		}
		if err != nil {
			return err
		}
		if old != nil {
			previous = master.Role(*old)
		}
		return insertAudit(ctx, tx, m.red, audit.WithRoleChange(previous, g.Role))
	})
	if err != nil {
		return "", err
	}
	return previous, nil
}

// lockGrantTargets returns NotFound unless the key, and the tenant of a
// tenant scope, exist, reports whether the key is revoked, and locks them
// against the changes that would undo that before the transaction ends. The
// key is locked FOR NO KEY UPDATE, so grant changes and a revocation on it are
// ordered; the tenant is locked for key share, so RemoveTenant waits for the
// grant being made.
func lockGrantTargets(ctx context.Context, tx pgx.Tx, keyID string, scope master.Scope) (revoked bool, err error) {
	var at *time.Time
	if err := tx.QueryRow(ctx, `SELECT revoked FROM api_keys WHERE id = $1 FOR NO KEY UPDATE`, keyID).Scan(&at); err != nil {
		return false, notFoundIn(err, "no such key")
	}
	if !scope.IsInstallation() {
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = $1 FOR KEY SHARE`, scope.TenantID).Scan(&one); err != nil {
			return false, notFoundIn(err, "no such tenant")
		}
	}
	return at != nil, nil
}

// grantColumns are a grant's columns with its tenant's name, joined.
const grantColumns = `g.key_id, g.scope, g.role, coalesce(t.name, '')`

// grantFrom joins each grant to its tenant, for the name.
const grantFrom = ` FROM grants g LEFT JOIN tenants t ON t.id = g.tenant_id`

// scanGrant reads one row of grantColumns.
func scanGrant(row scanner) (master.Grant, error) {
	var g master.Grant
	var role string
	err := row.Scan(&g.KeyID, &g.Scope.TenantID, &role, &g.TenantName)
	g.Role = master.Role(role)
	return g, err
}

// GrantsByKey implements master.Store: one read of the primary key's index,
// whose order (key, scope) puts the installation's empty scope first.
func (m *Master) GrantsByKey(ctx context.Context, keyID string, page master.Page) ([]master.Grant, string, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	var rows pgx.Rows
	var err error
	if page.Cursor == "" {
		// The first page has its own statement: the installation's scope
		// is the empty string, so no "scope > $2" includes it.
		rows, err = m.pool.Query(ctx, `SELECT `+grantColumns+grantFrom+` WHERE g.key_id = $1 ORDER BY g.key_id, g.scope LIMIT $2`, keyID, page.Limit+1)
	} else {
		f, derr := sqlkit.DecodeCursor(page.Cursor, listGrantsByKey, keyID, 1)
		if derr != nil {
			return nil, "", derr
		}
		rows, err = m.pool.Query(ctx, `SELECT `+grantColumns+grantFrom+` WHERE g.key_id = $1 AND g.scope > $2 ORDER BY g.key_id, g.scope LIMIT $3`, keyID, f[0], page.Limit+1)
	}
	out, err := collect(rows, err, scanGrant)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the key's grants cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listGrantsByKey, keyID, last.Scope.TenantID), nil
}

// scopeBinding is what a GrantsByScope cursor is bound to. The installation
// and a tenant can never give the same text, since a tenant id is not empty.
func scopeBinding(scope master.Scope) string {
	return "scope:" + scope.TenantID
}

// GrantsByScope implements master.Store. The cursor is bound to the scope.
func (m *Master) GrantsByScope(ctx context.Context, scope master.Scope, page master.Page) ([]master.Grant, string, error) {
	if err := scope.Validate(); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	after := ""
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listGrantsByScope, scopeBinding(scope), 1)
		if err != nil {
			return nil, "", err
		}
		after = f[0]
	}
	if err := m.open(); err != nil {
		return nil, "", err
	}
	// Key ids are never empty, so "after the empty id" is the first page.
	rows, err := m.pool.Query(ctx, `SELECT `+grantColumns+grantFrom+` WHERE g.scope = $1 AND g.key_id > $2 ORDER BY g.scope, g.key_id LIMIT $3`,
		scope.TenantID, after, page.Limit+1)
	out, err := collect(rows, err, scanGrant)
	if err != nil {
		return nil, "", m.fail(ctx, err, "the grants cannot be listed")
	}
	out, last, more := cut(out, page.Limit)
	if !more {
		return out, "", nil
	}
	return out, sqlkit.EncodeCursor(listGrantsByScope, scopeBinding(scope), last.KeyID), nil
}

// RolesFor implements master.Store with one statement (rolesForSQL). A name
// that is not a valid tenant name is asked as the empty name, which no tenant
// has, so the statement runs all the same and finds no tenant.
func (m *Master) RolesFor(ctx context.Context, keyID, tenantName string) (master.Roles, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return master.Roles{}, err
	}
	if master.ValidateTenantName(tenantName) != nil {
		tenantName = ""
	}
	if err := m.open(); err != nil {
		return master.Roles{}, err
	}
	var tenantID, inst, ten *string
	if err := m.pool.QueryRow(ctx, rolesForSQL, keyID, tenantName).Scan(&tenantID, &inst, &ten); err != nil {
		return master.Roles{}, m.fail(ctx, err, "the key's roles cannot be read")
	}
	r := master.Roles{TenantFound: tenantID != nil}
	if tenantID != nil {
		r.TenantID = *tenantID
	}
	if inst != nil {
		r.Installation = master.Role(*inst)
	}
	if ten != nil {
		r.Tenant = master.Role(*ten)
	}
	return r, nil
}

// rolesForSQL is RolesFor's statement, which always returns exactly one row:
// the tenant's id, NULL when no tenant has the name (the LEFT JOIN of a
// one-row table keeps the row), then the key's role on the installation and on
// that tenant, each NULL when there is no grant. The second grant lookup runs
// whether or not a tenant was found: without one it looks for the scope '*',
// which no id can be, so a missing tenant costs the same lookups as a tenant
// without a grant. Each is a point lookup: the tenants' unique index on name,
// and the grants' primary key at (key, the empty scope) and at (key, the
// tenant's id).
const rolesForSQL = `SELECT t.id,
	(SELECT role FROM grants WHERE key_id = $1 AND scope = ''),
	(SELECT role FROM grants WHERE key_id = $1 AND scope = coalesce(t.id, '*'))
	FROM (SELECT 1) AS one LEFT JOIN tenants AS t ON t.name = $2`

// RemoveGrant implements master.Store. The key's row is locked as SetGrant
// does, so the role it reports is the one the grant had when it was removed;
// one DELETE ... RETURNING then removes the grant and gives that role. A
// key or a tenant that does not exist is NotFound; a grant that does not
// exist, on a key and a tenant that do, is success.
func (m *Master) RemoveGrant(ctx context.Context, keyID string, scope master.Scope, audit master.AuditEntry, by master.Actor) (master.Role, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return "", err
	}
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if err := audit.ValidateRoleChange(); err != nil {
		return "", err
	}
	if err := by.Validate(); err != nil {
		return "", err
	}
	var previous master.Role
	err := m.write(ctx, "the grant cannot be removed", func(tx pgx.Tx) error {
		if err := lockKeys(ctx, tx, by, keyID); err != nil {
			return err
		}
		if err := checkActor(ctx, tx, by); err != nil {
			return err
		}
		if _, err := lockGrantTargets(ctx, tx, keyID, scope); err != nil {
			return err
		}
		var old string
		err := tx.QueryRow(ctx, `DELETE FROM grants WHERE key_id = $1 AND scope = $2 RETURNING role`, keyID, scope.TenantID).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		previous = master.Role(old)
		return insertAudit(ctx, tx, m.red, audit.WithRoleChange(previous, ""))
	})
	if err != nil {
		return "", err
	}
	return previous, nil
}

// --- Actors ---

// actorSQL is the check of a change's actor (master.Actor): the key's revoked
// time and expiry, and its role on the actor's scope, NULL when it holds no
// grant there; no row when the key does not exist. Two point lookups: the
// keys' primary key and the grants' primary key at (key, scope).
const actorSQL = `SELECT k.revoked, k.expires,
	(SELECT role FROM grants WHERE key_id = $1 AND scope = $2)
	FROM api_keys AS k WHERE k.id = $1`

// CheckActor implements master.Store with actorSQL, locking nothing. An
// actor that is not a key reads nothing.
func (m *Master) CheckActor(ctx context.Context, by master.Actor) error {
	if err := by.Validate(); err != nil {
		return err
	}
	if err := m.open(); err != nil {
		return err
	}
	if !by.IsKey() {
		return nil
	}
	found, revoked, expires, role, err := readActor(ctx, m.pool, actorSQL, by)
	if err != nil {
		return m.fail(ctx, err, "the acting key cannot be read")
	}
	return by.Judge(found, revoked, expires, role)
}

// actorCheck returns checkActor for the shared settings operations to run
// last in their transaction, before the commit.
func actorCheck(by master.Actor) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error { return checkActor(ctx, tx, by) }
}

// checkActor checks by inside a change's transaction. A change that can meet
// NotFound or Conflict runs it before it reads or writes anything else, so a
// refused actor learns nothing of what it named; a setting's change, which
// can meet neither, runs it last. What it read is locked until the transaction ends against every
// change that would undo it, so the check still holds at the commit: the key's
// row is held for share, which RevokeKey, SetGrant and RemoveGrant (each
// locking it FOR NO KEY UPDATE) wait for; and when the role is on a tenant,
// the tenant's row is held for key share, which RemoveTenant, the one change
// that removes grants without locking their key, waits for. Under read
// committed a lock that had to wait reads the row as the change it waited for
// left it, so a revocation committed first is seen.
func checkActor(ctx context.Context, tx pgx.Tx, by master.Actor) error {
	if !by.IsKey() {
		return nil
	}
	found, revoked, expires, role, err := readActor(ctx, tx, actorSQL+` FOR SHARE OF k`, by)
	if err != nil {
		return err
	}
	if found && role != "" && !by.Scope().IsInstallation() {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = $1 FOR KEY SHARE`, by.Scope().TenantID).Scan(&one)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Removed, with its grants, while this waited for its row.
			role = ""
		case err != nil:
			return err
		}
	}
	return by.Judge(found, revoked, expires, role)
}

// rowQuerier is what a pool and a transaction share for a one-row query.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readActor runs query, actorSQL with or without its lock, for by's key.
func readActor(ctx context.Context, q rowQuerier, query string, by master.Actor) (found bool, revoked, expires time.Time, role master.Role, err error) {
	var rev, exp *time.Time
	var r *string
	err = q.QueryRow(ctx, query, by.KeyID(), by.Scope().TenantID).Scan(&rev, &exp, &r)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, time.Time{}, time.Time{}, "", nil
	}
	if err != nil {
		return false, time.Time{}, time.Time{}, "", err
	}
	if r != nil {
		role = master.Role(*r)
	}
	return true, fromNullMicros(rev), fromNullMicros(exp), role, nil
}

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

// --- Settings ---

// --- Audit ---

// --- Helpers ---

// micros returns t cut to a whole microsecond, in UTC. Postgres keeps
// timestamptz to the microsecond; what is finer is dropped here, the same
// way, so what is written is exactly what is read back (master package
// documentation). master.ValidateTime keeps t after 1970, where dropping is
// the same as rounding down.
func micros(t time.Time) time.Time {
	return microsTime(t.UnixMicro())
}

// microsTime is the time v microseconds after 1970, in UTC.
func microsTime(v int64) time.Time {
	return time.UnixMicro(v).UTC()
}

// nullMicros is micros, with NULL for the zero time ("never").
func nullMicros(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return micros(t)
}

// fromNullMicros reads a nullable timestamptz, with the zero time for NULL,
// in UTC.
func fromNullMicros(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// nullText returns NULL for the empty string, for the grants' tenant_id.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// notFoundIn turns pgx.ErrNoRows into NotFound with what and passes any
// other error through for write to classify.
func notFoundIn(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.NotFound, what)
	}
	return err
}

// oneRow returns NotFound with what unless the statement changed a row.
func oneRow(tag pgconn.CommandTag, err error, what string) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fault.New(fault.NotFound, what)
	}
	return nil
}

// collect reads every row of a query with scan, closing the rows.
func collect[T any](rows pgx.Rows, err error, scan func(scanner) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (T, error) { return scan(row) })
}

// cut trims a page read with limit+1 rows to limit, and reports whether
// there was more and which row is the last one returned.
func cut[T any](rows []T, limit int) ([]T, T, bool) {
	var zero T
	if len(rows) <= limit {
		return rows, zero, false
	}
	rows = rows[:limit]
	return rows, rows[limit-1], true
}
