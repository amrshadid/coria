package engine_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/amrshadid/go-dicom/dataelem"
	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/filebase"
	"github.com/amrshadid/go-dicom/filewriter"
	"github.com/amrshadid/go-dicom/tag"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/deid/check"
	"github.com/amrshadid/coria/internal/deid/engine"
	"github.com/amrshadid/coria/internal/deid/profile"
)

// TestATimeOfZerosIsNoNeedle guards a time of midnight that the basic
// profile removes (Study Time 000000): it is removed, but is no needle, so
// the check after does not find it inside an ordinary decimal the file
// keeps (Data Collection Diameter 500.000000) and hold the file for
// nothing; a time that is not all zeros is still a needle.
func TestATimeOfZerosIsNoNeedle(t *testing.T) {
	ds := dataset.NewDataset()
	for _, e := range []struct {
		g, el uint16
		vr, v string
	}{
		{0x0008, 0x0016, "UI", "1.2.840.10008.5.1.4.1.1.2"}, {0x0008, 0x0018, "UI", "2.25.1234"},
		{0x0008, 0x0030, "TM", "000000"}, {0x0008, 0x0031, "TM", "101530"},
		{0x0010, 0x0020, "LO", "PID12345"}, {0x0018, 0x0090, "DS", "500.000000"},
	} {
		if err := ds.Add(dataelem.NewDataElement(tag.New(e.g, e.el), dataelem.VR(e.vr), []byte(e.v))); err != nil {
			t.Fatal(err)
		}
	}
	exp, problems := profile.Expand(&v1.ProfileDocument{Bases: []string{profile.AnnexE("basic")}}, profile.Shipped())
	if problems != nil {
		t.Fatal(problems)
	}
	k := func(c byte) []byte { return bytes.Repeat([]byte{c}, 32) }
	en, err := engine.New(exp, engine.Keys{UID: k('u'), Date: k('d'), Hash: k('h')}, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := en.Apply(context.Background(), ds, engine.Reception{})
	if err != nil {
		t.Fatal(err)
	}
	if ds.Contains(tag.New(0x0008, 0x0030)) {
		if el, _ := ds.Get(tag.New(0x0008, 0x0030)); el != nil {
			if b, _ := el.Bytes(); len(bytes.Trim(b, " \x00")) > 0 {
				t.Errorf("Study Time kept with a value: %q", b)
			}
		}
	}
	other := false
	for _, n := range rec.Needles {
		if n.Value == "000000" {
			t.Errorf("a time of zeros is a needle (from %s)", n.Path)
		}
		if n.Value == "101530" {
			other = true
		}
	}
	if !other {
		t.Error("a time that is not all zeros is no needle")
	}
	path := filepath.Join(t.TempDir(), "out.dcm")
	f, err := os.Create(path) // #nosec G304 -- the test's own temporary file.
	if err != nil {
		t.Fatal(err)
	}
	w := filewriter.NewDICOMFileWriter(filebase.NewFileWriter(f))
	w.SetFileMetaInfo(&filewriter.FileMetaInfo{MediaStorageSOPClassUID: "1.2.840.10008.5.1.4.1.1.2", MediaStorageSOPInstanceUID: "2.25.1", TransferSyntaxUID: "1.2.840.10008.1.2.1"})
	if err := w.AddDataElements(filewriter.ElementsFromDataset(ds)); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own temporary file.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("500.000000")) {
		t.Fatal("the file does not keep the decimal the test needs")
	}
	if _, err := check.New(check.Options{}).Check(context.Background(), bytes.NewReader(b), int64(len(b)), rec.Needles, nil); err != nil {
		t.Errorf("the check after holds the file: %v", err)
	}
}
