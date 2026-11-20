package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/amrshadid/go-dicom/dataset"

	"github.com/amrshadid/coria/internal/core/naming"
)

// TestSequencesAndPrivateBlocks guards the walk: rules reach elements at
// any depth and by path; a private element is matched by its creator as
// its own item declares it, compared exactly; a block's creator follows
// its block; a private element no rule names is removed; an item is
// removed with its sequence and its values become needles.
func TestSequencesAndPrivateBlocks(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{},
		rule("names", vrSel("PN"), aReplace("ANON")),
		rule("deep", pathSel("RequestAttributesSequence[*]/RequestedProcedureID"), aRemove),
		rule("vendor", privSel("VENDOR", "0019", "10"), aKeep),
		rule("others", kw("OtherPatientIDsSequence"), aRemove),
	)
	inner := build(t,
		el{0x00190010, "LO", "VENDOR"},
		el{0x00191010, "LO", "kept"},
		el{0x00191011, "LO", "dropped"},
		el{0x00190011, "LO", "OTHER"},
		el{0x00191110, "LO", "other creator"},
		el{0x00401001, "SH", "RP1"},
		el{tPatientName, "PN", "Inner^Name"},
	)
	lower := build(t, el{0x00190010, "LO", "vendor"}, el{0x00191010, "LO", "case differs"})
	ds := build(t,
		el{tPatientName, "PN", "Doe^John"},
		el{0x00101002, "SQ", []*dataset.Dataset{build(t, el{tPatientID, "LO", "OTHER-ID"})}},
		el{0x00400275, "SQ", []*dataset.Dataset{inner, lower}},
		el{0x00401001, "SH", "RP-TOP"},
	)
	rec := apply(t, en, ds)
	got := item(t, ds, 0x00400275, 0)
	if v, _ := text(got, tPatientName); v != "ANON" {
		t.Errorf("a name in an item: %q", v)
	}
	if got.Contains(tagOf(0x00401001)) {
		t.Error("the path's element is still in the item")
	}
	if v, _ := text(ds, 0x00401001); v != "RP-TOP" {
		t.Errorf("the path reached the top level: %q", v)
	}
	if v, _ := text(got, 0x00191010); v != "kept" || !got.Contains(tagOf(0x00190010)) {
		t.Errorf("the kept private element or its creator: %q", v)
	}
	for _, gone := range []uint32{0x00191011, 0x00190011, 0x00191110} {
		if got.Contains(tagOf(gone)) {
			t.Errorf("%08X is still in the item", gone)
		}
	}
	other := item(t, ds, 0x00400275, 1)
	if other.Contains(tagOf(0x00191010)) || other.Contains(tagOf(0x00190010)) {
		t.Error("a creator that differs in case was taken as the rule's")
	}
	if ds.Contains(tagOf(0x00101002)) {
		t.Error("the removed sequence is present")
	}
	found := false
	for _, n := range rec.Needles {
		found = found || n.Value == "OTHER-ID"
	}
	if !found {
		t.Error("a value of a removed sequence is not a needle")
	}
	readFile(t, writeFile(t, ds))
}

// TestContentItemSelector guards a content item rule: it selects the
// value of the structured report content item whose concept name it
// names, at any depth of the content tree, and nothing else.
func TestContentItemSelector(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{}, rule("finding", itemSel("DCM", "121071"), aReplace("removed")))
	concept := func(scheme, value string) []*dataset.Dataset {
		return []*dataset.Dataset{build(t, el{0x00080100, "SH", value}, el{0x00080102, "SH", scheme})}
	}
	nested := build(t, el{0x0040A043, "SQ", concept("DCM", "121071")}, el{0x0040A160, "UT", "John has a finding"})
	other := build(t, el{0x0040A043, "SQ", concept("DCM", "999999")}, el{0x0040A160, "UT", "kept text"})
	root := build(t, el{0x0040A730, "SQ", []*dataset.Dataset{nested, other}})
	ds := build(t, el{0x0040A730, "SQ", []*dataset.Dataset{root}})
	apply(t, en, ds)
	top := item(t, ds, 0x0040A730, 0)
	if v, _ := text(item(t, top, 0x0040A730, 0), 0x0040A160); v != "removed" {
		t.Errorf("the item's value: %q", v)
	}
	if v, _ := text(item(t, top, 0x0040A730, 1), 0x0040A160); v != "kept text" {
		t.Errorf("another item's value: %q", v)
	}
}

// TestFixedElements guards what the engine does whatever the profile
// says: pixel data kept even under a rule that removes its VR, Original
// Attributes and trailing padding removed, group lengths removed.
func TestFixedElements(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{}, rule("ob", vrSel("OW"), aRemove), rule("keep-all", vrSel("SQ"), aKeep))
	ds := build(t,
		el{0x00080000, "UL", []byte{4, 0, 0, 0}},
		el{0x04000561, "SQ", []*dataset.Dataset{build(t, el{tPatientName, "PN", "Doe^John"})}},
		el{0x7FE00010, "OW", []byte{1, 2, 3, 4}},
		el{0xFFFCFFFC, "OB", []byte{0, 0}},
	)
	rec := apply(t, en, ds)
	if !ds.Contains(tagOf(0x7FE00010)) {
		t.Error("pixel data was removed")
	}
	for _, gone := range []uint32{0x00080000, 0x04000561, 0xFFFCFFFC} {
		if ds.Contains(tagOf(gone)) {
			t.Errorf("%08X is present", gone)
		}
	}
	if len(rec.Needles) != 1 || rec.Needles[0].Value != "Doe^John" {
		t.Errorf("the Original Attributes' values as needles: %v entries", len(rec.Needles))
	}
}

// TestAVRTheRuleWasNotCheckedForFails guards an element whose VR in the
// file is not the one its rule was checked for: hashing an element the
// file holds as a date fails, removing it does not, and an element held
// as UN is treated as the dictionary's VR.
func TestAVRTheRuleWasNotCheckedForFails(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{}, rule("id", kw("PatientID"), aHash))
	var f *Failure
	if _, err := en.Apply(context.Background(), build(t, el{tPatientID, "DA", "20240101"}), Reception{}); !errors.As(err, &f) || f.Cause != CauseBadValue {
		t.Errorf("a hash of an element held as DA: %v", err)
	}
	un := build(t, el{tPatientID, "UN", "PAT001"})
	apply(t, en, un)
	elem, _ := un.Get(tagOf(tPatientID))
	if got := mustText(t, un, tPatientID); got != naming.HashText(testKeys.Hash, []byte("PAT001")) || elem.GetVR() != "LO" {
		t.Errorf("an element held as UN: %q, VR %s", got, elem.GetVR())
	}
	rm := newTestEngine(t, testKeys, Options{}, rule("id", kw("PatientID"), aRemove))
	apply(t, rm, build(t, el{tPatientID, "DA", "20240101"}))
}

// TestNonASCIIValuesNeedUTF8 guards the character set of values written:
// a constant outside the default repertoire makes a data set that
// declares none declare ISO_IR 192, and fails in one that declares
// another set.
func TestNonASCIIValuesNeedUTF8(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{}, rule("c", kw("PatientComments"), aReplace("Müller entfernt")))
	ds := build(t, el{tPatientComments, "LT", "x"})
	apply(t, en, ds)
	if v, _ := text(ds, 0x00080005); v != "ISO_IR 192" {
		t.Errorf("declared %q", v)
	}
	back := readFile(t, writeFile(t, ds))
	if v, _ := text(back, tPatientComments); v != "Müller entfernt" {
		t.Errorf("read back %q", v)
	}
	var f *Failure
	latin := build(t, el{0x00080005, "CS", "ISO_IR 100"}, el{tPatientComments, "LT", "x"})
	if _, err := en.Apply(context.Background(), latin, Reception{}); !errors.As(err, &f) || f.Cause != CauseBadValue {
		t.Errorf("under ISO_IR 100: %v", err)
	}
}
