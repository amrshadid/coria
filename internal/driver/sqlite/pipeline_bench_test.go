package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// benchTime is the time the pipeline's benchmarks start at.
var benchTime = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

// benchTenant opens a migrated tenant database in tb's temporary directory,
// with a storage and a receiver, and returns it and its path.
func benchTenant(tb testing.TB) (*Tenant, string) {
	tb.Helper()
	ctx := context.Background()
	path := filepath.Join(tb.TempDir(), "tenant.db")
	s, err := OpenTenant(ctx, path, "bench")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		tb.Fatal(err)
	}
	audit := func(id string) master.AuditEntry {
		return master.AuditEntry{ID: id, Time: benchTime, Method: "Bench", Outcome: master.OutcomeDone}
	}
	if _, err := s.AddStorage(ctx, tenant.Storage{Name: "store", Type: tenant.StorageDirectory, Directory: tenant.DirectoryStorage{Root: "fast"}},
		benchTime, audit("a1"), nil); err != nil {
		tb.Fatal(err)
	}
	r := tenant.Receiver{Name: "dimse", Type: tenant.ReceiverDIMSE, Storage: "store", DIMSE: tenant.DIMSEReceiver{Port: 11112, AETitle: "ARCHIVE"},
		QuietPeriod: tenant.DefaultQuietPeriod, MaxOpenTime: tenant.DefaultMaxOpenTime, MaxFileBytes: tenant.DefaultMaxFileBytes}
	if _, err := s.AddReceiver(ctx, r, benchTime, audit("a2"), nil); err != nil {
		tb.Fatal(err)
	}
	return s, path
}

// benchFile returns received file n of reception r.
func benchFile(r tenant.Reception, n int) tenant.ReceivedFile {
	var sum tenant.SHA256
	for i := range sum {
		sum[i] = byte(n >> (8 * (i % 4)) & 0xff)
	}
	sum[31] = 1
	return tenant.ReceivedFile{
		ReceptionID: r.ID, SOPInstanceUID: fmt.Sprintf("2.25.3.%d", n+1), SeriesUID: "2.25.2.1", StudyUID: r.StudyUID,
		SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", TransferSyntaxUID: "1.2.840.10008.1.2.1", Size: 524288, SHA256: sum,
		Incoming: fmt.Sprintf("%s/%08d.dcm", r.ID, n), Caller: tenant.Caller{CallingAETitle: "SCANNER1", Address: "192.0.2.7:51234"},
		Association: "assoc1",
	}
}

// BenchmarkReceivedBatch measures recording received files in batches of
// 1, 100 and 1000 new files, each batch one transaction, as a receiver
// writes them; it reports rows a second.
func BenchmarkReceivedBatch(b *testing.B) {
	for _, size := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("rows=%d", size), func(b *testing.B) {
			s, _ := benchTenant(b)
			ctx := context.Background()
			r, _, err := s.OpenReception(ctx, "dimse", "2.25.1.1", "rc1", benchTime)
			if err != nil {
				b.Fatal(err)
			}
			batch := make([]tenant.ReceivedFile, size)
			n := 0
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for i := range batch {
					batch[i] = benchFile(r, n)
					n++
				}
				if _, err := s.AddReceivedFiles(ctx, batch, benchTime); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "rows/s")
		})
	}
}

// BenchmarkCheckDuplicates measures the duplicate check of batches of 1,
// 100 and 1000 files, half of them held, against a tenant that holds one
// million instances (1000 archived receptions of 1000 files). The table is
// built once, straight through SQL; skipped under -short.
func BenchmarkCheckDuplicates(b *testing.B) {
	if testing.Short() {
		b.Skip("builds a table of one million rows")
	}
	s, path := benchTenant(b)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		b.Fatal(err)
	}
	for _, stmt := range []string{
		`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 999)
		 INSERT INTO receptions SELECT 'held' || i, 'dimse', '2.25.1.' || (i + 1), 1, 'done', 1, 1000, 0, 1000, 0, 0, 0, 0, 0, 0,
		   'sub', 'ses' || i, 'store', '', i, i, i, i, 1, '[]', 0, '', '', '', NULL FROM n`,
		`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 999999)
		 INSERT INTO received_files SELECT 'held' || (i / 1000), '2.25.3.' || (i + 1), randomblob(32), '2.25.2.' || (i / 1000 + 1),
		   '2.25.1.' || (i / 1000 + 1), '1.2.840.10008.5.1.4.1.1.4', '1.2.840.10008.1.2.1', 524288, 'x/' || i, '', '', '', '', 0, 0 FROM n`,
		`ANALYZE`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			b.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for _, size := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("files=%d", size), func(b *testing.B) {
			queries := make([]tenant.DuplicateQuery, size)
			for i := range queries {
				// Every other file is an instance the tenant holds, spread
				// across the table; the others are new.
				n := i * 997 % 1000000
				if i%2 == 1 {
					n = 2000000 + i
				}
				queries[i] = tenant.DuplicateQuery{SOPInstanceUID: fmt.Sprintf("2.25.3.%d", n+1),
					StudyUID: fmt.Sprintf("2.25.1.%d", n/1000+1), SeriesUID: fmt.Sprintf("2.25.2.%d", n/1000+1), SHA256: tenant.SHA256{1}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := s.CheckDuplicates(ctx, queries)
				if err != nil {
					b.Fatal(err)
				}
				if got[0].Kind != tenant.DifferentFile {
					b.Fatalf("a held instance answered %d", got[0].Kind)
				}
			}
			b.ReportMetric(float64(size*b.N)/b.Elapsed().Seconds(), "files/s")
		})
	}
}

// BenchmarkReceptionStep measures one step of a reception (receiving to
// complete, compared and set, with a hook), one transaction each, from one
// caller; it reports steps a second.
func BenchmarkReceptionStep(b *testing.B) {
	s, _ := benchTenant(b)
	ctx := context.Background()
	ids := make([]string, b.N)
	for i := range ids {
		r, _, err := s.OpenReception(ctx, "dimse", fmt.Sprintf("2.25.1.%d", i+1), fmt.Sprintf("rc%d", i), benchTime)
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = r.ID
	}
	hook := func(context.Context, tenant.Reception, tenant.Reception) error { return nil }
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := s.StepReception(ctx, tenant.ReceptionStep{ID: ids[i], From: reception.Receiving, Event: reception.Quiet,
			At: benchTime.Add(time.Minute)}, hook); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "steps/s")
}

// BenchmarkClaimWork measures claiming a unit of work, one transaction
// each, from a table that also holds 100,000 finished units, which the
// claim's partial index leaves out; it reports claims a second.
func BenchmarkClaimWork(b *testing.B) {
	s, path := benchTenant(b)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 100000)
		INSERT INTO work SELECT 'done' || i, 'reception', 'rc' || i, NULL, 'done', i, 1, 10, '', '', 0, 0 FROM n`); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO work SELECT 'q' || i, 'reception', 'rc' || i, NULL, 'queued', i, 0, 10, '', '', 0, 0 FROM n`, b.N); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		b.Fatal(err)
	}
	if err := db.Close(); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := s.ClaimWork(ctx, "reception", "worker-1", benchTime, time.Minute); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "claims/s")
}
