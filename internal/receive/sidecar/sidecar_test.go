package sidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/core/layout"
)

// inst returns an instance of the scan s/e/c with UID u.
func inst(t *testing.T, u string, withPixels bool) Instance {
	t.Helper()
	k, err := layout.InstanceKey(label.Path{Subject: "s", Session: "e", Scan: "c"}, u)
	if err != nil {
		t.Fatal(err)
	}
	in := Instance{SOPInstanceUID: u, Key: k, Size: 1000, SHA256: strings.Repeat("ab", 32),
		Dataset: json.RawMessage(`{"00080018":{"vr":"UI","Value":["` + u + `"]}}`)}
	if withPixels {
		in.PixelData = &PixelData{Offset: 300, Length: 700}
	}
	return in
}

// readAll reads every instance of a sidecar.
func readAll(t *testing.T, b []byte) []Instance {
	t.Helper()
	r := NewReader(bytes.NewReader(b))
	var out []Instance
	for {
		in, _, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, in)
	}
}

// TestWriteAndReadBack guards the format: what is written reads back the
// same, in order, and the bytes are the one form.
func TestWriteAndReadBack(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	for _, u := range []string{"1.2.3", "1.2.4", "1.2.5"} {
		if err := w.Add(inst(t, u, u != "1.2.4")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, b.Bytes())
	if len(got) != 3 || got[1].PixelData != nil || got[0].PixelData.Offset != 300 || got[2].SOPInstanceUID != "1.2.5" {
		t.Fatalf("read back %+v", got)
	}
	if !strings.HasPrefix(b.String(), header) || !strings.HasSuffix(b.String(), "\n"+footer) || strings.Count(b.String(), "\n") != 5 {
		t.Fatalf("not the one form:\n%s", b.String())
	}
}

// TestOrderAndBounds guards that an instance out of order is refused, and a
// data set over the bound is omitted, not kept and not refused.
func TestOrderAndBounds(t *testing.T) {
	w := NewWriter(io.Discard)
	if err := w.Add(inst(t, "1.2.5", false)); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(inst(t, "1.2.4", false)); !errors.Is(err, ErrOrder) {
		t.Fatalf("out of order gave %v", err)
	}
	var b bytes.Buffer
	w = NewWriter(&b)
	big := inst(t, "1.2.3", false)
	big.Dataset = json.RawMessage(`"` + strings.Repeat("x", MaxDataset) + `"`)
	if err := w.Add(big); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	got := readAll(t, b.Bytes())
	if len(got) != 1 || got[0].Dataset != nil || !got[0].DatasetOmitted {
		t.Fatalf("an over-long data set was kept: %+v", got)
	}
	if err := NewWriter(io.Discard).Close(); err == nil {
		t.Fatal("an empty sidecar was written")
	}
}

// TestMergeReplacesAndDrops guards a late arrival's merge: the old
// instances stay in order, an added one replaces the old one under its UID,
// and a dropped one is left out.
func TestMergeReplacesAndDrops(t *testing.T) {
	var old bytes.Buffer
	w := NewWriter(&old)
	for _, u := range []string{"1.1", "1.3", "1.5"} {
		_ = w.Add(inst(t, u, false))
	}
	_ = w.Close()
	var out bytes.Buffer
	n, err := Merge(&out, bytes.NewReader(old.Bytes()), List([]Instance{inst(t, "1.2", true), inst(t, "1.3", true), inst(t, "1.6", true)}),
		func(u string) bool { return u == "1.5" })
	if err != nil {
		t.Fatal(err)
	}
	got := readAll(t, out.Bytes())
	var uids []string
	for _, in := range got {
		uids = append(uids, in.SOPInstanceUID)
	}
	if n != 4 || strings.Join(uids, " ") != "1.1 1.2 1.3 1.6" || got[2].PixelData == nil {
		t.Fatalf("merged %d: %v", n, uids)
	}
	n, err = Merge(io.Discard, nil, List([]Instance{inst(t, "1.1", false)}), nil)
	if err != nil || n != 1 {
		t.Fatalf("a merge into nothing gave %d, %v", n, err)
	}
}

// TestReaderRefusesOtherForms guards that a damaged sidecar is refused, not
// half read.
func TestReaderRefusesOtherForms(t *testing.T) {
	var good bytes.Buffer
	w := NewWriter(&good)
	_ = w.Add(inst(t, "1.1", false))
	_ = w.Close()
	for name, b := range map[string]string{
		"cut":     good.String()[:good.Len()-4],
		"version": strings.Replace(good.String(), `"version":1`, `"version":2`, 1),
		"field":   strings.Replace(good.String(), `"size":`, `"other":1,"size":`, 1),
		"after":   good.String() + "x",
	} {
		r := NewReader(strings.NewReader(b))
		var err error
		for err == nil {
			_, _, err = r.Next()
		}
		if !errors.Is(err, ErrFormat) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}
