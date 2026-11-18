// Package master states what the installation's master database holds and
// what can be done with it: the tenants and how to reach each one's database,
// the API keys (as hashes), the grants that give a key a role, the
// installation's settings, and the installation's audit log.
//
// The master database is reached only through Store, an interface stated as
// operations ("create this tenant", "list this key's grants"), never as SQL.
// A database type is one driver package that implements Store and passes the
// contract suite in package mastertest; nothing outside the driver knows
// which database it is, so each database can do its best behind the same
// operations and another one is added without touching the rest.
//
// # Rules every driver keeps
//
// Every operation that changes the master takes the audit entry for that
// change and writes both or neither, so there is no way through this port to
// change the master without leaving a record. The one exception is
// MarkKeyUsed, which records that a key was used: the request that used it
// writes its own audit entry, and a second one for the same request would
// double the cost of every call.
//
// Ids and times are given by the caller, never made by the store, so a test
// knows exactly what it will read back. A time is kept in UTC to the
// microsecond: what is finer is dropped, and a time comes back in UTC
// whatever zone it went in with.
//
// Lists are paged by an opaque cursor and a limit, never by an offset: an
// item present before and after a listing is returned exactly once, whatever
// is inserted meanwhile.
//
// A failure is an error from package fault with the kind a caller acts on:
// Invalid for a value this package refuses, NotFound, Conflict, Unavailable
// when the database cannot be reached, Internal for the rest.
//
// # What this package must never do
//
// It must never import a database driver, database/sql or package sqlkit:
// the port must stay implementable by a database with no SQL at all, and a
// lint rule (.golangci.yml, depguard "master") holds it to that. It never
// seals, opens or hashes a secret: it keeps the sealed bytes and the hashes it
// is handed, and package secret makes them. Nothing it returns in an error
// carries a key's hash, a sealed value or a database address.
package master
