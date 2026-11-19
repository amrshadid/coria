package layout

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/core/label"
)

var scan = label.Path{Subject: "SUB-001", Session: "20240131-1.2.3", Scan: "3-1.2.4"}

// TestBuild guards the three key forms and the prefix, exactly as the
// layout states them.
func TestBuild(t *testing.T) {
	for _, c := range []struct {
		got  func() (string, error)
		want string
	}{
		{func() (string, error) { return InstanceKey(scan, "1.2.5.6") }, "data/SUB-001/20240131-1.2.3/3-1.2.4/1.2.5.6.dcm"},
		{func() (string, error) { return ManifestKey(scan) }, "meta/SUB-001/20240131-1.2.3/3-1.2.4/manifest.json"},
		{func() (string, error) { return MetadataKey(scan) }, "meta/SUB-001/20240131-1.2.3/3-1.2.4/metadata.json"},
		{func() (string, error) { return InstancePrefix(scan) }, "data/SUB-001/20240131-1.2.3/3-1.2.4/"},
	} {
		got, err := c.got()
		if err != nil || got != c.want {
			t.Errorf("got %q %v, want %q", got, err, c.want)
		}
	}
}

// TestBuildRefuses guards that no key is built from a part that is not
// valid: each label, the UID, the kind, and a UID on a meta key.
func TestBuildRefuses(t *testing.T) {
	bad := []Key{
		{Kind: Instance, Path: label.Path{Subject: "..", Session: "a", Scan: "b"}, SOPInstanceUID: "1.2"},
		{Kind: Instance, Path: label.Path{Subject: "a", Session: "b/c", Scan: "d"}, SOPInstanceUID: "1.2"},
		{Kind: Instance, Path: label.Path{Subject: "a", Session: "b", Scan: ""}, SOPInstanceUID: "1.2"},
		{Kind: Instance, Path: label.Path{Subject: "a", Session: "b", Scan: "NUL"}, SOPInstanceUID: "1.2"},
		{Kind: Instance, Path: scan, SOPInstanceUID: ""},
		{Kind: Instance, Path: scan, SOPInstanceUID: "1.02"},
		{Kind: Instance, Path: scan, SOPInstanceUID: "../../x"},
		{Kind: Manifest, Path: scan, SOPInstanceUID: "1.2"},
		{Kind: 0, Path: scan},
		{Kind: 9, Path: scan},
	}
	for _, k := range bad {
		if s, err := k.String(); err == nil || !errors.Is(err, ErrKey) {
			t.Errorf("%+v: built %q, %v", k, s, err)
		}
	}
	if _, err := InstancePrefix(label.Path{}); err == nil {
		t.Error("prefix of empty path built")
	}
}

// TestParse guards the parser on keys it must take and on near misses it
// must refuse because they do not build back to themselves.
func TestParse(t *testing.T) {
	k, err := Parse("data/SUB-001/20240131-1.2.3/3-1.2.4/1.2.5.6.dcm")
	if err != nil || k != (Key{Kind: Instance, Path: scan, SOPInstanceUID: "1.2.5.6"}) {
		t.Fatalf("%+v %v", k, err)
	}
	k, err = Parse("meta/SUB-001/20240131-1.2.3/3-1.2.4/manifest.json")
	if err != nil || k != (Key{Kind: Manifest, Path: scan}) {
		t.Fatalf("%+v %v", k, err)
	}
	k, err = Parse("meta/SUB-001/20240131-1.2.3/3-1.2.4/metadata.json")
	if err != nil || k != (Key{Kind: Metadata, Path: scan}) {
		t.Fatalf("%+v %v", k, err)
	}
	for _, s := range []string{
		"",
		"data",
		"data/",
		"/data/a/b/c/1.2.dcm",
		"data/a/b/c/1.2.dcm/",
		"data//a/b/c/1.2.dcm",
		"data/a/b//1.2.dcm",
		"data/a/b/c/d/1.2.dcm",
		"data/a/b/1.2.dcm",
		"data/a/b/c/1.2.DCM",
		"data/a/b/c/1.2",
		"data/a/b/c/.dcm",
		"data/a/b/c/1.02.dcm",
		"data/a/b/c/1.2.dcm.dcm",
		"data/a/b/c/manifest.json",
		"meta/a/b/c/1.2.dcm",
		"meta/a/b/c/Manifest.json",
		"meta/a/b/c/manifest.json ",
		"Data/a/b/c/1.2.dcm",
		"data/../b/c/1.2.dcm",
		"data/./b/c/1.2.dcm",
		"data/a/b/c\\d/1.2.dcm",
		"data/con/b/c/1.2.dcm",
		"data/a/b./c/1.2.dcm",
		"data/a/b/c/1.2.dcm\x00",
		"data/" + strings.Repeat("a", 129) + "/b/c/1.2.dcm",
		strings.Repeat("a/", 3000),
	} {
		if k, err := Parse(s); err == nil || !errors.Is(err, ErrKey) {
			t.Errorf("%.60q: parsed as %+v, %v", s, k, err)
		}
	}
}

// TestRoundTrip guards the property the layout rests on: every key the
// builder makes parses back to the same parts, over many drawn parts,
// including labels and UIDs of the greatest length.
func TestRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // G404: a fixed seed, so a failure is reproduced; nothing secret is drawn.
	const alpha = "ABCXYZabcxyz0189._-"
	lab := func() string {
		for {
			n := 1 + r.IntN(label.MaxLength)
			if r.IntN(4) == 0 {
				n = label.MaxLength
			}
			b := make([]byte, n)
			for i := range b {
				b[i] = alpha[r.IntN(len(alpha))]
			}
			if label.Valid(string(b)) {
				return string(b)
			}
		}
	}
	id := func() string {
		var b strings.Builder
		b.WriteString("1")
		for r.IntN(12) != 0 {
			c := []string{"0", "2", "840", "10008", "99999999"}[r.IntN(5)]
			if b.Len()+1+len(c) > 64 {
				break
			}
			b.WriteString(".")
			b.WriteString(c)
		}
		return b.String()
	}
	longest := 0
	for range 20000 {
		k := Key{Kind: Kind(1 + r.IntN(3)), Path: label.Path{Subject: lab(), Session: lab(), Scan: lab()}}
		if k.Kind == Instance {
			k.SOPInstanceUID = id()
		}
		s, err := k.String()
		if err != nil {
			t.Fatalf("%+v: %v", k, err)
		}
		longest = max(longest, len(s))
		back, err := Parse(s)
		if err != nil || back != k {
			t.Fatalf("%q: parsed as %+v, %v; built from %+v", s, back, err, k)
		}
	}
	if longest > MaxKeyLength {
		t.Fatalf("built a key of %d bytes, past MaxKeyLength %d", longest, MaxKeyLength)
	}
}

// FuzzParse guards the parser: it never panics, and whatever it accepts
// builds back to the same string, so it accepts only what the builder makes.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"data/a/b/c/1.2.dcm", "meta/a/b/c/manifest.json", "meta/a/b/c/metadata.json",
		"data//b/c/1.2.dcm", "data/a/b/c/1.02.dcm", "data/../b/c/1.dcm",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k, err := Parse(s)
		if err != nil {
			return
		}
		back, err := k.String()
		if err != nil || back != s {
			t.Fatalf("%q parsed as %+v, which builds %q, %v", s, k, back, err)
		}
	})
}
