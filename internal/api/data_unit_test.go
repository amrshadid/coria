package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"slices"
	"strconv"
	"testing"
	"testing/iotest"

	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/tenant"
)

// TestOpenReadsBounds guards the bounds of open reads: a caller's 17th is
// refused as resource exhausted while another caller's first is not; one
// ended frees its place, once however often its end is called; and the
// server's 257th is refused whoever asks.
func TestOpenReadsBounds(t *testing.T) {
	var o openReads
	var ends []func()
	for range MaxOpenReads {
		end, err := o.take("key/a")
		if err != nil {
			t.Fatal(err)
		}
		ends = append(ends, end)
	}
	if _, err := o.take("key/a"); fault.KindOf(err) != fault.Exhausted {
		t.Fatalf("the 17th read of one caller: %v, want exhausted", err)
	}
	endB, err := o.take("key/b")
	if err != nil {
		t.Fatalf("another caller's first read: %v", err)
	}
	ends[0]()
	ends[0]()
	if _, err := o.take("key/a"); err != nil {
		t.Fatalf("a read after one ended: %v", err)
	}
	if _, err := o.take("key/a"); fault.KindOf(err) != fault.Exhausted {
		t.Fatalf("an end called twice freed two places: %v", err)
	}
	endB()
	var all openReads
	for i := range MaxServerReads {
		if _, err := all.take("key/" + strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := all.take("local"); fault.KindOf(err) != fault.Exhausted {
		t.Fatalf("the server's 257th read: %v, want exhausted", err)
	}
}

// TestByteRangeOf guards the reading of a Range header against a body of
// 1000 bytes and of none, as RFC 9110 14 and the contract say.
func TestByteRangeOf(t *testing.T) {
	for _, c := range []struct {
		h           string
		size        int64
		kind        rangeKind
		first, last int64
	}{
		{"bytes=0-0", 1000, rangeOne, 0, 0},
		{"bytes=0-", 1000, rangeOne, 0, 999},
		{"bytes=10-20", 1000, rangeOne, 10, 20},
		{"bytes=990-5000", 1000, rangeOne, 990, 999},
		{"bytes=-1", 1000, rangeOne, 999, 999},
		{"bytes=-2000", 1000, rangeOne, 0, 999},
		{"bytes= 5-6 ", 1000, rangeOne, 5, 6},
		{"Bytes=0005-0006", 1000, rangeOne, 5, 6},
		{"bytes=999-", 1000, rangeOne, 999, 999},
		{"bytes=1000-", 1000, rangeUnsatisfiable, 0, 0},
		{"bytes=1000-2000", 1000, rangeUnsatisfiable, 0, 0},
		{"bytes=-0", 1000, rangeUnsatisfiable, 0, 0},
		{"bytes=0-", 0, rangeUnsatisfiable, 0, 0},
		{"bytes=-5", 0, rangeUnsatisfiable, 0, 0},
		{"bytes=99999999999999999999999-", 1000, rangeUnsatisfiable, 0, 0},
		{"bytes=0-99999999999999999999999", 1000, rangeOne, 0, 999},
		{"", 1000, rangeNone, 0, 0},
		{"bytes=0-1,3-4", 1000, rangeNone, 0, 0},
		{"bytes=5-4", 1000, rangeNone, 0, 0},
		{"bytes=-", 1000, rangeNone, 0, 0},
		{"bytes=a-b", 1000, rangeNone, 0, 0},
		{"bytes=+1-2", 1000, rangeNone, 0, 0},
		{"bytes 0-1", 1000, rangeNone, 0, 0},
		{"items=0-1", 1000, rangeNone, 0, 0},
		{"bytes=0x10-", 1000, rangeNone, 0, 0},
	} {
		r, kind := byteRangeOf(c.h, c.size)
		if kind != c.kind || (kind == rangeOne && (r.first != c.first || r.last != c.last)) {
			t.Errorf("%q of %d: %v %+v, want %v %d-%d", c.h, c.size, kind, r, c.kind, c.first, c.last)
		}
	}
}

// TestWindowOf guards the reading of a download's query: none, or exactly
// offset and length, each once, in decimal without sign or leading zero,
// length at least 1, and their sum within an int64.
func TestWindowOf(t *testing.T) {
	good := map[string]window{
		"":                                    {},
		"offset=0&length=1":                   {given: true, offset: 0, length: 1},
		"length=7&offset=10":                  {given: true, offset: 10, length: 7},
		"offset=9223372036854775806&length=1": {given: true, offset: 9223372036854775806, length: 1},
	}
	for q, want := range good {
		if got, err := windowOf(q); err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", q, got, err, want)
		}
	}
	for _, q := range []string{"offset=0", "length=1", "offset=0&length=0", "offset=00&length=1", "offset=0&length=01",
		"offset=-0&length=1", "offset=+1&length=1", "offset=0&length=1&x=", "offset=0&length=1&offset=0", "offset&length=1",
		"offset=0&length=1&", "x=1", "offset=9223372036854775807&length=1", "offset=0&length=99999999999999999999",
		"offset=0%20&length=1", "offset=0;length=1"} {
		if _, err := windowOf(q); fault.KindOf(err) != fault.Invalid {
			t.Errorf("%q: %v, want invalid", q, err)
		}
	}
}

// TestConditions guards If-Range, which lets a range apply only with the
// strong tag itself, and If-None-Match, which matches the tag weakly or
// "*" in a list.
func TestConditions(t *testing.T) {
	const tag = `"abc"`
	for _, c := range []struct {
		values []string
		holds  bool
	}{
		{nil, true}, {[]string{tag}, true}, {[]string{" " + tag + " "}, true},
		{[]string{"W/" + tag}, false}, {[]string{`"abd"`}, false}, {[]string{"Fri, 20 Nov 2026 09:00:00 GMT"}, false},
		{[]string{tag, tag}, false},
	} {
		if got := ifRangeHolds(c.values, tag); got != c.holds {
			t.Errorf("If-Range %q: %v, want %v", c.values, got, c.holds)
		}
	}
	for _, c := range []struct {
		values []string
		fails  bool
	}{
		{nil, false}, {[]string{tag}, true}, {[]string{"W/" + tag}, true}, {[]string{`"x", ` + tag}, true},
		{[]string{`"x"`, tag}, true}, {[]string{"*"}, true}, {[]string{`"x"`}, false}, {[]string{"abc"}, false},
	} {
		if got := noneMatchFails(c.values, tag); got != c.fails {
			t.Errorf("If-None-Match %q: %v, want %v", c.values, got, c.fails)
		}
	}
}

// TestCopyChecked guards the copy of an instance's bytes: every byte
// passed in order when the count and the SHA-256 agree, whatever the
// buffers' length; and when they do not (fewer bytes, more bytes, other
// bytes), the right finding, with the last piece never passed, so what was
// passed is short of the whole.
func TestCopyChecked(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 1000)
	sum := tenant.SHA256(sha256.Sum256(data))
	other := bytes.Clone(data)
	other[len(other)-1] ^= 1
	for _, size := range []int{1, 7, 1000, len(data), len(data) + 1, 1 << 16} {
		var got bytes.Buffer
		emit := func(b []byte) error { got.Write(b); return nil }
		bufs := [2][]byte{make([]byte, size), make([]byte, size)}
		n, err := copyChecked(bytes.NewReader(data), int64(len(data)), &sum, bufs, emit)
		if err != nil || n != int64(len(data)) || !bytes.Equal(got.Bytes(), data) {
			t.Errorf("buffers of %d: %d bytes, %v", size, n, err)
		}
		for _, c := range []struct {
			name   string
			src    []byte
			length int64
			want   error
		}{
			{"fewer", data[:len(data)-1], int64(len(data)), errShort},
			{"more", data, int64(len(data) - 1), errLong},
			{"other", other, int64(len(data)), errChecksum},
		} {
			got.Reset()
			n, err := copyChecked(bytes.NewReader(c.src), c.length, &sum, bufs, emit)
			if !errors.Is(err, c.want) || !mismatched(err) || n >= c.length || int64(got.Len()) != n {
				t.Errorf("buffers of %d, %s bytes: %d passed, %v, want %v before the last piece", size, c.name, n, err, c.want)
			}
		}
	}
	// No checksum: the count alone; nothing at all for an empty body.
	n, err := copyChecked(bytes.NewReader(other), int64(len(other)), nil, [2][]byte{make([]byte, 100), make([]byte, 100)}, func([]byte) error { return nil })
	if err != nil || n != int64(len(other)) {
		t.Errorf("without a checksum: %d, %v", n, err)
	}
	empty := tenant.SHA256(sha256.Sum256(nil))
	n, err = copyChecked(bytes.NewReader(nil), 0, &empty, [2][]byte{make([]byte, 1), make([]byte, 1)}, func([]byte) error {
		t.Error("an empty body passed a piece")
		return nil
	})
	if err != nil || n != 0 {
		t.Errorf("an empty body: %d, %v", n, err)
	}
	// An error of the reader stops the copy, the hash's goroutine ended,
	// with the bytes before it passed but the piece held.
	boom := errors.New("the disk failed")
	got := 0
	n, err = copyChecked(io.MultiReader(bytes.NewReader(data[:5000]), iotest.ErrReader(boom)), int64(len(data)), &sum,
		[2][]byte{make([]byte, 1000), make([]byte, 1000)}, func(b []byte) error { got += len(b); return nil })
	if !errors.Is(err, boom) || n != 4000 || got != 4000 {
		t.Errorf("an error of the reader: %d passed, %v", n, err)
	}
	// An error of emit stops the copy.
	stop := errors.New("the client went away")
	if _, err := copyChecked(bytes.NewReader(data), int64(len(data)), &sum, [2][]byte{make([]byte, 10), make([]byte, 10)}, func([]byte) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("an error of emit: %v", err)
	}
}

// fakeSender is a fileSender over bytes, which records the size of each
// SendTo and can give fewer bytes than it holds.
type fakeSender struct {
	data  []byte
	short int
	asked []int64
}

// Remaining returns the bytes left.
func (f *fakeSender) Remaining() int64 { return int64(len(f.data)) }

// SendTo writes the next n bytes, short of them by f.short.
func (f *fakeSender) SendTo(w io.Writer, n int64) (int64, error) {
	f.asked = append(f.asked, n)
	n = max(min(n, int64(len(f.data)))-int64(f.short), 0)
	k, err := w.Write(f.data[:n])
	f.data = f.data[k:]
	return int64(k), err
}

// TestSendRange guards the part of a range a storage's stream writes
// itself: every byte but the last piece, in order, in pieces of at most
// sendPiece each preceded by the deadline's step, leaving the last piece
// to the checked copy; a stream that writes fewer bytes than asked is
// errShort; and a canceled request stops before the next piece with its
// cause.
func TestSendRange(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), (2*sendPiece+3*httpChunk)/16)
	n := int64(len(data))
	f := &fakeSender{data: data}
	var out bytes.Buffer
	befores := 0
	sent, err := sendRange(context.Background(), f, &out, n, func() { befores++ })
	if err != nil || sent != n-httpChunk || !bytes.Equal(out.Bytes(), data[:n-httpChunk]) || f.Remaining() != httpChunk {
		t.Fatalf("%d sent, %v, %d left", sent, err, f.Remaining())
	}
	if befores != len(f.asked) || len(f.asked) != 3 || slices.Max(f.asked) > sendPiece {
		t.Errorf("pieces %v with %d deadline steps", f.asked, befores)
	}
	f = &fakeSender{data: data, short: 1}
	if sent, err := sendRange(context.Background(), f, io.Discard, n, func() {}); !errors.Is(err, errShort) || sent != sendPiece-1 {
		t.Errorf("a stream short of what was asked: %d, %v, want errShort", sent, err)
	}
	cause := errors.New("the key was revoked")
	ctx, cancel := context.WithCancelCause(context.Background())
	f = &fakeSender{data: data}
	sent, err = sendRange(ctx, f, io.Discard, n, func() {
		if len(f.asked) == 0 {
			cancel(cause)
		}
	})
	if !errors.Is(err, cause) || sent != sendPiece {
		t.Errorf("a canceled request: %d sent, %v, want one piece and the cause", sent, err)
	}
}
