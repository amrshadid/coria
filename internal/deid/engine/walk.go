package engine

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/amrshadid/go-dicom/dataelem"
	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/sequence"
	"github.com/amrshadid/go-dicom/tag"

	"github.com/amrshadid/coria/internal/deid/profile"
)

// run is one Apply: the plan of every change, made before any is applied.
type run struct {
	en       *Engine
	ctx      context.Context
	top      *dataset.Dataset
	record   *Record
	facts    facts
	received map[uint32]string
	ops      []op
	count    int
	// shift is the subject's date shift in days, 0 until a date needs it.
	shift int
	// assigns are the lookups whose code the table assigns on first sight,
	// asked for only once the whole plan has succeeded, so a file refused
	// for another reason takes no code from a table.
	assigns []assign
	// undo restores what apply changed, last first, should it stop part
	// way.
	undo []func()
}

// opKind is what an op does.
type opKind int

// The ops.
const (
	// opRemove removes the element.
	opRemove opKind = iota + 1
	// opValue gives an element present its new value.
	opValue
	// opNoItems leaves a sequence with no items.
	opNoItems
	// opPut gives the element its value, making it with vr when absent.
	opPut
	// opClean is opValue whose value is worked out once every needle is
	// known; it then becomes opValue, or nothing when the text is clean.
	opClean
	// opAssign is opValue whose value is the code a lookup table assigns,
	// asked for once the plan has succeeded; it then becomes opValue, or
	// nothing when the code is the value.
	opAssign
)

// op is one change of the plan.
type op struct {
	kind  opKind
	ds    *dataset.Dataset
	tag   tag.Tag
	value []byte
	// vr, when set, is the VR the element takes: an element made, or one
	// whose VR in the file was UN.
	vr dataelem.VR
	// text, entry and path are a clean's: its text as received, its
	// record entry and where it is.
	text    string
	entry   int
	path    string
	rule    string
	charset charsetKind
}

// charsetKind is what character set governs a data set's text (PS3.5
// 6.1): the nearest Specific Character Set (0008,0005), its own or an
// enclosing data set's.
type charsetKind int

// The kinds of character set.
const (
	// charsetAbsent: none is declared; text is the default repertoire.
	charsetAbsent charsetKind = iota + 1
	// charsetUTF8: ISO_IR 192, the set the library decodes text into.
	charsetUTF8
	// charsetOther: another set. The library declares ISO_IR 192 over
	// every data set whose text it decoded into UTF-8, so text under
	// another declaration is as the file wrote it, in a set this engine
	// cannot search or clean: a value of it outside the default
	// repertoire refuses the file.
	charsetOther
)

// scope is what a data set's elements inherit from the data sets around it.
type scope struct {
	charset charsetKind
	// concept is the concept name of the structured report content item
	// this data set is (PS3.3 C.17.3), nil when it is not one or has none.
	concept *profile.Concept
	// valueType is the content item's Value Type (0040,A040) without its
	// padding; empty when the data set is not a content item.
	valueType string
}

// Tags the engine treats by fixed rules.
const (
	tagSpecificCharacterSet = 0x00080005
	tagPatientID            = 0x00100020
	tagIssuerOfPatientID    = 0x00100021
	tagValueType            = 0x0040A040
	tagConceptNameCode      = 0x0040A043
	tagCodeValue            = 0x00080100
	tagCodingScheme         = 0x00080102
	tagIdentityRemoved      = 0x00120062
	tagDeidMethod           = 0x00120063
	tagTemporalModified     = 0x00280303
)

// pixelTags are the pixel data elements (PS3.5 7.6.2.1 Float and Double
// Float Pixel Data, PS3.3 C.7.6.3 Pixel Data), which only pixel rules
// change and this engine keeps as they are.
var pixelTags = map[uint32]bool{0x7FE00008: true, 0x7FE00009: true, 0x7FE00010: true}

// strippedTags are removed from every file whatever the profile says:
// trailing padding; the Original Attributes Sequence and the Encrypted Attributes Sequence hold
// values as they were before a change, and a digital signature or its MAC
// parameters cannot hold over a changed data set (PS3.15 E.1.1, C.12.1).
var strippedTags = map[uint32]string{
	0x04000561: "Original Attributes Sequence holds values as they were received (PS3.15 E.1.1)",
	0x04000500: "Encrypted Attributes Sequence holds values as they were received (PS3.15 E.1.1)",
	0xFFFAFFFA: "a digital signature cannot hold over a changed data set (PS3.15 E.1.1)",
	0x4FFE0001: "MAC parameters cannot hold over a changed data set (PS3.15 C.12.1)",
	0xFFFCFFFC: "data set trailing padding, which carries no information and may hold anything (PS3.10 7.2)",
}

// blockKey is a private block of a data set: its group and block number.
type blockKey struct {
	group uint16
	block uint16
}

// walk plans the changes to the elements of ds, which is inside the
// sequences seqs; path is where ds is, ending in "/" unless ds is the top
// level.
func (r *run) walk(ds *dataset.Dataset, seqs []uint32, path string, sc scope) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	tags := ds.Tags()
	slices.Sort(tags)
	// A private creator element (gggg,0010-00FF) reserves the block of its
	// group (PS3.5 7.8.1); it is kept when an element of its block is.
	creators := map[blockKey]string{}
	for _, t := range tags {
		if isCreator(t) {
			if v, ok := elementText(ds, t); ok {
				// A creator is compared without its padding, the space
				// or NUL that makes its length even (PS3.5 6.2), and any
				// surrounding space.
				creators[blockKey{t.Group(), t.Element()}] = strings.Trim(v, " \x00")
			}
		}
	}
	kept := map[blockKey]bool{}
	creatorEntries := map[blockKey]int{}
	for _, t := range tags {
		r.count++
		if r.count > MaxElements {
			return &Failure{Cause: CauseTooLarge}
		}
		elem, ok := ds.Get(t)
		if !ok {
			continue
		}
		p := path + formatTag(uint32(t))
		switch {
		case isCreator(t):
			creatorEntries[blockKey{t.Group(), t.Element()}] = len(r.record.Entries)
			r.record.Entries = append(r.record.Entries, Entry{})
		case t.Element() == 0 || t.Group() == 0x0002:
			r.fixed(ds, t, elem, p, opRemove, "a group length or file meta information, which is written anew with the file (PS3.5 7.2, PS3.10 7.1)")
		case uint32(t) == tagSpecificCharacterSet:
			r.fixed(ds, t, elem, p, opRemove, "the character set, which the engine declares itself once every value is known: ISO_IR 192 when any text is outside the default repertoire, none otherwise (PS3.5 6.1.2.3)")
		case pixelTags[uint32(t)]:
			r.fixed(ds, t, elem, p, 0, "pixel data, which only pixel rules change; this engine changes no pixels")
		case strippedTags[uint32(t)] != "":
			if seq, isSeq := elem.GetValue().(*sequence.Sequence); isSeq {
				if err := r.subtreeNeedles(seq, p, len(seqs)+1); err != nil {
					return err
				}
			}
			r.fixed(ds, t, elem, p, opRemove, strippedTags[uint32(t)])
		default:
			private := t.Group()%2 == 1
			var bk blockKey
			creator := ""
			if private && t.Element()>>8 >= 0x10 {
				bk = blockKey{t.Group(), t.Element() >> 8}
				creator = creators[bk]
			}
			removed, err := r.element(ds, t, elem, seqs, p, sc, creator)
			if err != nil {
				return err
			}
			if private && creator != "" && !removed {
				kept[bk] = true
			}
		}
	}
	for bk, i := range creatorEntries {
		t := tag.New(bk.group, bk.block)
		e := Entry{Path: path + formatTag(uint32(t)), Tag: uint32(t), VR: "LO", Rule: -1}
		if kept[bk] {
			e.Action, e.Reason = "keep", "the private creator of a block an element of which is kept (PS3.5 7.8.1)"
		} else {
			e.Action, e.Reason, e.Changed = "remove", "the private creator of a block no element of which is kept (PS3.5 7.8.1)", true
			r.ops = append(r.ops, op{kind: opRemove, ds: ds, tag: t})
		}
		r.record.Entries[i] = e
	}
	return nil
}

// fixed records an element the engine treats by a fixed rule, and plans
// its removal when kind is opRemove.
func (r *run) fixed(ds *dataset.Dataset, t tag.Tag, elem *dataelem.DataElement, path string, kind opKind, reason string) {
	e := Entry{Path: path, Tag: uint32(t), VR: effectiveVR(t, elem), Action: "keep", Rule: -1, Reason: reason}
	if kind == opRemove {
		e.Action, e.Changed = "remove", true
		r.ops = append(r.ops, op{kind: opRemove, ds: ds, tag: t})
	}
	r.record.Entries = append(r.record.Entries, e)
}

// element plans the change to one element by the rule the profile's
// precedence chooses, or by the floor, and reports whether the element is
// removed.
func (r *run) element(ds *dataset.Dataset, t tag.Tag, elem *dataelem.DataElement, seqs []uint32, path string, sc scope, creator string) (bool, error) {
	vr := effectiveVR(t, elem)
	if err := checkText(elem, vr, sc.charset); err != nil {
		return false, &Failure{Cause: CauseBadValue, Path: path}
	}
	loc := profile.Location{Sequences: seqs, Tag: uint32(t), Creator: creator, VR: vr}
	if sc.valueType != "" {
		loc.ContentItem, loc.ValueType = sc.concept, sc.valueType
	}
	dec := r.en.dec.Decide(loc, r.facts)
	e := Entry{Path: path, Tag: uint32(t), VR: vr, Action: dec.Action.Kind.String(), Rule: dec.Rule, RuleName: dec.Name, Origin: dec.Origin, Reason: dec.Reason}
	c := &target{ds: ds, tag: t, elem: elem, vr: vr, seqs: seqs, path: path, entry: len(r.record.Entries), sc: sc}
	if dec.Rule >= 0 {
		c.byRule = true
		c.rule = dec.Name
		if c.rule == "" {
			c.rule = "rule " + strconv.Itoa(dec.Rule+1)
		}
	}
	act := dec.Action
	if why := floorReason(uint32(t), vr); why != "" {
		c.floor = true
		if fa, changed := floorAction(act, t.Group()%2 == 1); changed {
			act = fa
			e.Action, e.Rule, e.RuleName, e.Origin = fa.Kind.String(), -1, "", ""
			e.Reason = "the engine never keeps " + why + ", whatever the profile decides"
			c.byRule, c.rule = false, ""
		}
	}
	c.seq, _ = elem.GetValue().(*sequence.Sequence)
	r.record.Entries = append(r.record.Entries, e)
	return r.act(c, act, dec.VRs)
}

// charsetVRs are the VRs whose value is text in the data set's character set
// (PS3.5 6.1.2.3, 6.2): every one the library decodes into UTF-8.
var charsetVRs = map[string]bool{
	"AE": true, "AS": true, "CS": true, "DA": true, "DS": true, "DT": true, "IS": true, "LO": true, "LT": true,
	"PN": true, "SH": true, "ST": true, "TM": true, "UC": true, "UI": true, "UR": true, "UT": true,
}

// checkText refuses a text value the engine could not search or clean
// safely: one outside the default repertoire under a character set the
// library did not decode it from, or one that is not valid UTF-8 where the
// library decoded it into UTF-8. A needle taken from such a value, or a
// clean of it, would compare bytes of different sets.
func checkText(elem *dataelem.DataElement, vr string, cs charsetKind) error {
	if !charsetVRs[vr] {
		return nil
	}
	b, ok := elem.Bytes()
	if !ok || isASCII(string(b)) {
		return nil
	}
	if cs == charsetOther || !utf8.Valid(b) {
		return errNotText
	}
	return nil
}

// items plans the changes inside a sequence's items.
func (r *run) items(seq *sequence.Sequence, seqs []uint32, t tag.Tag, path string, sc scope) error {
	if seq.Length() == 0 {
		return nil
	}
	if len(seqs)+1 > MaxDepth {
		return &Failure{Cause: CauseTooDeep, Path: path}
	}
	inner := slices.Concat(seqs, []uint32{uint32(t)})
	for i, it := range seq.Items() {
		item, ok := it.(*dataset.Dataset)
		if !ok {
			return &Failure{Cause: CauseBadValue, Path: path}
		}
		isc := scope{charset: charsetOf(item, sc.charset)}
		isc.concept, isc.valueType = contentItemOf(item)
		if err := r.walk(item, inner, path+"["+strconv.Itoa(i)+"]/", isc); err != nil {
			return err
		}
	}
	return nil
}

// subtreeNeedles keeps as needles the text values of a sequence that is
// removed or emptied, so the check after searches for them as for any
// value removed.
func (r *run) subtreeNeedles(seq *sequence.Sequence, path string, depth int) error {
	if depth > MaxDepth {
		return &Failure{Cause: CauseTooDeep, Path: path}
	}
	for i, it := range seq.Items() {
		item, ok := it.(*dataset.Dataset)
		if !ok {
			continue
		}
		base := path + "[" + strconv.Itoa(i) + "]/"
		tags := item.Tags()
		slices.Sort(tags)
		for _, t := range tags {
			r.count++
			if r.count > MaxElements {
				return &Failure{Cause: CauseTooLarge}
			}
			elem, ok := item.Get(t)
			if !ok {
				continue
			}
			p := base + formatTag(uint32(t))
			if inner, isSeq := elem.GetValue().(*sequence.Sequence); isSeq {
				if err := r.subtreeNeedles(inner, p, depth+1); err != nil {
					return err
				}
				continue
			}
			vr := effectiveVR(t, elem)
			if v, ok := elementText(item, t); ok {
				r.needle(vr, trimValue(vr, v), p)
			}
		}
	}
	return nil
}

// needle keeps a removed or replaced value for the check after: each value
// of a multi-valued one, and the whole. A registered UID is not one: it
// names a kind of object, stays wherever else it is, and is no trace of
// the patient.
func (r *run) needle(vr, value, path string) {
	kind := needleKind(vr)
	if kind == 0 || value == "" {
		return
	}
	add := func(v string) {
		v = strings.Trim(v, " \x00")
		if v == "" || (kind == NeedleUID && Registered(v)) {
			return
		}
		r.record.Needles = append(r.record.Needles, Needle{Kind: kind, Value: v, Path: path})
	}
	parts := strings.Split(value, `\`)
	for _, p := range parts {
		add(p)
	}
	if len(parts) > 1 {
		add(value)
	}
}

// needleKind is the kind of a removed value of a VR, 0 for a VR that holds
// no text.
func needleKind(vr string) NeedleKind {
	switch vr {
	case "PN":
		return NeedleName
	case "DA", "DT":
		return NeedleDate
	case "UI":
		return NeedleUID
	case "AE", "AS", "CS", "DS", "IS", "LO", "LT", "SH", "ST", "TM", "UC", "UR", "UT":
		return NeedleText
	}
	return 0
}

// shiftDays returns the subject's date shift, working it out the first
// time a date needs it.
func (r *run) shiftDays(path, rule string) (int, error) {
	if r.shift > 0 {
		return r.shift, nil
	}
	id, ok := topText(r.top, tagPatientID)
	if !ok || id == "" {
		return 0, &Failure{Cause: CauseNoSubject, Path: path, Rule: rule}
	}
	issuer, _ := topText(r.top, tagIssuerOfPatientID)
	r.shift = ShiftDays(r.en.keys.Date, issuer, id, r.en.dec.DateShiftDays())
	return r.shift, nil
}

// receivedValue is a top-level element's value as received, for a
// template's placeholder.
func (r *run) receivedValue(t uint32) (string, bool) {
	v, ok := r.received[t]
	return v, ok
}

// text returns an element's value as text without its padding, refusing a
// value not in memory or longer than MaxValueLength.
func (r *run) text(ds *dataset.Dataset, t tag.Tag, vr string) (string, error) {
	elem, ok := ds.Get(t)
	if !ok {
		return "", errNotText
	}
	b, ok := elem.Bytes()
	if !ok || len(b) > MaxValueLength {
		return "", errNotText
	}
	return trimValue(vr, string(b)), nil
}
