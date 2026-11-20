package engine

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/amrshadid/coria/internal/core/naming"
)

// trimValue removes the padding PS3.5 6.2 says is not part of a value: the
// trailing space or NUL that makes its length even, and for the VRs whose
// leading spaces are not significant (AE, CS, DS, IS, LO, SH, the dates and
// times, UI) the leading ones too. It is the trimming the naming rule's
// attributes apply, so a value hashed here and the same value hashed into a
// label give the same code.
func trimValue(vr, s string) string {
	s = strings.TrimRight(s, " \x00")
	switch vr {
	case "AE", "CS", "DS", "IS", "LO", "SH", "DA", "DT", "TM", "UI":
		s = strings.TrimLeft(s, " ")
	}
	return s
}

// hashValue is the keyed hash of a value as the naming rule's {#...}
// placeholders make it (naming.HashText), so a profile's hash of Patient ID
// equals the subject's label code. A CS value holds only upper-case letters
// (PS3.5 6.2), so there the code is in upper case.
func hashValue(key []byte, vr, value string) string {
	h := naming.HashText(key, []byte(value))
	if vr == "CS" {
		return strings.ToUpper(h)
	}
	return h
}

// renderTemplate writes a template of the expanded form: literal text, "{{"
// and "}}" for a brace, and "{#GGGGEEEE}" for the keyed hash of a
// top-level element's value as received, which value gives. A placeholder
// whose element is absent or empty fails: the empty value would hash to one
// code for every file without it.
func renderTemplate(tmpl string, key []byte, value func(uint32) (string, bool)) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(tmpl); i++ {
		switch {
		case strings.HasPrefix(tmpl[i:], "{{"):
			out.WriteByte('{')
			i++
		case strings.HasPrefix(tmpl[i:], "}}"):
			out.WriteByte('}')
			i++
		case strings.HasPrefix(tmpl[i:], "{#"):
			end := strings.IndexByte(tmpl[i:], '}')
			if end != 10 {
				return "", false
			}
			t, err := strconv.ParseUint(tmpl[i+2:i+10], 16, 32)
			if err != nil {
				return "", false
			}
			v, ok := value(uint32(t))
			if !ok || v == "" {
				return "", false
			}
			out.WriteString(naming.HashText(key, []byte(v)))
			i += end
		default:
			out.WriteByte(tmpl[i])
		}
	}
	return out.String(), true
}

// placeholders returns the tags of a template's placeholders.
func placeholders(tmpl string) []uint32 {
	var out []uint32
	for i := 0; i+11 <= len(tmpl); i++ {
		if strings.HasPrefix(tmpl[i:], "{{") {
			i++
			continue
		}
		if strings.HasPrefix(tmpl[i:], "{#") && tmpl[i+10] == '}' {
			if t, err := strconv.ParseUint(tmpl[i+2:i+10], 16, 32); err == nil {
				out = append(out, uint32(t))
			}
		}
	}
	return out
}

// minCleanLength is the shortest value clean removes from free text
// anywhere it appears, in a script that separates words with spaces: a
// shorter one matches inside ordinary words. A name's part of one or two
// letters (an initial, "Al", "Li") is removed too, but only as a whole word.
// In a script written without spaces between words (Han, Hiragana,
// Katakana, and Hangul, whose names are of one to three syllables), every
// value and name part is removed wherever it appears, whatever its length:
// a family name of two characters is a name.
const minCleanLength = 3

// Bounds of clean. maxCleanValue bounds a value clean reads, maxCleanTerms
// the terms of one file, and maxCleanWork the work of all the file's
// cleans together, counted as the characters searched times the characters
// of the terms searched for, each pass: past any of them the file is
// refused (CauseBadValue for a value, CauseTooLarge otherwise), so no file
// can make clean run for minutes, and no term is ever dropped to fit.
const (
	maxCleanValue = 64 << 10
	maxCleanTerms = 4096
	maxCleanWork  = 1 << 27
)

// errCleanTooLarge is a file whose cleans would pass maxCleanWork.
var errCleanTooLarge = errors.New("engine: the cleans of the file pass their bound")

// cleanTerm is one value clean removes, its runes folded (fold).
type cleanTerm struct {
	runes []rune
	// word is set for a term matched only as a whole word: not preceded
	// or followed by a letter or a digit.
	word bool
}

// cleanTerms returns the terms clean removes for a file's needles: each
// value, for a person's name each part (nameParts), and for a date the
// forms people write it in (dateForms), by the lengths minCleanLength
// gives; longest first, then by text, so the result is the
// same every run. ok is false when there are more than maxCleanTerms.
func cleanTerms(needles []Needle) ([]cleanTerm, bool) {
	seen := map[string]bool{}
	var out []cleanTerm
	add := func(v string, part bool) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		runes := foldString(v)
		key := string(runes)
		if seen[key] {
			return
		}
		t := cleanTerm{runes: runes}
		switch {
		case unspaced(runes), len(runes) >= minCleanLength:
		case part:
			t.word = true
		default:
			return
		}
		seen[key] = true
		out = append(out, t)
	}
	for _, n := range needles {
		add(n.Value, false)
		switch n.Kind {
		case NeedleName:
			for _, p := range nameParts(n.Value) {
				add(p, true)
			}
		case NeedleDate:
			for _, f := range dateForms(n.Value) {
				add(f, false)
			}
		}
	}
	slices.SortFunc(out, func(a, b cleanTerm) int {
		if len(a.runes) != len(b.runes) {
			return len(b.runes) - len(a.runes)
		}
		return slices.Compare(a.runes, b.runes)
	})
	return out, len(out) <= maxCleanTerms
}

// nameParts returns the parts of a person's name (PS3.5 6.2.1): its
// component groups split by "=", each split into its components by "^",
// and each component into its words by spaces, since a component often
// holds more than one ("van der Berg", "山田 太郎").
func nameParts(pn string) []string {
	var out []string
	for _, group := range strings.Split(pn, "=") {
		for _, c := range strings.Split(group, "^") {
			if c = strings.TrimSpace(c); c == "" {
				continue
			}
			out = append(out, c)
			if words := strings.Fields(c); len(words) > 1 {
				out = append(out, words...)
			}
		}
	}
	return out
}

// dateForms returns the forms a date of a DA or DT value is commonly
// written in by people, in free text: 2024-03-15, 2024/03/15, 2024.03.15,
// 15/03/2024, 03/15/2024, 15.03.2024 and 15-03-2024; none for a value
// that does not begin with eight digits.
func dateForms(v string) []string {
	if len(v) < 8 {
		return nil
	}
	for i := range 8 {
		if v[i] < '0' || v[i] > '9' {
			return nil
		}
	}
	y, m, d := v[:4], v[4:6], v[6:8]
	return []string{
		y + "-" + m + "-" + d, y + "/" + m + "/" + d, y + "." + m + "." + d,
		d + "/" + m + "/" + y, m + "/" + d + "/" + y, d + "." + m + "." + y, d + "-" + m + "-" + y,
	}
}

// unspaced reports whether a term is in a script written without spaces
// between words, or whose names are a syllable or two (Han, Hiragana,
// Katakana, Hangul): any of its letters is.
func unspaced(runes []rune) bool {
	for _, r := range runes {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// fold returns the one rune of r's case folding orbit (unicode.SimpleFold)
// every rune of it maps to, its least: "K", "k" and the Kelvin sign are one
// rune. It maps a rune to a rune, so positions in a folded text are
// positions in the text.
func fold(r rune) rune {
	least := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		least = min(least, f)
	}
	return least
}

// foldString returns the runes of s, each folded.
func foldString(s string) []rune {
	runes := []rune(s)
	for i, r := range runes {
		runes[i] = fold(r)
	}
	return runes
}

// cleaner removes a file's terms from its free text, within a budget of
// work shared by every clean of the file.
type cleaner struct {
	terms []cleanTerm
	// weight is the characters of every term together: the work of
	// searching one character of text for all of them, at most.
	weight int
	budget int
}

// newCleaner returns a cleaner of terms with a budget of work.
func newCleaner(terms []cleanTerm, budget int) *cleaner {
	c := &cleaner{terms: terms, budget: budget}
	for _, t := range terms {
		c.weight += len(t.runes)
	}
	return c
}

// affords reports whether one pass over n characters of text fits the
// budget, so a file far past it is refused before any work.
func (c *cleaner) affords(n int) bool {
	return c.weight == 0 || n <= c.budget/c.weight
}

// clean removes every term from s, in any letter case, comparing rune by
// rune, so no byte offset of one form is used in another. What is removed
// is replaced by nothing; runs of spaces left are kept, since the text's
// shape says nothing of what was there. Removing a term can join the text
// around it into another occurrence ("PAPAT001T001" holds "PAT001" again
// once it is cut), so the passes repeat until one removes nothing. Every
// pass is charged to the budget; ctx is read each pass.
func (c *cleaner) clean(ctx context.Context, s string) (string, error) {
	if len(c.terms) == 0 {
		return s, nil
	}
	runes := []rune(s)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !c.affords(len(runes)) {
			return "", errCleanTooLarge
		}
		c.budget -= len(runes) * c.weight
		folded := make([]rune, len(runes))
		for i, r := range runes {
			folded[i] = fold(r)
		}
		cut := make([]bool, len(runes))
		found := false
		for _, t := range c.terms {
			m := len(t.runes)
			for i := 0; i+m <= len(folded); i++ {
				if folded[i] != t.runes[0] || !slices.Equal(folded[i:i+m], t.runes) {
					continue
				}
				if t.word && (!wordEdge(folded, i-1) || !wordEdge(folded, i+m)) {
					continue
				}
				for j := i; j < i+m; j++ {
					cut[j] = true
				}
				found = true
			}
		}
		if !found {
			return string(runes), nil
		}
		kept := runes[:0]
		for i, r := range runes {
			if !cut[i] {
				kept = append(kept, r)
			}
		}
		runes = kept
	}
}

// wordEdge reports whether position i of a text is outside a word: before
// its start, past its end, or not a letter or a digit.
func wordEdge(runes []rune, i int) bool {
	return i < 0 || i >= len(runes) || (!unicode.IsLetter(runes[i]) && !unicode.IsDigit(runes[i]))
}

// isASCII reports whether s is only of the default character repertoire's
// bytes, which every character set of PS3.5 6.1 reads the same.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
