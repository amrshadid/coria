package access

import (
	"context"
	"time"

	"github.com/amrshadid/magnetar/internal/master"
)

// Store is the part of the master database this package uses: the
// operations it calls and no others. Stating it here keeps a change of the
// master port to this one file, and lets a test wrap the store to count what
// a check reads.
//
// RolesFor is being added to the master port; until it is there, a
// master.Store does not satisfy Store, and the tests give it RolesFor with
// an adapter over GrantsByKey. When the port has it, the assertion below
// that master.Store satisfies Store comes back.
type Store interface {
	// Key returns the key with this id, or NotFound.
	Key(ctx context.Context, id string) (master.Key, error)
	// RolesFor returns the key's role on the installation and its role on
	// the tenant with this id, each the empty Role for none, in one indexed
	// read; an empty tenantID asks for the installation only. It is on the
	// path of every request made with a key.
	RolesFor(ctx context.Context, keyID, tenantID string) (installation, tenant master.Role, err error)
	// GrantsByKey returns a page of a key's grants.
	GrantsByKey(ctx context.Context, keyID string, page master.Page) ([]master.Grant, string, error)
	// GrantsByScope returns a page of the grants on one scope.
	GrantsByScope(ctx context.Context, scope master.Scope, page master.Page) ([]master.Grant, string, error)
	// Tenant returns the tenant with this id, or NotFound.
	Tenant(ctx context.Context, id string) (master.Tenant, error)
	// TenantByName returns the tenant with this name, or NotFound.
	TenantByName(ctx context.Context, name string) (master.Tenant, error)
	// MarkKeyUsed records a key's use, at most once a minute per key.
	MarkKeyUsed(ctx context.Context, id string, at time.Time) (bool, error)
	// AppendAudit writes audit entries, all or none.
	AppendAudit(ctx context.Context, entries ...master.AuditEntry) error
	// CreateKey adds a key with its audit entry.
	CreateKey(ctx context.Context, k master.Key, audit master.AuditEntry) error
	// ListKeys returns a page of keys.
	ListKeys(ctx context.Context, page master.Page) ([]master.Key, string, error)
	// RevokeKey revokes a key with its audit entry and returns its revoke
	// time.
	RevokeKey(ctx context.Context, id string, at time.Time, audit master.AuditEntry) (time.Time, error)
	// SetGrant sets a key's role on a scope with its audit entry and returns
	// the role it replaced.
	SetGrant(ctx context.Context, g master.Grant, audit master.AuditEntry) (master.Role, error)
	// RemoveGrant removes a key's grant on a scope with its audit entry and
	// returns the role it had.
	RemoveGrant(ctx context.Context, keyID string, scope master.Scope, audit master.AuditEntry) (master.Role, error)
}
