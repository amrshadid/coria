package dir

import (
	"context"
	"io"
	"testing"

	"github.com/amrshadid/magnetar/internal/blob/blobtest"
)

// repeat yields n bytes by cycling through buf, so a benchmark measures the
// store and not the making of its input.
type repeat struct {
	buf  []byte
	off  int
	left int64
}

func (r *repeat) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n := copy(p, r.buf[r.off:])
	r.off = (r.off + n) % len(r.buf)
	r.left -= int64(n)
	return n, nil
}

var benchSizes = []struct {
	name string
	size int64
}{
	{"256KB", 256 << 10},
	{"256MB", 256 << 20},
}

// BenchmarkPut measures a Put that replaces one key, in MB/s, from the
// first byte to a synced file renamed into place and both directories of
// the rename synced: what a reception pays per file archived. The 256 MB
// case is skipped under -short.
func BenchmarkPut(b *testing.B) {
	buf := blobtest.PatternBytes(1, 1<<20)
	for _, c := range benchSizes {
		b.Run(c.name, func(b *testing.B) {
			if testing.Short() && c.size > 1<<20 {
				b.Skip("large objects are skipped under -short")
			}
			s := openTemp(b)
			ctx := context.Background()
			b.SetBytes(c.size)
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.Put(ctx, "bench/object.dcm", &repeat{buf: buf, left: c.size}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRead measures reading one whole object, in MB/s: what a
// verification or a download pays. The object is in the system's cache
// after the first round, so this is the store's own cost, not the disk's.
// The 256 MB case is skipped under -short.
func BenchmarkRead(b *testing.B) {
	buf := blobtest.PatternBytes(2, 1<<20)
	for _, c := range benchSizes {
		b.Run(c.name, func(b *testing.B) {
			if testing.Short() && c.size > 1<<20 {
				b.Skip("large objects are skipped under -short")
			}
			s := openTemp(b)
			ctx := context.Background()
			if _, err := s.Put(ctx, "bench/object.dcm", &repeat{buf: buf, left: c.size}); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(c.size)
			b.ResetTimer()
			for b.Loop() {
				r, err := s.Read(ctx, "bench/object.dcm", 0, -1)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, r); err != nil {
					b.Fatal(err)
				}
				if err := r.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
