package profile

import (
	"fmt"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// Location is where an element is in a data set, as a rule's selector reads
// it. The engine makes one for each element it visits.
type Location struct {
	// Sequences are the tags of the sequences the element is inside,
	// outermost first; empty at the top level.
	Sequences []uint32
	// Tag is the element's tag.
	Tag uint32
	// Creator is the private creator of the block a private data element
	// is in (PS3.5 7.8.1), without padding; empty for any other element.
	Creator string
	// VR is the element's value representation.
	VR string
	// ContentItem is the concept name of the structured report content
	// item whose value the element is (PS3.3 C.17.3); nil for any other
	// element.
	ContentItem *Concept
}

// Decision is what a profile does to one element: the rule that decides and
// its action, or the default when no rule does.
type Decision struct {
	// Index is the position of the rule in the expanded profile's rules,
	// from 0; -1 when no rule decides.
	Index int
	// Rule is the rule that decides; nil when no rule does.
	Rule *v1.ExpandedRule
	// Action is what happens to the element: the rule's action, or keep or
	// remove by the default.
	Action *v1.ProfileAction
}

// Program is an expanded profile compiled for deciding, element by element,
// which rule applies. It is never changed after it is made, so one Program
// serves every file at once.
type Program struct {
	expanded *v1.ExpandedProfile
	rules    []rule
	pixels   []pixelRule
	// The rules of each tier, by position, so a decision reads only the
	// rules that could select an element.
	places []int
	byTag  map[uint32][]int
	privEl map[privateKey][]int
	block  map[privateKey][]int
	groups []int
	byVR   map[string][]int
}

// privateKey is a private block: its creator and group.
type privateKey struct {
	creator string
	group   uint16
}

// The default actions.
var (
	keepAction   = &v1.ProfileAction{Kind: &v1.ProfileAction_Keep{Keep: &v1.KeepAction{}}}
	removeAction = &v1.ProfileAction{Kind: &v1.ProfileAction_Remove{Remove: &v1.RemoveAction{}}}
)

// Compile compiles an expanded profile, as Expand made it or as a stored
// version holds it. An expanded profile that does not compile was not made
// by Expand, or was changed after.
func Compile(e *v1.ExpandedProfile) (*Program, error) {
	if e == nil {
		return nil, fmt.Errorf("profile: no expanded profile")
	}
	if e.GetUnnamed() == v1.UnnamedElements_UNNAMED_ELEMENTS_UNSPECIFIED || e.GetDateShiftDays() < 1 || e.GetDateShiftDays() > MaxDateShiftDays {
		return nil, fmt.Errorf("profile: an expanded profile states its defaults")
	}
	if len(e.GetRules()) > MaxExpandedRules {
		return nil, fmt.Errorf("profile: an expanded profile has at most %d rules", MaxExpandedRules)
	}
	p := &Program{expanded: e, byTag: map[uint32][]int{}, privEl: map[privateKey][]int{}, block: map[privateKey][]int{}, byVR: map[string][]int{}}
	for i, er := range e.GetRules() {
		var list Problems
		r := er.GetRule()
		a := at{list: &list, rule: i + 1, name: r.GetName()}
		sel, _, ok := parseSelector(a.in("selector"), r.GetSelector())
		when := parseConditions(a, "when", r.GetWhen())
		if ok {
			checkAction(a, r.GetAction(), sel)
		}
		if len(list) > 0 {
			return nil, fmt.Errorf("profile: the expanded profile: %w", list)
		}
		p.rules = append(p.rules, rule{sel: sel, when: when, action: r.GetAction(), norm: r, origin: er.GetOrigin()})
		switch sel.tier {
		case TierPlace:
			p.places = append(p.places, i)
		case TierTag:
			p.byTag[sel.tag] = append(p.byTag[sel.tag], i)
		case TierPrivateElement:
			k := privateKey{sel.creator, sel.privGroup}
			p.privEl[k] = append(p.privEl[k], i)
		case TierPrivateBlock:
			k := privateKey{sel.creator, sel.privGroup}
			p.block[k] = append(p.block[k], i)
		case TierGroup:
			p.groups = append(p.groups, i)
		case TierVR:
			p.byVR[sel.vr] = append(p.byVR[sel.vr], i)
		}
	}
	for i, ep := range e.GetPixelRules() {
		var list Problems
		r := ep.GetRule()
		c, ok := checkPixelRule(at{list: &list, pixelRule: i + 1, name: r.GetName()}, r, map[string]int{})
		if !ok {
			return nil, fmt.Errorf("profile: the expanded profile: %w", list)
		}
		c.origin = ep.GetOrigin()
		p.pixels = append(p.pixels, c)
	}
	return p, nil
}

// Unnamed returns what happens to an element no rule decides, other than a
// private one.
func (p *Program) Unnamed() v1.UnnamedElements { return p.expanded.GetUnnamed() }

// DateShiftDays returns the largest date shift, in days.
func (p *Program) DateShiftDays() int32 { return p.expanded.GetDateShiftDays() }

// Decide returns what the profile does to the element at loc, given the
// file's and the reception's facts. Of the rules whose selector matches and
// whose conditions all hold, the one of the highest tier decides, and
// between rules of one tier the later. With none, a private element (an
// odd group) is removed and any other follows Unnamed.
func (p *Program) Decide(loc Location, f Facts) Decision {
	pk := privateKey{loc.Creator, group(loc.Tag)}
	// One list per tier, from the most specific.
	for _, candidates := range [][]int{
		p.places,
		p.byTag[loc.Tag],
		p.privEl[pk],
		p.block[pk],
		p.groups,
		p.byVR[loc.VR],
	} {
		// The rules of a tier are in order, so the last that applies is
		// the later rule.
		for j := len(candidates) - 1; j >= 0; j-- {
			i := candidates[j]
			r := p.rules[i]
			if r.sel.matches(loc) && allHold(r.when, f) {
				return Decision{Index: i, Rule: p.expanded.GetRules()[i], Action: r.action}
			}
		}
	}
	if group(loc.Tag)%2 == 1 || p.Unnamed() == v1.UnnamedElements_UNNAMED_ELEMENTS_REMOVE {
		return Decision{Index: -1, Action: removeAction}
	}
	return Decision{Index: -1, Action: keepAction}
}

// Pixels returns the pixel rule that decides for an image of rows and
// columns, given the file's and the reception's facts: the last that
// matches. ok is false when none does, and an image at risk is then
// quarantined.
func (p *Program) Pixels(rows, columns int, f Facts) (r *v1.ExpandedPixelRule, ok bool) {
	for i := len(p.pixels) - 1; i >= 0; i-- {
		c := p.pixels[i]
		if (c.rows == 0 || int(c.rows) == rows) && (c.cols == 0 || int(c.cols) == columns) && allHold(c.when, f) {
			return p.expanded.GetPixelRules()[i], true
		}
	}
	return nil, false
}

// matches reports whether the selector selects the element at loc. A tag
// or a path never selects a private element, which only its creator
// identifies; a private selector selects a data element of a block, never
// the creator element that reserves it.
func (s selector) matches(loc Location) bool {
	standard := loc.Creator == "" && group(loc.Tag)%2 == 0
	switch s.tier {
	case TierPlace:
		if s.concept != nil {
			return loc.ContentItem != nil && *loc.ContentItem == *s.concept
		}
		return standard && loc.Tag == s.tag && stepsMatch(s.steps, loc.Sequences)
	case TierTag:
		return standard && loc.Tag == s.tag
	case TierPrivateElement, TierPrivateBlock:
		if loc.Creator != s.creator || group(loc.Tag) != s.privGroup || loc.Tag&0xFF00 < 0x1000 {
			return false
		}
		return s.privElem < 0 || int(loc.Tag&0xFF) == s.privElem
	case TierGroup:
		return group(loc.Tag)&s.groupMask == s.groupValue
	case TierVR:
		return loc.VR == s.vr
	}
	return false
}

// stepsMatch reports whether a path's steps above its element match the
// sequences an element is inside: a sequence step one level of that tag,
// "**" any number of levels. It runs in time proportional to the product of
// the two lengths, whatever the number of "**".
func stepsMatch(steps []step, seqs []uint32) bool {
	// ok[j]: the steps so far match the first j sequences.
	ok := make([]bool, len(seqs)+1)
	ok[0] = true
	for _, s := range steps {
		next := make([]bool, len(seqs)+1)
		for j := range ok {
			switch {
			case s.anyDepth:
				// Zero levels, or one more after a match.
				next[j] = ok[j] || (j > 0 && next[j-1])
			case j > 0:
				next[j] = ok[j-1] && seqs[j-1] == s.tag
			}
		}
		ok = next
	}
	return ok[len(seqs)]
}
