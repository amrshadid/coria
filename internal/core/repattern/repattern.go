package repattern

import (
	"errors"
	"regexp"
	"regexp/syntax"
	"strconv"
)

// The bounds of one pattern.
const (
	// MaxLength is the longest pattern text, in bytes.
	MaxLength = 256
	// MaxRepeat is the largest count of a repetition, such as {100}.
	MaxRepeat = 100
	// MaxCost is the largest compiled size of one pattern: its
	// instructions and the ranges of its character classes.
	MaxCost = 10000
)

// Compile returns the pattern compiled to match a whole value, and its
// compiled size, which a caller adds up against its own budget, or an
// error saying in plain words why the pattern is refused. The pattern is
// parsed alone first, so a text such as "a)|(b" cannot escape the anchors
// around it, and measured on its compiled program before the regexp
// package compiles it.
func Compile(p string) (*regexp.Regexp, int, error) {
	if len(p) > MaxLength {
		return nil, 0, errors.New("a pattern is at most " + strconv.Itoa(MaxLength) + " bytes")
	}
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		return nil, 0, errors.New("the pattern is not a regular expression of RE2: " + err.Error())
	}
	if n := largestRepeat(re); n > MaxRepeat {
		return nil, 0, errors.New("a repetition counts at most " + strconv.Itoa(MaxRepeat) + ", and this one " + strconv.Itoa(n))
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return nil, 0, errors.New("the pattern does not compile: " + err.Error())
	}
	cost := len(prog.Inst)
	for _, in := range prog.Inst {
		cost += len(in.Rune) / 2
	}
	if cost > MaxCost {
		return nil, 0, errors.New("the pattern compiles to " + strconv.Itoa(cost) + " instructions and ranges, more than " + strconv.Itoa(MaxCost))
	}
	full, err := regexp.Compile(`\A(?:` + p + `)\z`)
	if err != nil {
		return nil, 0, errors.New("the pattern does not compile: " + err.Error())
	}
	return full, cost, nil
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
