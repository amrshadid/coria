package server_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/server"
)

// uncheckedRoutes sends a request without a key to every pattern the
// server has registered, over the network listener, and returns each
// pattern that answered with anything but a refusal (401). The health
// service is the one pattern meant to answer without a key.
func uncheckedRoutes(t *testing.T, e *env) []string {
	t.Helper()
	hc := e.tlsClient(false)
	var bad []string
	for _, pattern := range e.srv.Routes() {
		method, path := "POST", pattern
		if m, p, ok := strings.Cut(pattern, " "); ok {
			method, path = m, p
		}
		if path == server.HealthCheckProcedure || path == server.HealthWatchProcedure {
			continue
		}
		if strings.HasSuffix(path, "/") {
			// A subtree: probe a path inside it.
			path += "probe"
		}
		req, _ := http.NewRequest(method, e.netURL+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get(server.RequestIDHeader) == "" {
			bad = append(bad, pattern)
		}
	}
	return bad
}

// TestEveryRouteIsChecked guards that no route skips the request path:
// every pattern registered refuses a request without a key, with a request
// id. Then it registers a route outside the request path in each of the
// two ways one could be, and shows the same check catching each.
func TestEveryRouteIsChecked(t *testing.T) {
	e := newEnv(t, options{})
	routes := e.srv.Routes()
	if len(routes) < 20 {
		t.Fatalf("only %d routes registered: %v", len(routes), routes)
	}
	if bad := uncheckedRoutes(t, e); len(bad) > 0 {
		t.Fatalf("routes answered a request without a key: %v", bad)
	}

	t.Run("a plain route outside the path is caught", func(t *testing.T) {
		e := newEnv(t, options{})
		e.srv.HandleUnchecked("GET /debug/open", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "open to all")
		}))
		if bad := uncheckedRoutes(t, e); len(bad) != 1 || bad[0] != "GET /debug/open" {
			t.Fatalf("the unchecked route was not caught: %v", bad)
		}
	})
	t.Run("a second connect server outside the path is caught", func(t *testing.T) {
		e := newEnv(t, options{})
		e.srv.MountUnchecked(func(s *connect.Server) {
			s.Register(connect.Method{
				Spec:    connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: "/magnetar.test.Open/Call"},
				Handler: func(context.Context, connect.Spec, connect.ServerStream) error { return nil },
			})
		})
		bad := uncheckedRoutes(t, e)
		if len(bad) == 0 {
			t.Fatal("the unchecked connect server was not caught")
		}
		for _, p := range bad {
			if !strings.HasPrefix(p, "/magnetar.test.Open/") {
				t.Fatalf("a checked route was reported: %v", bad)
			}
		}
	})
}

// TestUnknownMethodIsChecked guards that a method nobody registered still
// passes the request path: without a key it is refused; with the local
// operator it is UNIMPLEMENTED.
func TestUnknownMethodIsChecked(t *testing.T) {
	e := newEnv(t, options{})
	spec := connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: magnetarv1connect.AccessServiceName + "/NoSuchMethod"}
	spec.Procedure = "/" + spec.Procedure
	err := e.network("").raw.CallUnary(e.ctx, spec, &emptyMsg{}, &emptyMsg{})
	if code, _ := codeOf(err); code != connect.CodeUnauthenticated {
		t.Fatalf("an unknown method without a key: %v", err)
	}
	err = e.local().raw.CallUnary(e.ctx, spec, &emptyMsg{}, &emptyMsg{})
	if code, _ := codeOf(err); code != connect.CodeUnimplemented {
		t.Fatalf("an unknown method for the local operator: %v", err)
	}
	// An unknown service falls to the catch-all route: refused without a
	// key, in the gRPC protocol a gRPC client can read.
	err = e.network("").raw.CallUnary(e.ctx, connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: "/no.such.Service/Call"}, &emptyMsg{}, &emptyMsg{})
	if code, _ := codeOf(err); code != connect.CodeUnauthenticated {
		t.Fatalf("an unknown service without a key: %v", err)
	}
	err = e.local().raw.CallUnary(e.ctx, connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: "/no.such.Service/Call"}, &emptyMsg{}, &emptyMsg{})
	if code, _ := codeOf(err); code != connect.CodeUnimplemented {
		t.Fatalf("an unknown service for the local operator: %v", err)
	}
}

// emptyMsg is an empty protobuf message for calls whose content does not
// matter.
type emptyMsg = healthStatus
