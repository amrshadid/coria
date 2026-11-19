package receive

import (
	"testing"
	"time"
)

// benchReception hands files to a pipeline on SQLite and a directory
// storage, each n-th one from gen, and measures the time from the first
// landing to the reception verified and done.
func benchReception(b *testing.B, files int, pixelBytes int64) {
	g := newGenerator(b)
	var total int64
	var elapsed time.Duration
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := newEnv(b, envOptions{workers: 4})
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
// verified: a reception of 200 slices of 256 KiB, and one file of 256 MiB.
func BenchmarkLandToVerified(b *testing.B) {
	b.Run("slices-256KiB", func(b *testing.B) { benchReception(b, 200, 256<<10) })
	b.Run("one-256MiB", func(b *testing.B) { benchReception(b, 1, 256<<20) })
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
