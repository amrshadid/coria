package importpath

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestCheckTakes guards that paths the rule allows are taken: any UTF-8,
// spaces, dots inside a name, and segments and paths at their bounds.
func TestCheckTakes(t *testing.T) {
	for _, p := range []string{
		"a", "a/b/c", "proj 1/arc001/Sess.01", "über/日本", "...x", "a.", ".hidden",
		strings.Repeat("x", MaxSegmentLength),
		strings.Repeat("ab/", (MaxPathLength-1)/3) + "c",
	} {
		if err := Check(p); err != nil {
			t.Errorf("Check(%.40q) = %v", p, err)
		}
	}
}

// TestCheckRefuses guards each rule of a path: every fault is found, with
// the segment it is in, so a job cannot name a path that leaves its root,
// means another directory on Windows, or cannot be opened.
func TestCheckRefuses(t *testing.T) {
	for _, tc := range []struct {
		p     string
		seg   int
		fault Fault
	}{
		{"", 0, Empty},
		{strings.Repeat("a/", MaxPathLength/2) + "a", 0, TooLong},
		{"/a", 0, Absolute},
		{"a/", 0, TrailingSlash},
		{"a//b", 2, EmptySegment},
		{"a/" + strings.Repeat("x", MaxSegmentLength+1), 2, SegmentTooLong},
		{".", 1, DotSegment},
		{"a/..", 2, DotSegment},
		{"a/./b", 2, DotSegment},
		{`a\b`, 1, BadByte},
		{"c:/x", 1, BadByte},
		{"a/b\x00c", 2, BadByte},
		{"a/\xff", 2, NotUTF8},
	} {
		err := Check(tc.p)
		var pe *PathError
		if !errors.As(err, &pe) || pe.Segment != tc.seg || pe.Fault != tc.fault {
			t.Errorf("Check(%.40q) = %v, want segment %d %v", tc.p, err, tc.seg, tc.fault)
		}
	}
}

// TestCheckErrorHoldsNoPath guards that a refusal names a segment by
// number only: a path can hold a patient's name, and an error reaches
// logs.
func TestCheckErrorHoldsNoPath(t *testing.T) {
	for _, p := range []string{"Doe^Jane/..", "Doe^Jane/a:b", "Doe^Jane//x", "/Doe^Jane", "Doe^Jane/\xff"} {
		err := Check(p)
		if err == nil || strings.Contains(err.Error(), "Doe") {
			t.Errorf("Check(%q) = %v", p, err)
		}
	}
}

// TestCompareWalkOrder guards the walk order: a directory's whole subtree
// before the next name in that directory, which plain byte order of whole
// paths reverses ("-" and "." are below "/").
func TestCompareWalkOrder(t *testing.T) {
	if Compare("a/b/c", "a/b.d") >= 0 || Compare("a/b.d", "a-c") >= 0 || Compare("a/b/c", "a-c") >= 0 {
		t.Fatal("a/b/c < a/b.d < a-c does not hold")
	}
	// Plain byte order gives the reverse, which is why it is not used.
	if "a-c" >= "a/b.d" || "a/b.d" >= "a/b/c" {
		t.Fatal("the premise about byte order is wrong")
	}
	got := []string{"a-c", "a/b.d", "a/b/c", "a", "a/b", "b", "a/b/c/d", "A"}
	slices.SortFunc(got, Compare)
	want := []string{"A", "a", "a/b", "a/b/c", "a/b/c/d", "a/b.d", "a-c", "b"}
	if !slices.Equal(got, want) {
		t.Fatalf("sorted %q, want %q", got, want)
	}
	for _, p := range want {
		if Compare(p, p) != 0 {
			t.Errorf("Compare(%q, itself) != 0", p)
		}
	}
}

// TestOverlap guards when two jobs read the same files: one path, or one
// under the other by whole segments; the empty path is the whole root.
func TestOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"a", "a", true}, {"a", "a/b", true}, {"a/b", "a", true}, {"", "x/y", true},
		{"a", "ab", false}, {"a/b", "a/c", false}, {"ab/c", "a", false},
	} {
		if got := Overlap(tc.a, tc.b); got != tc.want {
			t.Errorf("Overlap(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

// segmentCompare is the walk order written the slow, obvious way: segment
// lists compared element by element, a prefix first.
func segmentCompare(a, b string) int {
	return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
}

// FuzzCompare guards that Compare is the segment-by-segment order on every
// pair of valid paths, and antisymmetric.
func FuzzCompare(f *testing.F) {
	for _, s := range [][2]string{{"a/b/c", "a/b.d"}, {"a/b.d", "a-c"}, {"a", "a/b"}, {"x", "x"}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if Check(a) != nil || Check(b) != nil {
			return
		}
		got := Compare(a, b)
		if want := segmentCompare(a, b); got != want {
			t.Fatalf("Compare(%q, %q) = %d, want %d", a, b, got, want)
		}
		if Compare(b, a) != -got {
			t.Fatalf("Compare is not antisymmetric on %q, %q", a, b)
		}
	})
}

// FuzzCheck guards that a path Check takes holds no segment that could
// leave the root or name another directory, and that a refusal never holds
// the path's text.
func FuzzCheck(f *testing.F) {
	for _, s := range []string{"a/b", "a/../b", "/x", "a\\b", "c:", "a//b", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		err := Check(p)
		if err != nil {
			if len(p) >= 4 && strings.Contains(err.Error(), p) {
				t.Fatalf("the error %q holds the path", err)
			}
			return
		}
		if len(p) > MaxPathLength {
			t.Fatal("took a path too long")
		}
		for seg := range strings.SplitSeq(p, "/") {
			if seg == "" || seg == "." || seg == ".." || len(seg) > MaxSegmentLength || strings.ContainsAny(seg, "\\:\x00") {
				t.Fatalf("took %q with segment %q", p, seg)
			}
		}
	})
}
