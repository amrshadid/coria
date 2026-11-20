package engine

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/amrshadid/go-dicom/dataset"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/core/naming"
	"github.com/amrshadid/coria/internal/deid/profile"
	"github.com/amrshadid/coria/internal/receive/header"
)

// panicDecider panics deciding the element at tag.
type panicDecider struct {
	fakeDecider
	at uint32
}

func (p panicDecider) Decide(loc profile.Location, f profile.Facts) Decision {
	if loc.Tag == p.at {
		panic("a fault deciding " + formatTag(loc.Tag))
	}
	return p.fakeDecider.Decide(loc, f)
}

// TestAPanicIsARefusalAndChangesNothing guards that a fault of the engine
// is a Failure of cause internal, never a half-changed data set: a panic
// while planning leaves the data set untouched, and one part way through
// making the changes undoes those made.
func TestAPanicIsARefusalAndChangesNothing(t *testing.T) {
	mk := func() *dataset.Dataset { return sample(t) }
	en, err := newEngine(panicDecider{at: tStudyDesc}, testKeys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ds := mk()
	before := writeFile(t, ds)
	_, err = en.Apply(context.Background(), ds, Reception{})
	var f *Failure
	if !errors.As(err, &f) || f.Cause != CauseInternal || f.Reason() != ReasonFailed {
		t.Fatalf("a panic planning: %v", err)
	}
	if strings.Contains(err.Error(), "deciding") {
		t.Errorf("the panic's text reached the error: %v", err)
	}
	if !bytes.Equal(before, writeFile(t, ds)) {
		t.Error("a panic planning changed the data set")
	}

	half := newTestEngine(t, testKeys, Options{Lookups: &mapLookups{codes: map[string]string{}}, Method: "m@1"}, sampleRules()...)
	half.seams.beforeOp = func(i int) {
		if i == 6 {
			panic("a fault applying")
		}
	}
	ds = mk()
	before = writeFile(t, ds)
	if _, err := half.Apply(context.Background(), ds, Reception{}); !errors.As(err, &f) || f.Cause != CauseInternal {
		t.Fatalf("a panic applying: %v", err)
	}
	if !bytes.Equal(before, writeFile(t, ds)) {
		t.Error("a panic applying left the data set half changed")
	}
}

// countingLookups is Lookups that counts the codes it assigns.
type countingLookups struct {
	mapLookups
	assigned int
}

func (c *countingLookups) Assign(ctx context.Context, table, key string) (string, error) {
	c.assigned++
	return c.mapLookups.Assign(ctx, table, key)
}

// TestLookupsAssignOnlyAfterThePlan guards that a table assigns a code
// only for a file the engine goes on to change: a file refused by a later
// rule, or a later value, takes none; one that succeeds takes one for each
// value, once though it appears twice.
func TestLookupsAssignOnlyAfterThePlan(t *testing.T) {
	lk := &countingLookups{mapLookups: mapLookups{codes: map[string]string{}}}
	en := newTestEngine(t, testKeys, Options{Lookups: lk},
		rule("id", kw("PatientID"), aLookup("subjects", true)),
		rule("other", kw("OtherPatientIDs"), aLookup("subjects", true)),
		rule("no-sr", kw("ContentSequence"), aQuarantine),
		rule("date", kw("StudyDate"), aShift),
	)
	var f *Failure
	refused := build(t, el{tPatientID, "LO", "PAT001"}, el{tContentSequence, "SQ", []*dataset.Dataset{build(t)}})
	if _, err := en.Apply(context.Background(), refused, Reception{}); !errors.As(err, &f) || f.Cause != CauseRuleQuarantine {
		t.Fatalf("refused: %v", err)
	}
	bad := build(t, el{tPatientID, "LO", "PAT001"}, el{tStudyDate, "DA", "20241345"})
	if _, err := en.Apply(context.Background(), bad, Reception{}); !errors.As(err, &f) || f.Cause != CauseBadValue {
		t.Fatalf("a bad date: %v", err)
	}
	if lk.assigned != 0 {
		t.Fatalf("refused files took %d codes", lk.assigned)
	}
	ok := build(t, el{tPatientID, "LO", "PAT001"}, el{0x00101000, "LO", "PAT001"})
	apply(t, en, ok)
	if lk.assigned != 1 || mustText(t, ok, tPatientID) != "A0001" || mustText(t, ok, 0x00101000) != "A0001" {
		t.Errorf("assigned %d: %q %q", lk.assigned, mustText(t, ok, tPatientID), mustText(t, ok, 0x00101000))
	}
}

// TestKeysAndSetRulesAreChecked guards New's and the plan's checks: two
// equal keys are refused; a set rule whose VR is not one VR that holds
// text refuses the file.
func TestKeysAndSetRulesAreChecked(t *testing.T) {
	e, problems := profile.Expand(&v1.ProfileDocument{}, profile.Shipped())
	if problems != nil {
		t.Fatal(problems)
	}
	for _, keys := range []Keys{
		{UID: key('a'), Date: key('a'), Hash: key('h')},
		{UID: key('u'), Date: key('d'), Hash: key('u')},
		{UID: key('u'), Date: key('d'), Hash: key('d')},
	} {
		if _, err := New(e, keys, Options{}); err == nil {
			t.Error("equal keys were taken")
		}
	}
	for _, vr := range []string{"US or SS", "US", ""} {
		dec := setDecider{SetRule{Rule: 0, Tag: 0x00280106, VR: vr}}
		en, err := newEngine(dec, testKeys, Options{})
		if err != nil {
			t.Fatal(err)
		}
		var f *Failure
		if _, err := en.Apply(context.Background(), build(t), Reception{}); !errors.As(err, &f) || f.Cause != CauseBadValue {
			t.Errorf("a set rule of VR %q: %v", vr, err)
		}
	}
}

// setDecider is a decider with one set rule, which decides its element.
type setDecider struct{ s SetRule }

func (d setDecider) Decide(loc profile.Location, _ profile.Facts) Decision {
	if loc.Tag == d.s.Tag {
		return Decision{Rule: d.s.Rule, Action: Action{Kind: ActSet, Constant: "1"}}
	}
	return Decision{Rule: -1, Action: Action{Kind: ActKeep}}
}
func (d setDecider) Sets() []SetRule    { return []SetRule{d.s} }
func (setDecider) Templates() []string  { return nil }
func (setDecider) DateShiftDays() int32 { return 365 }

// TestUnknownBytesAreNeverKeptUnread guards a value of VR UN the engine
// cannot see into: kept, one that begins with an item tag (a sequence the
// library kept as bytes) refuses the file; removed, it goes; and a UN
// value of text is kept.
func TestUnknownBytesAreNeverKeptUnread(t *testing.T) {
	items := append([]byte{0xFE, 0xFF, 0x00, 0xE0, 0x10, 0, 0, 0}, []byte("\x10\x00\x10\x00\x08\x00\x00\x00Doe^John")...)
	keep := newTestEngine(t, testKeys, Options{}, rule("vendor", privSel("VENDOR", "0029", "xx"), aKeep))
	ds := build(t, el{0x00290010, "LO", "VENDOR"}, el{0x00291010, "UN", items})
	var f *Failure
	if _, err := keep.Apply(context.Background(), ds, Reception{}); !errors.As(err, &f) || f.Cause != CauseBadValue || f.Path != "00291010" {
		t.Errorf("a UN sequence kept: %v", err)
	}
	unknown := build(t, el{0x00089998, "UN", items})
	if _, err := keep.Apply(context.Background(), unknown, Reception{}); !errors.As(err, &f) || f.Cause != CauseBadValue {
		t.Errorf("an unknown standard element holding items, kept by default: %v", err)
	}
	rm := newTestEngine(t, testKeys, Options{})
	gone := build(t, el{0x00290010, "LO", "VENDOR"}, el{0x00291010, "UN", items})
	apply(t, rm, gone)
	if gone.Contains(tagOf(0x00291010)) {
		t.Error("the private UN sequence stayed")
	}
	plain := build(t, el{0x00290010, "LO", "VENDOR"}, el{0x00291010, "UN", "plain"})
	apply(t, keep, plain)
	if !plain.Contains(tagOf(0x00291010)) {
		t.Error("a UN value of text was not kept")
	}
}

// TestCreatorsAndContentItemsAnywhere guards two readings of a data set: a
// private creator padded with a NUL or spaces is its creator; and a
// content item is any item with a Value Type, here one of the Acquisition
// Context Sequence (0040,0555), which a content item rule selects.
func TestCreatorsAndContentItemsAnywhere(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{},
		rule("vendor", privSel("VENDOR", "0029", "10"), aKeep),
		rule("operator", itemSel("DCM", "113870", "PNAME"), aReplace("OPERATOR")),
		rule("note", itemSel("DCM", "121106", "TEXT"), aRemove),
	)
	concept := func(value string) []*dataset.Dataset {
		return []*dataset.Dataset{build(t, el{0x00080100, "SH", value}, el{0x00080102, "SH", "DCM"})}
	}
	ds := build(t,
		el{0x00290010, "LO", " VENDOR\x00"},
		el{0x00291010, "LO", "kept"},
		el{0x00400555, "SQ", []*dataset.Dataset{
			build(t, el{0x0040A040, "CS", "PNAME "}, el{0x0040A043, "SQ", concept("113870")}, el{0x0040A123, "PN", "Doe^John"}),
			build(t, el{0x0040A040, "CS", "TEXT"}, el{0x0040A043, "SQ", concept("121106")}, el{0x0040A160, "UT", "John Doe was here"}),
		}},
	)
	apply(t, en, ds)
	if v, _ := text(ds, 0x00291010); v != "kept" {
		t.Errorf("the padded creator's element: %q", v)
	}
	if v, _ := text(item(t, ds, 0x00400555, 0), 0x0040A123); v != "OPERATOR" {
		t.Errorf("a person name content item in the acquisition context: %q", v)
	}
	if item(t, ds, 0x00400555, 1).Contains(tagOf(0x0040A160)) {
		t.Error("a text content item in the acquisition context stayed")
	}
}

// TestHashedPatientIDIsTheSubjectsCode guards that the engine's hash of
// Patient ID and the subject label's code, made by the naming rule from
// the header the receiver reads, are made of the same text and so are
// equal, padding and leading spaces included (PS3.5 6.2: an LO's leading
// and trailing spaces are not part of its value).
func TestHashedPatientIDIsTheSubjectsCode(t *testing.T) {
	for _, id := range []string{"PAT001", " PAT001  ", "PAT 001", "pat-001\x00"} {
		ds := build(t,
			el{tSOPClass, "UI", ctImageStorage}, el{tSOPInstance, "UI", "1.2.3.4"},
			el{tStudyUID, "UI", "1.2.3"}, el{0x0020000E, "UI", "1.2.3.5"},
			el{tPatientID, "LO", id},
		)
		b := writeFile(t, ds)
		h, err := header.Read(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		hasher, err := naming.NewHasher(testKeys.Hash)
		if err != nil {
			t.Fatal(err)
		}
		labels, err := naming.Default().Labels(h, hasher)
		if err != nil {
			t.Fatal(err)
		}
		en := newTestEngine(t, testKeys, Options{}, rule("id", kw("PatientID"), aHash))
		got := readFile(t, b)
		apply(t, en, got)
		if want := strings.TrimPrefix(labels.Subject, "sub-"); mustText(t, got, tPatientID) != want {
			t.Errorf("%q: hashed %q, the subject's code %q", id, mustText(t, got, tPatientID), want)
		}
	}
}
