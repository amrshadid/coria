package receive

import (
	"bytes"
	"encoding/binary"
	"io"
	"strconv"
	"testing"

	"github.com/amrshadid/magnetar/internal/receive/header"
)

// generator makes DICOM files for scale tests and benchmarks from one public
// file, MR_small.dcm: its header as it is, with its SOP Instance UID made
// distinct per file and its pixel data made the length asked for, filled
// with zeros, streamed so a file of any size costs no memory. Where the
// pixel data element is and how long its header is are read with go-dicom
// (package header); the bytes are only copied and counted.
type generator struct {
	prefix []byte // up to and including the pixel data element's VR field
	suffix []byte // the elements after the pixel data, as they are
	uid    []byte // the SOP Instance UID as it is in the file
	uidAt  []int  // where it is in prefix
}

// newGenerator reads MR_small.dcm and finds what it changes.
func newGenerator(t testing.TB) *generator {
	t.Helper()
	b := corpus(t, "MR_small.dcm")
	h, err := header.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	off := h.PixelData.Offset
	if vr := string(b[off+4 : off+6]); vr != "OW" && vr != "OB" {
		t.Fatalf("the pixel data's VR is %q, not a long-form OW or OB (PS3.5 7.1.2)", vr)
	}
	g := &generator{prefix: append([]byte{}, b[:off+8]...), suffix: b[off+h.PixelData.Length:]}
	u, _ := h.UID(header.SOPInstanceUID)
	g.uid = []byte(u)
	for i := 0; ; {
		j := bytes.Index(g.prefix[i:], g.uid)
		if j < 0 {
			break
		}
		g.uidAt = append(g.uidAt, i+j)
		i += j + len(g.uid)
	}
	if len(g.uidAt) != 2 {
		t.Fatalf("the SOP Instance UID is in the file %d times, want 2 (meta and data set)", len(g.uidAt))
	}
	return g
}

// file returns the n-th file (0 <= n < 9000) with pixelBytes bytes of pixel
// data (even), as a stream, and its size.
func (g *generator) file(n int, pixelBytes int64) (io.Reader, int64) {
	p := append([]byte{}, g.prefix...)
	// The UID's last four digits become 1000+n: same length, so no
	// element's length changes, and no component starts with a zero.
	suffix := []byte(strconv.Itoa(1000 + n))
	for _, at := range g.uidAt {
		copy(p[at+len(g.uid)-4:], suffix)
	}
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(pixelBytes)) // #nosec G115 -- a test's sizes are far below 4 GiB.
	p = append(p, l[:]...)
	size := int64(len(p)) + pixelBytes + int64(len(g.suffix))
	return io.MultiReader(bytes.NewReader(p), io.LimitReader(zeros{}, pixelBytes), bytes.NewReader(g.suffix)), size
}

// zeros yields zero bytes forever.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
