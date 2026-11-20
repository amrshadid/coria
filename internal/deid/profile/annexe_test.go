package profile

import (
	"testing"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// TestAnnexECodesBecomeActions guards how a column of PS3.15 Table E.1-1
// becomes a base, on rows written here (CORIA keeps no copy of the
// table): each code its action, C by the attribute's VR and the dates
// option, a repeating group's row as exact tags or a group rule, a blank
// code no rule; and the document passes the language's checks, so a base
// built from the library's table is valid as it is.
func TestAnnexECodesBecomeActions(t *testing.T) {
	rows := []annexERow{
		{tag: 0x00100010, name: "Patient's Name", code: "Z"},
		{tag: 0x00100020, name: "Patient ID", code: "Z/D"},
		{tag: 0x00101000, name: "Other Patient IDs", code: "X"},
		{tag: 0x00401101, name: "Person Identification Code Sequence", code: "D"},
		{tag: 0x00080060, name: "Modality", code: "K"},
		{tag: 0x0020000D, name: "Study Instance UID", code: "U"},
		{tag: 0x00081030, name: "Study Description", code: "C"},
		{tag: 0x00080020, name: "Study Date", code: "C"},
		{tag: 0x00080030, name: "Study Time", code: "C"},
		{tag: 0x00080055, name: "Station AE Title", code: "C"},
		{tag: 0x00080080, name: "Institution Name", code: "X/Z/D"},
		{tag: 0x00080090, name: "Referring Physician's Name", code: "X/D"},
		{tag: 0x00081010, name: "Station Name", code: "X/Z"},
		{tag: 0x00081140, name: "Referenced Image Sequence", code: "X/Z/U*"},
		{tag: 0x00104000, name: "Patient Comments", code: ""},
		{tag: 0x60003000, pattern: "(60xx,3000)", name: "Overlay Data", code: "C"},
		{tag: 0x50000000, pattern: "(50xx,xxxx)", name: "Curve Data", code: "C"},
	}
	doc, err := annexEDocument(rows, "Retain Longitudinal Temporal Information with Modified Dates Option", true)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, r := range doc.GetRules() {
		sel := r.GetSelector().GetTag()
		if g := r.GetSelector().GetGroup(); g != "" {
			sel = g
		}
		a := r.GetAction()
		k := actionName(a)
		if u := a.GetRemoveUnlessRequired(); u != nil {
			k += "/" + u.GetFallback().String()
		}
		kinds[sel] = k
	}
	want := map[string]string{
		"00100010": "empty", "00100020": "remove_unless_required/REQUIRED_FALLBACK_DUMMY", "00101000": "remove",
		"00401101": "dummy", "00080060": "keep", "0020000D": "uid", "00081030": "clean", "00080020": "shift",
		"00080030": "keep", "00080055": "dummy",
		"00080080": "remove_unless_required/REQUIRED_FALLBACK_DUMMY", "00080090": "remove_unless_required/REQUIRED_FALLBACK_DUMMY",
		"00081010": "remove_unless_required/REQUIRED_FALLBACK_EMPTY", "00081140": "remove_unless_required/REQUIRED_FALLBACK_UID",
		"60003000": "clean", "601E3000": "clean", "50xx": "remove",
	}
	for sel, k := range want {
		if kinds[sel] != k {
			t.Errorf("%s: %q, want %q", sel, kinds[sel], k)
		}
	}
	if _, ok := kinds["00104000"]; ok {
		t.Error("a blank code made a rule")
	}
	if got := len(doc.GetRules()); got != 14+repeatingGroups+1 {
		t.Errorf("%d rules", got)
	}
	if _, err := NewCatalog(Base{Name: "test/annex-e:1", Document: doc}); err != nil {
		t.Errorf("the document is not a valid base: %v", err)
	}
	plain, err := annexEDocument(rows[7:8], "Clean Descriptors Option", false)
	if err != nil || actionName(plain.GetRules()[0].GetAction()) != "dummy" {
		t.Errorf("C on a date without the dates option: %v, %v", plain, err)
	}
	if _, err := annexEAction("Q", nil, false); err == nil {
		t.Error("an unknown code became an action")
	}
}

// actionName is the name of an action's kind, as the JSON form writes it.
func actionName(a *v1.ProfileAction) string {
	m := a.ProtoReflect()
	if fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("kind")); fd != nil {
		return string(fd.Name())
	}
	return ""
}
