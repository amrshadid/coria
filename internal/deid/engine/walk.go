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

	"github.com/amrshadid/coria/internal/core/naming"
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
	// source is what the file's text was written in (Reception.CharacterSet).
	source textSource
	// rawSources are the items of sequences held as UN bytes, each with
	// what its own text was written in, for the sequences held as UN bytes
	// inside it.
	rawSources map[*dataset.Dataset]textSource
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
	// opSequence gives an element held as UN bytes the sequence they were
	// parsed into, and vr (SQ).
	opSequence
	// opElement puts elem in place of the element of its tag, or adds it.
	opElement
)

// op is one change of the plan.
type op struct {
	kind  opKind
	ds    *dataset.Dataset
	tag   tag.Tag
	value []byte
	// seq is opSequence's sequence.
	seq *sequence.Sequence
	// elem is opElement's element.
	elem *dataelem.DataElement
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
	// source is what the bytes of a value the library did not decode (a
	// sequence or a private value held as UN) are written in, in this
	// data set.
	source textSource
}

// Tags the engine treats by fixed rules.
const (
	tagSpecificCharacterSet = 0x00080005
	tagPatientID            = 0x00100020
	tagValueType            = 0x0040A040
	tagConceptNameCode      = 0x0040A043
	tagCodeValue            = 0x00080100
	tagCodingScheme         = 0x00080102
	tagIdentityRemoved      = 0x00120062
	tagDeidMethod           = 0x00120063
	tagDeidMethodCodes      = 0x00120064
	tagTemporalModified     = 0x00280303
)

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
	creators := creatorsOf(ds, tags)
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
		case handled(t):
			h, why := profile.Pipeline(uint32(t))
			if h == profile.HandledKept {
				r.fixed(ds, t, elem, p, 0, why)
				break
			}
			if seq, isSeq := elem.GetValue().(*sequence.Sequence); isSeq {
				if err := r.subtreeNeedles(seq, nil, p, len(seqs)+1, subtreeByTag); err != nil {
					return err
				}
			}
			r.fixed(ds, t, elem, p, opRemove, why)
		default:
			private := t.Group()%2 == 1
			bk, creator := privateCreator(t, creators)
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

// creatorsOf returns the private creators of a data set by block: a
// private creator element (gggg,0010-00FF) reserves the block of its group
// (PS3.5 7.8.1), and is compared without its padding, the space or NUL
// that makes its length even (PS3.5 6.2), and any surrounding space.
func creatorsOf(ds *dataset.Dataset, tags []tag.Tag) map[blockKey]string {
	creators := map[blockKey]string{}
	for _, t := range tags {
		if isCreator(t) {
			if v, ok := elementText(ds, t); ok {
				creators[blockKey{t.Group(), t.Element()}] = strings.Trim(v, " \x00")
			}
		}
	}
	return creators
}

// handled reports whether the pipeline handles t in every file, whatever
// the profile says (profile.Pipeline): the one list the profile language
// refuses to name, so the engine and the language cannot drift apart.
func handled(t tag.Tag) bool {
	h, _ := profile.Pipeline(uint32(t))
	return h != profile.NotHandled
}

// privateCreator returns the creator of the block a private data element
// is in, "" for a standard element or one whose block has none.
func privateCreator(t tag.Tag, creators map[blockKey]string) (blockKey, string) {
	if t.Group()%2 == 1 && t.Element()>>8 >= 0x10 {
		bk := blockKey{t.Group(), t.Element() >> 8}
		return bk, creators[bk]
	}
	return blockKey{}, ""
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
	unItems := unknownItems(elem, vr)
	if unItems != nil {
		vr = "SQ"
	}
	if err := checkText(elem, vr, sc.charset); err != nil {
		return false, &Failure{Cause: CauseBadValue, Path: path}
	}
	loc := profile.Location{Sequences: seqs, Tag: uint32(t), Creator: creator, VR: vr}
	if sc.valueType != "" {
		loc.ContentItem, loc.ValueType = sc.concept, sc.valueType
	}
	dec := r.en.dec.Decide(loc, r.facts)
	e := Entry{Path: path, Tag: uint32(t), VR: vr, Action: dec.Action.Kind.String(), Rule: dec.Rule, RuleName: dec.Name, Origin: dec.Origin, Reason: dec.Reason}
	raw, _ := elementText(ds, t)
	nvr := needleVR(t, vr, raw)
	c := &target{ds: ds, tag: t, elem: elem, vr: vr, nvr: nvr, seqs: seqs, path: path, entry: len(r.record.Entries), sc: sc}
	if dec.Rule >= 0 {
		c.byRule = true
		c.rule = dec.Name
		if c.rule == "" {
			c.rule = "rule " + strconv.Itoa(dec.Rule+1)
		}
	}
	act := dec.Action
	if why := floorReason(uint32(t), vr, nvr); why != "" {
		c.floor = true
		if fa, changed := floorAction(act, t.Group()%2 == 1); changed {
			act = fa
			e.Action, e.Rule, e.RuleName, e.Origin = fa.Kind.String(), -1, "", ""
			e.Reason = "the engine never keeps " + why + ", whatever the profile decides"
			c.byRule, c.rule = false, ""
		}
	}
	c.seq, _ = elem.GetValue().(*sequence.Sequence)
	c.unItems = unItems
	if act.Kind == ActKeep && vr == "UN" && creator != "" && !c.floor {
		r.keepSafePrivate(c, creator, &e)
	}
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
		isc := scope{charset: charsetOf(item, sc.charset), source: sc.source}
		if src, ok := r.rawSources[item]; ok {
			isc.source = src
		}
		isc.concept, isc.valueType = contentItemOf(item)
		if err := r.walk(item, inner, path+"["+strconv.Itoa(i)+"]/", isc); err != nil {
			return err
		}
	}
	return nil
}

// subtreeMode is how subtreeNeedles takes the values of a sequence that
// is removed or emptied.
type subtreeMode int

// The modes.
const (
	// subtreeAll: every text value, for a sequence the floor covers,
	// which as a whole names or identifies the person (Other Patient IDs
	// Sequence, Person Identification Code Sequence).
	subtreeAll subtreeMode = iota + 1
	// subtreeByPlace: each element decided at its place, for a sequence a
	// rule removes or empties.
	subtreeByPlace
	// subtreeByTag: each element decided by its tag alone, for a sequence
	// the pipeline removes that holds attributes as they were received
	// (the Original Attributes Sequence: its Modified Attributes Sequence
	// holds attributes of the data set, PS3.3 C.12.1.1.9).
	subtreeByTag
)

// subtreeNeedles keeps as needles the values inside a sequence that is
// removed or emptied. Under subtreeAll every text value is one. Otherwise
// each element is decided as walk would decide it, at its place inside
// seqs (the sequences above seq, seq's own tag last) or by its tag alone,
// and its value is a needle when the floor covers it or a rule decides it,
// and the action would remove or change it. Not every text: a description,
// a code meaning or a reason the profile keeps where it stands ("CT HEAD")
// would make clean cut it out of what the file keeps, and the check after
// refuse the file for it.
func (r *run) subtreeNeedles(seq *sequence.Sequence, seqs []uint32, path string, depth int, mode subtreeMode) error {
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
		creators := creatorsOf(item, tags)
		concept, valueType := contentItemOf(item)
		for _, t := range tags {
			r.count++
			if r.count > MaxElements {
				return &Failure{Cause: CauseTooLarge}
			}
			elem, ok := item.Get(t)
			if !ok || isCreator(t) || handled(t) {
				continue
			}
			p := base + formatTag(uint32(t))
			inner, isSeq := elem.GetValue().(*sequence.Sequence)
			if b := unknownItems(elem, effectiveVR(t, elem)); b != nil {
				// A sequence held as UN bytes is read for its values like
				// any other, its text decoded as the file wrote it; one
				// that cannot be read gives none, and goes with the
				// sequence that holds it.
				if seq, err := decodeUnknownItems(b); err == nil && r.decodeRawItems(seq, r.sourceOf(item), depth+1) == nil {
					inner, isSeq = seq, true
				}
			}
			if isSeq {
				innerSeqs := seqs
				if mode == subtreeByPlace {
					innerSeqs = slices.Concat(seqs, []uint32{uint32(t)})
				}
				if err := r.subtreeNeedles(inner, innerSeqs, p, depth+1, mode); err != nil {
					return err
				}
				continue
			}
			raw, ok := elementText(item, t)
			if !ok {
				continue
			}
			vr := effectiveVR(t, elem)
			_, creator := privateCreator(t, creators)
			value := naming.TrimValue(vr, raw)
			if mode == subtreeAll {
				r.needle(needleVR(t, vr, raw), value, p)
				continue
			}
			loc := profile.Location{Sequences: seqs, Tag: uint32(t), Creator: creator, VR: vr}
			if valueType != "" {
				loc.ContentItem, loc.ValueType = concept, valueType
			}
			dec := r.en.dec.Decide(loc, r.facts)
			act, nvr := dec.Action, needleVR(t, vr, raw)
			floor := floorReason(uint32(t), vr, nvr) != ""
			if floor {
				act, _ = floorAction(act, t.Group()%2 == 1)
			}
			if (floor || dec.Rule >= 0) && changesValue(act, vr, value) {
				r.needle(nvr, value, p)
			}
		}
	}
	return nil
}

// changesValue reports whether an action removes or changes a value of VR
// vr: every action but keep and clean (which leave what they do not
// recognize), a generalization that leaves the value as it is (an age
// under 90), and a constant equal to the value.
func changesValue(a Action, vr, value string) bool {
	switch a.Kind {
	case ActKeep, ActClean:
		return false
	case ActGeneralize:
		var out string
		var err error
		if vr == "AS" {
			out, err = generalizeAge(value)
		} else {
			out, err = generalizeDate(value)
		}
		return err != nil || out != value
	case ActReplace, ActSet:
		return a.Template != "" || a.Constant != value
	}
	return true
}

// needle keeps a removed or replaced value for the check after: each value
// of a multi-valued one, and the whole. A registered UID is not one: it
// names a kind of object, stays wherever else it is, and is no trace of
// the patient. A value of VR UN is one when it reads as text (unknownText).
func (r *run) needle(vr, value, path string) {
	kind := needleKind(vr)
	if vr == "UN" {
		var ok bool
		if value, ok = unknownText(value); !ok {
			return
		}
	}
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
// no text. A value of VR UN (a private element of an implicit VR file, or
// one a system without its dictionary passed on, PS3.5 6.2.2) is text: its
// VR is not known, and searching for it as text is what finds a copy of
// it in a description.
func needleKind(vr string) NeedleKind {
	switch vr {
	case "PN":
		return NeedleName
	case "DA", "DT":
		return NeedleDate
	case "UI":
		return NeedleUID
	case "AE", "AS", "CS", "DS", "IS", "LO", "LT", "SH", "ST", "TM", "UC", "UR", "UT", "UN":
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
	r.shift = ShiftDays(r.en.keys.Date, id, r.en.dec.DateShiftDays())
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
	return naming.TrimValue(vr, string(b)), nil
}
