package engine

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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

// minCleanLength is the shortest value clean removes from free text: a
// shorter one (two letters, an initial) matches inside ordinary words and
// would cut text that identifies nothing. The check after still searches
// for the values clean leaves.
const minCleanLength = 3

// maxCleanTerms bounds the terms clean removes from one value, and
// maxCleanValue the value it reads, so a file with thousands of names and
// a long text cannot make one element cost minutes. The longest terms are
// kept; a value past the bound is refused (CauseBadValue).
const (
	maxCleanTerms = 1024
	maxCleanValue = 64 << 10
)

// cleanText removes from s every needle of at least minCleanLength
// characters, in any letter case, and each part of a person's name. What is
// removed is replaced by nothing; runs of spaces left are kept, since the
// text's shape says nothing of what was there.
func cleanText(s string, needles []Needle) string {
	for _, n := range cleanTerms(needles) {
		s = removeFold(s, n)
	}
	return s
}

// cleanTerms returns the terms clean removes, longest first, so a whole
// name goes before its parts.
func cleanTerms(needles []Needle) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if utf8.RuneCountInString(v) < minCleanLength || seen[strings.ToLower(v)] {
			return
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	for _, n := range needles {
		add(n.Value)
		if n.Kind == NeedleName {
			for _, p := range nameParts(n.Value) {
				add(p)
			}
		}
	}
	// Longest first, then by text, so the result is the same every run.
	slices.SortFunc(out, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	if len(out) > maxCleanTerms {
		out = out[:maxCleanTerms]
	}
	return out
}

// nameParts returns the parts of a person's name (PS3.5 6.2.1): its
// component groups split by "=", each split into its components by "^".
func nameParts(pn string) []string {
	var out []string
	for _, group := range strings.Split(pn, "=") {
		for _, c := range strings.Split(group, "^") {
			if c = strings.TrimSpace(c); c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

// removeFold removes every occurrence of term from s, matching letters in
// any case. Where lowering keeps every byte offset (ASCII, and most text),
// it searches the lowered text; otherwise it compares rune by rune with
// Unicode simple folding (strings.EqualFold).
func removeFold(s, term string) string {
	ls, lt := strings.ToLower(s), strings.ToLower(term)
	if len(ls) == len(s) && len(lt) == len(term) {
		var out strings.Builder
		for {
			i := strings.Index(ls, lt)
			if i < 0 {
				out.WriteString(s)
				return out.String()
			}
			out.WriteString(s[:i])
			s, ls = s[i+len(term):], ls[i+len(term):]
		}
	}
	n := utf8.RuneCountInString(term)
	var out strings.Builder
	for i := 0; i < len(s); {
		// The byte length of the next n runes of s from i.
		j, k := i, 0
		for k < n && j < len(s) {
			_, size := utf8.DecodeRuneInString(s[j:])
			j += size
			k++
		}
		if k == n && strings.EqualFold(s[i:j], term) {
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		out.WriteString(s[i : i+size])
		i += size
	}
	return out.String()
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
