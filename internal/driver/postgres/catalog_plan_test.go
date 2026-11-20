package postgres

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/driver/sqlkit"
	"github.com/amrshadid/coria/internal/testkit"
)

// catalogIndexes are the names Postgres gives the catalog's unique
// constraints, as the schema names them.
var catalogIndexes = testkit.CatalogIndexes{SubjectsFolded: "subjects_folded_key", SessionsFolded: "sessions_folded_key",
	ScansFolded: "scans_folded_key", InstancesKey: "instances_pkey"}

// fillCatalog writes a catalog of subjects subjects, five sessions each and
// six scans a session with two instances each, straight through SQL into a
// migrated tenant schema, with the values the reads filter by spread as a
// real tenant's are, and analyzes it.
func fillCatalog(tb testing.TB, conn *pgx.Conn, subjects int) {
	tb.Helper()
	sessions, scans := subjects*5, subjects*30
	at := func(expr string) string {
		return `timestamptz 'epoch' + (` + expr + `)::bigint * interval '1 microsecond'`
	}
	subject := `'sub-' || lpad(((i - 1) / 5 + 1)::text, 6, '0')`
	scanSubject := `'sub-' || lpad(((i - 1) / 30 + 1)::text, 6, '0')`
	scanSession := `'ses-' || (((i - 1) / 6) % 5)`
	description := `CASE i % 5 WHEN 0 THEN 'T1 MPRAGE ' || i WHEN 1 THEN 'Localizer' WHEN 2 THEN 'DWI b1000' WHEN 3 THEN '' ELSE 'Scout ' || i END`
	mustExec(tb, conn,
		`SET maintenance_work_mem = '256MB'`,
		fmt.Sprintf(`INSERT INTO subjects (id, label, folded, created, updated, session_count, instance_count, bytes) OVERRIDING SYSTEM VALUE
		 SELECT i, 'sub-' || lpad(i::text, 6, '0'), 'sub-' || lpad(i::text, 6, '0'), %s, %s, 5, 60, 60000 FROM generate_series(1::bigint, %d) i`,
			at("i * 5000000"), at("i * 5000000"), subjects),
		fmt.Sprintf(`INSERT INTO sessions (id, study_uid, subject_id, label, folded, storage, created, subject_label, subject_folded, study_date,
		   receiver, scan_count, instance_count, bytes, updated) OVERRIDING SYSTEM VALUE
		 SELECT i, '2.25.1.' || i, (i - 1) / 5 + 1, 'ses-' || ((i - 1) %% 5), 'ses-' || ((i - 1) %% 5), 'store', %s, %s, %s,
		   CASE WHEN i %% 50 = 0 THEN '' ELSE (2010 + i %% 15)::text || lpad((i %% 12 + 1)::text, 2, '0') || lpad((i %% 28 + 1)::text, 2, '0') END,
		   'recv' || (i %% 3), 6, 12, 12000, %s FROM generate_series(1::bigint, %d) i`,
			at("i * 1000000"), subject, subject, at("i * 1000000"), sessions),
		fmt.Sprintf(`INSERT INTO scans (id, session_id, series_uid, label, folded, instances, bytes, manifest_sha256, created, updated,
		   subject_label, subject_folded, session_label, session_folded, storage, modality, series_description, description_folded,
		   series_number, receiver) OVERRIDING SYSTEM VALUE
		 SELECT i, (i - 1) / 6 + 1, '2.25.2.' || i, 'scan-' || ((i - 1) %% 6), 'scan-' || ((i - 1) %% 6), 2, 2000, NULL, %s, %s,
		   %s, %s, %s, %s, 'store', CASE i %% 4 WHEN 0 THEN 'MR' WHEN 1 THEN 'CT' WHEN 2 THEN 'PT' ELSE 'SR' END, %s, lower(%s),
		   (i - 1) %% 6 + 1, 'recv' || (((i - 1) / 6 + 1) %% 3) FROM generate_series(1::bigint, %d) i`,
			at("((i - 1) / 6 + 1) * 1000000 + i"), at("((i - 1) / 6 + 1) * 1000000 + i"), scanSubject, scanSubject, scanSession, scanSession,
			description, description, scans),
		`INSERT INTO session_modalities (session_id, modality, created)
		 SELECT DISTINCT c.session_id, c.modality, s.created FROM scans AS c JOIN sessions AS s ON s.id = c.session_id`,
		fmt.Sprintf(`INSERT INTO instances SELECT '2.25.3.' || i, '2.25.1.1', '2.25.2.1', 'held', sha256(i::text::bytea), 'rc', (i - 1) / 2 + 1,
		   'store', 'k' || i, 0, 1000, 1000, sha256(i::text::bytea), now(), now() FROM generate_series(1::bigint, %d) i`, scans*2),
		`ANALYZE`)
}

// sampleCatalog reads the plan checks' arguments from the catalog: the
// rows of the scan in the middle of its table, and of its session and
// subject.
func sampleCatalog(tb testing.TB, conn *pgx.Conn) testkit.CatalogSample {
	tb.Helper()
	ctx := context.Background()
	var v testkit.CatalogSample
	var created, sesCreated, subCreated time.Time
	var n int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM scans`).Scan(&n); err != nil {
		tb.Fatal(err)
	}
	err := conn.QueryRow(ctx, `SELECT c.id, c.subject_label, c.session_label, c.label, c.subject_folded, c.session_folded, c.folded,
		c.description_folded, c.created, c.modality, c.receiver, s.id, s.created, s.subject_id, s.study_date, s.folded, sub.created,
		(SELECT min(sop_instance_uid) FROM instances WHERE scan_id = c.id AND state = 'held')
		FROM scans AS c JOIN sessions AS s ON s.id = c.session_id JOIN subjects AS sub ON sub.id = s.subject_id
		WHERE c.id >= $1 AND c.modality <> '' AND s.study_date <> '' ORDER BY c.id LIMIT 1`, n/2).Scan(
		&v.ScanID, &v.Scan.Subject, &v.Scan.Session, &v.Scan.Scan, &v.ScanKey.SubjectFolded, &v.ScanKey.SessionFolded, &v.ScanKey.Folded,
		&v.ScanKey.DescriptionFolded, &created, &v.Modality, &v.Receiver, &v.SessionID, &sesCreated, &v.SubjectID, &v.StudyDate,
		&v.SessionKey.Folded, &subCreated, &v.UID)
	if err != nil {
		tb.Fatal(err)
	}
	v.Subject, v.Session = v.Scan.Subject, [2]string{v.Scan.Subject, v.Scan.Session}
	v.ScanKey.ID, v.ScanKey.Created = v.ScanID, created.UTC()
	v.SessionKey.ID, v.SessionKey.Created, v.SessionKey.SubjectFolded, v.SessionKey.StudyDate = v.SessionID, sesCreated.UTC(),
		label.Fold(v.Subject), v.StudyDate
	v.SubjectKey = sqlkit.CatalogKey{ID: v.SubjectID, Created: subCreated.UTC(), Folded: label.Fold(v.Subject)}
	return v
}

// pgPlanFault returns why a Postgres plan of a catalog read is not one
// range of an index read in the list's order, or "": a sequential scan of
// any table, an explicit sort (Sort or Incremental Sort), or for a page
// after a cursor (paged) an index scan with no Index Cond, which walks its
// index from one end. The index named for the read is not required: where
// a filter keeps a third of the rows (one of three receivers), Postgres
// rightly reads the time index in its order and drops the others, a few
// rows a page, and an instance of a scan is found as well by the scan's
// index on both columns as by the primary key; usesIndex reports it.
func pgPlanFault(plan string, paged bool) string {
	lines := strings.Split(plan, "\n")
	for i, line := range lines {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "->"))
		switch {
		case strings.HasPrefix(l, "Seq Scan"):
			return "it reads a table whole: " + l
		case strings.HasPrefix(l, "Sort") || strings.HasPrefix(l, "Incremental Sort"):
			return "it sorts rows instead of reading them in order: " + l
		case paged && (strings.HasPrefix(l, "Index Scan") || strings.HasPrefix(l, "Index Only Scan")):
			cond := false
			for _, next := range lines[i+1:] {
				n := strings.TrimSpace(next)
				if strings.HasPrefix(n, "->") {
					break
				}
				if strings.HasPrefix(n, "Index Cond:") {
					cond = true
				}
			}
			if !cond {
				return "a page after a cursor walks its index from one end, with no Index Cond: " + l
			}
		}
	}
	return ""
}

// usesIndex reports whether plan reads index.
func usesIndex(plan, index string) bool {
	return strings.Contains(plan, " using "+index+" ") || strings.Contains(plan, " on "+index+" ")
}

// TestCatalogPlans guards that every read of the catalog is a range of the
// index named for it in the order the list answers, on a catalog of tens of
// thousands of subjects and hundreds of thousands of scans, analyzed: for
// each statement the driver sends, the plan Postgres makes for the values
// used (EXPLAIN ANALYZE) and its generic plan (the one a prepared statement
// keeps) are checked (pgPlanFault), and a read of another index than the
// one named for it is logged. The statements of a modality's
// sessions read by id and of a page's modalities are checked too.
func TestCatalogPlans(t *testing.T) {
	skipScale(t)
	address := newSchema(t)
	openTenantAt(t, address, "plans")
	conn := admin(t, address)
	began := time.Now()
	fillCatalog(t, conn, 10000)
	t.Logf("built in %v", time.Since(began))
	checkCatalogPlans(t, conn, sampleCatalog(t, conn), 0)
}

// fromTable matches the table a catalog read reads.
var fromTable = regexp.MustCompile(` FROM ([a-z_]+)`)

// tableRows returns the rows of the table a statement reads, as the
// planner's statistics count them.
func tableRows(t *testing.T, conn *pgx.Conn, statement string) int64 {
	t.Helper()
	m := fromTable.FindStringSubmatch(statement)
	if m == nil {
		t.Fatalf("no table in %s", statement)
	}
	var rows float64
	if err := conn.QueryRow(context.Background(), `SELECT reltuples FROM pg_class WHERE oid = $1::regclass`, m[1]).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return int64(rows)
}

// checkCatalogPlans checks both plans of every catalog read with arguments
// from v (testkit.CatalogPlanCases and the two statements of this driver's
// own) and logs them. A read of a table of fewer than strictRows rows has
// its faults logged, not failed: reading a few thousand rows whole and
// sorting them is the planner's right choice, as a tenant of ten million
// instances has only that many subjects and sessions; TestCatalogPlans
// checks those reads at a size where it is not.
func checkCatalogPlans(t *testing.T, conn *pgx.Conn, v testkit.CatalogSample, strictRows int64) {
	t.Helper()
	cases := testkit.CatalogPlanCases(catalogSQL, v, catalogIndexes, 101)
	ids := []int64{v.SessionID, v.SessionID + 1, v.SessionID + 2}
	cases = append(cases,
		testkit.CatalogPlanCase{Name: "a modality's sessions by id", Read: sqlkit.CatalogRead{SQL: sessionsByIDSQL, Args: []any{ids}},
			Index: "sessions_pkey", Paged: true},
		testkit.CatalogPlanCase{Name: "a page's modalities", Read: sqlkit.CatalogRead{SQL: modalitiesSQL, Args: []any{ids}},
			Index: "session_modalities_pkey", Paged: true})
	for _, c := range cases {
		for _, generic := range []bool{false, true} {
			plan := planOf(t, connQuerier{conn}, generic, c.Read.SQL, c.Read.Args...)
			what := c.Name
			if generic {
				what += " (generic)"
			}
			t.Logf("%s:\n%s", what, plan)
			if why := pgPlanFault(plan, c.Paged); why != "" {
				if rows := tableRows(t, conn, c.Read.SQL); rows < strictRows {
					t.Logf("%s, a table of %d rows: %s", what, rows, why)
				} else {
					t.Errorf("%s: %s", what, why)
				}
			}
			if !usesIndex(plan, c.Index) {
				t.Logf("%s reads another index than %s", what, c.Index)
			}
		}
	}
}
