package profile

import (
	"regexp"
	"strings"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// Bounds of a condition.
const (
	// MaxConditions is the most conditions of a rule.
	MaxConditions = 16
	// MaxPatternLength is the bound of a regular expression.
	MaxPatternLength = 256
	// maxConditionText is the bound of an equals or prefix text: the
	// longest value the subjects can hold, an LO of several values, is far
	// below it.
	maxConditionText = 1024
)

// Facts is what a condition reads of a file and its reception: the value of
// a subject as received, and whether it is present. The engine implements
// it over the data set being de-identified.
type Facts interface {
	// Value returns the subject's value and whether it is present.
	Value(subject v1.ConditionSubject) (value string, present bool)
}

// testKind is what a condition tests.
type testKind int

// The tests.
const (
	testEquals testKind = iota + 1
	testPrefix
	testPattern
	testPresent
)

// condition is a compiled ProfileCondition.
type condition struct {
	subject v1.ConditionSubject
	kind    testKind
	text    string
	re      *regexp.Regexp
	present bool
	negate  bool
}

// holds reports whether the condition holds for f.
func (c condition) holds(f Facts) bool {
	v, ok := f.Value(c.subject)
	var r bool
	switch c.kind {
	case testEquals:
		r = ok && v == c.text
	case testPrefix:
		r = ok && strings.HasPrefix(v, c.text)
	case testPattern:
		r = ok && c.re.MatchString(v)
	case testPresent:
		r = (ok && v != "") == c.present
	}
	return r != c.negate
}

// allHold reports whether every condition holds.
func allHold(cs []condition, f Facts) bool {
	for _, c := range cs {
		if !c.holds(f) {
			return false
		}
	}
	return true
}

// parseConditions compiles a rule's conditions.
func parseConditions(a at, field string, list []*v1.ProfileCondition) []condition {
	if len(list) > MaxConditions {
		a.in(field).add(CodeTooMany, "a rule has at most %d conditions", MaxConditions)
		return nil
	}
	out := make([]condition, 0, len(list))
	for i, c := range list {
		if cc, ok := parseCondition(a.index(field, i), c); ok {
			out = append(out, cc)
		}
	}
	return out
}

// parseCondition compiles one condition.
func parseCondition(a at, c *v1.ProfileCondition) (condition, bool) {
	before := len(*a.list)
	out := condition{subject: c.GetSubject(), negate: c.GetNegate()}
	if _, known := v1.ConditionSubject_name[int32(c.GetSubject())]; !known || c.GetSubject() == v1.ConditionSubject_CONDITION_SUBJECT_UNSPECIFIED {
		a.in("subject").add(CodeBadEnum, "a condition needs its subject, one of ConditionSubject")
	}
	switch t := c.GetTest().(type) {
	case *v1.ProfileCondition_Equals:
		out.kind, out.text = testEquals, t.Equals
		if len(t.Equals) > maxConditionText {
			a.in("equals").add(CodeTooLong, "at most %d bytes", maxConditionText)
		}
	case *v1.ProfileCondition_Prefix:
		out.kind, out.text = testPrefix, t.Prefix
		switch {
		case t.Prefix == "":
			a.in("prefix").add(CodeRequired, "an empty prefix holds for every value: use present")
		case len(t.Prefix) > maxConditionText:
			a.in("prefix").add(CodeTooLong, "at most %d bytes", maxConditionText)
		}
	case *v1.ProfileCondition_Pattern:
		out.kind = testPattern
		if len(t.Pattern) > MaxPatternLength {
			a.in("pattern").add(CodeBadPattern, "a pattern is at most %d bytes", MaxPatternLength)
			break
		}
		// The pattern is compiled alone first, so it is known to be whole
		// (a pattern such as "a)|(b" would otherwise escape the anchors
		// around it); then the whole value must match, so a pattern means
		// what it says without anchors. RE2 runs in time linear in the
		// value.
		if _, err := regexp.Compile(t.Pattern); err != nil {
			a.in("pattern").add(CodeBadPattern, "%q is not a regular expression of RE2: %v", clipText(t.Pattern), err)
			break
		}
		re, err := regexp.Compile(`\A(?:` + t.Pattern + `)\z`)
		if err != nil {
			a.in("pattern").add(CodeBadPattern, "%q is not a regular expression of RE2: %v", clipText(t.Pattern), err)
			break
		}
		out.re = re
		out.text = t.Pattern
	case *v1.ProfileCondition_Present:
		out.kind, out.present = testPresent, t.Present
	default:
		a.add(CodeOneOf, "a condition needs one test: equals, prefix, pattern or present")
	}
	return out, len(*a.list) == before
}
