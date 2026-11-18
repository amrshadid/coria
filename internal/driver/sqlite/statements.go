package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// statements are the master's fixed statements, prepared once on the pool
// that runs them, so a request does not pay for SQLite parsing and planning
// the same text again. The reads on the per-request path (a key by id, a
// key's roles) are each one of these and read point lookups of an index.
//
// Every ORDER BY names a total order: its last column is unique among the
// rows it sorts, so a keyset cursor is never ambiguous, whatever order an
// index would give.
type statements struct {
	all []*sql.Stmt

	// Read pool.
	tenantByID, tenantByName, listTenants *sql.Stmt
	keyByID, listKeys                     *sql.Stmt
	grantsByKeyFirst, grantsByKeyNext     *sql.Stmt
	grantsByScope, rolesFor               *sql.Stmt
	settingByName, listSettings           *sql.Stmt

	// Write pool, used inside write transactions through Tx.StmtContext,
	// except markKeyUsed, which is one statement on its own.
	tenantNameTaken, tenantExists, insertTenant *sql.Stmt
	tenantState, setTenantState                 *sql.Stmt
	deleteTenantGrants, deleteTenant            *sql.Stmt
	insertKey, keyRevoked, revokeKey            *sql.Stmt
	markKeyUsed                                 *sql.Stmt
	grantRole, upsertGrant, deleteGrant         *sql.Stmt
	upsertSetting, deleteSetting                *sql.Stmt
	insertAudit                                 *sql.Stmt
}

// prepare prepares every statement, or none.
func prepare(ctx context.Context, p *pools) (*statements, error) {
	s := &statements{}
	type entry struct {
		dst  **sql.Stmt
		db   *sql.DB
		text string
	}
	r, w := p.read, p.write
	list := []entry{
		{&s.tenantByID, r, `SELECT ` + tenantColumns + ` FROM tenants WHERE id = ?`},
		{&s.tenantByName, r, `SELECT ` + tenantColumns + ` FROM tenants WHERE name = ?`},
		{&s.listTenants, r, `SELECT ` + tenantColumns + ` FROM tenants WHERE name > ? ORDER BY name LIMIT ?`},
		{&s.keyByID, r, `SELECT ` + keyColumns + ` FROM api_keys WHERE id = ?`},
		{&s.listKeys, r, `SELECT ` + keyColumns + ` FROM api_keys WHERE (created, id) > (?, ?) ORDER BY created, id LIMIT ?`},
		{&s.grantsByKeyFirst, r, `SELECT ` + grantColumns + grantFrom + ` WHERE g.key_id = ? ORDER BY g.key_id, g.scope LIMIT ?`},
		{&s.grantsByKeyNext, r, `SELECT ` + grantColumns + grantFrom + ` WHERE g.key_id = ? AND g.scope > ? ORDER BY g.key_id, g.scope LIMIT ?`},
		{&s.grantsByScope, r, `SELECT ` + grantColumns + grantFrom + ` WHERE g.scope = ? AND g.key_id > ? ORDER BY g.scope, g.key_id LIMIT ?`},
		{&s.rolesFor, r, rolesForSQL},
		{&s.settingByName, r, `SELECT value FROM settings WHERE name = ?`},
		{&s.listSettings, r, `SELECT name, value FROM settings WHERE name > ? ORDER BY name LIMIT ?`},

		{&s.tenantNameTaken, w, `SELECT 1 FROM tenants WHERE name = ?`},
		{&s.tenantExists, w, `SELECT 1 FROM tenants WHERE id = ?`},
		{&s.insertTenant, w, `INSERT INTO tenants (` + tenantColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?)`},
		{&s.tenantState, w, `SELECT state FROM tenants WHERE id = ?`},
		{&s.setTenantState, w, `UPDATE tenants SET state = ?, state_reason = ? WHERE id = ?`},
		{&s.deleteTenantGrants, w, `DELETE FROM grants WHERE scope = ?`},
		{&s.deleteTenant, w, `DELETE FROM tenants WHERE id = ?`},
		{&s.insertKey, w, `INSERT INTO api_keys (id, hash, holder, created, expires) VALUES (?, ?, ?, ?, ?)`},
		{&s.keyRevoked, w, `SELECT revoked FROM api_keys WHERE id = ?`},
		{&s.revokeKey, w, `UPDATE api_keys SET revoked = ? WHERE id = ? AND revoked IS NULL`},
		// The throttle is in the WHERE: a row whose last use is within the
		// minute before at, or after at, is not matched, so nothing is
		// written and the stored time never moves backward.
		{&s.markKeyUsed, w, `UPDATE api_keys SET last_used = ? WHERE id = ? AND (last_used IS NULL OR last_used < ?)`},
		{&s.grantRole, w, `SELECT role FROM grants WHERE key_id = ? AND scope = ?`},
		// One statement replaces the role of an existing grant, so a key
		// changing role is never without one (D37).
		{&s.upsertGrant, w, `INSERT INTO grants (key_id, scope, tenant_id, role) VALUES (?, ?, ?, ?)
			ON CONFLICT (key_id, scope) DO UPDATE SET role = excluded.role`},
		{&s.deleteGrant, w, `DELETE FROM grants WHERE key_id = ? AND scope = ? RETURNING role`},
		{&s.upsertSetting, w, `INSERT INTO settings (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`},
		{&s.deleteSetting, w, `DELETE FROM settings WHERE name = ?`},
		{&s.insertAudit, w, `INSERT INTO audit (` + auditColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
	}
	for _, e := range list {
		stmt, err := e.db.PrepareContext(ctx, e.text)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("preparing %q: %w", e.text, err)
		}
		*e.dst = stmt
		s.all = append(s.all, stmt)
	}
	return s, nil
}

// close closes every prepared statement.
func (s *statements) close() {
	for _, stmt := range s.all {
		_ = stmt.Close() // Closing a statement on a closing pool has nothing to report.
	}
}

// rolesForSQL is RolesFor's statement, which always returns exactly one
// row: the tenant's id, NULL when no tenant has the name (the LEFT JOIN of a
// one-row table keeps the row), then the key's role on the installation and
// on that tenant, each NULL when there is no grant. The second grant
// lookup runs whether or not a tenant was found: without one it looks for
// the scope '*', which no id can be, so a missing tenant costs the same
// lookups as a tenant without a grant. Each is a point lookup:
// the tenants' unique index on name, and the grants' primary key at (key,
// the empty scope) and at (key, the tenant's id).
const rolesForSQL = `SELECT t.id,
	(SELECT role FROM grants WHERE key_id = ?1 AND scope = ''),
	(SELECT role FROM grants WHERE key_id = ?1 AND scope = coalesce(t.id, '*'))
	FROM (SELECT 1) AS one LEFT JOIN tenants AS t ON t.name = ?2`
