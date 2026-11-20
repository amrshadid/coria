package engine

// dummies are the dummy values of PS3.15 E.1.1's action D: a value that
// replaces the original, says nothing of it, and is a valid value of its VR
// (PS3.5 6.2), so an attribute the IOD requires with a value (Type 1) stays
// valid. Binary VRs that hold numbers take one value of zero, the least a
// Type 1 attribute may hold; the others take a fixed text a reader sees as
// a placeholder. A VR whose empty value is valid in every place (the OB,
// OW family, UN, UR) is empty.
var dummies = map[string][]byte{
	"AE": []byte("DUMMY"),
	"AS": []byte("000D"),
	"AT": {0, 0, 0, 0},
	"CS": []byte("DUMMY"),
	"DA": []byte("19000101"),
	"DS": []byte("0"),
	"DT": []byte("19000101000000"),
	"FD": {0, 0, 0, 0, 0, 0, 0, 0},
	"FL": {0, 0, 0, 0},
	"IS": []byte("0"),
	"LO": []byte("DUMMY"),
	"LT": []byte("DUMMY"),
	"OB": {},
	"OD": {},
	"OF": {},
	"OL": {},
	"OV": {},
	"OW": {},
	"PN": []byte("DUMMY"),
	"SH": []byte("DUMMY"),
	"SL": {0, 0, 0, 0},
	"SS": {0, 0},
	"ST": []byte("DUMMY"),
	"SV": {0, 0, 0, 0, 0, 0, 0, 0},
	"TM": []byte("000000"),
	"UC": []byte("DUMMY"),
	// A dummy UID must not be one value for every element: two instances
	// would share it. Annex E replaces UIDs with U, not D; this value
	// serves only where a table says D of a UID, and is the root of UUID
	// derived UIDs with the nil UUID (PS3.5 B.2).
	"UI": []byte("2.25.0"),
	"UL": {0, 0, 0, 0},
	"UN": {},
	"UR": {},
	"US": {0, 0},
	"UT": []byte("DUMMY"),
	"UV": {0, 0, 0, 0, 0, 0, 0, 0},
}

// Dummy returns the dummy value of a VR, and false for a VR PS3.5 6.2 does
// not define and for SQ, whose dummy is a sequence with no items. A binary
// value's zero bytes read the same in either byte order. The slice is the
// caller's.
func Dummy(vr string) ([]byte, bool) {
	d, ok := dummies[vr]
	if !ok {
		return nil, false
	}
	return append([]byte{}, d...), true
}
