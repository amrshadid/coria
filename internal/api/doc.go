// Package api is the handlers of the four services of the API contract
// (api/proto/magnetar/v1): TenantService, AccessService, SettingsService and
// AuditService. Each method turns a request into calls on the package that
// owns the change, and their answer into a response. The proto comments are
// the specification; this package is what makes each sentence in them true.
//
// # What the server does first
//
// The server makes one access check per request (access.Check) before a
// handler runs, with the requirement and the audit facts Methods gives for
// the request, and hands the Decision to the handler through the context
// (WithDecision). So the server answers UNAUTHENTICATED and the role, in the
// order the contract states; a handler then validates the request (sizes,
// names, exactly-one-of rules, page size and token) and answers
// INVALID_ARGUMENT naming the field, and only then acts.
//
// A request whose scope or tenant name is malformed cannot name a tenant to
// check a role on, so Methods asks for the same role on the installation
// instead: a caller with no right there is refused as for any tenant it holds
// no grant on, and one with that right is told the request is invalid. So a
// caller who may not call a method learns nothing about what a valid request
// looks like, and INVALID_ARGUMENT can still be answered.
//
// # Changes and reads
//
// A change goes through the package that owns it, with the request's audit
// entry (access.Decision.Entry) written in the same transaction: keys and
// grants through package access, tenants through package registry, the
// installation's settings through the master, and a tenant's settings through
// the tenant's store, which only the registry hands out. A read answers, then
// hands its entry to the access package's audit writer, which never blocks or
// fails: access.Record for the installation's log, or access.RecordTo with
// the tenant's store when the scope asked for is a tenant (the contract: "one
// entry in the log of the scope asked for"). An entry the tenant's log does
// not take is written to the installation's log, marked
// access.DetailRerouted. A request that did not complete is recorded with
// outcome failed and its real cause, in the tenant's log when its store was
// already in hand and in the installation's log otherwise; a creation of a
// tenant is the exception: the registry writes its entry itself, with the
// change that settles the creation, and says whether it did; a creation it
// refused before writing anything is recorded by the handler. A method whose
// requirement is fixed learns what its request names only after the check,
// and adds it to its entries as access.Facts.
//
// # The key at the change
//
// Every change carries the caller's key as its actor, with the role the
// method needs on its scope (access.Decision.Actor): into the master's change
// for keys, grants, tenants and the installation's settings, which checks it
// in its own transaction; and for a tenant's settings, as a tenant.Guard that
// asks the master (master.Store.CheckActor), which the tenant's store runs
// last in its transaction. So a key revoked, expired or stripped of its grant
// after the check makes no change. Such a change is answered as the check
// answers such a key, UNAUTHENTICATED or PERMISSION_DENIED with the same
// message, and recorded as refused in the installation's log, never in the
// tenant's (access.RefusedAtChange). The window a tenant's change leaves is
// package tenant's to state.
//
// # Errors
//
// A handler answers a *connect.Error, made from the error's fault kind
// through package codes. The server passes such an error through unchanged.
// An internal fault's cause is logged with the request's id and is never part
// of the answer, whose message is "internal error".
//
// # What this package must never do
//
// It must never read a credential or call access.Check: a handler that finds
// no Decision in its context answers INTERNAL and logs it, and never decides
// by itself. It must never undo the access rule for a tenant that does not
// exist: a later lookup that misses answers PERMISSION_DENIED, as for no
// grant, to a caller who may not list every tenant. It must never put a
// tenant's database address in an answer, an audit entry, an error or a log
// line, nor log anything a request or a response carries. It must never seal
// or open a secret itself: the registry seals a tenant's address when it
// writes the tenant's row. It must never start a goroutine.
package api
