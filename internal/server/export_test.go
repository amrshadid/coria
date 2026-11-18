package server

import (
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	"github.com/amrshadid/magnetar/internal/access"
)

// HandleUnchecked registers h on the server's mux without the request
// path: the route the route test must catch. Only tests can do this.
func (s *Server) HandleUnchecked(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// MountUnchecked mounts a second connect.Server built without the
// interceptor: the other way a route could skip the check.
func (s *Server) MountUnchecked(register func(*connect.Server)) {
	rpc := connect.NewServer()
	register(rpc)
	connecthttp.Mount(s.mux, rpc)
}

// SetSocketUser replaces the user a local socket peer must be, so a test
// run by one user can show that a peer who is not that user is refused.
func (s *Server) SetSocketUser(u string) { s.socketUser = u }

// WithRule adds a rule for procedure to the package's table until the
// returned function is called. Tests that use it do not run in parallel.
func WithRule(procedure string, need access.Requirement) (restore func()) {
	old, had := rules[procedure]
	rules[procedure] = on(need)
	return func() {
		if had {
			rules[procedure] = old
		} else {
			delete(rules, procedure)
		}
	}
}

// WithoutRule removes procedure from the package's table until the returned
// function is called.
func WithoutRule(procedure string) (restore func()) {
	old, had := rules[procedure]
	delete(rules, procedure)
	return func() {
		if had {
			rules[procedure] = old
		}
	}
}

// RuleProcedures returns the procedures of the package's table.
func RuleProcedures() []string {
	out := make([]string, 0, len(rules))
	for p := range rules {
		out = append(out, p)
	}
	return out
}

// RuleFor returns what the table says procedure needs for the request msg
// (nil for the empty request): whether it is public, and the requirement.
func RuleFor(procedure string, msg proto.Message) (public bool, req access.Requirement, ok bool) {
	r, ok := rules[procedure]
	if !ok {
		return false, access.Requirement{}, false
	}
	if r.public {
		return true, access.Requirement{}, true
	}
	if !r.readsMessage() {
		return false, r.static, true
	}
	if msg == nil {
		msg = r.newMsg()
	}
	return false, r.need(msg), true
}

// ReadsMessage reports whether procedure's rule reads the request's message
// before the check.
func ReadsMessage(procedure string) bool { return rules[procedure].readsMessage() }
