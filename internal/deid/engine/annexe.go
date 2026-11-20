package engine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/amrshadid/go-dicom/validate"
)

// Code is an action code of PS3.15 Table E.1-1: a single action (D, Z, X,
// K, C, U) or a combined one (X/Z, X/D, Z/D, X/Z/D, X/Z/U*) that the
// attribute's Type in the object's IOD resolves (PS3.15 E.1.1).
type Code string

// The codes of Table E.1-1.
const (
	// CodeDummy (D): replace with a non-zero length dummy value
	// consistent with the VR.
	CodeDummy Code = "D"
	// CodeZero (Z): replace with a zero length value, or a dummy value.
	CodeZero Code = "Z"
	// CodeRemove (X): remove.
	CodeRemove Code = "X"
	// CodeKeep (K): keep.
	CodeKeep Code = "K"
	// CodeClean (C): clean, keeping what does not identify.
	CodeClean Code = "C"
	// CodeUID (U): replace with a non-zero length UID that is internally
	// consistent within a set of instances.
	CodeUID Code = "U"
	// CodeXZ: X unless Z is needed to keep the IOD (Type 2).
	CodeXZ Code = "X/Z"
	// CodeXD: X unless D is needed to keep the IOD (Type 1 or 2).
	CodeXD Code = "X/D"
	// CodeZD: Z unless D is needed to keep the IOD (Type 1).
	CodeZD Code = "Z/D"
	// CodeXZD: X unless Z (Type 2) or D (Type 1) is needed.
	CodeXZD Code = "X/Z/D"
	// CodeXZU: X unless Z (Type 2) or the replacement of the instance UIDs
	// the sequence holds (Type 1) is needed.
	CodeXZU Code = "X/Z/U*"
)

// Resolution is what a code comes to for one attribute of one object.
type Resolution int

// The resolutions.
const (
	// ResolveRemove removes the element.
	ResolveRemove Resolution = iota + 1
	// ResolveEmpty leaves it with no value, or a sequence with no items.
	ResolveEmpty
	// ResolveDummy replaces its value with Dummy of its VR; a sequence
	// with no items.
	ResolveDummy
	// ResolveKeep keeps it.
	ResolveKeep
	// ResolveClean cleans its value of the file's identifying values.
	ResolveClean
	// ResolveUID remaps its UIDs, or, for a sequence, keeps it and remaps
	// the instance UIDs inside by their own codes.
	ResolveUID
)

// String names the resolution as a person reads it.
func (r Resolution) String() string {
	switch r {
	case ResolveRemove:
		return "remove"
	case ResolveEmpty:
		return "empty"
	case ResolveDummy:
		return "dummy"
	case ResolveKeep:
		return "keep"
	case ResolveClean:
		return "clean"
	case ResolveUID:
		return "uid"
	}
	return "resolution(" + strconv.Itoa(int(r)) + ")"
}

// Table is one column of PS3.15 Table E.1-1 (the basic profile, or one of
// its options), as the DICOM library will export it: the code of an
// attribute by its tag, and false for an attribute the column does not
// name. CORIA ships no copy of the table: until the library exports it,
// only a test's own table fills this.
type Table interface {
	Code(tag uint32) (Code, bool)
}

// Types gives an attribute's Type in the IOD of a SOP class (PS3.3, PS3.5
// 7.4), by the sequences it is inside, outermost first, and its tag; false
// when the SOP class or the attribute is not known there.
type Types interface {
	Type(sopClass string, sequences []uint32, tag uint32) (validate.AttributeType, bool)
}

// Resolve resolves a code for an attribute of Type typ in its object's IOD
// (PS3.15 E.1.1: "X unless Z is required to maintain IOD conformance (Type 3
// versus Type 2)", and so for each combined code). When the Type is not
// known (an unknown SOP class, or an attribute its IOD does not list) the
// code resolves toward keeping the attribute present, with an empty or a
// dummy value, as the strictest Type it could have needs: a file stays
// valid, and no original value stays.
func Resolve(code Code, typ validate.AttributeType, known bool) (Resolution, error) {
	type1 := !known || typ == validate.Type1 || typ == validate.Type1C
	type2 := known && (typ == validate.Type2 || typ == validate.Type2C)
	switch code {
	case CodeDummy:
		return ResolveDummy, nil
	case CodeZero:
		return ResolveEmpty, nil
	case CodeRemove:
		return ResolveRemove, nil
	case CodeKeep:
		return ResolveKeep, nil
	case CodeClean:
		return ResolveClean, nil
	case CodeUID:
		return ResolveUID, nil
	case CodeXZ:
		if type1 || type2 {
			return ResolveEmpty, nil
		}
		return ResolveRemove, nil
	case CodeXD:
		if type1 || type2 {
			return ResolveDummy, nil
		}
		return ResolveRemove, nil
	case CodeZD:
		if type1 {
			return ResolveDummy, nil
		}
		return ResolveEmpty, nil
	case CodeXZD:
		switch {
		case type1:
			return ResolveDummy, nil
		case type2:
			return ResolveEmpty, nil
		}
		return ResolveRemove, nil
	case CodeXZU:
		switch {
		case type1:
			return ResolveUID, nil
		case type2:
			return ResolveEmpty, nil
		}
		return ResolveRemove, nil
	}
	return 0, fmt.Errorf("engine: %q is not a code of PS3.15 Table E.1-1", code)
}

// ResolveIn resolves what a table says of the attribute at sequences and
// tag in an object of sopClass, its Type taken from types. ok is false when
// the table does not name the attribute.
func ResolveIn(table Table, types Types, sopClass string, sequences []uint32, tag uint32) (r Resolution, ok bool, err error) {
	code, named := table.Code(tag)
	if !named {
		return 0, false, nil
	}
	typ, known := types.Type(sopClass, sequences, tag)
	r, err = Resolve(code, typ, known)
	return r, err == nil, err
}

// IODTypes is Types over the DICOM library's IOD tables (validate's
// IODForSOPClass and LookupTable): the modules of the SOP class's IOD, and
// for an enhanced multi-frame object the functional group macros under its
// Shared and Per-frame Functional Groups Sequences (PS3.3 C.7.6.16). When
// the IOD lists an attribute at a place more than once (two modules, or a
// macro included twice), the strictest Type is taken. Answers are kept, so
// each is worked out once; one IODTypes serves every caller at once.
type IODTypes struct {
	answers sync.Map // key string -> typeAnswer
}

// typeAnswer is a kept answer.
type typeAnswer struct {
	typ   validate.AttributeType
	known bool
}

// maxTableNesting bounds how often a table may include itself on one path:
// the SR content macros include each other without end (PS3.3 C.17.3).
const maxTableNesting = 12

// The sequences under which an enhanced object's functional group macros
// are (PS3.3 C.7.6.16).
const (
	sharedFunctionalGroups   = 0x52009229
	perFrameFunctionalGroups = 0x52009230
)

// Type implements Types.
func (it *IODTypes) Type(sopClass string, sequences []uint32, tag uint32) (validate.AttributeType, bool) {
	var key strings.Builder
	key.WriteString(sopClass)
	for _, s := range sequences {
		key.WriteString("/" + formatTag(s))
	}
	key.WriteString("/" + formatTag(tag))
	if a, ok := it.answers.Load(key.String()); ok {
		ans := a.(typeAnswer)
		return ans.typ, ans.known
	}
	typ, known := lookupType(sopClass, sequences, tag)
	it.answers.Store(key.String(), typeAnswer{typ, known})
	return typ, known
}

// lookupType works out Type for IODTypes.
func lookupType(sopClass string, sequences []uint32, tag uint32) (validate.AttributeType, bool) {
	iod, ok := validate.IODForSOPClass(sopClass)
	if !ok {
		return 0, false
	}
	var found []validate.AttributeType
	for _, m := range iod.Modules {
		found = findType(m.Table, sequences, tag, found)
	}
	if len(sequences) > 0 && (sequences[0] == sharedFunctionalGroups || sequences[0] == perFrameFunctionalGroups) {
		for _, g := range iod.FunctionalGroups {
			found = findType(g.Table, sequences[1:], tag, found)
		}
	}
	if len(found) == 0 {
		return 0, false
	}
	// The Types run from Type1 to Type3, strictest first.
	return slices.Min(found), true
}

// findType appends to found the Type of every row of the table labeled
// label, its includes followed, that is the attribute tag inside the
// sequences given.
func findType(label string, sequences []uint32, tag uint32, found []validate.AttributeType) []validate.AttributeType {
	m := &typeMatch{sequences: sequences, tag: tag, seen: map[string]int{}, found: found}
	m.walk(label, 0)
	return m.found
}

// typeMatch walks a table's rows in order, as PS3.3 writes them flat with
// a depth each, and follows the path: matched is how many of the sequences
// the rows above the current one have matched. An include is followed only
// where its rows could be on the path, which keeps the SR content macros'
// mutual inclusion from growing without end.
type typeMatch struct {
	sequences []uint32
	tag       uint32
	seen      map[string]int
	matched   int
	found     []validate.AttributeType
}

// walk visits the rows of the table labeled label at depth base.
func (m *typeMatch) walk(label string, base int) {
	t, ok := validate.LookupTable(label)
	if !ok || m.seen[label] >= maxTableNesting {
		return
	}
	m.seen[label]++
	defer func() { m.seen[label]-- }()
	for _, r := range t.Rows {
		d := base + r.Depth
		if r.Include != "" {
			// The included rows are at depth d and below, under the row at
			// d-1: only reachable when that row is on the path.
			if d <= m.matched && d <= len(m.sequences) {
				m.walk(r.Include, d)
			}
			continue
		}
		if d > m.matched {
			// Inside an attribute that is not on the path.
			continue
		}
		m.matched = d
		switch {
		case d < len(m.sequences) && r.Mask == 0 && r.Tag == m.sequences[d]:
			m.matched = d + 1
		case d == len(m.sequences) && tagMatches(r, m.tag):
			m.found = append(m.found, r.Type)
		}
	}
}

// tagMatches reports whether a row is the attribute tag: its tag, or for a
// repeating group the bits of its mask.
func tagMatches(r validate.Row, tag uint32) bool {
	if r.Mask == 0 {
		return r.Tag == tag
	}
	return tag&r.Mask == r.Tag&r.Mask
}
