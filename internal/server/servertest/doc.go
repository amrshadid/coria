// Package servertest holds stand-in handlers of the API's services, so the
// server can be tested, by its own tests and by outside clients (grpcurl,
// Python grpcio, curl; scripts/check-server.sh), before the real handlers
// exist. The stand-ins hold no business logic of their own: the few methods
// that do something (CreateKey, SetGrant, ListKeys, WhoAmI) hand the
// request's Call to the access package, as a real handler will, and the
// rest answer UNIMPLEMENTED after the server's check has let them run.
//
// ListTenants is the server tests' tool: its page_token chooses a
// behavior, "panic" (the handler panics), "wait" (it holds the request for
// Standins.Delay), "block" (it waits for the request's deadline), "huge"
// (it answers more than the 4 MiB limit); any other token answers an empty
// page. CreateTenant echoes the tenant it is given, so the secret field of
// its request reaches the log path.
//
// What it must never do: be linked into the magnetar binary (only tests and
// the stand-in program in standin/ import it), or hold logic a real handler
// would need.
package servertest
