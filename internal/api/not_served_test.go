package api_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/amrshadid/coria/internal/api"
)

// notServedFile is the list of the services the contract defines and the
// server does not serve yet (its header says why such a list exists and
// when a line leaves it). It is one file, read here, by the tests of internal/server/servertest, and
// by scripts/prove/steps.py, so the three cannot disagree.
var notServedFile = filepath.Join("..", "..", "api", "not-served.txt")

// notServedName is the form of a line of notServedFile: a full service
// name.
var notServedName = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)*\.[A-Z][A-Za-z0-9]*$`)

// readNotServed returns the services notServedFile names, in its order. A
// line that is not a full service name, or a name given twice, fails the
// test.
func readNotServed(t testing.TB) []string {
	t.Helper()
	b, err := os.ReadFile(notServedFile)
	if err != nil {
		t.Fatalf("the list of services not served: %v", err)
	}
	var out []string
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !notServedName.MatchString(line) || slices.Contains(out, line) {
			t.Fatalf("%s:%d: %q is not a full service name given once", notServedFile, i+1, line)
		}
		out = append(out, line)
	}
	return out
}

// inServices reports whether proc, a full procedure name, is a method of
// one of services.
func inServices(services []string, proc string) bool {
	return slices.ContainsFunc(services, func(s string) bool { return strings.HasPrefix(proc, "/"+s+"/") })
}

// notServedProblems compares a list of services not served with the rpcs of
// the proto files (lines) and with what the table serves (known): each
// listed service must have at least one rpc in the files and none the table
// knows.
func notServedProblems(services []string, lines map[string]string, known func(string) bool) []string {
	var problems []string
	for _, s := range services {
		rpcs := 0
		for proc := range lines {
			if !strings.HasPrefix(proc, "/"+s+"/") {
				continue
			}
			rpcs++
			if known(proc) {
				problems = append(problems, proc+" is listed as not served, yet api.Methods serves it")
			}
		}
		if rpcs == 0 {
			problems = append(problems, s+" is listed as not served, and no proto file defines it")
		}
	}
	slices.Sort(problems)
	return problems
}

// TestNotServedServices guards the list of services not served yet, so it
// cannot hide a method that is served: every listed service is in the proto
// files and in the generated descriptors, none of its methods is in
// api.Methods, and a call to each of its methods over the test server, as
// the local operator, is answered UNIMPLEMENTED. It also shows the check
// itself reports a served service put on the list and a listed service no
// file defines.
func TestNotServedServices(t *testing.T) {
	notServed := readNotServed(t)
	lines := roleLines(t)
	var m api.Methods
	known := func(proc string) bool { _, ok := m.RequestFor(proc); return ok }
	for _, p := range notServedProblems(notServed, lines, known) {
		t.Error(p)
	}
	served := notServedProblems(append(slices.Clone(notServed), "coria.v1.AccessService"), lines, known)
	if len(served) == 0 || !strings.Contains(served[0], "AccessService") {
		t.Errorf("a served service on the list was not reported: %v", served)
	}
	missing := notServedProblems(append(slices.Clone(notServed), "coria.v1.NoSuchService"), lines, known)
	if len(missing) != 1 || !strings.Contains(missing[0], "NoSuchService") {
		t.Errorf("a listed service no file defines was not reported: %v", missing)
	}

	e := newEnv(t)
	asLocal := func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if info, ok := connect.CallInfoForClientContext(ctx); ok {
				info.RequestHeader().Set(localHeader, local.local)
			}
			return next(ctx, spec)
		}
	}
	cl := connect.NewClient(connecthttp.NewTransport(e.client, e.url), asLocal)
	for _, s := range notServed {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(s))
		sd, ok := d.(protoreflect.ServiceDescriptor)
		if err != nil || !ok {
			t.Errorf("%s is not a service of the generated descriptors: %v", s, err)
			continue
		}
		for i := range sd.Methods().Len() {
			md := sd.Methods().Get(i)
			proc := "/" + s + "/" + string(md.Name())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			req, res := dynamicpb.NewMessage(md.Input()), dynamicpb.NewMessage(md.Output())
			if md.IsStreamingServer() {
				st, err := cl.CallServerStream(ctx, connect.Spec{StreamType: connect.StreamTypeServer, Procedure: proc}, req)
				if err == nil {
					err = st.Receive(res)
					_ = st.Close()
				}
				if connect.CodeOf(err) != connect.CodeUnimplemented {
					t.Errorf("%s: %v, want UNIMPLEMENTED", proc, err)
				}
			} else {
				err := cl.CallUnary(ctx, connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: proc}, req, res)
				if connect.CodeOf(err) != connect.CodeUnimplemented {
					t.Errorf("%s: %v, want UNIMPLEMENTED", proc, err)
				}
			}
			cancel()
		}
	}
}
