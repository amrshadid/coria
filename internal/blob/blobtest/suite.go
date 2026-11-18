package blobtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/fault"
)

// Driver is what a driver gives the suite.
type Driver struct {
	// Open returns a fresh, empty store that no other test uses, and closes
	// it when tb ends (tb.Cleanup). Closing a store twice must be harmless,
	// since a test may close it itself.
	Open func(tb testing.TB) blob.Store
	// UsageKnown says whether the driver can report the space of its volume.
	// The suite holds it to what it says: a driver that says true must
	// report numbers that add up, and one that says false must report
	// Known false rather than numbers it made up.
	UsageKnown bool
	// KeyAndPrefix says whether one key can hold an object while other keys
	// lie under it ("a" and "a/b"), as a bucket allows. A storage on a file
	// system cannot, since one name cannot be a file and a directory at
	// once; it must then refuse the second Put as Conflict and keep the
	// first object. Such a storage may also refuse a key under which keys
	// once lay, after they were deleted, since the directory they were in
	// can remain. The archive's layout never makes such a pair.
	KeyAndPrefix bool
}

// Run runs the whole contract suite against the stores d opens. Each test
// has its own store and runs in parallel with the others.
func Run(t *testing.T, d Driver) {
	tests := []struct {
		name string
		fn   func(*env)
	}{
		{"PutRead", testPutRead},
		{"Empty", testEmpty},
		{"Ranges", testRanges},
		{"Replace", testReplace},
		{"ConcurrentPuts", testConcurrentPuts},
		{"FailedPut", testFailedPut},
		{"CanceledPut", testCanceledPut},
		{"RefusedKeys", testRefusedKeys},
		{"PrefixIsNotAnObject", testPrefixIsNotAnObject},
		{"KeyAndPrefix", testKeyAndPrefix},
		{"Delete", testDelete},
		{"List", testList},
		{"ListDelimiter", testListDelimiter},
		{"ListPartialPrefix", testListPartialPrefix},
		{"ListDefaultLimit", testListDefaultLimit},
		{"ListStable", testListStable},
		{"ListRefused", testListRefused},
		{"Usage", testUsage},
		{"Closed", testClosed},
		{"NextOnlyWhenMore", testNextOnlyWhenMore},
		{"ReadSeesOneVersion", testReadSeesOneVersion},
		{"PutDeleteRace", testPutDeleteRace},
		{"SiblingsRace", testSiblingsRace},
		{"NewParentsAtOnce", testNewParentsAtOnce},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.fn(&env{T: t, s: d.Open(t), d: d, ctx: context.Background()})
		})
	}
}

// BadKeys returns keys that every store must refuse as Invalid, each
// breaking one rule of blob.CheckKey: the forms a key takes when it tries to
// leave a storage's root, names nothing a disk can hold, or would mean two
// different things on two storages or two file systems.
func BadKeys() []string {
	return []string{
		"",
		"/a", "a/", "a//b", "/",
		".", "..", "../a", "a/..", "a/../b", "a/./b", "./a",
		`a\b`, `..\a`, "a:b", "c:/x", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b",
		"a\x00b", "a\nb", "a\tb", "\x1b[31m", "a\x7fb", "a\u0085b",
		"\xff", "a/\xc3",
		"a.", "a/b./c", "a ", "a/b /c", " a", "a b",
		"con", "CON", "a/nul.txt", "Aux", "prn.dcm", "com1", "LPT9.x", "com\u00b9", "con .txt",
		".blob-tmp-0123", "a/.BLOB-x", ".blob-", ".blob-tmp/obj",
		// Names a file system reads as another name: composed and
		// decomposed forms (one file on APFS), an invisible code point
		// before the reserved prefix (dropped by HFS+, so the key would
		// land in a driver's temporary directory), a Windows short name.
		"caf\u00e9", "cafe\u0301", "\u200c.blob-tmp/obj", "a\u200db", "\ufeffa", "ABCDEF~1", "PROGRA~1/x",
		"a+b", "a@b", "a#b", "a%2Fb", "a$", "a=b", "a,b", "a;b", "a'b", "a(b)", "a[b]", "a{b}", "a!b", "a&b", "a^b", "a`b",
		strings.Repeat("a", blob.MaxSegmentLength+1),
		strings.Repeat("a/", blob.MaxKeyLength/2) + "a",
		strings.Repeat("a/", blob.MaxKeySegments) + "a",
	}
}

// env is one test's store and the helpers that use it.
type env struct {
	*testing.T
	s   blob.Store
	d   Driver
	ctx context.Context
}

// put writes data to key and checks what Put says it wrote.
func (e *env) put(key string, data []byte) blob.Written {
	e.Helper()
	w, err := e.s.Put(e.ctx, key, bytes.NewReader(data))
	if err != nil {
		e.Fatalf("Put(%q, %d bytes): %v", key, len(data), err)
	}
	if w.Size != int64(len(data)) || w.SHA256 != sha256.Sum256(data) {
		e.Fatalf("Put(%q) says it wrote %d bytes, %x; want %d bytes, %x", key, w.Size, w.SHA256, len(data), sha256.Sum256(data))
	}
	return w
}

// read returns the bytes of a range of key.
func (e *env) read(key string, offset, length int64) []byte {
	e.Helper()
	r, err := e.s.Read(e.ctx, key, offset, length)
	if err != nil {
		e.Fatalf("Read(%q, %d, %d): %v", key, offset, length, err)
	}
	defer closeOrFail(e.T, r)
	b, err := io.ReadAll(r)
	if err != nil {
		e.Fatalf("reading %q: %v", key, err)
	}
	return b
}

// wantObject checks that key holds exactly data, by Read and by Stat.
func (e *env) wantObject(key string, data []byte) {
	e.Helper()
	if got := e.read(key, 0, -1); !bytes.Equal(got, data) {
		e.Fatalf("%q holds %d bytes (%x), want %d bytes (%x)", key, len(got), sha256.Sum256(got), len(data), sha256.Sum256(data))
	}
	info, err := e.s.Stat(e.ctx, key)
	if err != nil || info.Size != int64(len(data)) {
		e.Fatalf("Stat(%q) = %+v, %v; want size %d", key, info, err, len(data))
	}
}

// wantAbsent checks that key holds nothing, by Stat, Read and List.
func (e *env) wantAbsent(key string) {
	e.Helper()
	if info, err := e.s.Stat(e.ctx, key); fault.KindOf(err) != fault.NotFound {
		e.Fatalf("Stat(%q) = %+v, %v; want NotFound", key, info, err)
	}
	if r, err := e.s.Read(e.ctx, key, 0, -1); fault.KindOf(err) != fault.NotFound {
		if err == nil {
			closeOrFail(e.T, r)
		}
		e.Fatalf("Read(%q) = %v; want NotFound", key, err)
	}
	if keys, _ := e.listAll(blob.ListOptions{Prefix: key}); slices.Contains(keys, key) {
		e.Fatalf("List shows %q, which should hold nothing", key)
	}
}

// listAll pages through a listing with opts and returns every key and
// prefix, checking along the way that each page is within its limit, in
// order, continues after the page before, and ends with an empty Next.
func (e *env) listAll(opts blob.ListOptions) (keys, prefixes []string) {
	e.Helper()
	limit := blob.ListLimit(opts)
	last := ""
	for pages := 0; ; pages++ {
		if pages > 100000 {
			e.Fatalf("List(%+v) never ends", opts)
		}
		p, err := e.s.List(e.ctx, opts)
		if err != nil {
			e.Fatalf("List(%+v): %v", opts, err)
		}
		entries := merge(p.Keys, p.Prefixes)
		if len(entries) > limit {
			e.Fatalf("List(%+v) returned %d entries, over its limit", opts, len(entries))
		}
		if len(entries) == 0 && p.Next != "" {
			e.Fatalf("List(%+v) returned an empty page that is not the last", opts)
		}
		if len(entries) == 0 && pages > 0 {
			e.Fatalf("List(%+v) returned an empty page after a page whose Next promised more", opts)
		}
		for _, x := range entries {
			if x <= last {
				e.Fatalf("List(%+v): %q comes after %q, out of order or repeated", opts, x, last)
			}
			if !strings.HasPrefix(x, opts.Prefix) {
				e.Fatalf("List(%+v) returned %q, outside the prefix", opts, x)
			}
			last = x
		}
		if opts.Delimiter == "" && len(p.Prefixes) != 0 {
			e.Fatalf("List(%+v) returned prefixes without a delimiter: %q", opts, p.Prefixes)
		}
		keys = append(keys, p.Keys...)
		prefixes = append(prefixes, p.Prefixes...)
		if p.Next == "" {
			return keys, prefixes
		}
		opts.Token = p.Next
	}
}

// merge returns keys and prefixes together, sorted, as a page orders them.
func merge(keys, prefixes []string) []string {
	out := append(slices.Clone(keys), prefixes...)
	slices.Sort(out)
	return out
}

func closeOrFail(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// testPutRead guards that Put returns the size and SHA-256 of what it was
// given, and that Read and Stat give back exactly that, for sizes at and
// around the buffer sizes a driver is likely to use.
func testPutRead(e *env) {
	for i, n := range []int{1, 4095, 4096, 4097, 65536, 3<<20 + 1} {
		key := fmt.Sprintf("put/%d/object.dcm", n)
		data := PatternBytes(uint64(i), n)
		e.put(key, data)
		e.wantObject(key, data)
	}
	// The deepest key there may be.
	deep := strings.Repeat("d/", blob.MaxKeySegments-1) + "object"
	e.put(deep, []byte("deep"))
	e.wantObject(deep, []byte("deep"))
	// A reader that gives one byte at a time and a reader of unknown
	// length are the same to Put.
	data := PatternBytes(99, 1000)
	w, err := e.s.Put(e.ctx, "put/slow", io.LimitReader(&oneByte{r: bytes.NewReader(data)}, 1<<40))
	if err != nil || w.Size != 1000 || w.SHA256 != sha256.Sum256(data) {
		e.Fatalf("Put from a one-byte reader = %+v, %v", w, err)
	}
	e.wantObject("put/slow", data)
}

type oneByte struct{ r io.Reader }

func (o *oneByte) Read(b []byte) (int, error) {
	if len(b) > 1 {
		b = b[:1]
	}
	return o.r.Read(b)
}

// testEmpty guards an object of no bytes: it exists, it is listed, its size
// is 0, and its SHA-256 is that of nothing.
func testEmpty(e *env) {
	w := e.put("empty", nil)
	if w.SHA256 != sha256.Sum256(nil) {
		e.Fatalf("SHA-256 of nothing = %x", w.SHA256)
	}
	e.wantObject("empty", nil)
	if keys, _ := e.listAll(blob.ListOptions{}); !slices.Equal(keys, []string{"empty"}) {
		e.Fatalf("List = %q, want the empty object", keys)
	}
	for _, r := range [][2]int64{{0, 0}, {0, -1}, {0, 5}} {
		if got := e.read("empty", r[0], r[1]); len(got) != 0 {
			e.Fatalf("Read(empty, %d, %d) gave %d bytes", r[0], r[1], len(got))
		}
	}
	if _, err := e.s.Read(e.ctx, "empty", 1, 0); fault.KindOf(err) != fault.Invalid {
		e.Fatalf("Read(empty, 1, 0) = %v, want Invalid (past the end)", err)
	}
}

// testRanges guards Read's ranges at every edge: the first and last byte, a
// length that runs past the end and is cut there, an offset at the end
// (empty), an offset past it and negative values (Invalid), a missing key
// (NotFound), and a stream closed before it is drained.
func testRanges(e *env) {
	const n = 10007
	data := PatternBytes(7, n)
	e.put("r/x", data)
	for _, c := range []struct{ off, length, from, to int64 }{
		{0, -1, 0, n},
		{0, 0, 0, 0},
		{0, 1, 0, 1},
		{0, n, 0, n},
		{0, n + 1000, 0, n},
		{1, -1, 1, n},
		{1, n - 2, 1, n - 1},
		{n - 1, 1, n - 1, n},
		{n - 1, -1, n - 1, n},
		{n - 1, 100, n - 1, n},
		{n, 0, n, n},
		{n, -1, n, n},
		{n, 10, n, n},
		{4096, 4096, 4096, 8192},
		{5000, 1 << 40, 5000, n},
	} {
		if got := e.read("r/x", c.off, c.length); !bytes.Equal(got, data[c.from:c.to]) {
			e.Errorf("Read(%d, %d) gave %d bytes, want bytes %d to %d", c.off, c.length, len(got), c.from, c.to)
		}
	}
	for _, c := range [][2]int64{{n + 1, 0}, {n + 1, -1}, {1 << 40, 1}, {-1, 1}, {0, -2}} {
		if r, err := e.s.Read(e.ctx, "r/x", c[0], c[1]); fault.KindOf(err) != fault.Invalid {
			if err == nil {
				closeOrFail(e.T, r)
			}
			e.Errorf("Read(%d, %d) = %v, want Invalid", c[0], c[1], err)
		}
	}
	if _, err := e.s.Read(e.ctx, "r/missing", 0, 0); fault.KindOf(err) != fault.NotFound {
		e.Errorf("Read of a missing key with length 0 = %v, want NotFound", err)
	}
	r, err := e.s.Read(e.ctx, "r/x", 0, -1)
	if err != nil {
		e.Fatal(err)
	}
	if _, err := io.ReadFull(r, make([]byte, 10)); err != nil {
		e.Fatal(err)
	}
	closeOrFail(e.T, r)
}

// testReplace guards that a Put to a key that holds an object replaces it
// whole: larger by smaller leaves no tail of the old bytes, smaller by
// larger leaves nothing short.
func testReplace(e *env) {
	big, small := PatternBytes(1, 300000), PatternBytes(2, 10)
	e.put("k", big)
	e.put("k", small)
	e.wantObject("k", small)
	e.put("k", big)
	e.wantObject("k", big)
	e.put("k", nil)
	e.wantObject("k", nil)
	if keys, _ := e.listAll(blob.ListOptions{}); !slices.Equal(keys, []string{"k"}) {
		e.Fatalf("List after replacing = %q, want one key", keys)
	}
}

// testConcurrentPuts guards that Puts racing on one key never mix: every
// Read during the race, and the object left after it, is one Put's bytes,
// whole. The objects are small and few, so the test does not depend on how
// fast the storage is, only on what it shows.
func testConcurrentPuts(e *env) {
	const writers, rounds = 4, 6
	sums := map[[sha256.Size]byte]bool{}
	versions := map[int][]byte{}
	for w := range writers {
		for r := range rounds {
			v := w*rounds + r
			versions[v] = PatternBytes(uint64(1000+v), 20000+v*997)
			sums[sha256.Sum256(versions[v])] = true
		}
	}
	e.put("race/k", versions[0])
	ctx, stop := context.WithCancel(e.ctx)
	var wg, readers sync.WaitGroup
	errs := make(chan error, writers+2)
	for w := range writers {
		wg.Go(func() {
			for r := range rounds {
				data := versions[w*rounds+r]
				got, err := e.s.Put(e.ctx, "race/k", bytes.NewReader(data))
				if err != nil {
					errs <- fmt.Errorf("put: %w", err)
					return
				}
				if got.SHA256 != sha256.Sum256(data) {
					errs <- errors.New("put returned the checksum of other bytes than it was given")
					return
				}
			}
		})
	}
	for range 2 {
		readers.Go(func() {
			for ctx.Err() == nil {
				r, err := e.s.Read(e.ctx, "race/k", 0, -1)
				if err != nil {
					errs <- fmt.Errorf("Read during the race: %w", err)
					return
				}
				b, err := io.ReadAll(r)
				_ = r.Close() // The bytes are checked below; a close error would show as a later failure.
				if err != nil {
					errs <- fmt.Errorf("reading during the race: %w", err)
					return
				}
				if !sums[sha256.Sum256(b)] {
					errs <- fmt.Errorf("a Read during the race gave %d bytes that are no Put's whole object", len(b))
					return
				}
			}
		})
	}
	wg.Wait()
	stop()
	readers.Wait()
	close(errs)
	for err := range errs {
		e.Error(err)
	}
	final := e.read("race/k", 0, -1)
	if !sums[sha256.Sum256(final)] {
		e.Fatalf("after the race the key holds %d bytes that are no Put's whole object", len(final))
	}
	if keys, _ := e.listAll(blob.ListOptions{}); !slices.Equal(keys, []string{"race/k"}) {
		e.Fatalf("List after the race = %q, want one key", keys)
	}
}

// testFailedPut guards that a Put whose reader fails midway leaves nothing:
// not the key, not its prefix in a listing, and for a key that held an
// object, the old object untouched. The reader's error comes back through
// Put.
func testFailedPut(e *env) {
	for _, n := range []int64{0, 1, 70000} {
		_, err := e.s.Put(e.ctx, "fail/a/b/new", &failAfter{r: Pattern(3, 1<<20), n: n})
		if !errors.Is(err, errBroken) {
			e.Fatalf("Put from a reader that fails after %d bytes = %v, want its error", n, err)
		}
		e.wantAbsent("fail/a/b/new")
	}
	if keys, prefixes := e.listAll(blob.ListOptions{Delimiter: "/"}); len(keys)+len(prefixes) != 0 {
		e.Fatalf("after failed Puts, List shows %q %q, want nothing", keys, prefixes)
	}
	old := PatternBytes(4, 5000)
	e.put("fail/old", old)
	_, err := e.s.Put(e.ctx, "fail/old", &failAfter{r: Pattern(5, 1<<20), n: 100000})
	if !errors.Is(err, errBroken) {
		e.Fatalf("replacing Put from a failing reader = %v, want its error", err)
	}
	e.wantObject("fail/old", old)
}

// testCanceledPut guards that a Put whose context ends, before it starts
// or midway, returns an error that matches the context's and leaves the
// key as it was.
func testCanceledPut(e *env) {
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	if _, err := e.s.Put(ctx, "cancel/before", Pattern(1, 100)); !errors.Is(err, context.Canceled) {
		e.Fatalf("Put with a canceled context = %v, want context.Canceled", err)
	}
	e.wantAbsent("cancel/before")

	ctx, cancel = context.WithCancel(e.ctx)
	defer cancel()
	_, err := e.s.Put(ctx, "cancel/midway", &cancelAfter{r: Pattern(2, 8<<20), n: 1 << 20, cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		e.Fatalf("Put canceled midway = %v, want context.Canceled", err)
	}
	e.wantAbsent("cancel/midway")

	old := PatternBytes(6, 1234)
	e.put("cancel/old", old)
	ctx, cancel = context.WithCancel(e.ctx)
	defer cancel()
	if _, err := e.s.Put(ctx, "cancel/old", &cancelAfter{r: Pattern(7, 8<<20), n: 1 << 20, cancel: cancel}); !errors.Is(err, context.Canceled) {
		e.Fatalf("replacing Put canceled midway = %v, want context.Canceled", err)
	}
	e.wantObject("cancel/old", old)
}

// testRefusedKeys guards that every operation refuses a bad key as Invalid
// before doing anything: Put does not read its input, and nothing is
// written.
func testRefusedKeys(e *env) {
	for _, key := range BadKeys() {
		u := &untouched{}
		if _, err := e.s.Put(e.ctx, key, u); fault.KindOf(err) != fault.Invalid {
			e.Errorf("Put(%q) = %v, want Invalid", key, err)
		}
		if u.read {
			e.Errorf("Put(%q) read its input before refusing the key", key)
		}
		if r, err := e.s.Read(e.ctx, key, 0, -1); fault.KindOf(err) != fault.Invalid {
			if err == nil {
				closeOrFail(e.T, r)
			}
			e.Errorf("Read(%q) = %v, want Invalid", key, err)
		}
		if _, err := e.s.Stat(e.ctx, key); fault.KindOf(err) != fault.Invalid {
			e.Errorf("Stat(%q) = %v, want Invalid", key, err)
		}
		if err := e.s.Delete(e.ctx, key); fault.KindOf(err) != fault.Invalid {
			e.Errorf("Delete(%q) = %v, want Invalid", key, err)
		}
	}
	if keys, _ := e.listAll(blob.ListOptions{}); len(keys) != 0 {
		e.Fatalf("after refused keys, List shows %q", keys)
	}
}

// testPrefixIsNotAnObject guards that a key that is only a prefix of
// others holds nothing: Stat and Read say NotFound, and Delete of it is a
// harmless no-op that leaves the keys under it.
func testPrefixIsNotAnObject(e *env) {
	e.put("p/q/r", []byte("x"))
	for _, k := range []string{"p", "p/q"} {
		e.wantAbsent(k)
		if err := e.s.Delete(e.ctx, k); err != nil {
			e.Fatalf("Delete(%q), a prefix only: %v", k, err)
		}
	}
	e.wantObject("p/q/r", []byte("x"))
}

// testKeyAndPrefix guards what the driver declared for a key that is also
// the prefix of another: both kept, or the second refused as Conflict with
// the first untouched, in either order.
func testKeyAndPrefix(e *env) {
	for _, pair := range [][2]string{{"kp/a", "kp/a/b"}, {"pk/a/b", "pk/a"}} {
		e.put(pair[0], []byte("first"))
		_, err := e.s.Put(e.ctx, pair[1], bytes.NewReader([]byte("second")))
		if e.d.KeyAndPrefix {
			if err != nil {
				e.Fatalf("Put(%q) after %q: %v; the driver declared it holds both", pair[1], pair[0], err)
			}
			e.wantObject(pair[1], []byte("second"))
		} else {
			if fault.KindOf(err) != fault.Conflict {
				e.Fatalf("Put(%q) after %q = %v; want Conflict", pair[1], pair[0], err)
			}
			e.wantAbsent(pair[1])
		}
		e.wantObject(pair[0], []byte("first"))
	}
}

// testDelete guards that Delete removes one object and nothing else, that
// its prefix disappears from a listing when the last key under it goes, and
// that deleting a key that holds nothing is not an error.
func testDelete(e *env) {
	e.put("d/a/1", []byte("1"))
	e.put("d/a/2", []byte("2"))
	e.put("d/b", []byte("b"))
	if err := e.s.Delete(e.ctx, "d/a/1"); err != nil {
		e.Fatal(err)
	}
	e.wantAbsent("d/a/1")
	e.wantObject("d/a/2", []byte("2"))
	if err := e.s.Delete(e.ctx, "d/a/2"); err != nil {
		e.Fatal(err)
	}
	if keys, prefixes := e.listAll(blob.ListOptions{Prefix: "d/", Delimiter: "/"}); !slices.Equal(keys, []string{"d/b"}) || len(prefixes) != 0 {
		e.Fatalf("after deleting every key under d/a/, List(d/) = %q %q; want only d/b", keys, prefixes)
	}
	for _, k := range []string{"d/a/2", "never/was", "d/b/below"} {
		if err := e.s.Delete(e.ctx, k); err != nil {
			e.Fatalf("Delete(%q), which holds nothing: %v", k, err)
		}
	}
	e.wantObject("d/b", []byte("b"))
}

// listKeys is a set of keys whose byte order is easy to get wrong: "." and
// "-" sort before "/", "0" after it, upper case before "_" and "_" before
// lower case, so a lister that walks directories in name order without care
// returns them out of order. No two differ only in case, which a volume
// that ignores case would hold as one.
var listKeys = []string{
	"a-", "a.b", "a/b", "a/c/d", "a/c/e", "a/c.x", "a0/z",
	"b/x/y/z", "data/s1/x", "data/s10/x", "data/s1-/x", "data/s2/y",
	"z", "Z_", "_u/x",
}

// putAll puts each key with its own name as its bytes.
func (e *env) putAll(keys []string) {
	for _, k := range keys {
		e.put(k, []byte(k))
	}
}

// testList guards a listing without a delimiter: every key, in byte order,
// the same whatever the page size, with and without a prefix.
func testList(e *env) {
	e.putAll(listKeys)
	want := slices.Sorted(slices.Values(listKeys))
	for _, limit := range []int{1, 2, 3, 7, 1000, 0} {
		if keys, _ := e.listAll(blob.ListOptions{Limit: limit}); !slices.Equal(keys, want) {
			e.Fatalf("List with limit %d = %q, want %q", limit, keys, want)
		}
	}
	for _, c := range []struct {
		prefix string
		want   []string
	}{
		{"a/", []string{"a/b", "a/c.x", "a/c/d", "a/c/e"}},
		{"a/c", []string{"a/c.x", "a/c/d", "a/c/e"}},
		{"a/c/", []string{"a/c/d", "a/c/e"}},
		{"a/c/d", []string{"a/c/d"}},
		{"data/s1", []string{"data/s1-/x", "data/s1/x", "data/s10/x"}},
		{"nothing", nil},
		{"a/c/d/", nil},
	} {
		for _, limit := range []int{1, 2, 0} {
			if keys, _ := e.listAll(blob.ListOptions{Prefix: c.prefix, Limit: limit}); !slices.Equal(keys, c.want) {
				e.Errorf("List(%q, limit %d) = %q, want %q", c.prefix, limit, keys, c.want)
			}
		}
	}
}

// testListDelimiter guards a listing of one level: the keys at that level
// and one prefix for each group below it, in byte order together, across
// pages of every size.
func testListDelimiter(e *env) {
	e.putAll(listKeys)
	for _, c := range []struct {
		prefix   string
		keys     []string
		prefixes []string
	}{
		{"", []string{"Z_", "a-", "a.b", "z"}, []string{"_u/", "a/", "a0/", "b/", "data/"}},
		{"a/", []string{"a/b", "a/c.x"}, []string{"a/c/"}},
		{"a/c/", []string{"a/c/d", "a/c/e"}, nil},
		{"data/", nil, []string{"data/s1-/", "data/s1/", "data/s10/", "data/s2/"}},
		{"data/s1", nil, []string{"data/s1-/", "data/s1/", "data/s10/"}},
		{"b/", nil, []string{"b/x/"}},
		{"a", []string{"a-", "a.b"}, []string{"a/", "a0/"}},
		{"q/", nil, nil},
	} {
		for _, limit := range []int{1, 2, 3, 0} {
			keys, prefixes := e.listAll(blob.ListOptions{Prefix: c.prefix, Delimiter: "/", Limit: limit})
			if !slices.Equal(keys, c.keys) || !slices.Equal(prefixes, c.prefixes) {
				e.Errorf("List(%q, /, limit %d) = %q %q; want %q %q", c.prefix, limit, keys, prefixes, c.keys, c.prefixes)
			}
		}
	}
}

// testListPartialPrefix guards that a prefix ending inside a segment
// matches every name that begins with it, at the top level and below, and
// nothing that does not.
func testListPartialPrefix(e *env) {
	e.putAll([]string{"sub-", "subject", "sub/x", "su", "s", "subz/y/z", "t"})
	keys, _ := e.listAll(blob.ListOptions{Prefix: "sub"})
	if want := []string{"sub-", "sub/x", "subject", "subz/y/z"}; !slices.Equal(keys, want) {
		e.Fatalf("List(sub) = %q, want %q", keys, want)
	}
	keys, prefixes := e.listAll(blob.ListOptions{Prefix: "sub", Delimiter: "/", Limit: 1})
	if !slices.Equal(keys, []string{"sub-", "subject"}) || !slices.Equal(prefixes, []string{"sub/", "subz/"}) {
		e.Fatalf("List(sub, /) = %q %q", keys, prefixes)
	}
}

// testListDefaultLimit guards that a limit of 0 means pages of
// blob.MaxListLimit, and that a listing longer than one page continues on
// the next.
func testListDefaultLimit(e *env) {
	const n = blob.MaxListLimit + 3
	var wg sync.WaitGroup
	errs := make(chan error, n)
	sem := make(chan struct{}, 8)
	for i := range n {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if _, err := e.s.Put(e.ctx, fmt.Sprintf("many/k%05d", i), bytes.NewReader(nil)); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		e.Fatal(err)
	}
	p, err := e.s.List(e.ctx, blob.ListOptions{Prefix: "many/"})
	if err != nil {
		e.Fatal(err)
	}
	if len(p.Keys) != blob.MaxListLimit || p.Next == "" {
		e.Fatalf("first page holds %d keys (next %q), want %d and more", len(p.Keys), p.Next, blob.MaxListLimit)
	}
	p, err = e.s.List(e.ctx, blob.ListOptions{Prefix: "many/", Token: p.Next})
	if err != nil {
		e.Fatal(err)
	}
	if want := []string{"many/k01000", "many/k01001", "many/k01002"}; !slices.Equal(p.Keys, want) || p.Next != "" {
		e.Fatalf("second page = %q (next %q), want %q and the end", p.Keys, p.Next, want)
	}
	if keys, _ := e.listAll(blob.ListOptions{Prefix: "many/", Delimiter: "/"}); len(keys) != n {
		e.Fatalf("List(many/, /) = %d keys, want %d", len(keys), n)
	}
}

// testListStable guards that a listing continues exactly where the page
// before ended while keys change: a key put behind that point is not seen,
// one put ahead of it is, and one deleted ahead of it is not, with and
// without a delimiter.
func testListStable(e *env) {
	for _, delim := range []string{"", "/"} {
		prefix := "stable" + map[string]string{"": "flat", "/": "level"}[delim] + "/"
		var keys []string
		for i := range 20 {
			keys = append(keys, fmt.Sprintf("%sk%02d", prefix, i))
		}
		e.putAll(keys)
		opts := blob.ListOptions{Prefix: prefix, Delimiter: delim, Limit: 5}
		p, err := e.s.List(e.ctx, opts)
		if err != nil {
			e.Fatal(err)
		}
		if !slices.Equal(p.Keys, keys[:5]) {
			e.Fatalf("first page = %q", p.Keys)
		}
		e.put(prefix+"k02a", nil)
		e.put(prefix+"k10a", nil)
		if err := e.s.Delete(e.ctx, prefix+"k07"); err != nil {
			e.Fatal(err)
		}
		opts.Token = p.Next
		rest, _ := e.listAll(opts)
		want := append(slices.Clone(keys[5:7]), keys[8:11]...)
		want = append(want, prefix+"k10a")
		want = append(want, keys[11:]...)
		if !slices.Equal(rest, want) {
			e.Fatalf("delimiter %q: the rest = %q, want %q", delim, rest, want)
		}
	}
}

// testListRefused guards that List refuses a bad prefix, delimiter, limit
// or token as Invalid.
func testListRefused(e *env) {
	e.put("x/y", nil)
	for _, opts := range []blob.ListOptions{
		{Prefix: "/x"},
		{Prefix: "../"},
		{Prefix: "x//"},
		{Prefix: `x\`},
		{Delimiter: "|"},
		{Limit: -1},
		{Limit: blob.MaxListLimit + 1},
		{Token: "\x00 not a token"},
	} {
		if _, err := e.s.List(e.ctx, opts); fault.KindOf(err) != fault.Invalid {
			e.Errorf("List(%+v) = %v, want Invalid", opts, err)
		}
	}
}

// testUsage guards that Usage is what the driver declared: numbers that add
// up when it says it can know them, and Known false with zeros when it says
// it cannot.
func testUsage(e *env) {
	u, err := e.s.Usage(e.ctx)
	if err != nil {
		e.Fatal(err)
	}
	if u.Known != e.d.UsageKnown {
		e.Fatalf("Usage().Known = %v, but the driver declared %v", u.Known, e.d.UsageKnown)
	}
	if !u.Known {
		if u != (blob.Usage{}) {
			e.Fatalf("Usage says it cannot know but reports %+v", u)
		}
		return
	}
	if u.Total <= 0 || u.Free < 0 || u.Used < 0 || u.Free > u.Total || u.Used > u.Total {
		e.Fatalf("Usage = %+v, which does not add up", u)
	}
}

// testClosed guards that a closed store refuses every call as Unavailable
// and that closing it again is harmless.
func testClosed(e *env) {
	s := e.d.Open(e.T)
	if _, err := s.Put(e.ctx, "c", bytes.NewReader([]byte("c"))); err != nil {
		e.Fatal(err)
	}
	if err := s.Close(); err != nil {
		e.Fatal(err)
	}
	if err := s.Close(); err != nil {
		e.Fatalf("second Close: %v", err)
	}
	want := func(op string, err error) {
		e.Helper()
		if fault.KindOf(err) != fault.Unavailable {
			e.Errorf("%s after Close = %v, want Unavailable", op, err)
		}
	}
	_, err := s.Put(e.ctx, "c", bytes.NewReader(nil))
	want("Put", err)
	_, err = s.Read(e.ctx, "c", 0, -1)
	want("Read", err)
	_, err = s.Stat(e.ctx, "c")
	want("Stat", err)
	_, err = s.List(e.ctx, blob.ListOptions{})
	want("List", err)
	want("Delete", s.Delete(e.ctx, "c"))
	_, err = s.Usage(e.ctx)
	want("Usage", err)
}

// testNextOnlyWhenMore guards that a page carries a Next only when another
// entry follows it: a last page that is exactly full, with or without a
// delimiter, ends the listing.
func testNextOnlyWhenMore(e *env) {
	e.putAll([]string{"n/a", "n/b", "n/c", "n/d/x", "n/e/y", "n/f"})
	for _, c := range []struct {
		delim string
		limit int
		pages int
	}{
		{"", 6, 1}, {"", 3, 2}, {"", 2, 3}, {"", 1, 6},
		{"/", 6, 1}, {"/", 3, 2}, {"/", 2, 3}, {"/", 1, 6},
	} {
		opts := blob.ListOptions{Prefix: "n/", Delimiter: c.delim, Limit: c.limit}
		for page := 1; ; page++ {
			p, err := e.s.List(e.ctx, opts)
			if err != nil {
				e.Fatal(err)
			}
			if page == c.pages {
				if p.Next != "" {
					e.Fatalf("delimiter %q, limit %d: the last page, exactly full, has Next %q", c.delim, c.limit, p.Next)
				}
				break
			}
			if p.Next == "" {
				e.Fatalf("delimiter %q, limit %d: the listing ended at page %d, want %d pages", c.delim, c.limit, page, c.pages)
			}
			opts.Token = p.Next
		}
	}
}

// testReadSeesOneVersion guards that a stream from Read gives the object as
// it was when Read was called, even when a Put replaces it, or a Delete
// removes it, before the stream is drained.
func testReadSeesOneVersion(e *env) {
	old, next := PatternBytes(1, 200000), PatternBytes(2, 200000)
	for _, then := range []string{"put", "delete"} {
		e.put("v/k", old)
		r, err := e.s.Read(e.ctx, "v/k", 0, -1)
		if err != nil {
			e.Fatal(err)
		}
		head := make([]byte, 1000)
		if _, err := io.ReadFull(r, head); err != nil {
			e.Fatal(err)
		}
		if then == "put" {
			e.put("v/k", next)
		} else if err := e.s.Delete(e.ctx, "v/k"); err != nil {
			e.Fatal(err)
		}
		rest, err := io.ReadAll(r)
		closeOrFail(e.T, r)
		if err != nil {
			e.Fatal(err)
		}
		if got := append(head, rest...); !bytes.Equal(got, old) {
			e.Fatalf("after a %s during the read, the stream gave %d bytes that are not the old object whole", then, len(got))
		}
	}
}

// testPutDeleteRace guards Puts and Deletes of one key racing each other:
// every call succeeds, and the key ends holding nothing or one Put's object
// whole.
func testPutDeleteRace(e *env) {
	const rounds = 20
	versions := map[[sha256.Size]byte]bool{}
	var data [][]byte
	for i := range 4 {
		b := PatternBytes(uint64(300+i), 1000+i*313)
		data = append(data, b)
		versions[sha256.Sum256(b)] = true
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8*rounds)
	for w := range 4 {
		wg.Go(func() {
			for range rounds {
				if _, err := e.s.Put(e.ctx, "pd/k", bytes.NewReader(data[w])); err != nil {
					errs <- fmt.Errorf("put: %w", err)
				}
			}
		})
		wg.Go(func() {
			for range rounds {
				if err := e.s.Delete(e.ctx, "pd/k"); err != nil {
					errs <- fmt.Errorf("delete: %w", err)
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		e.Error(err)
	}
	r, err := e.s.Read(e.ctx, "pd/k", 0, -1)
	if fault.KindOf(err) == fault.NotFound {
		return
	}
	if err != nil {
		e.Fatal(err)
	}
	b, err := io.ReadAll(r)
	closeOrFail(e.T, r)
	if err != nil || !versions[sha256.Sum256(b)] {
		e.Fatalf("after the race the key holds %d bytes that are no Put's object (%v)", len(b), err)
	}
}

// testSiblingsRace guards that what one key goes through never fails or
// changes another under the same prefix: each goroutine puts, reads back,
// and deletes its own keys, some with failing readers, while the others do
// the same beside it. Every call that should succeed does, every
// acknowledged object reads back as written until its own goroutine
// deletes it, and a key that lies untouched under the prefix throughout
// stays.
func testSiblingsRace(e *env) {
	const workers, rounds = 8, 15
	keep := PatternBytes(9, 777)
	e.put("sib/a/b/keep", keep)
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds*3)
	for w := range workers {
		wg.Go(func() {
			for i := range rounds {
				key := fmt.Sprintf("sib/a/b/w%d/k%d", w, i%3)
				if w%2 == 1 {
					key = fmt.Sprintf("sib/a/w%d", w)
				}
				if i%4 == 3 {
					if _, err := e.s.Put(e.ctx, key+"x", &failAfter{r: Pattern(1, 100), n: 10}); !errors.Is(err, errBroken) {
						errs <- fmt.Errorf("a failing put: %w", err)
					}
					continue
				}
				data := PatternBytes(uint64(w*100+i), 500+i) // #nosec G115 -- small, non-negative test counters.
				if _, err := e.s.Put(e.ctx, key, bytes.NewReader(data)); err != nil {
					errs <- fmt.Errorf("put %s: %w", key, err)
					continue
				}
				r, err := e.s.Read(e.ctx, key, 0, -1)
				if err != nil {
					errs <- fmt.Errorf("an acknowledged object is gone: read %s: %w", key, err)
					continue
				}
				got, err := io.ReadAll(r)
				_ = r.Close() // Checked through the bytes.
				if err != nil || !bytes.Equal(got, data) {
					errs <- fmt.Errorf("an acknowledged object changed: %s (%w)", key, err)
				}
				if err := e.s.Delete(e.ctx, key); err != nil {
					errs <- fmt.Errorf("delete %s: %w", key, err)
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		e.Error(err)
	}
	e.wantObject("sib/a/b/keep", keep)
}

// testNewParentsAtOnce guards Puts that need the same new levels of
// prefix, started together: all succeed and all are listed.
func testNewParentsAtOnce(e *env) {
	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, n)
	var want []string
	for i := range n {
		key := fmt.Sprintf("np/a/b/c/k%d", i)
		want = append(want, key)
		wg.Go(func() {
			<-start
			if _, err := e.s.Put(e.ctx, key, bytes.NewReader([]byte(key))); err != nil {
				errs <- err
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		e.Error(err)
	}
	if keys, _ := e.listAll(blob.ListOptions{Prefix: "np/"}); !slices.Equal(keys, want) {
		e.Fatalf("List(np/) = %q, want %q", keys, want)
	}
}
