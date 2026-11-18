package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/master/mastertest"
)

// BenchmarkKeyByID measures the first read of every request with a key.
func BenchmarkKeyByID(b *testing.B) { mastertest.BenchKeyByID(b, openTest) }

// BenchmarkGrantsByKey measures the second read of every request with a key.
func BenchmarkGrantsByKey(b *testing.B) { mastertest.BenchGrantsByKey(b, openTest) }

// BenchmarkRolesFor measures the access check's read of a key's roles.
func BenchmarkRolesFor(b *testing.B) { mastertest.BenchRolesFor(b, openTest) }

// BenchmarkAppendAudit measures the audit write every request ends with.
func BenchmarkAppendAudit(b *testing.B) { mastertest.BenchAppendAudit(b, openTest) }

// BenchmarkMarkKeyUsedNoWrite measures marking a busy key used within the
// minute, which writes nothing.
func BenchmarkMarkKeyUsedNoWrite(b *testing.B) { mastertest.BenchMarkKeyUsedNoWrite(b, openTest) }

// benchCallers are the numbers of concurrent callers the change benchmarks
// measure: one alone, which must not wait for company, and enough at once
// that changes can share a commit.
var benchCallers = []int{1, 8, 64, 256}

// benchChanges runs change from each of callers goroutines until b.N changes
// are made, for each number of benchCallers, and reports changes a second
// and the mean time a caller waits for its change to be answered. With one
// caller, ns/op is the latency of a change alone. change is given a number
// no other call in the benchmark is given, for unique audit ids.
func benchChanges(b *testing.B, change func(ctx context.Context, n int64) error) {
	var seq atomic.Int64
	for _, callers := range benchCallers {
		b.Run(fmt.Sprintf("callers=%d", callers), func(b *testing.B) {
			ctx := context.Background()
			var done, waited atomic.Int64
			var wg sync.WaitGroup
			errs := make(chan error, callers)
			b.ReportAllocs()
			b.ResetTimer()
			start := time.Now()
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for done.Add(1) <= int64(b.N) {
						t := time.Now()
						if err := change(ctx, seq.Add(1)); err != nil {
							errs <- err
							return
						}
						waited.Add(int64(time.Since(t)))
					}
				}()
			}
			wg.Wait()
			elapsed := time.Since(start)
			b.StopTimer()
			close(errs)
			for err := range errs {
				b.Fatal(err)
			}
			b.ReportMetric(float64(b.N)/elapsed.Seconds(), "changes/s")
			b.ReportMetric(float64(waited.Load())/float64(b.N)/1e6, "ms-wait/change")
		})
	}
}

// settingChange returns a change that sets one of 512 settings with its
// audit entry, the shape of every change: a row and the entry that records
// it, in one transaction.
func settingChange(set func(ctx context.Context, name, value string, audit master.AuditEntry) error) func(context.Context, int64) error {
	return func(ctx context.Context, n int64) error {
		a := master.AuditEntry{ID: fmt.Sprintf("bench-%09d", n), Time: mastertest.T0, Method: "Bench", Outcome: master.OutcomeDone}
		return set(ctx, fmt.Sprintf("bench.%03d", n%512), "v", a)
	}
}

// BenchmarkMasterChanges measures changes to the master, each a setting with
// its audit entry, from 1, 8, 64 and 256 concurrent callers.
func BenchmarkMasterChanges(b *testing.B) {
	benchChanges(b, settingChange(openTest(b).SetSetting))
}

// BenchmarkWriteMachinery measures what the write path itself costs a
// caller alone, apart from the disk: a transaction that writes nothing, so
// that its commit has nothing to sync.
func BenchmarkWriteMachinery(b *testing.B) {
	m := openTest(b).(*Master)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := m.writeTx(ctx, "bench", func(*sql.Tx) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
