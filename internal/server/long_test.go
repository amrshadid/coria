package server_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	coriav1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/server"
	"github.com/amrshadid/coria/internal/server/servertest"
)

// Stand-in routes for the long-method tests: two with the same handler, one
// named a long method and one not, and a third that streams until a write
// fails.
const (
	longPattern  = "GET /tenants/{tenant}/long/{rest...}"
	shortPattern = "GET /tenants/{tenant}/short/{rest...}"
	flowPattern  = "GET /tenants/{tenant}/flow/{rest...}"
)

// TestLongMethodHasNoDeadline guards that a call named a long method is not
// cut at the maximum or the default deadline, while any other call is (the
// same handler on a route not named): a subject's instances may take hours
// to stream (PS3.18 10.4 retrieval has no bound either).
func TestLongMethodHasNoDeadline(t *testing.T) {
	// The handler waits 900ms or until its context ends, and says which.
	wait := func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		select {
		case <-ctx.Done():
			_, _ = io.WriteString(w, "cut")
		case <-time.After(900 * time.Millisecond):
			_, _ = io.WriteString(w, "alive")
		}
		return nil
	}
	e := newEnv(t, options{
		noNetwork: true, deadline: 300 * time.Millisecond, maxDL: 400 * time.Millisecond,
		patterns: []server.PatternRoute{
			{Pattern: longPattern, Need: readerOnPathTenant, Handler: wait},
			{Pattern: shortPattern, Need: readerOnPathTenant, Handler: wait},
		},
		longMethods: []string{longPattern},
	})
	e.makeTenants("lab")
	local := e.socketClient(false)
	for _, c := range []struct{ path, want string }{
		{"/tenants/lab/long/a", "alive"},
		{"/tenants/lab/short/a", "cut"},
	} {
		status, body, _ := rawGet(t, local, "http://local", "GET", c.path, "")
		if status != http.StatusOK || body != c.want {
			t.Errorf("%s: %d %q, want 200 %q", c.path, status, body, c.want)
		}
	}
}

// TestLongMethodNamesAreChecked guards that a long method that no served
// procedure or route has is refused when the server is built, so a renamed
// route cannot lose its exemption without notice.
func TestLongMethodNamesAreChecked(t *testing.T) {
	build := func(long ...string) error {
		_, err := server.New(server.Config{
			NoSocket: true, Access: noChecker{}, Register: func(*connect.Server) {}, LongMethods: long,
			PatternRoutes: []server.PatternRoute{{Pattern: downloadPattern, Need: readerOnPathTenant, Handler: server.NotImplemented}},
		})
		return err
	}
	if err := build(downloadPattern); err != nil {
		t.Errorf("a served route refused as a long method: %v", err)
	}
	for _, bad := range []string{"GET /tenants/{tenant}/nothing", "/coria.v1.DataService/ReadFiles"} {
		if err := build(bad); err == nil || !strings.Contains(err.Error(), "long method") {
			t.Errorf("%q accepted as a long method though nothing serves it: %v", bad, err)
		}
	}
}

// TestLongMethodStallEndsTheResponse guards the stall rule: a client that
// stops reading a long method's response does not hold the handler for ever;
// a write that cannot finish within the stall timeout fails.
func TestLongMethodStallEndsTheResponse(t *testing.T) {
	failed := make(chan time.Duration, 1)
	flow := func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		chunk := make([]byte, 1<<20)
		start := time.Now()
		for ctx.Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				failed <- time.Since(start)
				return nil
			}
			_ = http.NewResponseController(w).Flush()
		}
		return nil
	}
	e := newEnv(t, options{
		noNetwork: true, stall: 300 * time.Millisecond,
		patterns:    []server.PatternRoute{{Pattern: flowPattern, Need: readerOnPathTenant, Handler: flow}},
		longMethods: []string{flowPattern},
	})
	e.makeTenants("lab")
	conn, err := net.Dial("unix", server.SocketPath(e.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /tenants/lab/flow/x HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// Read the status line only, then stop reading.
	if line, err := bufio.NewReaderSize(conn, 16).ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("status line %q: %v", line, err)
	}
	select {
	case d := <-failed:
		if d < 250*time.Millisecond || d > 5*time.Second {
			t.Errorf("the write failed after %v, want about the 300ms stall timeout", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a client that stopped reading held the handler for 10s")
	}
}

// Stand-in procedures for the compression test: the same server stream, one
// named a long method and one not.
const (
	longStream  = "/coria.test.Bulk/Long"
	shortStream = "/coria.test.Bulk/Short"
)

// TestLongProcedureIsNotCompressed guards that a long procedure's messages
// are sent uncompressed though the client accepts gzip, in the Connect and
// the gRPC protocols, while another procedure's are compressed as asked: a
// long procedure streams archived bytes, which gzip carries at a fraction
// of the speed they are otherwise sent.
func TestLongProcedureIsNotCompressed(t *testing.T) {
	methods := servertest.Contract()
	methods[longStream] = servertest.Entry{Fixed: access.Authenticated()}
	methods[shortStream] = servertest.Entry{Fixed: access.Authenticated()}
	register := func(s *connect.Server) {
		for _, proc := range []string{longStream, shortStream} {
			s.Register(connect.Method{
				Spec: connect.Spec{StreamType: connect.StreamTypeServer, Procedure: proc},
				Handler: func(_ context.Context, _ connect.Spec, st connect.ServerStream) error {
					var in coriav1.WhoAmIRequest
					if err := st.Receive(&in); err != nil {
						return err
					}
					return st.Send(&coriav1.FileChunk{Data: make([]byte, 64<<10)})
				},
			})
		}
	}
	e := newEnv(t, options{noNetwork: true, methods: methods, register: register, longMethods: []string{longStream}})
	hc := e.socketClient(true)
	for _, c := range []struct{ contentType, accept string }{
		{"application/connect+proto", "Connect-Accept-Encoding"},
		{"application/grpc+proto", "Grpc-Accept-Encoding"},
	} {
		for proc, want := range map[string]byte{longStream: 0, shortStream: 1} {
			req, err := http.NewRequest(http.MethodPost, "http://local"+proc, bytes.NewReader(make([]byte, 5)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", c.contentType)
			req.Header.Set(c.accept, "gzip")
			req.Header.Set("Te", "trailers")
			res, err := hc.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			_ = res.Body.Close()
			// The first byte of an enveloped message is its flags; bit 0
			// says it is compressed.
			if err != nil || res.StatusCode != http.StatusOK || len(body) < 5 || body[0]&1 != want {
				t.Errorf("%s %s: %d, %d bytes, flags %v, %v; want compressed %v", c.contentType, proc, res.StatusCode, len(body), body[:min(len(body), 1)], err, want == 1)
			}
		}
	}
}

// TestRouteSendsAFile guards that a route's writer takes a file through
// ReadFrom, which net/http hands to the system's copy from file to socket
// on a plain HTTP/1.1 connection, and that the bytes so sent are the file's
// with the status the handler wrote.
func TestRouteSendsAFile(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 300_000)
	name := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	took := make(chan bool, 1)
	send := func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, ok := w.(io.ReaderFrom)
		took <- ok
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, err = io.Copy(w, &io.LimitedReader{R: f, N: int64(len(data))})
		return err
	}
	e := newEnv(t, options{
		noNetwork: true,
		patterns:  []server.PatternRoute{{Pattern: flowPattern, Need: readerOnPathTenant, Handler: send}},
	})
	e.makeTenants("lab")
	status, body, _ := rawGet(t, e.socketClient(false), "http://local", "GET", "/tenants/lab/flow/x", "")
	if !<-took {
		t.Error("the route's writer has no ReadFrom, so a file cannot reach sendfile")
	}
	if status != http.StatusPartialContent || body != string(data) {
		t.Errorf("%d, %d bytes, want 206 and the file's %d", status, len(body), len(data))
	}
}
