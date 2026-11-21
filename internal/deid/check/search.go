package check

import (
	"context"
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/amrshadid/coria/internal/deid/engine"
)

// workBudget bounds the work of a check that does not grow with the file:
// each form found ending at a position (whether or not its edges then
// fit) and each rune of a form checked against a character set costs one
// unit. The automaton's own steps, at most two a byte, are not charged.
// Normal files spend a few thousand units; a file whose text makes forms
// end on one another at every byte (a run of one letter against forms of
// every length) spends the budget within a second or two whatever its
// size, and is refused as too large rather than searched for minutes.
const workBudget = 1 << 25

// search finds the forms in p: in one pass over the text, then one pass
// over each raw range for the forms the text did not hold. It adds at most
// one finding per form to fs.
func search(ctx, quiet context.Context, p *parsed, set formSet, source []string, opts Options, fs *findings, sm seams) error {
	forms := set.forms
	budget := int64(workBudget)
	if sm.work != nil {
		defer func() { sm.work(workBudget - budget) }()
	}
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
	at, err := m.scan(ctx, p.hay, &budget, func(pat, end int) {
		start := end - m.lens[pat]
		for _, i := range targets[pat] {
			if !done[i] && edgesFit(p.hay, start, end, forms[i].mode) {
				done[i] = true
				fs.add(Finding{Path: p.pathAt(start), Kind: forms[i].kind})
			}
		}
	})
	if err != nil {
		if sm.refusedAt != nil {
			sm.refusedAt(at)
		}
		return budgetRefusal(err)
	}
	m = nil

	// The raw bytes: every byte form of every form not found yet, the
	// bytes of all forms within MaxFormBytes.
	type rawTarget struct {
		form      int
		short, g0 bool
	}
	rawIndex := map[string]int{}
	var rawPatterns [][]byte
	var rawTargets [][]rawTarget
	total := set.bytes
	enc := newEncoder(quiet, source, &budget)
	enc.library = sm.library
	for i, f := range forms {
		if done[i] {
			continue
		}
		raws, err := f.encodings(enc, opts.MinLength)
		if err != nil {
			return err
		}
		if budget < 0 {
			return &Refusal{Cause: CauseTooLarge}
		}
		for _, r := range raws {
			at, ok := rawIndex[string(r.bytes)]
			if !ok {
				if total += len(r.bytes); total > opts.MaxFormBytes {
					return &Refusal{Cause: CauseTooManyNeedles}
				}
				at = len(rawPatterns)
				rawIndex[string(r.bytes)] = at
				rawPatterns = append(rawPatterns, r.bytes)
				rawTargets = append(rawTargets, nil)
			}
			rawTargets[at] = append(rawTargets[at], rawTarget{form: i, short: r.short, g0: r.g0})
		}
	}
	if len(rawPatterns) == 0 {
		return nil
	}
	rm := newMatcher(rawPatterns)
	for _, seg := range p.segs {
		text := p.data[seg[0]:seg[1]]
		_, err := rm.scan(ctx, text, &budget, func(pat, end int) {
			start := end - rm.lens[pat]
			off := seg[0] + int64(start)
			for _, t := range rawTargets[pat] {
				if done[t.form] || (t.short && p.inBinary(off)) || (t.g0 && !p.inShifted(off, seg[0]+int64(end))) ||
					!edgesFit(text, start, end, forms[t.form].mode) {
					continue
				}
				done[t.form] = true
				fs.add(Finding{Path: p.locate(off), Kind: forms[t.form].kind, Raw: true})
			}
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
	case modeWord:
		return wordEdge(text, start, -1) && wordEdge(text, end, 1)
	case modeDigits:
		return (start == 0 || !isDigit(text[start-1])) && (end == len(text) || !isDigit(text[end]))
	case modeIdentifier:
		if !identifierEdge(text, start, end) {
			return false
		}
	case modeUID:
		if isDigit(text[start]) && start > 0 && (isDigit(text[start-1]) ||
			(text[start-1] == '.' && start > 1 && isDigit(text[start-2]))) {
			return false
		}
		return !isDigit(text[end-1]) || end == len(text) || !isDigit(text[end])
	}
	if isDigit(text[start]) && start > 0 && isDigit(text[start-1]) {
		return false
	}
	if (m == modePlain || m == modeIdentifier) && isDigit(text[end-1]) && end < len(text) && isDigit(text[end]) {
		return false
	}
	return true
}

// runeAt returns the rune beside position i of text on side dir (-1
// before it, 1 after it), its size, and whether there is one.
func runeAt(text []byte, i, dir int) (rune, int, bool) {
	if dir < 0 {
		if i == 0 {
			return 0, 0, false
		}
		r, size := utf8.DecodeLastRune(text[:i])
		return r, size, true
	}
	if i >= len(text) {
		return 0, 0, false
	}
	r, size := utf8.DecodeRune(text[i:])
	return r, size, true
}

// wordEdge reports whether a word ends at position i of text on side dir:
// no letter or digit of any script beside it.
func wordEdge(text []byte, i, dir int) bool {
	r, _, ok := runeAt(text, i, dir)
	return !ok || (!unicode.IsLetter(r) && !unicode.IsDigit(r))
}

// identifierEdge reports whether the match text[start:end] of an
// identifier stands as engine.EdgeIdentifier says on each side where it
// ends in a letter: no letter of any script and no "_" beside it
// (engine.IdentifierWordRune).
// Bytes that are not UTF-8 (a form in another character set) read as no
// letter, so such a side is matched as plain: the search errs toward
// finding.
func identifierEdge(text []byte, start, end int) bool {
	if first, _, _ := runeAt(text, start, 1); unicode.IsLetter(first) {
		if r, _, ok := runeAt(text, start, -1); ok && engine.IdentifierWordRune(r) {
			return false
		}
	}
	if last, _, _ := runeAt(text, end, -1); unicode.IsLetter(last) {
		if r, _, ok := runeAt(text, end, 1); ok && engine.IdentifierWordRune(r) {
			return false
		}
	}
	return true
}

// dateEdge reports whether a date stands alone at position i of text on
// side dir (-1 before it, 1 after it): no ASCII letter or digit there (a
// letter of another script, as 年 in 1957年, does not join a date), and no
// date's separator ("-", "/", ".") with a digit beyond it, which would make
// it part of a longer date.
func dateEdge(text []byte, i, dir int) bool {
	r, size, ok := runeAt(text, i, dir)
	if !ok {
		return true
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
