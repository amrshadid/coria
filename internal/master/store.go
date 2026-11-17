package master

import (
	"context"
	"time"
)

// Store is the master database, stated as operations. A driver package
// implements it for one database type and proves it with the contract suite
// in package mastertest.
//
// Every method takes a context, and fails with an error from package fault.
// Every method that changes the master, except MarkKeyUsed, takes the audit
// entry for that change and writes both or neither: if the entry cannot be
// written (its id is taken, the database fails), the change is not made, and
// if the change cannot be made, the entry is not written. A refusal or a
// failure that changes nothing is recorded with AppendAudit.
//
// A value is validated with its Validate method before anything is written,
// and refused as Invalid. A list's limit outside 1 to 1000, or a cursor this
// store did not make for that list, is Invalid.
type Store interface {
	// Migrate brings the database's schema to the version this binary
	// knows, applying what is missing, in order, under a lock that holds
	// across processes. It refuses, changing nothing, a database whose
	// applied migrations differ from this binary's or that is at a version
	// this binary does not know.
	Migrate(ctx context.Context) error
	// Version returns the schema version the database is at: 0 before the
	// first migration.
	Version(ctx context.Context) (int, error)
	// Ping reports whether the database answers, as Unavailable if not.
	Ping(ctx context.Context) error
	// Close releases the database. Calls made after it fail.
	Close() error

	// CreateTenant adds a tenant. A name or id already taken is Conflict.
	CreateTenant(ctx context.Context, t Tenant, audit AuditEntry) error
	// Tenant returns the tenant with this id, or NotFound.
	Tenant(ctx context.Context, id string) (Tenant, error)
	// TenantByName returns the tenant with this name, or NotFound.
	TenantByName(ctx context.Context, name string) (Tenant, error)
	// ListTenants returns a page of tenants in order of name, and the cursor
	// of the next page, empty after the last.
	ListTenants(ctx context.Context, page Page) ([]Tenant, string, error)
	// SetTenantState sets a tenant's state and its reason, or is NotFound.
	SetTenantState(ctx context.Context, id string, state TenantState, reason string, audit AuditEntry) error
	// RemoveTenant removes a tenant and every grant on it, or is NotFound.
	RemoveTenant(ctx context.Context, id string, audit AuditEntry) error

	// CreateKey adds a key. An id already taken is Conflict.
	CreateKey(ctx context.Context, k Key, audit AuditEntry) error
	// Key returns the key with this id, revoked or expired or not, or
	// NotFound. It is on the path of every API request: one indexed read.
	Key(ctx context.Context, id string) (Key, error)
	// ListKeys returns a page of keys in order of creation, then id.
	ListKeys(ctx context.Context, page Page) ([]Key, string, error)
	// MarkKeyUsed sets a key's last-used time to at, only when the time it
	// holds is unset or more than a minute before at, so a busy key costs
	// one write a minute, not one a request (M0 design note, part 4). It
	// reports whether it wrote. It is one statement with no read first, and
	// takes no audit entry: the request that used the key writes its own.
	// An unknown key writes nothing and is not an error, since the caller
	// has just read the key.
	MarkKeyUsed(ctx context.Context, id string, at time.Time) (bool, error)
	// RevokeKey sets a key's revoked time to at and returns it, or is
	// NotFound. A key already revoked keeps the time it was first revoked,
	// which is returned; nothing changes, and the audit entry, which records
	// the request, is still written (the API contract: revoking a revoked
	// key succeeds with its first revoke time).
	RevokeKey(ctx context.Context, id string, at time.Time, audit AuditEntry) (time.Time, error)

	// SetGrant gives a key a role on a scope. When the key already has a
	// grant on that scope, its role is replaced in one step, so a role is
	// changed with no moment of no access (D37). It returns the role the
	// grant had before, or the empty Role when there was none, and the
	// audit entry it writes has the old and new role in its details
	// (AuditEntry.WithRoleChange), both taken inside the change, so two
	// concurrent sets each see what the other left. An unknown key or
	// tenant is NotFound.
	SetGrant(ctx context.Context, g Grant, audit AuditEntry) (Role, error)
	// GrantsByKey returns a page of a key's grants, the installation first,
	// then by tenant id. An unknown key has none. It is on the path of every
	// API request: one indexed read.
	GrantsByKey(ctx context.Context, keyID string, page Page) ([]Grant, string, error)
	// GrantsByScope returns a page of the grants on one scope, by key id.
	GrantsByScope(ctx context.Context, scope Scope, page Page) ([]Grant, string, error)
	// RemoveGrant removes a key's grant on a scope and returns the role it
	// had, or the empty Role when there was none: removing a grant the key
	// does not hold changes nothing and succeeds (the API contract). The
	// audit entry it writes has the old role in its details and "none" as
	// the new. An unknown key or tenant is NotFound.
	RemoveGrant(ctx context.Context, keyID string, scope Scope, audit AuditEntry) (Role, error)

	// Setting returns a setting's value, or NotFound.
	Setting(ctx context.Context, name string) (string, error)
	// SetSetting sets a setting's value, adding the setting or replacing it.
	SetSetting(ctx context.Context, name, value string, audit AuditEntry) error
	// UnsetSetting removes a setting's value and reports whether there was
	// one: removing a value that is not set changes nothing and succeeds, as
	// the API contract's UnsetSetting does. The audit entry is written
	// either way.
	UnsetSetting(ctx context.Context, name string, audit AuditEntry) (bool, error)
	// ListSettings returns a page of settings in order of name.
	ListSettings(ctx context.Context, page Page) ([]Setting, string, error)

	// AppendAudit writes up to 10,000 entries, all or none. An id already
	// taken is Conflict. No entries is no write.
	AppendAudit(ctx context.Context, entries ...AuditEntry) error
	// ListAudit returns a page of the entries the filter keeps, newest
	// first: by time, then by id, both descending.
	ListAudit(ctx context.Context, filter AuditFilter, page Page) ([]AuditEntry, string, error)
}
