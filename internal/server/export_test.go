package server

import (
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
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
