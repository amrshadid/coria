package engine

import (
	"context"
	"testing"

	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/validate"
	"github.com/amrshadid/go-dicom/valuerep"

	"github.com/amrshadid/coria/internal/deid/profile"
)

// TestEveryCodeResolvesByType guards PS3.15 E.1.1's resolution of each
// code of Table E.1-1 by the attribute's Type, and toward keeping the
// attribute present when its Type is not known.
func TestEveryCodeResolvesByType(t *testing.T) {
	const unknown = -1
	cases := []struct {
		code Code
		typ  validate.AttributeType
		want Resolution
	}{
		{CodeDummy, validate.Type3, ResolveDummy},
		{CodeZero, validate.Type1, ResolveEmpty},
		{CodeRemove, validate.Type1, ResolveRemove},
		{CodeKeep, validate.Type3, ResolveKeep},
		{CodeClean, validate.Type3, ResolveClean},
		{CodeUID, validate.Type3, ResolveUID},
		{CodeXZ, validate.Type3, ResolveRemove},
		{CodeXZ, validate.Type2, ResolveEmpty},
		{CodeXZ, validate.Type1, ResolveEmpty},
		{CodeXZ, unknown, ResolveEmpty},
		{CodeXD, validate.Type3, ResolveRemove},
		{CodeXD, validate.Type2C, ResolveDummy},
		{CodeXD, validate.Type1C, ResolveDummy},
		{CodeZD, validate.Type2, ResolveEmpty},
		{CodeZD, validate.Type3, ResolveEmpty},
		{CodeZD, validate.Type1, ResolveDummy},
		{CodeZD, unknown, ResolveDummy},
		{CodeXZD, validate.Type3, ResolveRemove},
		{CodeXZD, validate.Type2, ResolveEmpty},
		{CodeXZD, validate.Type1, ResolveDummy},
		{CodeXZD, unknown, ResolveDummy},
		{CodeXZU, validate.Type3, ResolveRemove},
		{CodeXZU, validate.Type2, ResolveEmpty},
		{CodeXZU, validate.Type1, ResolveUID},
	}
	for _, c := range cases {
		got, err := Resolve(c.code, c.typ, c.typ != unknown)
		if err != nil || got != c.want {
			t.Errorf("%s at Type %v: %v, %v; want %v", c.code, c.typ, got, err, c.want)
		}
	}
	if _, err := Resolve("X/Y", validate.Type1, true); err == nil {
		t.Error("an unknown code resolved")
	}
}

// TestTypesComeFromTheLibrarysIODs guards IODTypes against attributes
// whose Types PS3.3 states: in the CT Image IOD, Patient's Name is Type 2
// (C.7.1.1), Study Instance UID and SOP Instance UID Type 1 (C.7.2.1,
// C.12.1), Patient Comments Type 3, Referenced Image Sequence Type 3 with
// its Referenced SOP Instance UID Type 1 (C.7.6.1, Table 10-11); in the
// Enhanced CT Image IOD, Pixel Spacing in the shared Pixel Measures macro
// is 1C (C.7.6.16.2.1); and nothing is known of a SOP class PS3.4 does not
// list.
func TestTypesComeFromTheLibrarysIODs(t *testing.T) {
	it := &IODTypes{}
	cases := []struct {
		sop   string
		seqs  []uint32
		tag   uint32
		want  validate.AttributeType
		known bool
	}{
		{ctImageStorage, nil, tPatientName, validate.Type2, true},
		{ctImageStorage, nil, tStudyUID, validate.Type1, true},
		{ctImageStorage, nil, tSOPInstance, validate.Type1, true},
		{ctImageStorage, nil, tPatientComments, validate.Type3, true},
		{ctImageStorage, nil, 0x00081140, validate.Type3, true},
		{ctImageStorage, []uint32{0x00081140}, tRefSOPInstance, validate.Type1, true},
		{"1.2.840.10008.5.1.4.1.1.2.1", []uint32{0x52009229, 0x00289110}, 0x00280030, validate.Type1C, true},
		{"1.2.3.4", nil, tPatientName, 0, false},
	}
	for _, c := range cases {
		for range 2 { // the second answer is the kept one
			got, ok := it.Type(c.sop, c.seqs, c.tag)
			if ok != c.known || (ok && got != c.want) {
				t.Errorf("%s %v %08X: %v %v, want %v %v", c.sop, c.seqs, c.tag, got, ok, c.want, c.known)
			}
		}
	}
}

// smallTable is a column of Table E.1-1 written in the test: CORIA ships
// no copy of the standard's table.
type smallTable map[uint32]Code

func (s smallTable) Code(t uint32) (Code, bool) { c, ok := s[t]; return c, ok }

// TestATableResolvesPerObject guards ResolveIn: a table's code resolved by
// the Type the object's IOD gives the attribute, and false for an
// attribute the table does not name.
func TestATableResolvesPerObject(t *testing.T) {
	table := smallTable{tPatientName: CodeZ, tStudyUID: CodeUID, tPatientComments: CodeXZD, tReferring: CodeXZD}
	it := &IODTypes{}
	for tg, want := range map[uint32]Resolution{tPatientName: ResolveEmpty, tStudyUID: ResolveUID, tPatientComments: ResolveRemove, tReferring: ResolveEmpty} {
		got, ok, err := ResolveIn(table, it, ctImageStorage, nil, tg)
		if err != nil || !ok || got != want {
			t.Errorf("%08X: %v %v %v, want %v", tg, got, ok, err, want)
		}
	}
	if _, ok, _ := ResolveIn(table, it, ctImageStorage, nil, tModality); ok {
		t.Error("an attribute the table does not name resolved")
	}
}

// CodeZ is Z, named apart so the table above reads like the standard's.
const CodeZ = CodeZero

// fakeDecider decides by tag, for the actions the language does not yet
// write.
type fakeDecider map[uint32]Action

func (f fakeDecider) Decide(loc profile.Location, _ profile.Facts) Decision {
	if a, ok := f[loc.Tag]; ok {
		return Decision{Rule: 0, Name: "fake", Action: a}
	}
	return Decision{Rule: -1, Action: Action{Kind: ActKeep}}
}
func (fakeDecider) Sets() []SetRule      { return nil }
func (fakeDecider) Templates() []string  { return nil }
func (fakeDecider) DateShiftDays() int32 { return 365 }

// TestRemoveUnlessRequiredAndDummy guards the two actions decided by the
// object's IOD and the VR: an optional element is removed, one required
// present is emptied, one required with a value takes the fallback (a
// dummy, or a remapped UID; a sequence kept and walked), an unknown SOP
// class keeps every such element with the fallback; dummy writes a value
// valid for the VR, and empties a sequence.
func TestRemoveUnlessRequiredAndDummy(t *testing.T) {
	dec := fakeDecider{
		tPatientName:     {Kind: ActRemoveUnlessRequired, Fallback: FallbackDummy},
		tStudyUID:        {Kind: ActRemoveUnlessRequired, Fallback: FallbackUID},
		tPatientComments: {Kind: ActRemoveUnlessRequired, Fallback: FallbackDummy},
		tStudyDate:       {Kind: ActDummy},
		tRefStudySeq:     {Kind: ActDummy},
		0x00081140:       {Kind: ActRemoveUnlessRequired, Fallback: FallbackUID},
	}
	en, err := newEngine(dec, testKeys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(sop string) *dataset.Dataset {
		return build(t,
			el{tSOPClass, "UI", sop},
			el{tStudyDate, "DA", "20240101"},
			el{tRefStudySeq, "SQ", []*dataset.Dataset{build(t, el{tRefSOPInstance, "UI", "1.2.9"})}},
			el{0x00081140, "SQ", []*dataset.Dataset{build(t, el{tRefSOPInstance, "UI", "1.2.10"})}},
			el{tPatientName, "PN", "Doe^John"},
			el{tPatientComments, "LT", "a comment"},
			el{tStudyUID, "UI", "1.2.3"},
		)
	}
	ct := mk(ctImageStorage)
	if _, err := en.Apply(context.Background(), ct, Reception{}); err != nil {
		t.Fatal(err)
	}
	if v, ok := text(ct, tPatientName); !ok || v != "" {
		t.Errorf("Type 2 name: %q %v", v, ok)
	}
	if v := mustText(t, ct, tStudyUID); v != RemapUID(testKeys.UID, "1.2.3") {
		t.Errorf("Type 1 UID: %q", v)
	}
	if ct.Contains(tagOf(tPatientComments)) || ct.Contains(tagOf(0x00081140)) {
		t.Error("a Type 3 element is present")
	}
	if v := mustText(t, ct, tStudyDate); v != "19000101" {
		t.Errorf("dummy date %q", v)
	}
	if n, _ := ct.SequenceLength(tagOf(tRefStudySeq)); n != 0 {
		t.Errorf("a dummy sequence has %d items", n)
	}
	other := mk("1.2.3.4.5")
	if _, err := en.Apply(context.Background(), other, Reception{}); err != nil {
		t.Fatal(err)
	}
	if v := mustText(t, other, tPatientName); v != "DUMMY" {
		t.Errorf("unknown SOP class, name: %q", v)
	}
	if v := mustText(t, other, tPatientComments); v != "DUMMY" {
		t.Errorf("unknown SOP class, comments: %q", v)
	}
	if n, _ := other.SequenceLength(tagOf(0x00081140)); n != 1 {
		t.Errorf("unknown SOP class, a sequence kept for its UIDs has %d items", n)
	}
	readFile(t, writeFile(t, other))
}

// TestEveryDummyIsAValueOfItsVR guards the dummy table: every VR of PS3.5
// 6.2 but SQ has one, each text one passes the DICOM library's check of
// its VR, each binary one holds whole values of its width, and the slice
// returned is the caller's.
func TestEveryDummyIsAValueOfItsVR(t *testing.T) {
	widths := map[string]int{"AT": 4, "FD": 8, "FL": 4, "SL": 4, "SS": 2, "SV": 8, "UL": 4, "US": 2, "UV": 8}
	for _, vr := range []string{"AE", "AS", "AT", "CS", "DA", "DS", "DT", "FD", "FL", "IS", "LO", "LT", "OB", "OD", "OF", "OL", "OV", "OW", "PN", "SH", "SL", "SS", "ST", "SV", "TM", "UC", "UI", "UL", "UN", "UR", "US", "UT", "UV"} {
		d, ok := Dummy(vr)
		if !ok {
			t.Errorf("no dummy for %s", vr)
			continue
		}
		if w, binary := widths[vr]; binary {
			if len(d) != w {
				t.Errorf("%s dummy is %d bytes", vr, len(d))
			}
			continue
		}
		if len(d) > 0 {
			if err := valuerep.ValidateValue(vr, string(d)); err != nil {
				t.Errorf("%s dummy %q: %v", vr, d, err)
			}
		}
		if len(d) == 0 {
			continue
		}
		d[0] = 'x'
		if again, _ := Dummy(vr); again[0] == 'x' {
			t.Errorf("%s: the dummy table was changed through a returned slice", vr)
		}
	}
	if _, ok := Dummy("SQ"); ok {
		t.Error("SQ has a byte dummy")
	}
}
