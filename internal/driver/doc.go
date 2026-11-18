// Package driver opens a database from its address, choosing the database
// type by the address's scheme: "sqlite:<path>" or "postgres://...". It is
// the one place that knows the list of database types, so the rest of the
// binary asks for a master.Store and never names one (decision D12).
//
// # What this package must never do
//
// It must never hold the logic of a database type: each type is a package
// below this one, and this package only picks it. It never repeats an
// address in an error, since an address may hold a password.
package driver
