// Package tenant states what one tenant's own database holds and what can be
// done with it. For now that is the tenant's overrides of the installation's
// settings and the tenant's audit log; the catalog, receptions, events and
// jobs are added to this port with the code that uses them.
//
// Each tenant has its own database, reached only through Store, an interface
// stated as operations, never as SQL. A database type is one driver package
// that implements Store and passes the contract suite in package tenanttest;
// nothing outside the driver knows which database it is, so each database can
// do its best behind the same operations. Package registry opens, upgrades
// and watches each tenant's store and is the only way the rest of the server
// gets one.
//
// # Rules every driver keeps
//
// They are the master port's rules (package master), and the values they
// apply to are the master's own types: a setting has the master's name and
// value bounds, and an audit entry is a master.AuditEntry listed with a
// master.AuditFilter and paged with a master.Page. One type for both logs
// means an entry reads the same wherever it was written, and a bound changed
// in one place changes in both.
//
// Every operation that changes the tenant's database takes the audit entry
// for that change and writes both or neither, so no change goes unrecorded.
// Ids and times are given by the caller. Lists are paged by an opaque cursor
// bound to its list, its filter and its tenant, never by an offset. A failure
// is an error from package fault with the kind a caller acts on: Invalid,
// NotFound, Unavailable when the database cannot be reached or the store is
// closed, Internal for the rest, a duplicate audit id included
// (master.ErrDuplicateAuditID). Store's documentation says each rule.
//
// # What this package must never do
//
// It must never import a database driver, database/sql or package sqlkit: the
// port must stay implementable by a database with no SQL at all, and a lint
// rule (.golangci.yml, depguard "tenant") holds it to that. It holds no
// access rules and no secrets: who may enter a tenant lives only in the
// master, so revoking is one write in one place, a tenant's database restored
// from a backup cannot bring access back, and it can be copied or handed over
// without carrying them.
package tenant
