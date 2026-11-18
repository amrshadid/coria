package tenant

import (
	"context"

	"github.com/amrshadid/magnetar/internal/master"
)

// Store is one tenant's database, stated as operations. A driver package
// implements it for one database type and proves it with the contract suite
// in package tenanttest.
//
// Every method takes a context and fails with an error from package fault.
// Every method that changes the database takes the audit entry for that
// change and writes both or neither: if the entry cannot be written (its id
// is taken, the database fails), the change is not made, and if the change
// cannot be made, the entry is not written. A refusal or a failure that
// changes nothing is recorded with AppendAudit.
//
// A value is validated with the master package's rules before anything is
// written, and refused as Invalid. A list's limit outside 1 to 1000, or a
// cursor this store did not make for that list, is Invalid.
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
	// Close releases the database. Calls made after it fail as
	// Unavailable; a second Close does nothing.
	Close() error

	// Setting returns the tenant's override of a setting, or NotFound when
	// the tenant has none and the installation's value applies.
	Setting(ctx context.Context, name string) (string, error)
	// SetSetting sets the tenant's override of a setting, adding it or
	// replacing it. The name and value are bounded as the master's are
	// (master.ValidateSettingName, master.ValidateSettingValue).
	SetSetting(ctx context.Context, name, value string, audit master.AuditEntry) error
	// UnsetSetting removes the tenant's override and reports whether there
	// was one: removing one that is not set changes nothing and succeeds, as
	// the API contract's UnsetSetting does. The audit entry is written
	// either way.
	UnsetSetting(ctx context.Context, name string, audit master.AuditEntry) (bool, error)
	// ListSettings returns a page of the tenant's overrides in order of
	// name, and the cursor of the next page, empty after the last.
	ListSettings(ctx context.Context, page master.Page) ([]master.Setting, string, error)

	// AppendAudit writes up to master.MaxAuditBatch entries to the tenant's
	// audit log, all or none. An id already taken is Conflict. No entries
	// is no write.
	AppendAudit(ctx context.Context, entries ...master.AuditEntry) error
	// ListAudit returns a page of the entries the filter keeps, newest
	// first: by time, then by id, both descending.
	ListAudit(ctx context.Context, filter master.AuditFilter, page master.Page) ([]master.AuditEntry, string, error)
}
