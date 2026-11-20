package profile

import (
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// Bounds of a condition.
const (
	// MaxConditions is the most conditions of a rule.
	MaxConditions = 16
	// MaxPatternLength is the bound of a regular expression's text.
	MaxPatternLength = 256
	// MaxPatternRepeat is the largest count of a repetition, such as
	// {100}. A count is the bound that matters: the text of x{999} is
	// short, and its compiled form a thousand copies of x.
	MaxPatternRepeat = 100
	// MaxPatternCost is the bound of one regular expression's compiled
	// size: its instructions and the ranges of its character classes. A
	// class such as \pL holds hundreds of ranges, so the ranges are
	// counted with the instructions, which alone would let \pL{100}
	// through at a hundred.
	MaxPatternCost = 10000
	// MaxPatternBudget is the bound of the compiled size of every regular
	// expression of a document, or of an expanded profile, counted once
	// for each condition that has one: what one check compiles and what
	// one decision may run is bounded, whatever the number of rules.
	MaxPatternBudget = 200000
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
	before := len(a.c.list)
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
		out.re = compilePattern(a.in("pattern"), t.Pattern)
		out.text = t.Pattern
	case *v1.ProfileCondition_Present:
		out.kind, out.present = testPresent, t.Present
	default:
		a.add(CodeOneOf, "a condition needs one test: equals, prefix, pattern or present")
	}
	return out, len(a.c.list) == before
}

// compiledPattern is a regular expression a check compiled, or why it did
// not, and its compiled size.
type compiledPattern struct {
	re      *regexp.Regexp
	cost    int
	problem Code
	message string
}

// compilePattern compiles a condition's pattern so that it matches the
// whole value, and counts its size against the check's budget. It returns
// nil, after recording a problem, for a pattern that is not RE2, is too
// large, or passes the budget.
//
// The pattern is parsed alone first, so it is known to be whole (a pattern
// such as "a)|(b" would otherwise escape the anchors around it), and its
// size is measured on its compiled program before the regexp package
// compiles it, once for each distinct text in a check: a size bound on the
// text alone let short patterns compile to megabytes. RE2 then runs in
// time linear in the value.
func compilePattern(a at, p string) *regexp.Regexp {
	cp, seen := a.c.patterns[p]
	if !seen {
		cp = measurePattern(p)
		if cp.problem == "" {
			// Within the bounds just measured, compiling cannot fail.
			cp.re = regexp.MustCompile(`\A(?:` + p + `)\z`)
		}
		a.c.patterns[p] = cp
	}
	if cp.problem != "" {
		a.add(cp.problem, "%s", cp.message)
		return nil
	}
	a.c.cost += cp.cost
	if a.c.cost > MaxPatternBudget {
		if !a.c.overBudget {
			a.c.overBudget = true
			a.add(CodeTooLarge, "the regular expressions of the profile compile to more than %d instructions and ranges, counted once for each condition", MaxPatternBudget)
		}
		return nil
	}
	return cp.re
}

// measurePattern parses a pattern and measures its compiled size, without
// the regexp package's own compilation, whose cost is what is bounded.
func measurePattern(p string) compiledPattern {
	if len(p) > MaxPatternLength {
		return compiledPattern{problem: CodeBadPattern, message: "a pattern is at most " + strconv.Itoa(MaxPatternLength) + " bytes"}
	}
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		return compiledPattern{problem: CodeBadPattern, message: "\"" + clipText(p) + "\" is not a regular expression of RE2: " + err.Error()}
	}
	if n := largestRepeat(re); n > MaxPatternRepeat {
		return compiledPattern{problem: CodeBadPattern, message: "a repetition counts at most " + strconv.Itoa(MaxPatternRepeat) + ", and this one " + strconv.Itoa(n)}
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return compiledPattern{problem: CodeBadPattern, message: "\"" + clipText(p) + "\" does not compile: " + err.Error()}
	}
	cost := len(prog.Inst)
	for _, in := range prog.Inst {
		cost += len(in.Rune) / 2
	}
	if cost > MaxPatternCost {
		return compiledPattern{problem: CodeBadPattern, message: "the pattern compiles to " + strconv.Itoa(cost) + " instructions and ranges, more than " + strconv.Itoa(MaxPatternCost)}
	}
	return compiledPattern{cost: cost}
}

// largestRepeat returns the largest count of a repetition in re, such as
// 999 for x{2,999}.
func largestRepeat(re *syntax.Regexp) int {
	n := 0
	if re.Op == syntax.OpRepeat {
		n = max(re.Min, re.Max)
	}
	for _, sub := range re.Sub {
		n = max(n, largestRepeat(sub))
	}
	return n
}
