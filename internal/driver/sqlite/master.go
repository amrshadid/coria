package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

// masterLists names the master's settings and audit lists for the code it
// shares with a tenant's database (shared.go).
var masterLists = lists{settings: listSettings, audit: listAudit, missing: "no such setting"}

// Master is the master database on a SQLite file. It is safe for concurrent
// use.
type Master struct {
	base

	// The prepared statements, made on first use after the schema exists,
	// since a statement cannot be prepared against tables not yet made.
	mu    sync.Mutex
	stmts atomic.Pointer[statements]
}

var _ master.Store = (*Master)(nil)

// OpenMaster opens the master database in the SQLite file at path, creating
// the file, readable by its owner only, when it does not exist. On Unix an
// existing file, its -wal and -shm files, or its directory that someone other
// than the owner can read or write is refused (see checkPrivate). It does
// not migrate: the caller calls Migrate, so that opening a database never
// changes it unasked.
func OpenMaster(ctx context.Context, path string) (*Master, error) {
	if masterMigrationsErr != nil {
		return nil, fault.Wrap(fault.Internal, masterMigrationsErr, "the master's migrations cannot be read")
	}
	p, err := openPools(ctx, path, masterDB)
	if err != nil {
		return nil, err
	}
	m := &Master{}
	m.p, m.name = p, masterDB
	return m, nil
}

// Migrate implements master.Store.
func (m *Master) Migrate(ctx context.Context) error {
	if err := m.open(); err != nil {
		return err
	}
	_, err := sqlkit.Migrate(ctx, m.p.write, dialect, masterMigrations)
	return m.fail(ctx, err, "the master database cannot be migrated")
}

// Version implements master.Store.
func (m *Master) Version(ctx context.Context) (int, error) {
	if err := m.open(); err != nil {
		return 0, err
	}
	v, err := sqlkit.Version(ctx, m.p.read, dialect)
	return v, m.fail(ctx, err, "the master's schema version cannot be read")
}

// Ping implements master.Store. It reaches both pools, since either failing
// fails requests.
func (m *Master) Ping(ctx context.Context) error { return m.ping(ctx) }

// Close implements master.Store.
func (m *Master) Close() error {
	return m.shutdown(func() {
		if s := m.stmts.Load(); s != nil {
			s.close()
		}
	})
}

// ready returns the prepared statements, preparing them on first use.
func (m *Master) ready(ctx context.Context) (*statements, error) {
	if err := m.open(); err != nil {
		return nil, err
	}
	if s := m.stmts.Load(); s != nil {
		return s, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.stmts.Load(); s != nil {
		return s, nil
	}
	s, err := prepare(ctx, m.p)
	if err != nil {
		return nil, m.notReady(ctx, err)
	}
	m.stmts.Store(s)
	return s, nil
}

// write runs fn in one write transaction (base.writeTx) with the prepared
// statements. Every changing operation and its audit entry go through here
// together, which is what makes them both or neither.
func (m *Master) write(ctx context.Context, message string, fn func(tx *sql.Tx, s *statements) error) error {
	s, err := m.ready(ctx)
	if err != nil {
		return err
	}
	return m.writeTx(ctx, message, func(tx *sql.Tx) error { return fn(tx, s) })
}

// b, shared and writeShared give the operations the master shares with a
// tenant's database (shared.go) what they need.
func (m *Master) b() *base { return &m.base }

// shared returns the master's prepared settings and audit statements.
func (m *Master) shared(ctx context.Context) (*sharedStatements, error) {
	s, err := m.ready(ctx)
	if err != nil {
		return nil, err
	}
	return &s.sharedStatements, nil
}

// writeShared runs fn in a write transaction with those statements.
func (m *Master) writeShared(ctx context.Context, message string, fn func(tx *sql.Tx, s *sharedStatements) error) error {
	return m.write(ctx, message, func(tx *sql.Tx, s *statements) error { return fn(tx, &s.sharedStatements) })
}

// --- Tenants ---

// CreateTenant implements master.Store.
func (m *Master) CreateTenant(ctx context.Context, t master.Tenant, audit master.AuditEntry) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the tenant cannot be created", func(tx *sql.Tx, s *statements) error {
		// The name is looked up first to say which of name and id is
		// taken. Inside one transaction begun with BEGIN IMMEDIATE no other
		// writer, in this process or another, runs between the look-up and
		// the insert; the UNIQUE constraint holds the rule even so.
		var one int
		err := tx.StmtContext(ctx, s.tenantNameTaken).QueryRowContext(ctx, t.Name).Scan(&one)
		if err == nil {
			return fault.New(fault.Conflict, "a tenant named "+t.Name+" exists")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.StmtContext(ctx, s.insertTenant).ExecContext(ctx,
			t.ID, t.Name, t.Driver, t.SealedAddress, string(t.State), t.StateReason, micros(t.Created))
		if isUniqueViolation(err) {
			return fault.Wrap(fault.Conflict, err, "a tenant with this id or name exists")
		}
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, s, audit)
	})
}

const tenantColumns = `id, name, driver, address, state, state_reason, created`

// scanTenant reads one row of tenantColumns.
func scanTenant(row interface{ Scan(...any) error }) (master.Tenant, error) {
	var t master.Tenant
	var state string
	var created int64
	err := row.Scan(&t.ID, &t.Name, &t.Driver, &t.SealedAddress, &state, &t.StateReason, &created)
	t.State = master.TenantState(state)
	t.Created = fromMicros(created)
	return t, err
}

// Tenant implements master.Store.
func (m *Master) Tenant(ctx context.Context, id string) (master.Tenant, error) {
	if err := master.ValidateID("tenant id", id); err != nil {
		return master.Tenant{}, err
	}
	s, err := m.ready(ctx)
	if err != nil {
		return master.Tenant{}, err
	}
	t, err := scanTenant(s.tenantByID.QueryRowContext(ctx, id))
	return t, m.notFound(ctx, err, "no such tenant", "the tenant cannot be read")
}

// TenantByName implements master.Store.
func (m *Master) TenantByName(ctx context.Context, name string) (master.Tenant, error) {
	if err := master.ValidateTenantName(name); err != nil {
		return master.Tenant{}, err
	}
	s, err := m.ready(ctx)
	if err != nil {
		return master.Tenant{}, err
	}
	t, err := scanTenant(s.tenantByName.QueryRowContext(ctx, name))
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
	s, err := m.ready(ctx)
	if err != nil {
		return nil, "", err
	}
	// Names are never empty, so "after the empty name" is the first page.
	rows, err := s.listTenants.QueryContext(ctx, after, page.Limit+1)
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

// SetTenantState implements master.Store. The old state is read and the new
// one written in one write transaction, so the audit entry says what this
// call replaced even when another call changes the same tenant at once.
func (m *Master) SetTenantState(ctx context.Context, id string, state master.TenantState, reason string, audit master.AuditEntry) error {
	if err := master.ValidateID("tenant id", id); err != nil {
		return err
	}
	if err := master.ValidateTenantState(state, reason); err != nil {
		return err
	}
	if err := audit.ValidateWith(master.DetailOldState, master.DetailNewState); err != nil {
		return err
	}
	return m.write(ctx, "the tenant's state cannot be set", func(tx *sql.Tx, s *statements) error {
		var old string
		err := tx.StmtContext(ctx, s.tenantState).QueryRowContext(ctx, id).Scan(&old)
		if errors.Is(err, sql.ErrNoRows) {
			return fault.New(fault.NotFound, "no such tenant")
		}
		if err != nil {
			return err
		}
		res, err := tx.StmtContext(ctx, s.setTenantState).ExecContext(ctx, string(state), reason, id)
		if err := oneRow(res, err, "no such tenant"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, s, audit.WithStateChange(master.TenantState(old), state))
	})
}

// RemoveTenant implements master.Store.
func (m *Master) RemoveTenant(ctx context.Context, id string, audit master.AuditEntry) error {
	if err := master.ValidateID("tenant id", id); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the tenant cannot be removed", func(tx *sql.Tx, s *statements) error {
		// The grants go first, in the same transaction: the foreign key
		// refuses a tenant removed under its grants, so a forgotten step
		// fails loudly instead of leaving grants on nothing.
		if _, err := tx.StmtContext(ctx, s.deleteTenantGrants).ExecContext(ctx, id); err != nil {
			return err
		}
		res, err := tx.StmtContext(ctx, s.deleteTenant).ExecContext(ctx, id)
		if err := oneRow(res, err, "no such tenant"); err != nil {
			return err
		}
		return insertAudit(ctx, tx, s, audit)
	})
}

// --- Keys ---

// CreateKey implements master.Store.
func (m *Master) CreateKey(ctx context.Context, k master.Key, audit master.AuditEntry) error {
	if err := k.Validate(); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	return m.write(ctx, "the key cannot be created", func(tx *sql.Tx, s *statements) error {
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
	out, last, more := cut(out, page.Limit)
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
func (m *Master) RevokeKey(ctx context.Context, id string, at time.Time, audit master.AuditEntry) (time.Time, error) {
	if err := master.ValidateID("key id", id); err != nil {
		return time.Time{}, err
	}
	if err := master.ValidateTime("revocation time", at); err != nil {
		return time.Time{}, err
	}
	if err := audit.ValidateWith(master.DetailAlreadyRevoked); err != nil {
		return time.Time{}, err
	}
	var revoked time.Time
	err := m.write(ctx, "the key cannot be revoked", func(tx *sql.Tx, s *statements) error {
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

// --- Grants ---

// SetGrant implements master.Store. The key's revocation and the old role are
// read and the new role written in one write transaction, and SQLite has one
// writer at a time, so a set racing a revocation or another set is ordered
// with it: no grant is written after the key is revoked, and each set reads
// what the others left.
func (m *Master) SetGrant(ctx context.Context, g master.Grant, audit master.AuditEntry) (master.Role, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	if err := audit.ValidateRoleChange(); err != nil {
		return "", err
	}
	var previous master.Role
	err := m.write(ctx, "the grant cannot be set", func(tx *sql.Tx, s *statements) error {
		revoked, err := m.grantTargetsExist(ctx, tx, s, g.KeyID, g.Scope)
		if err != nil {
			return err
		}
		if revoked {
			return fault.New(fault.Conflict, "the key is revoked")
		}
		var old string
		err = tx.StmtContext(ctx, s.grantRole).QueryRowContext(ctx, g.KeyID, g.Scope.TenantID).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		previous = master.Role(old)
		if _, err := tx.StmtContext(ctx, s.upsertGrant).ExecContext(ctx,
			g.KeyID, g.Scope.TenantID, nullText(g.Scope.TenantID), string(g.Role)); err != nil {
			return err
		}
		return insertAudit(ctx, tx, s, audit.WithRoleChange(previous, g.Role))
	})
	if err != nil {
		return "", err
	}
	return previous, nil
}

// grantTargetsExist returns NotFound unless the key, and the tenant of a
// tenant scope, exist, and reports whether the key is revoked. The foreign
// keys would refuse a missing one too, but could not say which.
func (m *Master) grantTargetsExist(ctx context.Context, tx *sql.Tx, s *statements, keyID string, scope master.Scope) (revoked bool, err error) {
	var at sql.NullInt64
	err = tx.StmtContext(ctx, s.keyRevoked).QueryRowContext(ctx, keyID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fault.New(fault.NotFound, "no such key")
	}
	if err != nil {
		return false, err
	}
	if scope.IsInstallation() {
		return at.Valid, nil
	}
	var one int
	err = tx.StmtContext(ctx, s.tenantExists).QueryRowContext(ctx, scope.TenantID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fault.New(fault.NotFound, "no such tenant")
	}
	return at.Valid, err
}

// grantColumns are a grant's columns with its tenant's name, joined.
const grantColumns = `g.key_id, g.scope, g.role, coalesce(t.name, '')`

// grantFrom joins each grant to its tenant, for the name.
const grantFrom = ` FROM grants g LEFT JOIN tenants t ON t.id = g.tenant_id`

// scanGrant reads one row of grantColumns.
func scanGrant(row interface{ Scan(...any) error }) (master.Grant, error) {
	var g master.Grant
	var role string
	err := row.Scan(&g.KeyID, &g.Scope.TenantID, &role, &g.TenantName)
	g.Role = master.Role(role)
	return g, err
}

// GrantsByKey implements master.Store: a range of the primary key's index,
// whose order (key, scope) puts the installation's empty scope first, with
// one lookup of the tenants' primary key per grant for the name. The cursor
// is bound to the key.
func (m *Master) GrantsByKey(ctx context.Context, keyID string, page master.Page) ([]master.Grant, string, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return nil, "", err
	}
	if err := page.Validate(); err != nil {
		return nil, "", err
	}
	var after []string
	if page.Cursor != "" {
		f, err := sqlkit.DecodeCursor(page.Cursor, listGrantsByKey, keyID, 1)
		if err != nil {
			return nil, "", err
		}
		after = f
	}
	s, err := m.ready(ctx)
	if err != nil {
		return nil, "", err
	}
	var rows *sql.Rows
	if after == nil {
		// The first page has its own statement: the installation's scope
		// is the empty string, so no "scope > ?" includes it.
		rows, err = s.grantsByKeyFirst.QueryContext(ctx, keyID, page.Limit+1)
	} else {
		rows, err = s.grantsByKeyNext.QueryContext(ctx, keyID, after[0], page.Limit+1)
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
	s, err := m.ready(ctx)
	if err != nil {
		return nil, "", err
	}
	// Key ids are never empty, so "after the empty id" is the first page.
	rows, err := s.grantsByScope.QueryContext(ctx, scope.TenantID, after, page.Limit+1)
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
// that is not a valid tenant name is asked as the empty name, which no
// tenant has, so the statement runs all the same and finds no tenant.
func (m *Master) RolesFor(ctx context.Context, keyID, tenantName string) (master.Roles, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return master.Roles{}, err
	}
	if master.ValidateTenantName(tenantName) != nil {
		tenantName = ""
	}
	s, err := m.ready(ctx)
	if err != nil {
		return master.Roles{}, err
	}
	var tenantID, inst, ten sql.NullString
	err = s.rolesFor.QueryRowContext(ctx, keyID, tenantName).Scan(&tenantID, &inst, &ten)
	if err != nil {
		return master.Roles{}, m.fail(ctx, err, "the key's roles cannot be read")
	}
	return master.Roles{
		Installation: master.Role(inst.String),
		Tenant:       master.Role(ten.String),
		TenantID:     tenantID.String,
		TenantFound:  tenantID.Valid,
	}, nil
}

// RemoveGrant implements master.Store. One DELETE ... RETURNING removes the
// grant and gives the role it had; only when it removed nothing are the key
// and tenant looked up, to tell "no grant" (success) from NotFound.
func (m *Master) RemoveGrant(ctx context.Context, keyID string, scope master.Scope, audit master.AuditEntry) (master.Role, error) {
	if err := master.ValidateID("key id", keyID); err != nil {
		return "", err
	}
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if err := audit.ValidateRoleChange(); err != nil {
		return "", err
	}
	var previous master.Role
	err := m.write(ctx, "the grant cannot be removed", func(tx *sql.Tx, s *statements) error {
		var old string
		err := tx.StmtContext(ctx, s.deleteGrant).QueryRowContext(ctx, keyID, scope.TenantID).Scan(&old)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := m.grantTargetsExist(ctx, tx, s, keyID, scope); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		previous = master.Role(old)
		return insertAudit(ctx, tx, s, audit.WithRoleChange(previous, ""))
	})
	if err != nil {
		return "", err
	}
	return previous, nil
}

// --- Settings ---

// Setting implements master.Store.
func (m *Master) Setting(ctx context.Context, name string) (string, error) {
	return getSetting(ctx, m, masterLists, name)
}

// SetSetting implements master.Store.
func (m *Master) SetSetting(ctx context.Context, name, value string, audit master.AuditEntry) error {
	return setSetting(ctx, m, name, value, audit)
}

// UnsetSetting implements master.Store.
func (m *Master) UnsetSetting(ctx context.Context, name string, audit master.AuditEntry) (bool, error) {
	return unsetSetting(ctx, m, name, audit)
}

// ListSettings implements master.Store.
func (m *Master) ListSettings(ctx context.Context, page master.Page) ([]master.Setting, string, error) {
	return listSettingsPage(ctx, m, masterLists, page)
}

// --- Audit ---

// AppendAudit implements master.Store.
func (m *Master) AppendAudit(ctx context.Context, entries ...master.AuditEntry) error {
	return appendAudit(ctx, m, entries)
}

// insertAudit writes one audit entry inside the transaction of the change it
// records.
func insertAudit(ctx context.Context, tx *sql.Tx, s *statements, e master.AuditEntry) error {
	return execAudit(ctx, tx.StmtContext(ctx, s.insertAudit), e)
}

// execAudit writes one audit entry with the transaction's insert statement.
// The audit table's only unique key is the id, so a unique violation here is
// a duplicate id: master.ErrDuplicateAuditID, never a Conflict.
func execAudit(ctx context.Context, stmt *sql.Stmt, e master.AuditEntry) error {
	details, err := encodeDetails(e.Details)
	if err != nil {
		return err
	}
	_, err = stmt.ExecContext(ctx, e.ID, micros(e.Time), e.KeyID, e.LocalUser, e.RemoteAddress,
		e.Method, e.TenantName, e.Object, string(e.Outcome), e.Reason, details, e.RequestID)
	if isUniqueViolation(err) {
		return fault.Wrap(fault.Internal, errors.Join(master.ErrDuplicateAuditID, err), "an audit entry with this id exists")
	}
	return err
}

const auditColumns = `id, at, key_id, local_user, remote_addr, method, tenant_name, object, outcome, reason, details, request_id`

// scanAudit reads one row of auditColumns.
func scanAudit(row interface{ Scan(...any) error }) (master.AuditEntry, error) {
	var e master.AuditEntry
	var at int64
	var outcome, details string
	if err := row.Scan(&e.ID, &at, &e.KeyID, &e.LocalUser, &e.RemoteAddress, &e.Method,
		&e.TenantName, &e.Object, &outcome, &e.Reason, &details, &e.RequestID); err != nil {
		return e, err
	}
	e.Time = fromMicros(at)
	e.Outcome = master.Outcome(outcome)
	var err error
	e.Details, err = decodeDetails(details)
	return e, err
}

// auditBinding is what a ListAudit cursor is bound to: every field of the
// filter, so a cursor made with one filter is refused with another.
func auditBinding(f master.AuditFilter) string {
	t := func(x time.Time) string {
		if x.IsZero() {
			return ""
		}
		return strconv.FormatInt(micros(x), 10)
	}
	return sqlkit.Binding(f.TenantName, f.KeyID, string(f.Outcome), t(f.Since), t(f.Until))
}

// auditQuery returns the statement and arguments of one page of ListAudit.
// The statement is put together from fixed fragments according to which
// filters are set, so that each combination reads the index named for it in
// 0001_init.sql instead of testing "parameter is empty OR column =
// parameter", which SQLite cannot answer from an index. No value is ever
// written into the text; every one is a parameter. The cursor, when there is
// one, is the (at, id) of the last row returned.
func auditQuery(filter master.AuditFilter, after []any, limit int) (string, []any) {
	var where []string
	var args []any
	if filter.TenantName != "" {
		where = append(where, "tenant_name = ?")
		args = append(args, filter.TenantName)
	}
	if filter.KeyID != "" {
		where = append(where, "key_id = ?")
		args = append(args, filter.KeyID)
	}
	if filter.Outcome != "" {
		where = append(where, "outcome = ?")
		args = append(args, string(filter.Outcome))
	}
	if !filter.Since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, micros(filter.Since))
	}
	if !filter.Until.IsZero() {
		where = append(where, "at < ?")
		args = append(args, micros(filter.Until))
	}
	if after != nil {
		// Newest first, so the next page is what sorts before the last row.
		where = append(where, "(at, id) < (?, ?)")
		args = append(args, after...)
	}
	query := "SELECT " + auditColumns + " FROM audit"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- every fragment is a constant above; values are parameters.
	}
	// The id breaks ties between entries of the same time, so the order is
	// total and a cursor at (at, id) is unambiguous.
	query += " ORDER BY at DESC, id DESC LIMIT ?"
	return query, append(args, limit)
}

// ListAudit implements master.Store. The cursor is bound to the whole filter.
func (m *Master) ListAudit(ctx context.Context, filter master.AuditFilter, page master.Page) ([]master.AuditEntry, string, error) {
	return listAuditPage(ctx, m, masterLists, filter, page)
}

// --- Helpers ---

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
// every SQL database (research R6). encoding/json sorts the keys, so an
// entry's text does not depend on the map's order.
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
