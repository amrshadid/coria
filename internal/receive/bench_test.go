package receive

import (
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/testkit"
)

// backend is where a benchmark's tenant keeps its catalog and its bytes.
type backend struct {
	name string
	// opts returns the environment's options for one run, or skips b when
	// the service it needs is not there.
	opts func(b *testing.B) envOptions
}

// backends are the catalogs and storages a reception is measured on: SQLite
// with a directory always; Postgres and S3 when their services are named
// (testkit), each run on a schema or a prefix of its own.
var backends = []backend{
	{name: "sqlite-dir", opts: func(*testing.B) envOptions { return envOptions{} }},
	{name: "postgres-dir", opts: func(b *testing.B) envOptions {
		address, ok := testkit.PostgresSchema(b)
		if !ok {
			b.Skipf("%s is not set", testkit.PostgresEnv)
		}
		return envOptions{postgres: address}
	}},
	{name: "sqlite-s3", opts: func(b *testing.B) envOptions {
		store := openTestS3(b, "bench-"+newID()[:12]+"/")
		return envOptions{storage: func(blob.Store) blob.Store { return store }}
	}},
}

// benchReception hands files to a pipeline, each made by the generator, and
// measures the time from the first landing to the reception verified and
// done: files and MiB a second.
func benchReception(b *testing.B, be backend, files int, pixelBytes int64) {
	g := newGenerator(b)
	var total int64
	var elapsed time.Duration
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		opts := be.opts(b)
		opts.workers = 4
		e := newEnv(b, opts)
		e.deadline = 20 * time.Minute
		b.StartTimer()
		start := time.Now()
		for n := range files {
			r, size := g.File(n, pixelBytes)
			e.sendReader(r)
			total += size
		}
		e.clock.Advance(10 * time.Second)
		e.settle(1)
		elapsed += time.Since(start)
		b.StopTimer()
		_ = e.p.Stop(e.ctx)
		b.StartTimer()
	}
	sec := elapsed.Seconds()
	b.ReportMetric(float64(files*b.N)/sec, "files/s")
	b.ReportMetric(float64(total)/(1<<20)/sec, "MiB/s")
}

// BenchmarkLandToVerified measures files and bytes a second from landing to
// verified, on each backend: a reception of 200 slices of 256 KiB, and one
// file of 256 MiB. Its sqlite-dir case is the one CI holds against the base
// branch (make bench-gate).
func BenchmarkLandToVerified(b *testing.B) {
	for _, be := range backends {
		b.Run(be.name+"/slices-256KiB", func(b *testing.B) { benchReception(b, be, 200, 256<<10) })
		b.Run(be.name+"/one-256MiB", func(b *testing.B) { benchReception(b, be, 1, 256<<20) })
	}
}

// BenchmarkLand measures the time a sender waits for each acknowledgement:
// one file of 256 KiB landed, synced and answered.
func BenchmarkLand(b *testing.B) {
	g := newGenerator(b)
	e := newEnv(b, envOptions{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, size := g.File(i%8000, 256<<10)
		b.SetBytes(size)
		e.sendReader(r)
	}
}
