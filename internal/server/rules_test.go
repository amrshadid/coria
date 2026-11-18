package server_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	magnetarv1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/server"
)

// contractFiles are the generated descriptors of the contract's files.
var contractFiles = []protoreflect.FileDescriptor{
	magnetarv1.File_magnetar_v1_access_proto,
	magnetarv1.File_magnetar_v1_tenant_proto,
	magnetarv1.File_magnetar_v1_settings_proto,
	magnetarv1.File_magnetar_v1_audit_proto,
	magnetarv1.File_magnetar_v1_scope_proto,
}

// contractMethods returns every method of the contract by procedure name.
func contractMethods() map[string]protoreflect.MethodDescriptor {
	out := map[string]protoreflect.MethodDescriptor{}
	for _, f := range contractFiles {
		for i := range f.Services().Len() {
			svc := f.Services().Get(i)
			for j := range svc.Methods().Len() {
				m := svc.Methods().Get(j)
				out["/"+string(svc.FullName())+"/"+string(m.Name())] = m
			}
		}
	}
	return out
}

// TestTableCoversContract guards the method-to-requirement table against
// the generated descriptors in both directions: every method of the
// contract has an entry, and every entry names a method of the contract or
// the health service.
func TestTableCoversContract(t *testing.T) {
	problems := tableProblems(contractMethods(), server.RuleProcedures())
	for _, p := range problems {
		t.Error(p)
	}
}

// TestTableTestCatchesGaps guards the table test itself: with an entry
// removed, and with an entry for a method that does not exist, it reports
// each.
func TestTableTestCatchesGaps(t *testing.T) {
	procs := server.RuleProcedures()
	dropped := slices.DeleteFunc(slices.Clone(procs), func(p string) bool { return strings.HasSuffix(p, "/WhoAmI") })
	if p := tableProblems(contractMethods(), dropped); len(p) != 1 || !strings.Contains(p[0], "WhoAmI") {
		t.Fatalf("a missing entry was not reported: %v", p)
	}
	extra := append(slices.Clone(procs), "/magnetar.v1.AccessService/Nothing")
	if p := tableProblems(contractMethods(), extra); len(p) != 1 || !strings.Contains(p[0], "Nothing") {
		t.Fatalf("an entry naming no method was not reported: %v", p)
	}
}

// tableProblems compares the contract's methods with the table's
// procedures.
func tableProblems(methods map[string]protoreflect.MethodDescriptor, procs []string) []string {
	var problems []string
	have := map[string]bool{}
	for _, p := range procs {
		have[p] = true
		if _, ok := methods[p]; !ok && p != server.HealthCheckProcedure && p != server.HealthWatchProcedure {
			problems = append(problems, "the table has "+p+", which is no method of the contract")
		}
	}
	for p := range methods {
		if !have[p] {
			problems = append(problems, "the method "+p+" has no entry in the table")
		}
	}
	slices.Sort(problems)
	return problems
}

// rolePattern finds each method's "Role:" line, which may run onto the
// next comment line, and the rpc it belongs to.
var rolePattern = regexp.MustCompile(`(?s)// Role: (.*?)\n\s*// (?:Audit|Errors):.*?rpc (\w+)\(`)

// servicePattern finds the services of a proto file and where each starts.
var servicePattern = regexp.MustCompile(`(?m)^service (\w+) \{`)

// TestTableMatchesRoleLines guards that each entry gives the requirement
// its method's "Role:" line in api/proto/magnetar/v1 states. The lines are
// read from the proto files, since the generated descriptors carry no
// comments; each phrase the contract uses is mapped to what it means, and
// a phrase not known here fails the test, so a new kind of role line is
// looked at before it is served.
func TestTableMatchesRoleLines(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "api", "proto", "magnetar", "v1", "*.proto"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no proto files: %v", err)
	}
	methods := contractMethods()
	seen := map[string]bool{}
	for _, file := range files {
		b, err := os.ReadFile(file) //nolint:gosec // G304: the repository's own proto files.
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		services := servicePattern.FindAllStringSubmatchIndex(text, -1)
		for _, m := range rolePattern.FindAllStringSubmatchIndex(text, -1) {
			role := strings.Join(strings.Fields(strings.ReplaceAll(text[m[2]:m[3]], "//", " ")), " ")
			name := text[m[4]:m[5]]
			svc := ""
			for _, s := range services {
				if s[0] < m[0] {
					svc = text[s[2]:s[3]]
				}
			}
			proc := "/magnetar.v1." + svc + "/" + name
			md, ok := methods[proc]
			if !ok {
				t.Errorf("%s: a Role line for %s, which is no method", filepath.Base(file), proc)
				continue
			}
			seen[proc] = true
			checkRole(t, proc, role, md)
		}
	}
	for p := range methods {
		if !seen[p] {
			t.Errorf("the method %s has no Role line the test could read", p)
		}
	}
}

// checkRole compares what the table says proc needs with what its Role
// line says.
func checkRole(t *testing.T, proc, role string, md protoreflect.MethodDescriptor) {
	t.Helper()
	public, got, ok := server.RuleFor(proc, nil)
	if !ok || public {
		t.Errorf("%s: no entry, or a public one", proc)
		return
	}
	switch role {
	case "admin on the installation.":
		expectStatic(t, proc, got, access.OnInstallation(master.RoleAdmin))
	case "none; any caller the server authenticates.",
		"none beyond an authenticated caller; the caller's grants decide what is listed.":
		expectStatic(t, proc, got, access.Authenticated())
	case "reader on that tenant, or reader on the installation.":
		msg := newRequest(md)
		setString(msg, "name", "lab")
		_, got, _ = server.RuleFor(proc, msg)
		if got != access.TenantRegistration("lab") {
			t.Errorf("%s: %q, but the table does not ask for the tenant's registration", proc, role)
		}
	case "reader on the scope asked for.":
		expectScope(t, proc, md, master.RoleReader)
	case "admin on the scope set.", "admin on the scope.", "admin on the scope asked for.":
		expectScope(t, proc, md, master.RoleAdmin)
	default:
		t.Errorf("%s: the Role line %q is a phrase this test does not know; add what it means", proc, role)
	}
}

// expectStatic checks a requirement that must not depend on the request,
// and that the message is not read before the check.
func expectStatic(t *testing.T, proc string, got, want access.Requirement) {
	t.Helper()
	if got != want {
		t.Errorf("%s: the table's requirement is not the Role line's", proc)
	}
	if server.ReadsMessage(proc) {
		t.Errorf("%s: its requirement is fixed, yet the message is read before the check", proc)
	}
}

// expectScope checks a role on the request's scope: on the tenant it
// names, on the installation, and on the installation when the scope is
// missing.
func expectScope(t *testing.T, proc string, md protoreflect.MethodDescriptor, role master.Role) {
	t.Helper()
	tenant := newRequest(md)
	if !setScope(tenant.ProtoReflect(), &magnetarv1.Scope{Kind: &magnetarv1.Scope_Tenant{Tenant: "lab"}}, 2) {
		t.Errorf("%s: no scope field found in its request", proc)
		return
	}
	inst := newRequest(md)
	setScope(inst.ProtoReflect(), installation(), 2)
	for _, c := range []struct {
		msg  proto.Message
		want access.Requirement
	}{
		{tenant, access.OnTenant("lab", role)},
		{inst, access.OnInstallation(role)},
		{newRequest(md), access.OnInstallation(role)},
	} {
		if _, got, _ := server.RuleFor(proc, c.msg); got != c.want {
			t.Errorf("%s: the requirement for %v is not %s on that scope", proc, c.msg, role)
		}
	}
}

// newRequest returns an empty request message of md, as the generated
// type.
func newRequest(md protoreflect.MethodDescriptor) proto.Message {
	mt, err := protoregistryFind(md.Input().FullName())
	if err != nil {
		return dynamicpb.NewMessage(md.Input())
	}
	return mt.New().Interface()
}

// setString sets a top-level string field.
func setString(m proto.Message, field, v string) {
	r := m.ProtoReflect()
	r.Set(r.Descriptor().Fields().ByName(protoreflect.Name(field)), protoreflect.ValueOfString(v))
}

// setScope sets the first field of type magnetar.v1.Scope found within
// depth levels of messages, and reports whether it found one.
func setScope(m protoreflect.Message, s *magnetarv1.Scope, depth int) bool {
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.Message() == nil || fd.IsList() || fd.IsMap() {
			continue
		}
		if fd.Message().FullName() == "magnetar.v1.Scope" {
			m.Set(fd, protoreflect.ValueOfMessage(s.ProtoReflect()))
			return true
		}
		if depth > 1 && setScope(m.Mutable(fd).Message(), s, depth-1) {
			return true
		}
	}
	return false
}

// protoregistryFind finds a generated message type by name.
func protoregistryFind(name protoreflect.FullName) (protoreflect.MessageType, error) {
	return protoregistry.GlobalTypes.FindMessageByName(name)
}
