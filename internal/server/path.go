package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
)

// Call is what the request path tells a handler about the request it is
// serving. A handler reads it with CallFrom and passes Credential and Facts
// on to the access operations that act for the caller (access.CreateKey,
// access.WhoAmI, ...), which check again and write the audit entry of the
// change in its transaction.
type Call struct {
	// RequestID is the id the server made for the request, the one in the
	// Magnetar-Request-Id response header, the log and the audit entry.
	RequestID string
	// Method is the full procedure name, or the route of a plain HTTP
	// request.
	Method string
	// Credential is what the request presented.
	Credential access.Credential
	// Facts are the request's facts for an audit entry: its method, its
	// remote address and its request id.
	Facts access.Request
	// Decision is what the check allowed. It is the zero Decision for the
	// health service, which is not checked.
	Decision access.Decision
}

// callKey is the context key of a request's Call.
type callKey struct{}

// CallFrom returns the Call of the request ctx belongs to, and false outside
// a request the server is serving.
func CallFrom(ctx context.Context) (*Call, bool) {
	c, ok := ctx.Value(callKey{}).(*Call)
	return c, ok
}

// exchange is what the request path needs to know of one request, whatever
// carried it: an RPC through the Connect interceptor or a plain HTTP request
// through the middleware.
type exchange struct {
	// method is the procedure or the route; it chooses the rule.
	method string
	// metric is the method's name as a metric label: method itself when it
	// is a known route, "unknown" otherwise, so a caller cannot make series
	// without bound.
	metric string
	// authorization is the request's Authorization header.
	authorization string
	// remote is the caller's network address; empty on the local socket.
	remote string
	// rule says what the method needs.
	rule rule
	// readMessage reads the request's first message, for a rule that needs
	// it; nil for a plain HTTP request. The caller keeps the message and
	// hands it to the handler, so it is read once.
	readMessage func() (proto.Message, error)
}

// pass is the request path, the one function every request goes through:
// the Connect interceptor calls it for every RPC, unary and streaming, and
// the HTTP middleware for every plain HTTP route. In order:
//
//  1. the request id: the one the envelope made for this request (it is
//     already in the Magnetar-Request-Id response header), or a new one;
//  2. the credential: the Authorization bearer key, else the local operator
//     when the connection came through the local socket's peer check, else
//     none;
//  3. access.Check with the method's requirement, unless the method is the
//     public health service. For a requirement that depends on the request,
//     the first message is read first; nothing is read for any other;
//  4. a refusal is returned as it is: the access package's fixed message
//     and kind, which the caller turns into the API code;
//  5. the handler, with the Call in its context and any panic recovered.
//
// It logs one line for the request and counts it in the metrics, whatever
// the outcome. The error it returns is the handler's or the check's, not
// yet turned into an API code (see answer).
func (s *Server) pass(ctx context.Context, x exchange, next func(context.Context) error) (err error) {
	start := time.Now()
	id, ok := requestIDFrom(ctx)
	if !ok {
		id = newRequestID()
		ctx = withRequestID(ctx, id)
	}
	call := &Call{RequestID: id, Method: x.method}
	defer func() { s.finish(ctx, x, call, start, err) }()

	local, isLocal := localFrom(ctx)
	call.Credential = credential(x.authorization, local, isLocal)
	call.Facts = access.Request{Method: x.method, RemoteAddress: x.remote, RequestID: id}

	if !x.rule.public {
		req := x.rule.static
		if x.rule.readsMessage() && x.readMessage != nil {
			msg, rerr := x.readMessage()
			if rerr != nil {
				// The message could not be read. The caller is still checked
				// first, against the rule's fallback, so an unauthenticated or
				// unentitled caller is refused before it learns anything
				// about its message.
				if _, cerr := s.access.Check(ctx, call.Credential, x.rule.fallback(), call.Facts); cerr != nil {
					return cerr
				}
				return rerr
			}
			req = x.rule.need(msg)
		}
		d, cerr := s.access.Check(ctx, call.Credential, req, call.Facts)
		if cerr != nil {
			return cerr
		}
		call.Decision = d
	}

	ctx = context.WithValue(ctx, callKey{}, call)
	return s.run(ctx, call, next)
}

// run calls the handler and recovers a panic in it: the panic is logged
// with the request id and the stack, the caller is answered INTERNAL with no
// detail, and the server goes on serving.
func (s *Server) run(ctx context.Context, call *Call, next func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			if isAbort(p) {
				// net/http's own way to abort a response; it must reach
				// net/http, which closes the connection quietly.
				panic(p)
			}
			s.log.LogAttrs(ctx, slog.LevelError, "panic in a handler",
				slog.String("request_id", call.RequestID),
				slog.String("method", call.Method),
				slog.String("panic", fmt.Sprint(p)),
				slog.String("stack", string(debug.Stack())))
			err = errPanic
		}
	}()
	return next(ctx)
}

// errPanic is the error a recovered panic becomes: Internal, with the fixed
// message, so nothing of the panic reaches the caller.
var errPanic = fault.New(fault.Internal, "internal error")

// finish logs the request's line and counts it in the metrics.
func (s *Server) finish(ctx context.Context, x exchange, call *Call, start time.Time, err error) {
	took := time.Since(start)
	code := codeName(s.code(err))
	s.metrics.Observe(x.metric, code, took)

	attrs := []slog.Attr{
		slog.String("request_id", call.RequestID),
		slog.String("method", x.method),
		slog.String("code", code),
		slog.Float64("duration_ms", float64(took.Microseconds())/1000),
	}
	if x.remote != "" {
		attrs = append(attrs, slog.String("remote", x.remote))
	}
	p := call.Decision.Principal()
	switch {
	case p.IsLocalOperator():
		attrs = append(attrs, slog.String("caller", "local:"+p.LocalUser()))
	case p.KeyID() != "":
		attrs = append(attrs, slog.String("caller", "key:"+p.KeyID()))
	}
	level := slog.LevelInfo
	if err != nil {
		attrs = append(attrs, slog.String("kind", s.kindOf(err)))
		// The cause of a fault of the server goes to the log, never to the
		// caller. A fault's Error carries its cause, which never holds a
		// secret (package fault, and the access package's rule).
		switch c := s.code(err); c {
		case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss, connect.CodeUnavailable:
			level = slog.LevelError
			attrs = append(attrs, slog.String("error", err.Error()))
		}
	}
	s.log.LogAttrs(ctx, level, "request", attrs...)
}

// kindOf names an error's kind for the log line: the fault kind, or the API
// code of an error the RPC library made.
func (s *Server) kindOf(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code().String()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "context"
	}
	return fault.KindOf(err).String()
}

// code returns the API code the caller is answered with for err.
func (s *Server) code(err error) connect.Code {
	if err == nil {
		return 0
	}
	return s.answer(err).Code()
}

// codeName names a code as a log field and a metric label: "ok" for
// success.
func codeName(c connect.Code) string {
	if c == 0 {
		return "ok"
	}
	return c.String()
}

// answer turns an error from the request path into what the caller is sent.
// An error the RPC library made (a message over the limit, a malformed
// message, an unknown method) keeps its code and message. A deadline or a
// cancellation is DEADLINE_EXCEEDED or CANCELED. Any other error is mapped
// by its fault kind through Config.ToConnect, which shows the caller only
// the error's message for the caller (fault.Message) and never its cause.
func (s *Server) answer(err error) *connect.Error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return ce
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, "the request's deadline passed")
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, "the request was canceled")
	}
	return s.toConnect(err)
}

// credential reads what a request presents. A bearer key in the
// Authorization header is the credential wherever it comes from, so a key
// can be tried on the local socket too. Without one, a request that came
// through the local socket's peer check is the local operator; any other is
// a request with no key. A header that is not a bearer key is passed on as
// it is, which the check refuses as malformed: never an error of a
// different shape, so a caller learns nothing from the form of its header.
func credential(authorization string, local localPeer, isLocal bool) access.Credential {
	if authorization != "" {
		scheme, token, found := strings.Cut(strings.TrimSpace(authorization), " ")
		if found && strings.EqualFold(scheme, "Bearer") {
			if t := strings.TrimSpace(token); t != "" {
				return access.APIKey(t)
			}
		}
		return access.APIKey(authorization)
	}
	if isLocal {
		return access.LocalOperator(local.user)
	}
	return access.APIKey("")
}
