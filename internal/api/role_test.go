package api_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/gen/coria/v1/coriav1connect"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/api"
	"github.com/amrshadid/coria/internal/core/settings"
	"github.com/amrshadid/coria/internal/master"
)

// roleLines reads the "Role:" line of every rpc of the contract's proto
// files, by full procedure name. The generated descriptors carry no
// comments, so the files themselves are read.
func roleLines(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "api", "proto", "coria", "v1", "*.proto"))
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
				proc := "/coria.v1." + svc + "/" + name
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
	"admin on the installation.":                 fixedAdmin,
	"none; any caller the server authenticates.": fixedAuthenticated,
	// The table lets any authenticated caller reach RotateKey; package access
	// decides admin or the key itself before anything else (TestRotateKey*
	// there, and the API's own test of a key rotating another).
	"admin on the installation; or the key being rotated itself, so a system renews its own key with no person involved. Any other key is answered permission denied.": fixedAuthenticated,
	"none beyond an authenticated caller; the caller's grants decide what is listed.":                                                                                  fixedAuthenticated,
	"reader on that tenant, or reader on the installation.":                                                                                                            registration,
	"reader on the scope asked for.": scopeReader,
	"admin on the scope set.":        scopeAdmin,
	"admin on the scope.":            scopeAdmin,
	"admin on the scope asked for.":  scopeAdmin,
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

// requirementTable is what the server asks of a method table: api.Methods
// for the services served, api.Pending for those not served yet.
type requirementTable interface {
	RequestFor(procedure string) (proto.Message, bool)
	Requirement(procedure string, msg proto.Message) (access.Requirement, access.Request)
}

// TestRequirementsFollowTheRoleLines guards the method-to-requirement table
// against the proto text itself, in both directions: every rpc of the proto
// files has a Role line of a known shape and a line with that shape,
// request by request, in api.Methods for a service served and in
// api.Pending for a service not served yet, so a method is held to its
// Role line from the change that writes its contract; and every method of
// the descriptors is an rpc of the files. A malformed or missing scope or
// name is held to the role on the installation (the accepted reference).
func TestRequirementsFollowTheRoleLines(t *testing.T) {
	lines := roleLines(t)
	notServed := readNotServed(t)
	methods := contractMethods()
	for proc := range methods {
		if _, ok := lines[proc]; !ok {
			t.Errorf("%s is in the descriptors but not an rpc of the proto files", proc)
		}
	}
	for proc, text := range lines {
		var m requirementTable = api.Methods{}
		if inServices(notServed, proc) {
			// A service not served is not among the files contractMethods
			// reads, which are the served ones; its descriptor is looked up
			// by name.
			m = api.Pending{}
			if _, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(strings.ReplaceAll(strings.TrimPrefix(proc, "/"), "/", "."))); err != nil {
				t.Errorf("%s is an rpc of the files but not in the descriptors: %v", proc, err)
			}
		} else if _, ok := methods[proc]; !ok {
			t.Errorf("%s is an rpc of the files but not in the descriptors", proc)
		}
		shape, ok := rolePhrases[text]
		if !ok {
			t.Errorf("%s: the Role line %q is not one this test knows; add it to rolePhrases with its shape", proc, text)
			continue
		}
		msg, known := m.RequestFor(proc)
		if !known {
			t.Errorf("%s: not in the table (%T)", proc, m)
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

// TestPendingRules guards the table of the methods not served yet from
// drifting from the list and the contract: each of its lines names a
// method of a service the list names, reads a request of the method's own
// input type, and the count says when a method was added to or dropped
// from a service not served without its line. It also holds the audit
// facts of the reading methods, and of the plain HTTP download, to their
// Audit lines: the object is the path of labels the request names, a value
// that does not pass the label rule is recorded as "invalid", and an
// instance id is recorded only as present, never as given.
func TestPendingRules(t *testing.T) {
	notServed := readNotServed(t)
	lines := roleLines(t)
	var p api.Pending
	procs := p.Procedures()
	if len(procs) != 0 {
		t.Errorf("the table of methods not served has %d lines; this test was written for 0, so look at what changed", len(procs))
	}
	for _, proc := range procs {
		if !inServices(notServed, proc) {
			t.Errorf("%s has a line among the methods not served, but its service is not on the list: move the line to the served table", proc)
		}
		if _, ok := lines[proc]; !ok {
			t.Errorf("%s has a line among the methods not served, but is not an rpc of the proto files", proc)
			continue
		}
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(strings.ReplaceAll(strings.TrimPrefix(proc, "/"), "/", ".")))
		md, ok := d.(protoreflect.MethodDescriptor)
		if err != nil || !ok {
			t.Errorf("%s is not a method of the descriptors: %v", proc, err)
			continue
		}
		if msg, _ := p.RequestFor(proc); msg == nil || msg.ProtoReflect().Descriptor().FullName() != md.Input().FullName() {
			t.Errorf("%s: the table reads %v, the method takes a %s", proc, msg, md.Input().FullName())
		}
	}

	const uid = "2.25.4711"
	facts := func(proc string, msg proto.Message) access.Request {
		t.Helper()
		_, f := api.Methods{}.Requirement(proc, msg)
		for k, v := range f.Details {
			if strings.Contains(v, uid) || strings.Contains(v, "Smith") {
				t.Errorf("%s: the detail %s holds what the request gave: %q", proc, k, v)
			}
		}
		if strings.Contains(f.Object, uid) {
			t.Errorf("%s: the object holds a UID: %q", proc, f.Object)
		}
		return f
	}
	for _, c := range []struct {
		name    string
		proc    string
		msg     proto.Message
		object  string
		details map[string]string
	}{
		{"ReadFiles", coriav1connect.DataServiceReadFilesProcedure,
			&v1.ReadFilesRequest{Tenant: "lab1", SubjectLabel: "S01", SessionLabel: "E01", ScanLabel: "T1", InstanceId: uid, StartAfter: &v1.ReadPosition{InstanceId: uid}},
			"S01/E01/T1", map[string]string{"instance": "present", "start_after": "present", "announce_only": "false"}},
	} {
		f := facts(c.proc, c.msg)
		if f.Object != c.object {
			t.Errorf("%s: the object is %q, want %q", c.name, f.Object, c.object)
		}
		for k, v := range c.details {
			if f.Details[k] != v {
				t.Errorf("%s: the detail %s is %q, want %q", c.name, k, f.Details[k], v)
			}
		}
		for k, v := range f.Details {
			if strings.Contains(v, "data/") {
				t.Errorf("%s: the detail %s holds a storage key: %q", c.name, k, v)
			}
		}
	}

	// The download over plain HTTP: reader on the tenant the path names,
	// the scan's path as its object, the instance id only as present.
	need, f := api.DownloadFacts("lab1", "S01", "E01", "T1", uid, true, false)
	if need != access.OnTenant("lab1", master.RoleReader) {
		t.Errorf("a download needs %+v, want reader on the tenant", need)
	}
	if f.Object != "S01/E01/T1" || f.Details["instance"] != "present" || f.Details["range"] != "true" || f.Details["window"] != "false" {
		t.Errorf("a download's facts are %+v", f)
	}
	for k, v := range f.Details {
		if strings.Contains(v, uid) {
			t.Errorf("a download's detail %s holds the instance id: %q", k, v)
		}
	}
	if need, _ := api.DownloadFacts("Lab 1", "S01", "E01", "T1", uid, false, false); need != access.OnInstallation(master.RoleReader) {
		t.Errorf("a download into a malformed tenant needs %+v, want reader on the installation", need)
	}
	if strings.Contains(api.DownloadMethod, "%") || !strings.HasPrefix(api.DownloadMethod, "GET /tenants/{tenant}/instances/") {
		t.Errorf("the download's audit method is %q", api.DownloadMethod)
	}
}
