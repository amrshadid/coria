package sqlite

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// fillPipeline writes many rows into every pipeline table of a migrated
// tenant database at path, straight through SQL, and runs ANALYZE, so the
// planner chooses as it would on a tenant that has worked for a while.
func fillPipeline(tb testing.TB, path string) {
	tb.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 200)
		 INSERT INTO storages SELECT 'store' || i, 'directory', 'fast', 'd' || i, '', '', '', '', 0, '', NULL, 0, 0, 0 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 200)
		 INSERT INTO receivers SELECT 'recv' || i, 'dimse', 1, 'store' || i, 11000 + i, '', '{}', '', 60000000, 3600000000, 4294967296, 0, 0 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 20000)
		 INSERT INTO receptions SELECT 'rc' || i, 'recv' || (i % 200 + 1),
		   CASE i % 50 WHEN 0 THEN '2.25.9.' || i ELSE '2.25.1.' || (i % 5000 + 1) END, i / 5000 + 1,
		   CASE i % 50 WHEN 0 THEN 'receiving' WHEN 1 THEN 'complete' WHEN 2 THEN 'archived' WHEN 3 THEN 'failed' ELSE 'done' END,
		   CASE WHEN i % 50 >= 3 THEN 1 ELSE 0 END,
		   0, 0, 0, 0, 0, 0, 0, 0, 0, '', '', CASE WHEN i % 50 >= 2 THEN 'store' || (i % 200 + 1) ELSE '' END, '',
		   i * 1000000, i * 1000000, i * 1000000, NULL, 0, '[]', 0, '', '', '', NULL FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 100000)
		 INSERT INTO received_files SELECT 'rc' || (i % 20000 + 1), '2.25.3.' || i, randomblob(32), '2.25.2.1', '2.25.1.1',
		   '1.2.840.10008.5.1.4.1.1.4', '1.2.840.10008.1.2.1', 1000, 'x/' || i, '', '', '', '', 0, 0 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 20000)
		 INSERT INTO quarantine SELECT 'q' || i, CASE i % 3 WHEN 0 THEN 'not_dicom' WHEN 1 THEN 'bad_uid' ELSE 'different_file' END,
		   '{}', CASE i % 4 WHEN 0 THEN 'held' WHEN 1 THEN 'released' ELSE 'rejected' END, 'recv' || (i % 200 + 1), 'rc' || i,
		   '', '', '', 10, NULL, NULL, '', '2.25.3.' || i, '2.25.1.' || (i % 5000 + 1), '', '', '', 0, i * 1000000, NULL,
		   '', '', '', '', '' FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 100000)
		 INSERT INTO work SELECT 'w' || i, 'reception', 'rc' || i, NULL, 'done', i, 1, 10, '', '', 0, 0 FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 100)
		 INSERT INTO work SELECT 'live' || i, 'reception', 'rc' || i, 'key' || i, 'queued', i, 0, 10, '', '', 0, 0 FROM n`,
		`ANALYZE`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			tb.Fatalf("%s: %v", stmt, err)
		}
	}
}

// queryPlan returns SQLite's plan of query, one line per step.
func queryPlan(tb testing.TB, db *sql.DB, query string, args ...any) []string {
	tb.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		tb.Fatalf("%s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	return plan
}

// TestPipelinePlans guards that every list, claim and lookup of the
// pipeline is answered from an index, never by reading a whole table: it
// fills every table, runs ANALYZE, and checks SQLite's plan of the very
// statement the driver runs for a step that reads no table without an
// index, and that the index named for it is the one used.
func TestPipelinePlans(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tenant.db")
	s, err := OpenTenant(ctx, path, "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fillPipeline(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	after := []any{int64(5000000), "rc5"}
	rq := func(f tenant.ReceptionFilter) (string, []any) { return receptionsQuery(f, after, 101) }
	qq := func(f tenant.QuarantineFilter) (string, []any) { return quarantineQuery(f, after, 101) }
	cases := []struct {
		name, index string
		query       string
		args        []any
	}{
		{"ListStorages", "sqlite_autoindex_storages_1", listStoragesSQL, []any{"store5", 101}},
		{"ListReceivers", "sqlite_autoindex_receivers_1", listReceiversSQL, []any{"recv5", 101}},
		{"a storage in use", "receptions_by_storage", storageUsedSQL, []any{"store5"}},
		{"a receiver of a storage", "receivers_by_storage", receiverOfStorageSQL, []any{"store5"}},
		{"the receivers on a port", "receivers_by_port", receiversOnPortSQL, []any{11005, "recv5"}},
		{"a receiver named by a reception", "receptions_by_receiver", receptionNamesReceiverSQL, []any{"recv5"}},
		{"a receiver named by a quarantined file", "quarantine_by_receiver", quarantineNamesReceiverSQL, []any{"recv5"}},
		{"OpenReception", "receptions_open", openReceptionSQL, []any{"recv5", "2.25.1.5"}},
		{"the next arrival", "receptions_arrival", nextArrivalSQL, []any{"2.25.1.5"}},
		{"a study's arrivals", "receptions_arrival", arrivalsSQL, []any{"2.25.1.5", 100}},
		{"ReceivedFiles", "PRIMARY KEY", listReceivedSQL, []any{"rc5", "", []byte{}, 101}},
		{"CheckDuplicates", "received_by_instance", duplicatesSQL, []any{`["2.25.3.5","2.25.3.6"]`}},
		{"ClaimWork", "work_claimable", claimNextSQL, []any{"reception", int64(50), claimCandidates}},
		{"a unit by its key", "work_by_key", workByKeySQL, []any{"key5"}},
	}
	for _, c := range []struct {
		name, index string
		f           tenant.ReceptionFilter
	}{
		{"ListReceptions", "receptions_by_start", tenant.ReceptionFilter{}},
		{"ListReceptions by time", "receptions_by_start", tenant.ReceptionFilter{Since: fromMicros(1000000), Until: fromMicros(9000000000)}},
		{"ListReceptions by receiver", "receptions_by_receiver", tenant.ReceptionFilter{Receiver: "recv5"}},
		{"ListReceptions by study", "receptions_by_study", tenant.ReceptionFilter{StudyUID: "2.25.1.5"}},
		{"ListReceptions by step", "receptions_by_state", tenant.ReceptionFilter{States: []reception.State{reception.Failed}}},
		{"ListReceptions by steps", "receptions_by_state", tenant.ReceptionFilter{States: []reception.State{reception.Failed, reception.Archived}}},
		{"ListReceptions in progress", "receptions_in_progress", tenant.ReceptionFilter{InProgress: true}},
	} {
		q, a := rq(c.f)
		cases = append(cases, struct {
			name, index string
			query       string
			args        []any
		}{c.name, c.index, q, a})
	}
	for _, c := range []struct {
		name, index string
		f           tenant.QuarantineFilter
	}{
		{"ListQuarantined", "quarantine_by_time", tenant.QuarantineFilter{}},
		{"ListQuarantined by state", "quarantine_by_state", tenant.QuarantineFilter{State: tenant.QuarantineHeld}},
		{"ListQuarantined by reason", "quarantine_by_reason", tenant.QuarantineFilter{Reason: tenant.QuarantineBadUID}},
		{"ListQuarantined by receiver", "quarantine_by_receiver", tenant.QuarantineFilter{Receiver: "recv5"}},
		{"ListQuarantined by reception", "quarantine_by_reception", tenant.QuarantineFilter{ReceptionID: "rc5"}},
		{"ListQuarantined by study", "quarantine_by_study", tenant.QuarantineFilter{StudyUID: "2.25.1.5"}},
		{"ListQuarantined by instance", "quarantine_by_instance", tenant.QuarantineFilter{SOPInstanceUID: "2.25.3.5"}},
	} {
		q, a := qq(c.f)
		cases = append(cases, struct {
			name, index string
			query       string
			args        []any
		}{c.name, c.index, q, a})
	}
	for _, c := range cases {
		plan := queryPlan(t, db, c.query, c.args...)
		text := strings.Join(plan, "\n")
		t.Logf("%s:\n%s", c.name, text)
		for _, line := range plan {
			// A scan of a subquery's rows (each bounded by its own LIMIT) or
			// of json_each's array is not a scan of a table.
			if strings.HasPrefix(line, "SCAN ") && !strings.Contains(line, "USING ") && !strings.Contains(line, "json_each") &&
				!strings.HasPrefix(line, "SCAN (subquery") && !strings.Contains(line, "CONSTANT ROW") {
				t.Errorf("%s reads a whole table: %q", c.name, line)
			}
		}
		if !strings.Contains(text, " "+c.index) {
			t.Errorf("%s does not use %s", c.name, c.index)
		}
	}
}

// TestPipelineSchemaWithSQLite guards the pipeline's schema as SQLite's own
// command line tool, not this package, reads it from a migrated tenant
// database: every table is there, STRICT, with the indexes the listings and
// claims rely on, and the file passes SQLite's integrity check. It is
// skipped without the tool (SQLITE3 names one, or it is looked up on PATH),
// unless MAGNETAR_SQLITE3_REQUIRED is set.
func TestPipelineSchemaWithSQLite(t *testing.T) {
	tool := os.Getenv("SQLITE3")
	if tool == "" {
		tool = "sqlite3"
	}
	tool, err := exec.LookPath(tool)
	if err != nil {
		if os.Getenv("MAGNETAR_SQLITE3_REQUIRED") != "" {
			t.Fatalf("the sqlite3 tool is required: %v", err)
		}
		t.Skipf("the sqlite3 tool is not here: %v", err)
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tenant.db")
	s, err := OpenTenant(ctx, path, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	run := func(query string) string {
		t.Helper()
		out, err := exec.Command(tool, "-batch", "-noheader", path, query).CombinedOutput() //nolint:gosec // G204, G702: the tool and the database are this test's own.
		if err != nil {
			t.Fatalf("sqlite3 %q: %v: %s", query, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Logf("schema:\n%s", run(".schema"))
	if got := run("PRAGMA integrity_check"); got != "ok" {
		t.Errorf("integrity check: %s", got)
	}
	tables := run(`SELECT name || ' ' || strict FROM pragma_table_list WHERE schema = 'main' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	for _, want := range []string{"storages 1", "receivers 1", "receptions 1", "reception_associations 1", "received_files 1",
		"quarantine 1", "quarantine_totals 1", "work 1"} {
		if !strings.Contains("\n"+tables+"\n", "\n"+want+"\n") {
			t.Errorf("sqlite3 shows no STRICT table %q among:\n%s", want, tables)
		}
	}
	indexes := run(`SELECT name FROM sqlite_schema WHERE type = 'index' AND sql IS NOT NULL ORDER BY name`)
	for _, want := range []string{"storages_by_root", "storages_by_bucket", "receivers_by_storage", "receivers_by_port",
		"receptions_arrival", "receptions_open", "receptions_by_start", "receptions_by_receiver", "receptions_by_study",
		"receptions_by_state", "receptions_in_progress", "receptions_by_storage", "received_by_instance", "quarantine_by_time",
		"quarantine_by_state", "quarantine_by_reason", "quarantine_by_receiver", "quarantine_by_reception",
		"quarantine_by_study", "quarantine_by_instance", "work_by_key", "work_claimable"} {
		if !strings.Contains("\n"+indexes+"\n", "\n"+want+"\n") {
			t.Errorf("sqlite3 shows no index %s", want)
		}
	}
	if got := run(`SELECT files || ' ' || bytes FROM quarantine_totals`); got != "0 0" {
		t.Errorf("the quarantine's totals start at %q, want 0 0", got)
	}
	plan := run(`EXPLAIN QUERY PLAN ` + strings.ReplaceAll(strings.ReplaceAll(claimNextSQL, "?", "1"), "kind = 1", "kind = 'reception'"))
	if !strings.Contains(plan, "work_claimable") {
		t.Errorf("sqlite3's plan of a claim does not use work_claimable:\n%s", plan)
	}
}
