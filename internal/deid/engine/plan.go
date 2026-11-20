package engine

import (
	"strings"

	"github.com/amrshadid/go-dicom/dataelem"
	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/sequence"
	"github.com/amrshadid/go-dicom/tag"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/deid/profile"
)

// facts is what conditions read of a file, read once from its top level
// before anything changes, so every condition of every element sees the
// file as received. A value is its text without the padding PS3.5 6.2
// says is not part of it, its case as written; an element absent, or
// present with no value, is absent.
type facts map[v1.ConditionSubject]string

// The elements the file's condition subjects are (PS3.6).
var factTags = map[v1.ConditionSubject]uint32{
	v1.ConditionSubject_CONDITION_SUBJECT_MODALITY:          0x00080060,
	v1.ConditionSubject_CONDITION_SUBJECT_SOP_CLASS_UID:     0x00080016,
	v1.ConditionSubject_CONDITION_SUBJECT_MANUFACTURER:      0x00080070,
	v1.ConditionSubject_CONDITION_SUBJECT_MODEL_NAME:        0x00081090,
	v1.ConditionSubject_CONDITION_SUBJECT_SOFTWARE_VERSIONS: 0x00181020,
}

// readFacts reads the facts of ds and its reception.
func readFacts(ds *dataset.Dataset, rec Reception) facts {
	f := facts{}
	for s, t := range factTags {
		if v, ok := topText(ds, t); ok && v != "" {
			f[s] = v
		}
	}
	for s, v := range map[v1.ConditionSubject]string{
		v1.ConditionSubject_CONDITION_SUBJECT_RECEIVER:         rec.Receiver,
		v1.ConditionSubject_CONDITION_SUBJECT_CALLING_AE_TITLE: rec.CallingAETitle,
		v1.ConditionSubject_CONDITION_SUBJECT_CALLED_AE_TITLE:  rec.CalledAETitle,
	} {
		if v = strings.TrimSpace(v); v != "" {
			f[s] = v
		}
	}
	return f
}

// Value implements profile.Facts.
func (f facts) Value(s v1.ConditionSubject) (string, bool) {
	v, ok := f[s]
	return v, ok
}

// sopClass is the file's SOP Class UID as received.
func (f facts) sopClass() (string, bool) {
	return f.Value(v1.ConditionSubject_CONDITION_SUBJECT_SOP_CLASS_UID)
}

// topText returns a top-level element's value as text without its
// padding, and whether ds holds it; a sequence, or a value not in memory,
// is present and empty.
func topText(ds *dataset.Dataset, t uint32) (string, bool) {
	elem, ok := ds.Get(tag.Tag(t))
	if !ok {
		return "", false
	}
	v, _ := elementText(ds, tag.Tag(t))
	return trimValue(effectiveVR(tag.Tag(t), elem), v), true
}

// elementText returns an element's value as its bytes read as text, false
// for a sequence, an element absent, and a value not in memory. A data set
// the DICOM library read holds its text decoded into UTF-8.
func elementText(ds *dataset.Dataset, t tag.Tag) (string, bool) {
	elem, ok := ds.Get(t)
	if !ok {
		return "", false
	}
	if _, isSeq := elem.GetValue().(*sequence.Sequence); isSeq {
		return "", false
	}
	b, ok := elem.Bytes()
	if !ok || len(b) > MaxValueLength {
		return "", false
	}
	return string(b), true
}

// effectiveVR is the VR an element is treated as: SQ for a sequence; the
// data dictionary's for a standard element the file holds as UN or with no
// VR, when the dictionary gives one (PS3.5 6.2.2: UN is a VR not known to
// the writer, not a VR of the attribute); the file's otherwise.
func effectiveVR(t tag.Tag, elem *dataelem.DataElement) string {
	if _, isSeq := elem.GetValue().(*sequence.Sequence); isSeq {
		return "SQ"
	}
	vr := string(elem.GetVR())
	if (vr == "UN" || vr == "") && t.Group()%2 == 0 {
		if d := t.GetVR(); d != "" && !strings.Contains(d, " or ") && d != "SQ" {
			return d
		}
	}
	if vr == "" {
		return "UN"
	}
	return vr
}

// vrIfUN returns vr when the file holds the element as UN, so a value
// written into it is written with the VR it was treated as.
func vrIfUN(elem *dataelem.DataElement, vr string) dataelem.VR {
	if v := elem.GetVR(); v == "UN" || v == "" {
		return dataelem.VR(vr)
	}
	return ""
}

// isCreator reports whether t is a private creator element (PS3.5 7.8.1).
func isCreator(t tag.Tag) bool {
	return t.Group()%2 == 1 && t.Element() >= 0x10 && t.Element() <= 0xFF
}

// charsetOf returns the character set governing ds: its own declaration,
// or inherited from the data set it is an item of.
func charsetOf(ds *dataset.Dataset, inherited charsetKind) charsetKind {
	v, ok := elementText(ds, tagSpecificCharacterSet)
	if !ok {
		return inherited
	}
	switch strings.Trim(v, " \x00") {
	case "":
		return charsetAbsent
	case "ISO_IR 192":
		return charsetUTF8
	}
	return charsetOther
}

// conceptOf returns the concept name of an item of the Content Sequence
// (PS3.3 C.17.3): the scheme and value of its Concept Name Code Sequence's
// first item, each without padding; nil for an item of any other sequence
// or without a concept name.
func conceptOf(seqTag uint32, item *dataset.Dataset) *profile.Concept {
	if seqTag != tagContentSequence {
		return nil
	}
	elem, ok := item.Get(tagConceptNameCode)
	if !ok {
		return nil
	}
	seq, ok := elem.GetValue().(*sequence.Sequence)
	if !ok || seq.Length() == 0 {
		return nil
	}
	code, ok := seq.Items()[0].(*dataset.Dataset)
	if !ok {
		return nil
	}
	scheme, _ := topText(code, tagCodingScheme)
	value, _ := topText(code, tagCodeValue)
	if scheme == "" || value == "" {
		return nil
	}
	return &profile.Concept{Scheme: scheme, Value: value}
}

// planSets plans the elements set rules make: a set rule makes its element
// at the top level when the file lacks it and the rule is the one that
// would decide it there.
func (r *run) planSets() error {
	for _, s := range r.en.dec.Sets() {
		t := tag.Tag(s.Tag)
		if r.top.Contains(t) {
			continue
		}
		dec := r.en.dec.Decide(profile.Location{Tag: s.Tag, VR: s.VR}, r.facts)
		if dec.Rule != s.Rule {
			continue
		}
		rule := dec.Name
		if rule == "" {
			rule = "set rule"
		}
		path := formatTag(s.Tag)
		out := dec.Action.Constant
		if dec.Action.Template != "" {
			var ok bool
			if out, ok = renderTemplate(dec.Action.Template, r.en.keys.Hash, r.receivedValue); !ok {
				return &Failure{Cause: CauseBadValue, Path: path, Rule: rule}
			}
		}
		if !validText(s.VR, out) || !r.charsetTakes(charsetOf(r.top, charsetAbsent), out) {
			return &Failure{Cause: CauseBadValue, Path: path, Rule: rule}
		}
		r.ops = append(r.ops, op{kind: opPut, ds: r.top, tag: t, value: []byte(out), vr: dataelem.VR(s.VR)})
		r.record.Entries = append(r.record.Entries, Entry{
			Path: path, Tag: s.Tag, VR: s.VR, Action: "create", Rule: dec.Rule,
			RuleName: dec.Name, Origin: dec.Origin, Reason: dec.Reason, Changed: true,
		})
	}
	return nil
}

// finishCleans works out each clean's text now that every needle of the
// file is known.
func (r *run) finishCleans() error {
	for i := range r.ops {
		o := &r.ops[i]
		if o.kind != opClean {
			continue
		}
		out := cleanText(o.text, r.record.Needles)
		if out == o.text {
			o.kind = 0
			continue
		}
		if !r.charsetTakes(o.charset, out) {
			return &Failure{Cause: CauseBadValue, Path: o.path, Rule: o.rule}
		}
		o.kind, o.value = opValue, []byte(out)
		r.record.Entries[o.entry].Changed = true
	}
	return nil
}

// planMethod plans the record of the de-identification in the data set
// (PS3.15 E.1.1, E.3.6), and the character set declaration a value
// outside the default repertoire needs.
func (r *run) planMethod() {
	put := func(t uint32, vr, value, reason string) {
		r.ops = append(r.ops, op{kind: opPut, ds: r.top, tag: tag.Tag(t), value: []byte(value), vr: dataelem.VR(vr)})
		r.record.Entries = append(r.record.Entries, Entry{Path: formatTag(t), Tag: t, VR: vr, Action: "set", Rule: -1, Reason: reason, Changed: true})
	}
	if r.needUTF8 {
		put(tagSpecificCharacterSet, "CS", "ISO_IR 192", "a value written is outside the default repertoire, and the data set declared no character set (PS3.5 6.1.2.3)")
	}
	put(tagIdentityRemoved, "CS", "YES", "the file was de-identified (PS3.15 E.1.1)")
	if r.en.method != "" {
		put(tagDeidMethod, "LO", r.en.method, "the profile version that de-identified the file (PS3.15 E.1.1)")
	}
	if r.record.DatesShifted {
		put(tagTemporalModified, "CS", "MODIFIED", "dates were shifted (PS3.15 E.3.6)")
	}
}

// apply makes every planned change. Nothing here can fail: every value
// was worked out and checked before.
func (r *run) apply() {
	for _, o := range r.ops {
		switch o.kind {
		case opRemove:
			o.ds.Remove(o.tag)
		case opValue:
			if elem, ok := o.ds.Get(o.tag); ok {
				elem.SetValue(o.value)
				if o.vr != "" {
					elem.SetVR(o.vr)
				}
			}
		case opNoItems:
			if elem, ok := o.ds.Get(o.tag); ok {
				elem.SetValue(sequence.New())
			}
		case opPut:
			if elem, ok := o.ds.Get(o.tag); ok {
				elem.SetValue(o.value)
				elem.SetVR(o.vr)
			} else {
				// Add fails only for a nil element or a tag that is not a
				// tag.Tag, neither of which can be here.
				_ = o.ds.Add(dataelem.NewDataElement(o.tag, o.vr, o.value))
			}
		}
	}
}
