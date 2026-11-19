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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/api"
	"github.com/amrshadid/magnetar/internal/core/settings"
	"github.com/amrshadid/magnetar/internal/master"
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
	served := notServedProblems(append(slices.Clone(notServed), "magnetar.v1.AccessService"), lines, known)
	if len(served) == 0 || !strings.Contains(served[0], "AccessService") {
		t.Errorf("a served service on the list was not reported: %v", served)
	}
	missing := notServedProblems(append(slices.Clone(notServed), "magnetar.v1.NoSuchService"), lines, known)
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

// roleLines reads the "Role:" line of every rpc of the contract's proto
// files, by full procedure name. The generated descriptors carry no
// comments, so the files themselves are read.
func roleLines(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "api", "proto", "magnetar", "v1", "*.proto"))
	if err != nil || len(files) == 0 {
		t.Fatalf("the proto files: %v (%d found)", err, len(files))
	}
	service := regexp.MustCompile(`^service (\w+) \{`)
	rpc := regexp.MustCompile(`^\s*rpc (\w+)\(`)
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: the repository's own proto files.
		if err != nil {
			t.Fatal(err)
		}
		var svc string
		var comment []string
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case service.MatchString(line):
				svc = service.FindStringSubmatch(line)[1]
				comment = nil
			case strings.HasPrefix(trimmed, "//"):
				comment = append(comment, strings.TrimSpace(strings.TrimPrefix(trimmed, "//")))
			case rpc.MatchString(line):
				name := rpc.FindStringSubmatch(line)[1]
				proc := "/magnetar.v1." + svc + "/" + name
				out[proc] = roleOf(comment)
				comment = nil
			default:
				comment = nil
			}
		}
	}
	return out
}

// roleOf returns the text of the Role line in a method's comment, with its
// continuation lines, up to the Audit line; "" when there is none.
func roleOf(comment []string) string {
	var parts []string
	in := false
	for _, l := range comment {
		switch {
		case strings.HasPrefix(l, "Role:"):
			in = true
			parts = append(parts, strings.TrimSpace(strings.TrimPrefix(l, "Role:")))
		case in && (l == "" || strings.HasPrefix(l, "Audit:") || strings.HasPrefix(l, "Errors:")):
			in = false
		case in:
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// The shapes of requirement a Role line can state.
type roleShape int

const (
	// fixedAdmin: admin on the installation, whatever the request.
	fixedAdmin roleShape = iota + 1
	// fixedAuthenticated: any caller the server authenticates.
	fixedAuthenticated
	// registration: reader on the tenant the request names, or reader on
	// the installation.
	registration
	// scopeReader and scopeAdmin: the role on the scope the request names.
	scopeReader
	scopeAdmin
	// settingChange: admin on the scope the request names, except for a
	// setting set on a tenant by the installation's admin only, which needs
	// admin on the installation whatever the scope.
	settingChange
	// tenantReader, tenantOperator and tenantAdmin: the role on the tenant
	// the request names by its field "tenant".
	tenantReader
	tenantOperator
	tenantAdmin
	// tenantReaderOrOperator: reader on the tenant, and operator on it when
	// the request sets one of the fields operatorWhen names for the line.
	tenantReaderOrOperator
	// monitorScope: reader on the tenant the request names, operator on
	// the installation for every tenant.
	monitorScope
)

// rolePhrases is every Role line the contract has, with the shape it
// states. A Role line not here fails the test, so a change of the proto
// text is a change of this table, made on purpose.
var rolePhrases = map[string]roleShape{
	"admin on the installation.":                                                      fixedAdmin,
	"none; any caller the server authenticates.":                                      fixedAuthenticated,
	"none beyond an authenticated caller; the caller's grants decide what is listed.": fixedAuthenticated,
	"reader on that tenant, or reader on the installation.":                           registration,
	"reader on the scope asked for.":                                                  scopeReader,
	"admin on the scope set.":                                                         scopeAdmin,
	"admin on the scope.":                                                             scopeAdmin,
	"admin on the scope asked for.":                                                   scopeAdmin,
	"admin on the scope set; for a setting set on a tenant by the installation's admin only, admin on the installation, whatever the scope.": settingChange,
	"admin on the scope; for a setting set on a tenant by the installation's admin only, admin on the installation, whatever the scope.":     settingChange,
	"reader on the tenant.":   tenantReader,
	"operator on the tenant.": tenantOperator,
	"admin on the tenant.":    tenantAdmin,
	"reader on the tenant; operator on the tenant to filter by Study Instance UID, since looking a study up by an identifier the caller already holds is a search for a person's data.": tenantReaderOrOperator,
	"reader on the tenant; operator on the tenant to filter by a UID, since looking a file up by an identifier the caller already holds is a search for a person's data.":               tenantReaderOrOperator,
	"reader on the tenant; operator on the tenant with include_header, since the header holds what the sender sent, identifying data included.":                                         tenantReaderOrOperator,
	"reader on the tenant asked for; operator on the installation for every tenant.":                                                                                                    monitorScope,
}

// operatorWhen names, for each Role line of the shape tenantReaderOrOperator,
// the fields of the request whose presence needs operator.
var operatorWhen = map[string][]string{
	"reader on the tenant; operator on the tenant to filter by Study Instance UID, since looking a study up by an identifier the caller already holds is a search for a person's data.": {"study_instance_uid"},
	"reader on the tenant; operator on the tenant to filter by a UID, since looking a file up by an identifier the caller already holds is a search for a person's data.":               {"study_instance_uid", "sop_instance_uid"},
	"reader on the tenant; operator on the tenant with include_header, since the header holds what the sender sent, identifying data included.":                                         {"include_header"},
}

// withField returns msg with the top-level field named name set: a string
// to a value it could hold (a UID, a name), a bool to true.
func withField(t *testing.T, msg proto.Message, name, value string) proto.Message {
	t.Helper()
	m := proto.Clone(msg).ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		t.Fatalf("%s has no field %s", m.Descriptor().FullName(), name)
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		m.Set(fd, protoreflect.ValueOfBool(true))
	case protoreflect.StringKind:
		m.Set(fd, protoreflect.ValueOfString(value))
	default:
		t.Fatalf("%s.%s is neither a string nor a bool", m.Descriptor().FullName(), name)
	}
	return m.Interface()
}

// withSettingName returns msg with the setting's name (a field "name", or
// "name" inside "setting") set.
func withSettingName(t *testing.T, msg proto.Message, name string) proto.Message {
	t.Helper()
	m := proto.Clone(msg).ProtoReflect()
	target := m
	if s := m.Descriptor().Fields().ByName("setting"); s != nil {
		target = m.Mutable(s).Message()
	}
	target.Set(target.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString(name))
	return m.Interface()
}

// checkTenantRole checks a requirement of role on the tenant a request
// names by its field "tenant": on that tenant when it is valid, and on the
// installation when it is malformed or missing.
func checkTenantRole(t *testing.T, proc string, need func(proto.Message) access.Requirement, msg proto.Message, role master.Role) {
	t.Helper()
	for _, c := range []struct {
		name, tenant string
		want         access.Requirement
	}{
		{"a tenant", "lab1", access.OnTenant("lab1", role)},
		{"a malformed tenant", "Lab 1", access.OnInstallation(role)},
		{"no tenant", "", access.OnInstallation(role)},
	} {
		named := withField(t, msg, "tenant", c.tenant)
		if c.tenant == "" {
			named = msg
		}
		if got := need(named); got != c.want {
			t.Errorf("%s with %s: %+v, want %+v", proc, c.name, got, c.want)
		}
	}
}

// withScope returns msg with its scope (a field "scope", or "scope" inside
// "setting") set to s, nil to leave it unset.
func withScope(t *testing.T, msg proto.Message, s *v1.Scope) proto.Message {
	t.Helper()
	m := proto.Clone(msg).ProtoReflect()
	target := m
	fd := m.Descriptor().Fields().ByName("scope")
	if fd == nil {
		setting := m.Descriptor().Fields().ByName("setting")
		if setting == nil {
			t.Fatalf("%s has no scope", m.Descriptor().FullName())
		}
		target = m.Mutable(setting).Message()
		fd = target.Descriptor().Fields().ByName("scope")
	}
	if s != nil {
		target.Set(fd, protoreflect.ValueOfMessage(s.ProtoReflect()))
	}
	return m.Interface()
}

// TestRequirementsFollowTheRoleLines guards the method-to-requirement table
// against the proto text itself, in both directions: every rpc of the proto
// files has a Role line of a known shape and a line in api.Methods with that
// shape, request by request, and every method of the descriptors is an rpc of
// the files. A malformed or missing scope or name is held to the role on the
// installation (the accepted reference).
func TestRequirementsFollowTheRoleLines(t *testing.T) {
	lines := roleLines(t)
	// The services not served yet are held by TestNotServedServices.
	notServed := readNotServed(t)
	for proc := range lines {
		if inServices(notServed, proc) {
			delete(lines, proc)
		}
	}
	methods := contractMethods()
	for proc := range methods {
		if _, ok := lines[proc]; !ok {
			t.Errorf("%s is in the descriptors but not an rpc of the proto files", proc)
		}
	}
	var m api.Methods
	for proc, text := range lines {
		if _, ok := methods[proc]; !ok {
			t.Errorf("%s is an rpc of the files but not in the descriptors", proc)
		}
		shape, ok := rolePhrases[text]
		if !ok {
			t.Errorf("%s: the Role line %q is not one this test knows; add it to rolePhrases with its shape", proc, text)
			continue
		}
		msg, known := m.RequestFor(proc)
		if !known {
			t.Errorf("%s: not in api.Methods", proc)
			continue
		}
		need := func(req proto.Message) access.Requirement {
			r, _ := m.Requirement(proc, req)
			return r
		}
		switch shape {
		case fixedAdmin, fixedAuthenticated:
			want := access.OnInstallation(master.RoleAdmin)
			if shape == fixedAuthenticated {
				want = access.Authenticated()
			}
			if msg != nil {
				t.Errorf("%s: a fixed Role line, but the table reads the request", proc)
			}
			if got := need(nil); got != want {
				t.Errorf("%s: %+v, want %+v", proc, got, want)
			}
		case registration:
			if msg == nil {
				t.Fatalf("%s: the table does not read the request", proc)
			}
			named := proto.Clone(msg)
			named.ProtoReflect().Set(named.ProtoReflect().Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("lab1"))
			if got := need(named); got != access.TenantRegistration("lab1") {
				t.Errorf("%s with a tenant: %+v", proc, got)
			}
			if got := need(msg); got != access.OnInstallation(master.RoleReader) {
				t.Errorf("%s with no name: %+v", proc, got)
			}
		case scopeReader, scopeAdmin:
			role := master.RoleReader
			if shape == scopeAdmin {
				role = master.RoleAdmin
			}
			if msg == nil {
				t.Fatalf("%s: the table does not read the request", proc)
			}
			for _, c := range []struct {
				name  string
				scope *v1.Scope
				want  access.Requirement
			}{
				{"a tenant", tenantScope("lab1"), access.OnTenant("lab1", role)},
				{"the installation", installation(), access.OnInstallation(role)},
				{"a malformed tenant", tenantScope("Lab 1"), access.OnInstallation(role)},
				{"no scope", nil, access.OnInstallation(role)},
			} {
				if got := need(withScope(t, msg, c.scope)); got != c.want {
					t.Errorf("%s with %s: %+v, want %+v", proc, c.name, got, c.want)
				}
			}
		case monitorScope:
			if msg == nil {
				t.Fatalf("%s: the table does not read the request", proc)
			}
			for _, c := range []struct {
				name  string
				scope *v1.Scope
				want  access.Requirement
			}{
				{"a tenant", tenantScope("lab1"), access.OnTenant("lab1", master.RoleReader)},
				{"the installation", installation(), access.OnInstallation(master.RoleOperator)},
				{"a malformed tenant", tenantScope("Lab 1"), access.OnInstallation(master.RoleOperator)},
				{"no scope", nil, access.OnInstallation(master.RoleOperator)},
			} {
				if got := need(withScope(t, msg, c.scope)); got != c.want {
					t.Errorf("%s with %s: %+v, want %+v", proc, c.name, got, c.want)
				}
			}
		case settingChange:
			if msg == nil {
				t.Fatalf("%s: the table does not read the request", proc)
			}
			// An ordinary setting: admin on the scope named.
			ordinary := withSettingName(t, msg, settings.EventsRetention)
			for _, c := range []struct {
				name  string
				scope *v1.Scope
				want  access.Requirement
			}{
				{"a tenant", tenantScope("lab1"), access.OnTenant("lab1", master.RoleAdmin)},
				{"the installation", installation(), access.OnInstallation(master.RoleAdmin)},
				{"a malformed tenant", tenantScope("Lab 1"), access.OnInstallation(master.RoleAdmin)},
				{"no scope", nil, access.OnInstallation(master.RoleAdmin)},
			} {
				if got := need(withScope(t, ordinary, c.scope)); got != c.want {
					t.Errorf("%s of an ordinary setting with %s: %+v, want %+v", proc, c.name, got, c.want)
				}
			}
			// Each setting the installation's admin alone sets on a tenant:
			// admin on the installation, whatever the scope.
			for _, n := range []string{settings.ReceiverPorts, settings.IncomingLimit} {
				named := withSettingName(t, msg, n)
				for _, sc := range []*v1.Scope{tenantScope("lab1"), installation(), tenantScope("Lab 1"), nil} {
					if got := need(withScope(t, named, sc)); got != access.OnInstallation(master.RoleAdmin) {
						t.Errorf("%s of %s with scope %v: %+v, want admin on the installation", proc, n, sc, got)
					}
				}
			}
		case tenantReader, tenantOperator, tenantAdmin, tenantReaderOrOperator:
			role := map[roleShape]master.Role{tenantReader: master.RoleReader, tenantOperator: master.RoleOperator, tenantAdmin: master.RoleAdmin, tenantReaderOrOperator: master.RoleReader}[shape]
			if msg == nil {
				t.Fatalf("%s: the table does not read the request", proc)
			}
			checkTenantRole(t, proc, need, msg, role)
			for _, f := range operatorWhen[text] {
				checkTenantRole(t, proc+" with "+f, need, withField(t, msg, f, "2.25.1"), master.RoleOperator)
			}
		}
	}
}
