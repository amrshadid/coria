package server

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"connectrpc.com/connect/v2"
)

// RequestIDHeader is the response header that carries the request's id
// (access.proto, "The request id"). A request header of the same name is
// deleted unread: the client cannot choose the id.
const RequestIDHeader = "Magnetar-Request-Id"

// The headers by which a client gives its own deadline: gRPC and gRPC-Web
// send grpc-timeout, the Connect protocol connect-timeout-ms. The RPC
// library applies either; the envelope applies the default only when
// neither is there.
const (
	grpcTimeoutHeader    = "Grpc-Timeout"
	connectTimeoutHeader = "Connect-Timeout-Ms"
)

// isAbort reports whether a recovered panic is net/http's sentinel for
// aborting a response, which is let through every recover so net/http
// sees it and closes the connection quietly.
func isAbort(p any) bool {
	err, ok := p.(error)
	return ok && errors.Is(err, http.ErrAbortHandler)
}

// requestIDKey is the context key of a request's id.
type requestIDKey struct{}

// withRequestID returns ctx carrying a request id.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestIDFrom returns the request id ctx carries.
func requestIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

// idEncoding writes a request id in lowercase base32 without padding: only
// letters and digits, so it is safe in a header, a log and an audit entry.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newRequestID returns a new request id: 80 random bits, 16 characters. The
// contract asks for uniqueness, not secrecy; at 80 bits two ids collide
// once in about 2^40 requests, beyond what an installation serves.
func newRequestID() string {
	var b [10]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go 1.24 and later).
	return idEncoding.EncodeToString(b[:])
}

// NewID returns a new id for an audit entry, of the form access.Config's
// NewID needs: 26 characters of a-z and 2-7, 128 random bits, so ids made by
// any number of servers on one master do not collide.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return idEncoding.EncodeToString(b[:])
}

// envelope is the outermost handler of every listener: it runs for every
// request before any route, so what it does holds for answers the RPC
// library gives before the request path runs (an unsupported content type,
// a malformed timeout) as much as for any route. In order:
//
//   - it deletes a client's own Magnetar-Request-Id, makes the request's id
//     and sets it on the response, so every answer carries it, success,
//     refusal, error or panic alike;
//   - it refuses new work once the server is stopping, with UNAVAILABLE;
//   - it counts the request in flight, for the drain on stop;
//   - it applies the default deadline when the client sent none;
//   - it recovers a panic that escaped the request path (a handler's panic
//     is recovered inside it, in pass), logs it with the id and a stack, and
//     answers INTERNAL.
func (s *Server) envelope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(RequestIDHeader)
		id := newRequestID()
		w.Header().Set(RequestIDHeader, id)

		if !s.admit() {
			s.writeAnyError(w, r, connect.NewError(connect.CodeUnavailable, "the server is stopping"))
			return
		}
		defer s.inFlight.Done()
		end := s.metrics.Begin()
		defer end()

		ctx := withRequestID(r.Context(), id)
		if r.Header.Get(grpcTimeoutHeader) == "" && r.Header.Get(connectTimeoutHeader) == "" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, s.deadline)
			defer cancel()
		}
		defer func() {
			if p := recover(); p != nil {
				if isAbort(p) {
					panic(p)
				}
				s.log.LogAttrs(ctx, slog.LevelError, "panic outside a handler",
					slog.String("request_id", id),
					slog.String("path", r.URL.Path),
					slog.String("panic", fmt.Sprint(p)),
					slog.String("stack", string(debug.Stack())))
				s.writeAnyError(w, r, connect.NewError(connect.CodeInternal, "internal error"))
			}
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// admit counts a request in flight unless the server is stopping. The check
// and the count are under one lock with Stop's switch, so no request is
// admitted after Stop has begun waiting for those in flight.
func (s *Server) admit() bool {
	s.stopMu.RLock()
	defer s.stopMu.RUnlock()
	if s.stopping {
		return false
	}
	s.inFlight.Add(1)
	return true
}
