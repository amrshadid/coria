// Package registry holds one open store per tenant and is the only way the
// rest of the server gets a tenant's database (M0 design note, part 2). It
// opens each tenant's database from the address sealed in the master,
// upgrades it, watches it, and records the tenant's state in the master, so
// that one tenant's trouble stays its own.
//
// # Getting a store
//
// Store(name) is on the path of every request into a tenant. It reads an
// immutable map behind an atomic pointer and the tenant's current view
// behind another: no lock, no allocation, and no call to the master. A
// tenant whose view is not ready is refused with kind Unavailable; the
// others are served. A caller uses the store for one request and does not
// keep it, which leaves the registry free to replace the store behind a name
// (and, later, to close an idle one and reopen it on demand) without its
// callers changing.
//
// # States
//
// A tenant is upgrading while its database is opened and migrated, ready
// when it answers at this binary's schema version, unreachable when it does
// not answer (it is retried, and returns to ready on its own), and failed
// when it cannot be served until a person acts: its address does not open
// with this key file, its database type is unknown, its file is one others
// can read or write (the driver refuses it, and the reason says how to fix
// it), or its schema cannot be brought to this binary's version. A failed
// tenant is retried only when the registry starts again.
//
// The state a request sees changes at once. The state written to the master,
// with an audit entry whose actor is the system, follows it, but a change
// between ready and unreachable is written at most once a minute per tenant,
// so a flapping database cannot fill the master's audit log. A reason never
// holds the database's address, host, user or password: the text a driver
// gives is stripped of each of them before it is kept, logged or returned.
//
// The master records upgrading only for a tenant being created (its row is
// written that way first). An upgrade at start shows as upgrading in the
// registry's view but is not written: in M0 a migration takes milliseconds,
// and writing it would cost two master writes per tenant per start.
//
// # Opening, in parallel and bounded
//
// Every open runs under the tenant's own deadline, and at most MaxOpening
// run at once. A driver that does not return by its deadline is left to
// finish in a goroutine the registry still owns: the tenant is unreachable
// meanwhile, and a store it returns late is closed. So one dead database
// delays neither the start past its deadline nor the other tenants.
//
// # Creating and removing
//
// Create writes the tenant's row as upgrading, then opens and migrates its
// database, then records ready. A crash between leaves an upgrading row,
// which the next start opens like any other, and a repeated Create with the
// same id and name finishes what the first began. Remove unregisters the
// tenant and leaves its data where it is (D36), and says where: the database
// type and a location with no user and no password.
//
// # What this package must never do
//
// It must never start a goroutine that nothing waits for: Close stops the
// timer, waits for every open in flight, and closes every store. It must
// never import a database driver (a lint rule holds it): drivers are given
// to it, which is also how its tests run a database that can be made to
// fail, hang and recover. It must never put a database address or password
// in a state, an error, a log line or an audit entry. It never reads the
// time of day but through Config.Now, and its interval and deadlines are
// given in Config, never package variables, so a test moves time instead of
// sleeping.
package registry
