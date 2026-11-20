package check

import (
	"context"
	"errors"
	"unicode"
	"unicode/utf8"
)

// workPerByte bounds the work of a check: each byte of the text and of the
// raw file may cost this many steps of the automaton, which takes two at
// most plus one per pattern ending there. Past it, the patterns end on one
// another so densely (a run of one letter against forms of every length)
// that the file is refused rather than searched for minutes.
const workPerByte = 64

// workFixed is the work every check may spend whatever the file's size.
const workFixed = 1 << 20

// search finds the forms in p: in one pass over the text, then one pass
// over each raw range for the forms the text did not hold. It returns the
// findings, at most one per form.
func search(ctx, quiet context.Context, p *parsed, forms []form, source []string, minLength int, fs *findings) error {
	budget := int64(workPerByte)*int64(len(p.hay)+len(p.data)) + workFixed
	done := make([]bool, len(forms))

	// The text: one pattern per distinct folded form.
	index := map[string]int{}
	var patterns [][]byte
	var targets [][]int
	for i, f := range forms {
		at, ok := index[f.folded]
		if !ok {
			at = len(patterns)
			index[f.folded] = at
			patterns = append(patterns, []byte(f.folded))
			targets = append(targets, nil)
		}
		targets[at] = append(targets[at], i)
	}
	m := newMatcher(patterns)
	err := m.scan(ctx, p.hay, &budget, func(pat, end int) bool {
		start := end - m.lens[pat]
		for _, i := range targets[pat] {
			if !done[i] && edgesFit(p.hay, start, end, forms[i].mode) {
				done[i] = true
				fs.add(Finding{Path: p.pathAt(start), Kind: forms[i].kind})
			}
		}
		return true
	})
	if err != nil {
		return budgetRefusal(err)
	}

	// The raw bytes: every byte form of every form not found yet.
	type rawTarget struct {
		form  int
		short bool
	}
	rawIndex := map[string]int{}
	var rawPatterns [][]byte
	var rawTargets [][]rawTarget
	for i, f := range forms {
		if done[i] {
			continue
		}
		for _, r := range f.encodings(quiet, source, minLength) {
			at, ok := rawIndex[string(r.bytes)]
			if !ok {
				at = len(rawPatterns)
				rawIndex[string(r.bytes)] = at
				rawPatterns = append(rawPatterns, r.bytes)
				rawTargets = append(rawTargets, nil)
			}
			rawTargets[at] = append(rawTargets[at], rawTarget{form: i, short: r.short})
		}
	}
	if len(rawPatterns) == 0 {
		return nil
	}
	rm := newMatcher(rawPatterns)
	for _, seg := range p.segs {
		text := p.data[seg[0]:seg[1]]
		err := rm.scan(ctx, text, &budget, func(pat, end int) bool {
			start := end - rm.lens[pat]
			off := seg[0] + int64(start)
			for _, t := range rawTargets[pat] {
				if done[t.form] || (t.short && p.inBinary(off)) || !edgesFit(text, start, end, forms[t.form].mode) {
					continue
				}
				done[t.form] = true
				fs.add(Finding{Path: p.locate(off), Kind: forms[t.form].kind, Raw: true})
			}
			return true
		})
		if err != nil {
			return budgetRefusal(err)
		}
	}
	return nil
}

// budgetRefusal turns a pass past its budget into the refusal too_large,
// and returns any other error as it is.
func budgetRefusal(err error) error {
	var b errBudget
	if errors.As(err, &b) {
		return &Refusal{Cause: CauseTooLarge}
	}
	return err
}

// edgesFit reports whether the match text[start:end] stands on its own as
// m says. Folded text and upper-cased raw bytes are judged alike: what is
// read beside a match is an ASCII digit, an ASCII letter, a separator, or
// a rune of another script.
func edgesFit(text []byte, start, end int, m mode) bool {
	switch m {
	case modeBounded:
		return dateEdge(text, start, -1) && dateEdge(text, end, 1)
	case modeUID:
		if isDigit(text[start]) && start > 0 && (isDigit(text[start-1]) ||
			(text[start-1] == '.' && start > 1 && isDigit(text[start-2]))) {
			return false
		}
		if isDigit(text[end-1]) && end < len(text) && (isDigit(text[end]) ||
			(text[end] == '.' && end+1 < len(text) && isDigit(text[end+1]))) {
			return false
		}
		return true
	}
	if isDigit(text[start]) && start > 0 && isDigit(text[start-1]) {
		return false
	}
	if m == modePlain && isDigit(text[end-1]) && end < len(text) && isDigit(text[end]) {
		return false
	}
	return true
}

// dateEdge reports whether a date stands alone at position i of text on
// side dir (-1 before it, 1 after it): no ASCII letter or digit there (a
// letter of another script, as 年 in 1957年, does not join a date), and no
// date's separator ("-", "/", ".") with a digit beyond it, which would make
// it part of a longer date.
func dateEdge(text []byte, i, dir int) bool {
	var r rune
	var size int
	if dir < 0 {
		if i == 0 {
			return true
		}
		r, size = utf8.DecodeLastRune(text[:i])
	} else {
		if i >= len(text) {
			return true
		}
		r, size = utf8.DecodeRune(text[i:])
	}
	if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
		return false
	}
	if r != '-' && r != '/' && r != '.' {
		return true
	}
	j := i + dir*size
	if dir < 0 {
		return j <= 0 || !isDigit(text[j-1])
	}
	return j >= len(text) || !isDigit(text[j])
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
