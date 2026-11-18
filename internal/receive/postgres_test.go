package receive

import (
	"os"
	"testing"
)

// postgresEnv is the address of an empty Postgres schema for this package's
// end-to-end test (postgres://.../db?search_path=schema), which the test
// makes its tenant's database in; the schema must be empty, since a tenant's
// schema belongs to one tenant. requirePostgres turns a missing address from
// a skip into a failure.
const (
	postgresEnv     = "MAGNETAR_TEST_RECEIVE_POSTGRES"
	requirePostgres = "MAGNETAR_REQUIRE_POSTGRES"
)

// TestStudyIsArchivedOnPostgres guards the whole pipeline on the Postgres
// driver: the study is archived byte for byte, and the catalog's totals and
// the reception's counts agree with what was sent.
func TestStudyIsArchivedOnPostgres(t *testing.T) {
	address := os.Getenv(postgresEnv)
	if address == "" {
		if os.Getenv(requirePostgres) != "" {
			t.Fatalf("%s is not set, and %s is", postgresEnv, requirePostgres)
		}
		t.Skipf("%s is not set; the Postgres end-to-end test is skipped", postgresEnv)
	}
	e := newEnv(t, envOptions{postgres: address})
	e.send(studyFiles...)
	e.sendBytes([]byte("not a DICOM file"), Received)
	rs := e.settle(1)
	if !done(rs[0]) || rs[0].Counts.FilesArchived != 7 {
		t.Fatalf("reception %+v", rs[0])
	}
	checkArchive(t, e, rs[0], sentMap(t, studyFiles...))
	totals, err := e.store.CatalogTotals(e.ctx)
	if err != nil || totals.Instances != 7 || totals.Scans != 2 {
		t.Fatalf("totals %+v %v", totals, err)
	}
	if len(e.held()) != 1 || e.incomingFiles() != 1 {
		t.Fatalf("held %d, incoming %d", len(e.held()), e.incomingFiles())
	}
}
