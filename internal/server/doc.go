// Package server is the process that serves the API: it listens, puts every
// request through one checked path, hides secrets in what it logs, bounds
// what a request may cost, and stops cleanly. It holds no business logic:
// the handlers of the API's services are given to it (Config.Register).
//
// # The request path
//
// One function, pass, is the request path. The Connect interceptor calls it
// for every RPC, unary and streaming, and the HTTP middleware for every
// plain HTTP route; nothing reaches a handler any other way. In order: the
// request id; the credential (the Authorization bearer key, or the local
// operator); access.Check with the method's requirement; a refusal answered
// with the access package's fixed message and code; then the handler, with
// a Call in its context (CallFrom) and any panic recovered and answered
// INTERNAL. The order of checks is the contract's: unauthenticated first,
// then the role, then the handler validates the request.
//
// The requirement of each method is in one table keyed by the full
// procedure name (rules.go), taken from the "Role:" line of the method in
// api/proto/magnetar/v1; where the requirement depends on the request (a
// role on the tenant the request names) the table holds a function of the
// request message, and only then is the message read before the check. A
// test walks the generated descriptors and the proto files in both
// directions. New refuses a method registered without an entry.
//
// There is one way to add a route: the one connect.Server the server builds
// with its interceptor (Config.Register is handed it), and Config.HTTPRoutes,
// each wrapped in the middleware. A method nobody registered and a path no
// route serves still pass the request path, so an unauthenticated caller
// is refused before it is told there is nothing there. The route test sends
// an unauthenticated request to every pattern registered and fails on any
// that answers without refusing.
//
// # The envelope
//
// Around everything, for every request, the envelope deletes a client's own
// Magnetar-Request-Id header, makes the request's id and sets it on the
// response (so every answer carries it: success, refusal, error, panic, and
// the RPC library's own errors); refuses new requests once Stop has begun;
// counts requests in flight for the drain; and applies the 30-second
// default deadline when the client sent none.
//
// # Listeners
//
// The local socket, <data-dir>/run/magnetar.sock, speaks HTTP/2 without TLS
// (h2c) and HTTP/1.1. Its directory is private to the server's user, and on
// Linux and macOS each connection's peer user is read from the kernel
// (SO_PEERCRED, LOCAL_PEERCRED) and must be the server's own effective
// user, or the connection is closed before a byte is read. That caller is
// the local operator, admin everywhere. A socket file left by a process
// that is gone is removed only after a dial shows nothing answers on it.
//
// On Windows the peer's user is not read: Windows offers no peer
// credentials for AF_UNIX sockets, only the peer's process id
// (SIO_AF_UNIX_GETPEERPID), which can be reused by another process before it
// is looked up and so does not reliably name the caller. The guard on
// Windows is the directory alone: <data-dir>\run is given, at every start, a
// protected access list that grants the server's user and nobody else, and
// the socket inherits it. Any process of that user can use the socket.
//
// The network listener exists only when Config.Listen is given. With a
// certificate it speaks TLS 1.2 or later with HTTP/2 and HTTP/1.1. Without
// one, only a loopback address written as an address is accepted, and it
// speaks h2c and HTTP/1.1.
//
// # Limits
//
// A message over 4 MiB is refused, read or sent (RESOURCE_EXHAUSTED); a
// call without a deadline gets 30 seconds; headers are bounded to 64 KiB,
// HTTP/2 streams to 100 per connection, the time to send headers to 10
// seconds, an idle connection to 2 minutes. Each number has its reason at
// its constant in server.go.
//
// # One server per data directory
//
// TakeLock takes an operating system file lock (flock on Linux and macOS,
// an exclusive open on Windows) on <data-dir>/run/serve.lock and refuses a
// second server, naming the first one's process. The system drops the lock
// when the process ends however it ends, so a kill with signal 9 leaves
// nothing stale.
//
// # Stopping
//
// Stop refuses new requests, lets those in flight finish within the drain
// deadline (then cancels them), and closes in order: the access package
// (which writes the audit entries it holds), the tenant registry, the
// master database, and the lock.
//
// # What it must never do
//
// It must never serve a route outside the request path, log anything
// derived from a request or a response that has not passed through package
// redact, log the Authorization header at any level, show a caller the
// cause of an internal fault, or leave a goroutine running after Stop
// returns.
package server
