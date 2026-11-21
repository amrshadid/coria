package check

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/amrshadid/go-dicom/anonymize"
	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/filewriter"
	"github.com/amrshadid/go-dicom/tag"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/deid/engine"
	"github.com/amrshadid/coria/internal/deid/profile"
)

// methodEngine returns an engine on the basic profile of PS3.15 Annex E
// that names its method, so it writes the record of the de-identification
// with a code, its meaning, the method and the versions.
func methodEngine(t *testing.T) *engine.Engine {
	t.Helper()
	e, problems := profile.Expand(&v1.ProfileDocument{Bases: []string{profile.AnnexE("basic")}}, profile.Shipped())
	if problems != nil {
		t.Fatal(problems)
	}
	k := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	en, err := engine.New(e, engine.Keys{UID: k('u'), Date: k('d'), Hash: k('h')}, engine.Options{Method: "research@3"})
	if err != nil {
		t.Fatal(err)
	}
	return en
}

// deidentifyName de-identifies a file of a patient of the name given with
// en, writes it, and returns its path and the engine's record.
func deidentifyName(t *testing.T, en *engine.Engine, name string, more ...el) (string, *engine.Record) {
	t.Helper()
	ds := build(t, append([]el{
		{0x00080016, "UI", testSOPClass},
		{0x00080018, "UI", testSOPInstance},
		{0x00080060, "CS", "OT"},
		{0x00100010, "PN", name},
		{0x00100020, "LO", "PID-58213"},
	}, more...)...)
	rec, err := en.Apply(context.Background(), ds, engine.Reception{})
	if err != nil {
		t.Fatal(err)
	}
	// The file meta information names the instance by its remapped UID, as
	// the pipeline's writer does.
	sop := strings.TrimRight(string(ds.GetValue(tag.Tag(0x00080018))), " \x00")
	return writeDS(t, ds, filewriter.FileMetaInfo{MediaStorageSOPInstanceUID: sop}), rec
}

// checkWritten checks the file at path as the pipeline checks a file the
// engine just made: with the run's needles and its record.
func checkWritten(t *testing.T, path string, needles engine.Needles, written engine.Written) (Report, error) {
	t.Helper()
	b := readBytes(t, path)
	return New(Options{}).CheckWritten(context.Background(), bytes.NewReader(b), int64(len(b)), needles, written, nil)
}

// TestTheWrittenMethodIsNoResidual guards a patient whose family name is a
// word of the record of the de-identification the engine writes (PS3.15
// E.1.1): "Coria", the name of the software in De-identification Method
// (0012,0063), and "Basic" and "Profile", words of the basic profile's
// code meaning there and in the code sequence (0012,0064). Checked with
// the run's record, the file passes; checked without it, every element
// searched, the same file is held at the record, which is the file held
// forever that the record prevents.
func TestTheWrittenMethodIsNoResidual(t *testing.T) {
	en := methodEngine(t)
	for _, name := range []string{"Coria^Maria", "Basic^Ann", "Profile^Lee"} {
		t.Run(name, func(t *testing.T) {
			path, rec := deidentifyName(t, en, name)
			if rec.Written.Method == "" || len(rec.Written.MethodCodes) != 1 {
				t.Fatalf("the engine recorded no method it wrote: %q, %d codes", rec.Written.Method, len(rec.Written.MethodCodes))
			}
			if _, err := checkWritten(t, path, rec.Needles, rec.Written); err != nil {
				t.Fatalf("held with the run's record: %v", err)
			}
			_, err := checkWritten(t, path, rec.Needles, engine.Written{})
			var res *Residual
			if !errors.As(err, &res) || res.First.Kind != KindName {
				t.Fatalf("without the record, want a name found in it, got %v", err)
			}
			if p := res.First.Path; p != "00120063" && p != "00120064[0]/00080104" {
				t.Errorf("without the record, found at %s", p)
			}
		})
	}
}

// TestAKeptMethodIsSearched guards a De-identification Method (0012,0063)
// the file was received with and the profile kept: a profile on no Annex
// E base that names no method writes none, so its record names none, and
// the received value, holding the patient's name, is a residual.
func TestAKeptMethodIsSearched(t *testing.T) {
	e, problems := profile.Expand(&v1.ProfileDocument{Rules: []*v1.ProfileRule{
		profileRule("names", vrSelector("PN"), actRemove),
	}}, profile.Shipped())
	if problems != nil {
		t.Fatal(problems)
	}
	k := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	en, err := engine.New(e, engine.Keys{UID: k('u'), Date: k('d'), Hash: k('h')}, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	path, rec := deidentifyName(t, en, "Coria^Maria", el{0x00120063, "LO", "Exported by Coria"})
	if rec.Written.Method != "" {
		t.Fatalf("the engine says it wrote %q", rec.Written.Method)
	}
	_, err = checkWritten(t, path, rec.Needles, rec.Written)
	var res *Residual
	if !errors.As(err, &res) || res.First.Path != "00120063" {
		t.Fatalf("want the kept method held, got %v", err)
	}
}

// writtenOf is a record of the de-identification as the engine writes one
// on the basic profile, for files made here by hand.
var writtenOf = engine.Written{
	Method: `Basic Application Confidentiality Profile\research@3\CORIA unknown\go-dicom unknown`,
	MethodCodes: []anonymize.MethodCode{
		{Value: "113100", Scheme: "DCM", Meaning: "Basic Application Confidentiality Profile"},
	},
}

// codeItem is an item of De-identification Method Code Sequence, with
// more elements after the three of a code.
func codeItem(t *testing.T, c anonymize.MethodCode, more ...el) *dataset.Dataset {
	t.Helper()
	return build(t, append([]el{{0x00080100, "SH", c.Value}, {0x00080102, "SH", c.Scheme}, {0x00080104, "LO", c.Meaning}}, more...)...)
}

// TestOnlyTheExactRecordIsSkipped guards the bounds of what is not
// searched: a De-identification Method that is not exactly the one the
// run wrote, a code sequence with an element more, the run's very value
// inside a sequence item, and bytes in the raw search past the record are
// each searched and found; the run's record itself, at the top level, is
// not.
func TestOnlyTheExactRecordIsSkipped(t *testing.T) {
	coria := needle(engine.NeedleName, "Coria^Maria")
	method := el{0x00120063, "LO", writtenOf.Method}
	codes := el{0x00120064, "SQ", []*dataset.Dataset{codeItem(t, writtenOf.MethodCodes[0])}}
	cases := []struct {
		name string
		els  []el
		want string
		raw  bool
	}{
		{"the record as written", []el{method, codes}, "", false},
		{"a method with a value more", []el{{0x00120063, "LO", writtenOf.Method + `\Coria`}, codes}, "00120063", false},
		{"a method with a word changed", []el{{0x00120063, "LO", `Basic Application Confidentiality Profile\research@3\CORIA 9`}, codes}, "00120063", false},
		{"a code item with an element more", []el{method, {0x00120064, "SQ", []*dataset.Dataset{
			codeItem(t, writtenOf.MethodCodes[0], el{0x00080103, "SH", "Coria"})}}}, "00120064[0]/00080103", false},
		{"a code item more", []el{method, {0x00120064, "SQ", []*dataset.Dataset{
			codeItem(t, writtenOf.MethodCodes[0]), codeItem(t, anonymize.MethodCode{Value: "1", Scheme: "99X", Meaning: "Coria"})}}}, "00120064[1]/00080104", false},
		{"the record's value in an item", []el{method, codes, {0x00400275, "SQ", []*dataset.Dataset{
			build(t, method)}}}, "00400275[0]/00120063", false},
		{"bytes past the record in the raw search", []el{method, codes, {0x00130010, "LO", "ACME 1.0"},
			{0x00131010, "OW", []byte("\x01CORIA\x01\x01")}}, "00131010", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, tc.els...)
			rep, err := checkWritten(t, path, engine.Needles{coria}, writtenOf)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want a pass, got %v: %+v", err, rep.Findings)
				}
				return
			}
			var res *Residual
			if !errors.As(err, &res) {
				t.Fatalf("want a residual at %s, got %v", tc.want, err)
			}
			for _, f := range rep.Findings {
				if f.Path == tc.want && f.Raw == tc.raw {
					return
				}
			}
			t.Fatalf("no finding at %s (raw %v): %+v", tc.want, tc.raw, rep.Findings)
		})
	}
}
