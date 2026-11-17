// Package access decides who is calling and what they may do, records each
// request in the audit log, and manages API keys and their grants (M0 design
// note, parts 4 to 6).
//
// It knows nothing about gRPC, HTTP or the command line. A server hands it a
// Credential (a presented key, or the local operator the socket reported), a
// Requirement (only to be authenticated, a role on the installation, a role
// on a tenant named by the request, or the right to read a tenant's
// registration) and the facts of the request it needs for the audit entry,
// and gets back a Decision or an error from package fault.
//
// # The one way in
//
// Access.Check is the only function that makes a Principal. Principal and
// Decision have no exported fields, so no other package can build one that
// carries rights; their zero values carry none. Every operation of this
// package that acts for a caller (making and revoking keys, setting grants,
// WhoAmI) calls Check itself, so a handler cannot forget it.
//
// The checks run in the order the API contract states (access.proto, "The
// order of checks"): the credential first, answered Unauthenticated when it
// is no good for any reason; then the role, answered Denied, or NotFound for
// a tenant that does not exist when the caller may list every tenant (D37).
// Validating the request itself comes after, in the handler, or in this
// package for the operations it implements.
//
// # Refusals
//
// Every way a credential can be no good (none, malformed, unknown id, wrong
// secret, revoked, expired) is one kind and one fixed message. A good key
// with no sufficient grant and a good key naming a tenant that does not exist
// are one kind and one fixed message too, and both make the same calls to
// the store in the same order (Key, TenantByName, RolesFor), so neither the
// answer nor the work done tells a caller which tenants exist. The audit
// entry keeps the real cause in its reason; the caller never sees it.
//
// # What is audited, and when
//
// A change is never unaudited. Its entry is written in the same transaction
// as the change, through the master port: if the entry cannot be written,
// the change is not made.
//
// A refusal or a read is audited unless the server is overwhelmed or is
// killed within the flush interval, and both cases are counted or bounded.
// Its entry does not cost a synced commit on the request's path. It is
// handed to the audit writer, one goroutine of this package with a bounded
// queue, which appends entries in batches: when a batch is full
// (Config.AuditBatch), or when Config.AuditInterval passes. A batch the
// master refuses is appended again one entry at a time, so one bad entry
// loses only itself. In detail:
//
//   - Overwhelmed: when the queue is full, the entry is dropped and
//     AuditStats.Dropped counts it. The request is neither failed nor
//     delayed. The first drop after a minute without a notice is reported at
//     the next batch by one entry of method "access.AuditWriter" with the
//     number of entries dropped since the last notice, so every drop reaches
//     the log, at most a minute late, and a flood costs one entry a minute.
//   - Killed: the entries of at most one interval (and the queue behind
//     them) are lost. That is the window a crash can cost.
//   - Closed: Close writes what the writer holds until its deadline; what is
//     left then is dropped and counted.
//   - Refused by the master: AuditStats.Failed counts it and the log names
//     the entry and its cause.
//
// The reason for the split: on SQLite a synced commit costs milliseconds,
// so one per refusal would cap the whole server at a few hundred requests a
// second, and a flood of bad keys would use up the master's one writer for
// everyone. Changes keep the strict rule, since a change nobody can see in
// the log is the failure that matters.
//
// # What it must never do
//
// It must never cache a key or a grant: every check reads both, so a revoked
// key is refused on its very next request. It must never put a key's secret
// in an audit entry, an error, a log line or anything it returns, except the
// one secret.NewAPIKey that CreateKey returns, which prints only its id. It
// must never read the wall clock for an entry's time or an expiry, nor make
// ids itself: the clock and the id source are given to New. It must never
// let recording a key's last use or a refusal's entry fail or delay a
// request: both go to bounded workers that drop when they fall behind, and
// a key marked used within the minute is not marked again. It must never
// leave a goroutine running after Close returns.
package access
