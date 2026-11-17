package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
)

// jsonDirEnv names the directory TestWriteJSONOutputs writes into. The test
// is the first half of the outside check of the JSON documents: it runs every
// command with --json against the in-process server and leaves each output as
// a file, and scripts/check-json.py, which is Python and not Magnetar, parses
// them with Python's json module.
const jsonDirEnv = "MAGNETAR_CHECK_JSON_DIR"

// TestWriteJSONOutputs runs every command with --json and writes standard
// output to <dir>/<name>.json, for scripts/check-json.py. It skips only when
// the variable is unset; "make check-json" always sets it.
func TestWriteJSONOutputs(t *testing.T) {
	dir := os.Getenv(jsonDirEnv)
	if dir == "" {
		t.Skipf("%s is not set; make check-json runs this test", jsonDirEnv)
	}
	r := newRig(t)
	r.fake.AddAudit(&v1.AuditEntry{Id: "1", Time: timestamppb.New(fixedNow), KeyId: "k1", Method: "/magnetar.v1.TenantService/CreateTenant", Object: "lab1", Outcome: v1.AuditOutcome_AUDIT_OUTCOME_DONE, RequestId: "req-1"})
	r.env["LAB2_DB"] = "postgres://lab2:x@db.example.org/lab2"
	steps := []struct {
		name string
		args []string
	}{
		{"version", []string{"version"}},
		{"whoami", []string{"whoami"}},
		{"tenant-create", []string{"tenant", "create", "lab1"}},
		{"tenant-create-postgres", []string{"tenant", "create", "lab2", "--db-env", "LAB2_DB"}},
		{"tenant-list", []string{"tenant", "list"}},
		{"tenant-show", []string{"tenant", "show", "lab1"}},
		{"key-create", []string{"key", "create", "--holder", "ci"}},
		{"key-list", []string{"key", "list"}},
		{"key-show", []string{"key", "show", "k00000000001"}},
		{"grant-set", []string{"grant", "set", "k00000000001", "--role", "reader", "--tenant", "lab1"}},
		{"grant-list", []string{"grant", "list", "--key", "k00000000001"}},
		{"grant-remove", []string{"grant", "remove", "k00000000001", "--tenant", "lab1"}},
		{"key-revoke", []string{"key", "revoke", "k00000000001"}},
		{"setting-get", []string{"setting", "get", "audit.retention"}},
		{"setting-set", []string{"setting", "set", "audit.retention", "90d", "--tenant", "lab1"}},
		{"setting-list", []string{"setting", "list", "--tenant", "lab1"}},
		{"setting-unset", []string{"setting", "unset", "audit.retention", "--tenant", "lab1"}},
		{"audit-list", []string{"audit", "list"}},
		{"tenant-remove", []string{"tenant", "remove", "lab1", "--yes"}},
	}
	for _, s := range steps {
		res := r.run(append(s.args, "--json")...)
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("magnetar %v: exit %d, standard error %q", s.args, res.code, res.stderr)
		}
		// The directory is the one make check-json made for this run.
		if err := os.WriteFile(filepath.Join(dir, s.name+".json"), []byte(res.stdout), 0o600); err != nil { //nolint:gosec // dir is the test's own scratch directory
			t.Fatal(err)
		}
	}
}
