package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/server/redact"
)

// errNoSuchMethod answers a procedure the table does not know, after the
// check: a method nobody registered, or one registered on the connect.Server
// after New, which the table never saw and so must never be served.
var errNoSuchMethod = connect.NewError(connect.CodeUnimplemented, "no such method")

// interceptor is the Connect server interceptor: every RPC the server's
// connect.Server dispatches, unary and streaming, registered method or
// unknown one, goes through it and so through pass. It is installed when
// the connect.Server is made (buildRoutes), so a method registered on it
// cannot skip it. A procedure the table does not know is checked as any
// authenticated caller's and then answered UNIMPLEMENTED: its handler, if
// one was registered late, is never called.
func (s *Server) interceptor(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		info, _ := connect.CallInfoForServerContext(ctx)
		x := exchange{method: spec.Procedure, metric: spec.Procedure}
		if info != nil {
			x.authorization = info.RequestHeader().Get("Authorization")
			x.remote = remoteOf(ctx, info.PeerAddr)
		}
		handler := func(ctx context.Context, st connect.ServerStream) error { return next(ctx, spec, st) }
		if r, known := s.ruleFor(spec.Procedure); known {
			x.rule = r
		} else {
			// The caller must still be authenticated before it is told the
			// method does not exist. The procedure comes from the request, so
			// it is cut for the log and the audit entry, and counted under one
			// label.
			x.rule = on(access.Authenticated())
			x.method = cut(spec.Procedure, maxLoggedPath)
			x.metric = "unknown"
			handler = func(context.Context, connect.ServerStream) error { return errNoSuchMethod }
		}
		replay := &replayStream{ServerStream: stream, s: s, ctx: ctx, method: x.method}
		if info != nil {
			replay.codec = info.Codec
		}
		x.readMessage = func() (proto.Message, error) {
			m := x.rule.newMsg()
			if err := stream.Receive(m); err != nil {
				return nil, err
			}
			// Kept for the handler, and not logged: nothing from a request
			// is logged before the check has allowed it.
			replay.peeked = m
			return m, nil
		}
		err := s.pass(ctx, x, func(ctx context.Context) error {
			replay.ctx = ctx
			return handler(ctx, replay)
		})
		if err == nil {
			return nil
		}
		ce := s.answer(err)
		if ce.Code() == connect.CodeUnauthenticated && info != nil {
			// RFC 9110 15.5.2: a 401 names the scheme the caller should use.
			info.ResponseHeader().Set("Www-Authenticate", "Bearer")
		}
		return ce
	}
}

// replayStream hands the handler the message the check already read, as
// its first Receive, so a message is read from the wire once; it logs each
// message at debug level, redacted, once the check has allowed the request;
// and it refuses to send an answer over the size limit in the request's
// encoding.
type replayStream struct {
	connect.ServerStream
	s      *Server
	ctx    context.Context
	method string
	codec  string
	peeked proto.Message
}

// Receive returns the message the check read, once, then reads from the
// stream.
func (r *replayStream) Receive(msg any) error {
	if r.peeked != nil {
		dst, ok := msg.(proto.Message)
		if !ok {
			return connect.NewError(connect.CodeInternal, "internal error")
		}
		proto.Reset(dst)
		proto.Merge(dst, r.peeked)
		r.peeked = nil
		r.logMessage("message received", dst)
		return nil
	}
	if err := r.ServerStream.Receive(msg); err != nil {
		return err
	}
	r.logMessage("message received", msg)
	return nil
}

// jsonCodec is the name of the RPC library's JSON codec.
const jsonCodec = "json"

// Send logs the message, redacted, and sends it, unless its encoding is
// over the limit. The RPC library's own send limit counts the compressed
// bytes, so a large answer that compresses well would pass it and then be
// refused by a stock gRPC client, whose limit counts the bytes after
// decompression. The size is the encoding's: the binary size for protobuf,
// and for JSON the JSON form's, which can be several times larger (a byte of
// a string may be written as six); the JSON form is measured only when the
// binary size is within that factor of the limit.
func (r *replayStream) Send(msg any) error {
	if m, ok := msg.(proto.Message); ok {
		size := proto.Size(m)
		if r.codec == jsonCodec && size > MaxMessageBytes/8 {
			if b, err := protojson.Marshal(m); err == nil {
				size = len(b)
			}
		}
		if size > MaxMessageBytes {
			return connect.NewError(connect.CodeResourceExhausted, "the answer is larger than the 4 MiB limit")
		}
	}
	r.logMessage("message sent", msg)
	return r.ServerStream.Send(msg)
}

// logMessage writes a message at debug level, through the redactor. It is
// the only place the server logs a request's or a response's content, and
// it runs only once the check has allowed the request.
func (r *replayStream) logMessage(what string, msg any) {
	m, ok := msg.(proto.Message)
	if !ok || !r.s.log.Enabled(r.ctx, slog.LevelDebug) {
		return
	}
	id, _ := requestIDFrom(r.ctx)
	r.s.log.LogAttrs(r.ctx, slog.LevelDebug, what,
		slog.String("request_id", id),
		slog.String("method", r.method),
		slog.Any("message", redact.LogValue(m)))
}

// HTTPHandler serves a plain HTTP route once the request path has checked
// it. The Call is in ctx. An error it returns is answered by the server in
// the same JSON form as a Connect error, with the matching HTTP status; it
// returns nil when it wrote its own answer.
type HTTPHandler func(ctx context.Context, w http.ResponseWriter, r *http.Request) error

// HTTPRoute is a plain HTTP route: a pattern of http.ServeMux, the
// requirement a caller must meet, and the handler.
type HTTPRoute struct {
	// Pattern is an http.ServeMux pattern, such as "GET /metrics".
	Pattern string
	// Need is what the caller must hold. A plain HTTP route is never
	// public: the zero Requirement, which nobody meets, refuses everyone.
	Need access.Requirement
	// Handler serves the request.
	Handler HTTPHandler
}

// middleware wraps a plain HTTP route in the request path.
func (s *Server) middleware(route HTTPRoute) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x := exchange{
			method:        route.Pattern,
			metric:        route.Pattern,
			authorization: r.Header.Get("Authorization"),
			remote:        remoteOf(r.Context(), r.RemoteAddr),
			rule:          on(route.Need),
		}
		err := s.pass(r.Context(), x, func(ctx context.Context) error {
			return route.Handler(ctx, w, r.WithContext(ctx))
		})
		if err != nil {
			// In the request's own protocol: the catch-all route also meets
			// gRPC clients asking for a service that is not here (a
			// reflection request, say), which cannot read a JSON body.
			s.writeAnyError(w, r, s.answer(err))
		}
	})
}

// httpStatus is the HTTP status of each API code, as the Connect protocol
// gives it for a unary call, so a plain HTTP route and an RPC answer the
// same refusal with the same status.
var httpStatus = map[connect.Code]int{
	connect.CodeCanceled:           499,
	connect.CodeUnknown:            http.StatusInternalServerError,
	connect.CodeInvalidArgument:    http.StatusBadRequest,
	connect.CodeDeadlineExceeded:   http.StatusGatewayTimeout,
	connect.CodeNotFound:           http.StatusNotFound,
	connect.CodeAlreadyExists:      http.StatusConflict,
	connect.CodePermissionDenied:   http.StatusForbidden,
	connect.CodeResourceExhausted:  http.StatusTooManyRequests,
	connect.CodeFailedPrecondition: http.StatusBadRequest,
	connect.CodeAborted:            http.StatusConflict,
	connect.CodeOutOfRange:         http.StatusBadRequest,
	connect.CodeUnimplemented:      http.StatusNotImplemented,
	connect.CodeInternal:           http.StatusInternalServerError,
	connect.CodeUnavailable:        http.StatusServiceUnavailable,
	connect.CodeDataLoss:           http.StatusInternalServerError,
	connect.CodeUnauthenticated:    http.StatusUnauthorized,
}

// writeHTTPError writes a Connect-shaped error, {"code": ..., "message":
// ...}, with its HTTP status.
func (s *Server) writeHTTPError(w http.ResponseWriter, ce *connect.Error) {
	status, ok := httpStatus[ce.Code()]
	if !ok {
		status = http.StatusInternalServerError
	}
	body, err := json.Marshal(struct {
		Code    string `json:"code"`
		Message string `json:"message,omitempty"`
	}{ce.Code().String(), ce.Message()})
	if err != nil {
		body = []byte(`{"code":"internal"}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("X-Content-Type-Options", "nosniff")
	if status == http.StatusUnauthorized {
		h.Set("Www-Authenticate", "Bearer")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// errorWriter writes an error in whichever protocol a request speaks
// (Connect, gRPC, gRPC-Web), for answers the envelope gives before the
// request reaches its route: a server that is stopping, a panic outside a
// handler.
var errorWriter = connecthttp.NewErrorWriter()

// writeAnyError answers r with ce in its own protocol, or as JSON for a
// request that is not an RPC. A 401 names the Bearer scheme (RFC 9110
// 15.5.2) in either form.
func (s *Server) writeAnyError(w http.ResponseWriter, r *http.Request, ce *connect.Error) {
	if ce.Code() == connect.CodeUnauthenticated {
		w.Header().Set("Www-Authenticate", "Bearer")
	}
	if errorWriter.IsSupported(r) {
		if err := errorWriter.Write(w, r, ce); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Debug("an error answer could not be written", "error", err)
		}
		return
	}
	s.writeHTTPError(w, ce)
}

// remoteOf returns the caller's address for the log and the audit entry:
// empty on the local socket, where the "address" names no machine.
func remoteOf(ctx context.Context, addr string) string {
	if _, ok := localFrom(ctx); ok {
		return ""
	}
	return addr
}
