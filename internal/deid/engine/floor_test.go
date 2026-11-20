package engine

import (
	"strings"
	"testing"

	"github.com/amrshadid/go-dicom/dataset"

	"github.com/amrshadid/coria/internal/core/naming"
)

// floorSample is a data set holding a value of each attribute of the
// floor, a person's name in a nested item and a private element written
// as PN, each value distinct so a search for it finds only it.
func floorSample(t testing.TB) *dataset.Dataset {
	return build(t,
		el{tSOPClass, "UI", ctImageStorage},
		el{0x00080092, "ST", "Referrer Street 9"},
		el{0x00080094, "SH", "555-0100"},
		el{tPatientName, "PN", "Floor^Patient"},
		el{tPatientID, "LO", "FLOOR-ID-1"},
		el{tBirthDate, "DA", "19611213"},
		el{0x00100032, "TM", "081500"},
		el{0x00101000, "LO", "OTHER-ID-7"},
		el{0x00101001, "PN", "Other^Name"},
		el{0x00101002, "SQ", []*dataset.Dataset{build(t, el{tPatientID, "LO", "SEQ-ID-3"})}},
		el{0x00101005, "PN", "Birth^Name"},
		el{0x00101040, "LO", "Patient Road 12"},
		el{0x00101060, "PN", "Mother^Maiden"},
		el{0x00102154, "SH", "555-0199"},
		el{0x00102155, "LT", "tel:555-0142"},
		el{0x00400275, "SQ", []*dataset.Dataset{build(t,
			el{0x00401102, "ST", "Person Lane 3"},
			el{0x00401103, "LO", "555-0177"},
			el{0x00401104, "LT", "mailto:x@example.org"},
			el{0x00321032, "PN", "Requesting^Doctor"},
		)}},
		el{0x00290010, "LO", "VENDOR"},
		el{0x00291001, "PN", "Private^Person"},
	)
}

// floorValues are the identifying values of floorSample.
var floorValues = []string{
	"Referrer Street 9", "555-0100", "Floor^Patient", "FLOOR-ID-1", "19611213", "081500", "OTHER-ID-7",
	"Other^Name", "SEQ-ID-3", "Birth^Name", "Patient Road 12", "Mother^Maiden", "555-0199", "tel:555-0142",
	"Person Lane 3", "555-0177", "mailto:x@example.org", "Requesting^Doctor", "Private^Person",
}

// TestTheFloorHoldsWhateverTheDeciderSays guards the floor: a decider that
// answers keep for every element (as a wrong profile, or wrong code
// deciding from one, would) still leaves no value of the attributes that
// name or identify the person, at any depth, a private element of VR PN
// included; each of their values is a needle though no rule removed it;
// and the file stays readable.
func TestTheFloorHoldsWhateverTheDeciderSays(t *testing.T) {
	keepAll := fakeDecider{}
	en, err := newEngine(keepAll, testKeys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ds := floorSample(t)
	rec := apply(t, en, ds)
	hay := strings.Join(allText(readFile(t, writeFile(t, ds))), "\x00")
	needles := map[string]bool{}
	for _, n := range rec.Needles {
		needles[n.Value] = true
	}
	for _, v := range floorValues {
		if strings.Contains(hay, v) {
			t.Errorf("%q stayed in the file", v)
		}
		if !needles[v] {
			t.Errorf("%q is not a needle", v)
		}
	}
	// The CT Image IOD requires Patient's Name present (Type 2): it is
	// emptied, not removed.
	if v, ok := text(ds, tPatientName); !ok || v != "" {
		t.Errorf("Patient's Name: %q, present %v", v, ok)
	}
	floorEntries := 0
	for _, e := range rec.Entries {
		if strings.HasPrefix(e.Reason, "the engine never keeps") {
			floorEntries++
			if e.Rule != -1 || !e.Changed {
				t.Errorf("%s: rule %d, changed %v", e.Path, e.Rule, e.Changed)
			}
		}
	}
	if floorEntries < len(floorValues)-1 {
		t.Errorf("%d entries say the floor acted", floorEntries)
	}
}

// TestTheFloorLeavesWhatReplacesTheValue guards that the floor overrides
// only what would keep a value: a profile's hash of Patient ID, its
// template for Patient's Name and its shift of the birth date stand, and
// a clean of Patient ID, which leaves what it does not recognize (a short
// identifier is no term), does not.
func TestTheFloorLeavesWhatReplacesTheValue(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{},
		rule("id", kw("PatientID"), aHash),
		rule("name", kw("PatientName"), aTemplate("S-{#PatientID}")),
		rule("birth", kw("PatientBirthDate"), aShift),
		rule("other", kw("OtherPatientIDs"), aClean),
	)
	ds := build(t, el{tPatientID, "LO", "P1"}, el{tPatientName, "PN", "Doe^Jo"}, el{tBirthDate, "DA", "19611213"}, el{0x00101000, "LO", "P2"})
	apply(t, en, ds)
	code := naming.HashText(testKeys.Hash, []byte("P1"))
	if got := mustText(t, ds, tPatientID); got != code {
		t.Errorf("hash: %q", got)
	}
	if got := mustText(t, ds, tPatientName); got != "S-"+code {
		t.Errorf("template: %q", got)
	}
	if got := mustText(t, ds, tBirthDate); got == "19611213" || len(got) != 8 {
		t.Errorf("shift: %q", got)
	}
	if v, ok := text(ds, 0x00101000); ok && v == "P2" {
		t.Error("a clean of Other Patient IDs kept it")
	}
}
