package engine

// The floor: attributes that name or identify the person, which the engine
// never keeps, at any depth, whatever the profile decides. A profile that
// would keep one (keep, or clean, which leaves what it does not recognize)
// gets remove-unless-required with a dummy instead, so the file stays valid
// by its IOD and no original value stays; an action that replaces the value
// (hash, lookup, replace, shift, generalize, dummy, empty, remove) is left
// as the profile says. Each one's original value is always a needle, so the
// check after searches for it whatever the profile decided.
//
// The reason is defense in depth: a profile, or the code that decides from
// it, that wrongly answers keep for one of these would leak the person's
// identity with nothing to catch it, since needles otherwise come only from
// values a rule removed. A floor the profile cannot lower makes one layer's
// mistake not a leak.
//
// PS3.15 Table E.1-1 (2026d) gives every attribute here an action that
// removes or replaces its value in the basic profile, and no option column
// keeps it: none of the eleven options (Retain Patient Characteristics,
// Retain Longitudinal Temporal Information among them) names Patient's
// Name, Patient ID, birth date or time, addresses or telephone numbers.
// So no profile built on Annex E has a retain option that would lower the
// floor, and the engine reads none.

// floorTags are the attributes of the floor besides every element of VR PN,
// with the code PS3.15 Table E.1-1's basic profile gives each.
var floorTags = map[uint32]string{
	0x00100010: "Patient's Name (0010,0010), Z in PS3.15 Table E.1-1",
	0x00100020: "Patient ID (0010,0020), Z/D in PS3.15 Table E.1-1",
	0x00101000: "Other Patient IDs (0010,1000), X in PS3.15 Table E.1-1",
	0x00101001: "Other Patient Names (0010,1001), X in PS3.15 Table E.1-1",
	0x00101002: "Other Patient IDs Sequence (0010,1002), X in PS3.15 Table E.1-1",
	0x00101005: "Patient's Birth Name (0010,1005), X in PS3.15 Table E.1-1",
	0x00100030: "Patient's Birth Date (0010,0030), Z in PS3.15 Table E.1-1",
	0x00100032: "Patient's Birth Time (0010,0032), X in PS3.15 Table E.1-1",
	0x00101040: "Patient's Address (0010,1040), X in PS3.15 Table E.1-1",
	0x00101060: "Patient's Mother's Birth Name (0010,1060), X in PS3.15 Table E.1-1",
	0x00102154: "Patient's Telephone Numbers (0010,2154), X in PS3.15 Table E.1-1",
	0x00102155: "Patient's Telecom Information (0010,2155), X in PS3.15 Table E.1-1",
	0x00401102: "Person's Address (0040,1102), X in PS3.15 Table E.1-1",
	0x00401103: "Person's Telephone Numbers (0040,1103), X in PS3.15 Table E.1-1",
	0x00401104: "Person's Telecom Information (0040,1104), X in PS3.15 Table E.1-1",
	0x00080092: "Referring Physician's Address (0008,0092), X in PS3.15 Table E.1-1",
	0x00080094: "Referring Physician's Telephone Numbers (0008,0094), X in PS3.15 Table E.1-1",
}

// floorReason returns why the floor covers an element of tag and VR vr, and
// "" when it does not. Every element of VR PN is a person's name, which
// PS3.15 Table E.1-1 removes or replaces wherever it lists one; the
// element's VR in the file decides, so a private element written as PN is
// covered too.
func floorReason(tag uint32, vr string) string {
	if r, ok := floorTags[tag]; ok {
		return r
	}
	if vr == "PN" {
		return "a person's name (VR PN), which PS3.15 Table E.1-1 removes or replaces wherever it lists one"
	}
	return ""
}

// floorAction returns the action the engine takes instead of a, for an
// element the floor covers, and whether it differs: a itself when a
// replaces or removes the value; when a would keep it, removal for a
// private element (no IOD requires one) and remove-unless-required with a
// dummy for a standard one, so the file stays valid by its IOD.
func floorAction(a Action, private bool) (Action, bool) {
	instead := Action{Kind: ActRemoveUnlessRequired, Fallback: FallbackDummy}
	if private {
		instead = Action{Kind: ActRemove}
	}
	switch a.Kind {
	case ActKeep, ActClean:
		return instead, true
	case ActRemoveUnlessRequired:
		// A UID fallback keeps a sequence for the UIDs inside, which for
		// these attributes would keep what they hold.
		if a.Fallback == FallbackUID || private {
			return instead, true
		}
	}
	return a, false
}
