package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"google.golang.org/protobuf/proto"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// Expand checks a document against a catalog and returns it expanded: its
// bases' rules copied in, each base's own bases first, then the document's
// rules; every keyword its tag; the defaults explicit. Problems lists every
// problem found, in the order of the document; the expanded form is nil
// when there is one.
//
// A base named again, directly or inside another base, is used once, where
// it first comes: naming the basic profile after an option that is built on
// it must not let the basic profile override the option.
func Expand(doc *v1.ProfileDocument, cat *Catalog) (*v1.ExpandedProfile, Problems) {
	if doc == nil {
		return nil, Problems{{Code: CodeRequired, Message: "a profile needs a document"}}
	}
	var list Problems
	top := at{list: &list}
	if len(doc.GetBases()) > MaxBases {
		top.in("bases").add(CodeTooMany, "a document names at most %d bases", MaxBases)
	}
	var order []*catalogBase
	named := map[string]bool{}
	seen := map[string]bool{}
	for i, n := range doc.GetBases() {
		if i >= MaxBases {
			break
		}
		a := top.index("bases", i)
		cb, ok := cat.bases[n]
		switch {
		case named[n]:
			a.add(CodeDuplicateBase, "%s is named twice", clipText(n))
			continue
		case !ok:
			a.add(CodeUnknownBase, "%q is not a base this server ships; ListProfileBases names them", clipText(n))
			continue
		case cb.reason != "":
			a.add(CodeBaseUnavailable, "%s is not available: %s", n, cb.reason)
			continue
		}
		named[n] = true
		order = cat.flatten(cb, seen, order)
	}
	rules, pixels := checkDocument(&list, doc)
	if len(list) > 0 {
		return nil, list
	}

	e := &v1.ExpandedProfile{Unnamed: v1.UnnamedElements_UNNAMED_ELEMENTS_KEEP, DateShiftDays: DefaultDateShiftDays}
	for _, cb := range order {
		e.Bases = append(e.Bases, cb.base.Name)
		for _, r := range cb.rules {
			e.Rules = append(e.Rules, &v1.ExpandedRule{Rule: cloneRule(r.norm), Origin: cb.base.Name})
		}
		for _, p := range cb.pixels {
			e.PixelRules = append(e.PixelRules, &v1.ExpandedPixelRule{Rule: clonePixelRule(p.norm), Origin: cb.base.Name})
		}
		takeDefaults(e, cb.base.Document)
	}
	for _, r := range rules {
		e.Rules = append(e.Rules, &v1.ExpandedRule{Rule: cloneRule(r.norm)})
	}
	for _, p := range pixels {
		e.PixelRules = append(e.PixelRules, &v1.ExpandedPixelRule{Rule: clonePixelRule(p.norm)})
	}
	takeDefaults(e, doc)
	if len(e.Rules) > MaxExpandedRules {
		top.in("bases").add(CodeTooMany, "with its bases the profile has %d rules, more than %d", len(e.Rules), MaxExpandedRules)
		return nil, list
	}
	return e, nil
}

// takeDefaults sets what doc says of the defaults over what came before
// it, so the document's own word wins over its bases' and a later base's
// over an earlier's.
func takeDefaults(e *v1.ExpandedProfile, doc *v1.ProfileDocument) {
	if u := doc.GetUnnamed(); u != v1.UnnamedElements_UNNAMED_ELEMENTS_UNSPECIFIED {
		e.Unnamed = u
	}
	if d := doc.GetDateShiftDays(); d != 0 {
		e.DateShiftDays = d
	}
}

// flatten appends cb to order after the bases it is built on, each base
// once, where it first comes.
func (c *Catalog) flatten(cb *catalogBase, seen map[string]bool, order []*catalogBase) []*catalogBase {
	if seen[cb.base.Name] {
		return order
	}
	seen[cb.base.Name] = true
	for _, n := range cb.base.Document.GetBases() {
		order = c.flatten(c.bases[n], seen, order)
	}
	return append(order, cb)
}

// Checksum returns an expanded profile's canonical text and its SHA-256 in
// lowercase hexadecimal.
func Checksum(e *v1.ExpandedProfile) (text []byte, sum string, err error) {
	if e == nil {
		return nil, "", errors.New("profile: no expanded profile")
	}
	text, err = Canonical(e)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(text)
	return text, hex.EncodeToString(h[:]), nil
}

// cloneRule returns a deep copy of a rule.
func cloneRule(r *v1.ProfileRule) *v1.ProfileRule {
	c, _ := proto.Clone(r).(*v1.ProfileRule)
	return c
}

// clonePixelRule returns a deep copy of a pixel rule.
func clonePixelRule(r *v1.PixelRule) *v1.PixelRule {
	c, _ := proto.Clone(r).(*v1.PixelRule)
	return c
}
