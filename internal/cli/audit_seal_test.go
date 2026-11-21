package cli_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// TestAuditVerify guards "audit verify": the summary for a person and the
// document with --json; a log verified in several calls followed to its end,
// each call from the seq the one before answered, with the values kept
// outside sent on each; a log with a problem listed with its kind and seq
// and exit 1 with one line; a digest that is not 64 hexadecimal digits
// refused before anything is sent; a tenant that does not exist.
func TestAuditVerify(t *testing.T) {
	r := newRig(t)
	r.must("tenant", "create", "lab1")

	expect(t, r.run("audit", "verify"), 0,
		"Log:             the installation's audit log\n"+
			"Intact:          yes\n"+
			"Checked:         12 entries, 3 checkpoints\n"+
			"Unsealed:        2 (sealed at the next pass; no verification covers them)\n"+
			"Last checkpoint: seq 9 to 12, 2026-11-22 12:00:00 +03:00\n"+
			"Its digest:      "+strings.Repeat("03", 32)+"\n", "")
	doc := document(t, r.run("audit", "verify", "--tenant", "lab1", "--json"))
	if doc["intact"] != true || doc["entriesChecked"] != "12" || doc["lastCheckpoint"].(map[string]any)["tenant"] != "lab1" {
		t.Errorf("verify --json: %v", doc)
	}

	// A long log: two answers that go on, then the last.
	r.fake.SetVerifyAnswer(nil, 2)
	before := len(r.fake.VerifyRequests())
	digest := strings.Repeat("ab", 32)
	res := r.run("audit", "verify", "--expect-seq", "40", "--expect-digest", digest, "--to", "100", "--json")
	reqs := r.fake.VerifyRequests()[before:]
	if res.code != 0 || len(reqs) != 3 || reqs[0].GetFromSeq() != 0 || reqs[1].GetFromSeq() != 5 || reqs[2].GetFromSeq() != 9 {
		t.Fatalf("a long log: exit %d, requests %v", res.code, reqs)
	}
	for _, q := range reqs {
		if q.GetExpectedMinSeq() != 40 || hex.EncodeToString(q.GetExpectedCheckpointDigest()) != digest || q.GetToSeq() != 100 || q.GetScope().GetInstallation() == nil {
			t.Errorf("a call of a long log asked %v", q)
		}
	}
	if d := document(t, res); d["entriesChecked"] != "20" || d["checkpointsChecked"] != "5" || d["nextSeq"] != "0" {
		t.Errorf("the calls summed: %v", d)
	}

	// A log with a problem.
	r.fake.SetVerifyAnswer(&v1.VerifyAuditResponse{Intact: false, EntriesChecked: 12, CheckpointsChecked: 3, Problems: []*v1.AuditVerifyProblem{
		{Kind: v1.AuditVerifyProblemKind_AUDIT_VERIFY_PROBLEM_KIND_CHAIN_BROKEN, Seq: 7, Detail: "the chain does not reach the checkpoint's head"},
	}}, 0)
	res = r.run("audit", "verify")
	if res.code != 1 || !strings.Contains(res.stdout, "Intact:          NO\n") ||
		!strings.Contains(res.stdout, "PROBLEM       SEQ  TIME  DETAIL\nchain-broken  7    -     the chain does not reach the checkpoint's head\n") ||
		res.stderr != "coria: the installation's audit log did not verify: 1 problems\n" {
		t.Errorf("a log with a problem: %+v", res)
	}
	for name, args := range map[string][]string{
		"short digest":   {"audit", "verify", "--expect-digest", "abcd"},
		"digest not hex": {"audit", "verify", "--expect-digest", strings.Repeat("zz", 32)},
		"an argument":    {"audit", "verify", "lab1"},
	} {
		if res := r.run(args...); res.code != 2 || res.stdout != "" || strings.Count(res.stderr, "\n") != 1 {
			t.Errorf("%s: %+v, want exit 2 and one line", name, res)
		}
	}
	if res := r.run("audit", "verify", "--tenant", "nope"); res.code != 4 || !strings.Contains(res.stderr, "no tenant has that name") {
		t.Errorf("a tenant that does not exist: %+v", res)
	}
}

// TestAuditCheckpoints guards "audit checkpoints": newest first in a table,
// the list document, paging with --limit and the note that more exist.
func TestAuditCheckpoints(t *testing.T) {
	r := newRig(t)
	r.must("tenant", "create", "lab1")
	expect(t, r.run("audit", "checkpoints"), 0,
		"FROM  TO  ENTRIES  TIME                        KEY  PRUNED  DIGEST\n"+
			"9     12  4        2026-11-22 12:00:00 +03:00  1    no      "+strings.Repeat("03", 32)+"\n"+
			"5     8   4        2026-11-21 12:00:00 +03:00  1    no      "+strings.Repeat("02", 32)+"\n"+
			"1     4   4        2026-11-20 12:00:00 +03:00  1    no      "+strings.Repeat("01", 32)+"\n", "")
	if cs := document(t, r.run("audit", "checkpoints", "--tenant", "lab1", "--json"))["checkpoints"].([]any); len(cs) != 3 || cs[0].(map[string]any)["tenant"] != "lab1" {
		t.Errorf("checkpoints --json: %v", cs)
	}
	res := r.run("audit", "checkpoints", "--limit", "2")
	if res.code != 0 || strings.Count(res.stdout, "\n") != 3 || !strings.Contains(res.stderr, "more exist; showing the first 2") {
		t.Errorf("--limit 2: %+v", res)
	}
}

// TestAuditExport guards "audit export": the file written whole as JSON
// lines, the header first, each line one object; the summary for a person
// and as a document; an existing file never replaced (exit 5) and no part
// file left; a range sent as asked; the lines on standard output without
// --out, where --json is refused.
func TestAuditExport(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "audit.jsonl")
	expect(t, r.run("audit", "export", "--out", out), 0,
		"File:        "+out+"\n"+
			"Log:         the installation's audit log (master), installation inst-fake-1\n"+
			"Exported:    seq 1 to 12: 12 entries, 3 checkpoints (0 pruned)\n"+
			"Last digest: "+strings.Repeat("03", 32)+" (keep it outside the installation, for audit verify --expect-digest)\n", "")
	b, err := os.ReadFile(out) //nolint:gosec // G304: the test's own file.
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 1+12+3 {
		t.Fatalf("%d lines", len(lines))
	}
	kinds := ""
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil || len(m) != 1 {
			t.Fatalf("a line that is not one message: %q (%v)", l, err)
		}
		for k := range m {
			kinds += k[:1]
		}
	}
	if kinds != "heeeeceeeeceeeec" {
		t.Errorf("the messages in order: %s", kinds)
	}
	if info, err := os.Stat(out); err != nil || (info.Mode().Perm()&0o077 != 0 && !strings.Contains(strings.ToLower(os.Getenv("OS")), "windows")) {
		t.Errorf("the export's mode: %v %v", info, err)
	}
	if res := r.run("audit", "export", "--out", out); res.code != 5 || !strings.Contains(res.stderr, "never replaces a file") {
		t.Errorf("an existing file: %+v", res)
	}
	if _, err := os.Stat(out + ".part"); !os.IsNotExist(err) {
		t.Errorf("a part file was left: %v", err)
	}
	second := filepath.Join(dir, "range.jsonl")
	doc := document(t, r.run("audit", "export", "--from", "6", "--to", "7", "--out", second, "--json"))
	if doc["fromSeq"] != float64(5) || doc["toSeq"] != float64(8) || doc["entries"] != float64(4) || doc["checkpoints"] != float64(1) || doc["file"] != second {
		t.Errorf("a range: %v", doc)
	}
	res := r.run("audit", "export")
	if res.code != 0 || res.stderr != "" || strings.Count(res.stdout, "\n") != 16 || !strings.HasPrefix(res.stdout, `{"header":{`) {
		t.Errorf("to standard output: %+v", res)
	}
	if res := r.run("audit", "export", "--json"); res.code != 2 || res.stdout != "" {
		t.Errorf("--json to standard output: %+v", res)
	}
}

// TestAuditPublicKey guards "audit public-key": the PEM of the key that
// signs now and nothing else, so the output is a key file as it is; a
// version the server does not hold (exit 4); --installation alone refused;
// every key in the --json document.
func TestAuditPublicKey(t *testing.T) {
	r := newRig(t)
	res := r.run("audit", "public-key")
	if res.code != 0 || res.stderr != "" || !strings.HasPrefix(res.stdout, "-----BEGIN PUBLIC KEY-----\n") || !strings.HasSuffix(res.stdout, "-----END PUBLIC KEY-----\n") ||
		strings.Count(res.stdout, "BEGIN") != 1 {
		t.Errorf("public-key: %+v", res)
	}
	if v1res := r.run("audit", "public-key", "--version", "1"); v1res.stdout != res.stdout {
		t.Errorf("--version 1 differs from the current key: %q", v1res.stdout)
	}
	if res := r.run("audit", "public-key", "--version", "2"); res.code != 4 || res.stdout != "" {
		t.Errorf("a version not held: %+v", res)
	}
	if res := r.run("audit", "public-key", "--installation", "other"); res.code != 2 {
		t.Errorf("--installation alone: %+v", res)
	}
	doc := document(t, r.run("audit", "public-key", "--json"))
	if ks := doc["publicKeys"].([]any); len(ks) != 1 || doc["installationId"] != "inst-fake-1" || ks[0].(map[string]any)["current"] != true {
		t.Errorf("public-key --json: %v", doc)
	}
}
