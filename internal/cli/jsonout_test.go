package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/cli/client"
	"github.com/amrshadid/coria/internal/cli/fakeserver"
)

// jsonDirEnv names the directory TestWriteJSONOutputs writes into. The test
// is the first half of the outside check of the JSON documents: it runs every
// command with --json against the in-process server and leaves each output as
// a file, and scripts/check/cli-json.py, which is Python and not CORIA, parses
// them with Python's json module.
const jsonDirEnv = "CORIA_CHECK_JSON_DIR"

// TestWriteJSONOutputs runs every command with --json and writes standard
// output to <dir>/<name>.json, for scripts/check/cli-json.py. It skips only when
// the variable is unset; "make check-json" always sets it.
func TestWriteJSONOutputs(t *testing.T) {
	dir := os.Getenv(jsonDirEnv)
	if dir == "" {
		t.Skipf("%s is not set; make check-json runs this test", jsonDirEnv)
	}
	r := newRig(t)
	r.fake.AddAudit(&v1.AuditEntry{Id: "1", Time: timestamppb.New(fixedNow), KeyId: "k1", Method: "/coria.v1.TenantService/CreateTenant", Object: "lab1", Outcome: v1.AuditOutcome_AUDIT_OUTCOME_DONE, RequestId: "req-1"})
	r.env["LAB2_DB"] = "postgres://lab2:x@db.example.org/lab2"
	r.env["LAB1_S3"] = "made-up-secret-access-key-for-the-test"
	for _, id := range []string{"rcp-retry", "rcp-discard"} {
		r.fake.AddReception("lab1", &v1.Reception{Id: id, Receiver: "scanner1", StudyInstanceUid: "1.2." + id, Arrival: 1, Step: v1.ReceptionStep_RECEPTION_STEP_FAILED, StartTime: timestamppb.New(fixedNow)})
	}
	for _, id := range []string{"q-release", "q-reject"} {
		r.fake.AddQuarantined("lab1", &v1.QuarantinedFile{Id: id, Reason: v1.QuarantineReason_QUARANTINE_REASON_BAD_LABEL, State: v1.QuarantineState_QUARANTINE_STATE_HELD, Receiver: "scanner1", ContentKept: true, QuarantineTime: timestamppb.New(fixedNow)}, `{}`)
	}
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
		{"storage-add", []string{"storage", "add", "lab1", "main", "--type", "dir", "--root", "fast"}},
		{"storage-add-s3", []string{"storage", "add", "lab1", "cold", "--type", "s3", "--endpoint", "https://s3.example", "--region", "r", "--bucket", "lab1-archive", "--access-key-id", "AKIAEXAMPLE", "--credential-env", "LAB1_S3"}},
		{"storage-list", []string{"storage", "list", "lab1"}},
		{"storage-show", []string{"storage", "show", "lab1", "cold"}},
		{"storage-set", []string{"storage", "set", "lab1", "cold", "--capacity", "1TiB"}},
		{"storage-test", []string{"storage", "test", "lab1", "main"}},
		{"receiver-add", []string{"receiver", "add", "lab1", "scanner1", "--storage", "main", "--port", "11112", "--aet", "CORIA", "--allow-aet", "PRISMA1", "--enabled"}},
		{"receiver-list", []string{"receiver", "list", "lab1"}},
		{"receiver-show", []string{"receiver", "show", "lab1", "scanner1"}},
		{"receiver-set", []string{"receiver", "set", "lab1", "scanner1", "--quiet-period", "5m"}},
		{"reception-list", []string{"reception", "list", "lab1"}},
		{"reception-show", []string{"reception", "show", "lab1", "rcp-retry"}},
		{"reception-retry", []string{"reception", "retry", "lab1", "rcp-retry"}},
		{"reception-discard", []string{"reception", "discard", "lab1", "rcp-discard", "--reason", "test data", "--yes"}},
		{"quarantine-list", []string{"quarantine", "list", "lab1", "--state", "held"}},
		{"quarantine-show", []string{"quarantine", "show", "lab1", "q-release", "--header"}},
		{"quarantine-release", []string{"quarantine", "release", "lab1", "q-release", "--reason", "naming rule fixed"}},
		{"quarantine-reject", []string{"quarantine", "reject", "lab1", "q-reject", "--reason", "test data", "--yes"}},
		{"receiver-remove", []string{"receiver", "remove", "lab1", "scanner1", "--yes"}},
		{"storage-remove", []string{"storage", "remove", "lab1", "cold", "--yes"}},
		{"key-create", []string{"key", "create", "--holder", "ci"}},
		{"key-list", []string{"key", "list"}},
		{"key-show", []string{"key", "show", "k00000000001"}},
		{"grant-set", []string{"grant", "set", "k00000000001", "--role", "reader", "--tenant", "lab1"}},
		{"grant-list", []string{"grant", "list", "--key-id", "k00000000001"}},
		{"grant-remove", []string{"grant", "remove", "k00000000001", "--tenant", "lab1"}},
		{"key-revoke", []string{"key", "revoke", "k00000000001"}},
		{"setting-get", []string{"setting", "get", "test.retention"}},
		{"setting-set", []string{"setting", "set", "test.retention", "90d", "--tenant", "lab1"}},
		{"setting-list", []string{"setting", "list", "--tenant", "lab1"}},
		{"setting-unset", []string{"setting", "unset", "test.retention", "--tenant", "lab1"}},
		{"audit-list", []string{"audit", "list"}},
		{"tenant-remove", []string{"tenant", "remove", "lab1", "--yes"}},
	}
	for _, s := range steps {
		res := r.run(append(s.args, "--json")...)
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("coria %v: exit %d, standard error %q", s.args, res.code, res.stderr)
		}
		// The directory is the one make check-json made for this run.
		if err := os.WriteFile(filepath.Join(dir, s.name+".json"), []byte(res.stdout), 0o600); err != nil { //nolint:gosec // dir is the test's own scratch directory
			t.Fatal(err)
		}
	}
	// The contexts need a server that asks for a key, so they run against a
	// network fake of their own, with a file of contexts in the test's
	// private directory.
	n := newNetworkRig(t)
	keyFile := fakeserver.PrivateFile(t, testKey+"\n")
	cfgEnv := map[string]string{"CORIA_CONFIG": filepath.Join(t.TempDir(), "contexts.json")}
	for _, s := range []struct {
		name string
		args []string
	}{
		{"context-add", []string{"context", "add", "prod", "--server", n.env[client.EnvServer], "--key-file", keyFile, "--ca-file", n.env[client.EnvCAFile]}},
		{"context-list", []string{"context", "list"}},
		{"context-show", []string{"context", "show", "prod"}},
		{"context-use", []string{"context", "use", "prod"}},
		{"context-remove", []string{"context", "remove", "prod"}},
	} {
		res := runWith(cfgEnv, append(s.args, "--json")...)
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("coria %v: exit %d, standard error %q", s.args, res.code, res.stderr)
		}
		if err := os.WriteFile(filepath.Join(dir, s.name+".json"), []byte(res.stdout), 0o600); err != nil { //nolint:gosec // dir is the test's own scratch directory
			t.Fatal(err)
		}
	}
}
