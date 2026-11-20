package check

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/amrshadid/go-dicom/charset"

	"github.com/amrshadid/coria/internal/deid/engine"
)

// mode is how a form's edges are matched.
type mode int

const (
	// modePlain: a form beginning with a digit is not matched after a
	// digit, one ending with a digit not before one.
	modePlain mode = iota
	// modeFullDate: as plain, but digits may follow, as a time follows a
	// date in a DT value.
	modeFullDate
	// modeBounded: matched only standing alone as a date
	// (engine.WrittenForm.Bounded).
	modeBounded
	// modeUID: as plain, and not followed by a dot and a digit nor
	// preceded by a digit and a dot, which would make it part of a longer
	// UID.
	modeUID
)

// form is one written form of a needle.
type form struct {
	// text is the form as written, for encoding into raw bytes.
	text string
	// folded is the form folded (foldText), for the text search.
	folded string
	kind   Kind
	mode   mode
	// unspaced is set for a form in a script written without spaces,
	// searched for in the raw bytes at any length.
	unspaced bool
}

// formSet is the forms of a file's needles.
type formSet struct {
	forms   []form
	skipped int
}

// forms returns the distinct forms of needles, refusing more needles than
// MaxNeedles or one longer than MaxNeedleBytes.
func (c *Checker) forms(needles engine.Needles) (formSet, error) {
	var set formSet
	if len(needles) > c.opts.MaxNeedles {
		return set, &Refusal{Cause: CauseTooManyNeedles}
	}
	type key struct {
		folded string
		mode   mode
	}
	seen := map[key]bool{}
	skippedSeen := map[string]bool{}
	add := func(text string, kind Kind, m mode) {
		text = strings.Trim(text, " \x00")
		if text == "" {
			return
		}
		folded := foldText(text)
		n := utf8.RuneCountInString(folded)
		un := engine.Unspaced(text)
		if (un && n < c.opts.MinUnspaced) || (!un && n < c.opts.MinLength) {
			if !skippedSeen[folded] {
				skippedSeen[folded] = true
				set.skipped++
			}
			return
		}
		k := key{folded, m}
		if seen[k] {
			return
		}
		seen[k] = true
		set.forms = append(set.forms, form{text: text, folded: folded, kind: kind, mode: m, unspaced: un})
	}
	for _, n := range needles {
		if len(n.Value) > c.opts.MaxNeedleBytes {
			return formSet{}, &Refusal{Cause: CauseTooManyNeedles}
		}
		kind := kindOf(n.Kind)
		values := strings.Split(n.Value, `\`)
		if len(values) > 1 {
			add(n.Value, kind, modePlain)
		}
		for _, v := range values {
			v = strings.Trim(v, " \x00")
			if v == "" {
				continue
			}
			switch n.Kind {
			case engine.NeedleUID:
				if !engine.Registered(v) {
					add(v, kind, modeUID)
				}
			case engine.NeedleName:
				for _, f := range engine.NameForms(v) {
					add(f, kind, modePlain)
				}
			case engine.NeedleDate:
				forms, partial := engine.DateForms(v)
				if !partial {
					add(v, kind, modePlain)
				}
				for _, f := range forms {
					m := modeFullDate
					if f.Bounded {
						m = modeBounded
					}
					add(f.Text, kind, m)
				}
			default:
				add(v, kind, modePlain)
			}
		}
	}
	return set, nil
}

// foldText returns s with every rune folded (engine.Fold: case, and
// full-width ASCII) and every run of white space made one space, so
// "MRN  778812" and "MRN 778812" are one text. Bytes that are not UTF-8
// become U+FFFD, which no form holds.
func foldText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(engine.Fold(r))
	}
	return b.String()
}

// sourceEncodings returns the encodings of the character set a file was
// received in, as the DICOM library names them; the default repertoire
// read as ISO 8859-1 when it declared none, as the library reads such a
// file (PS3.5 6.1.2.3).
func sourceEncodings(characterSet *string) []string {
	cs := ""
	if characterSet != nil {
		cs = *characterSet
	}
	enc, err := charset.ConvertEncodings(strings.Split(cs, `\`))
	if err != nil || len(enc) == 0 {
		return []string{charset.DefaultEncoding}
	}
	return enc
}

// rawForm is one byte form of a form, for the raw search.
type rawForm struct {
	bytes []byte
	// short is set for an encoding under MinLength bytes (a name of one
	// or two characters of a script written without spaces), matched only
	// outside the values of standard binary elements, whose numbers can
	// spell any two or three bytes by chance.
	short bool
}

// encodings returns the raw forms of f: its text in upper case, lower case
// and as written (the raw bytes' ASCII letters are upper-cased, but no
// other letter: "müller" and "MÜLLER" are two byte strings), each in UTF-8
// and in the source encodings, the whole list (which writes ISO 2022
// escapes) and each encoding alone, escape sequences taken out, ASCII
// letters in upper case. A form under minBytes is kept only when it is
// unspaced, as short.
func (f form) encodings(ctx context.Context, source []string, minBytes int) []rawForm {
	var out []rawForm
	seen := map[string]bool{}
	put := func(b []byte) {
		b = stripEscapes(b)
		if len(b) == 0 || (len(b) < minBytes && !f.unspaced) {
			return
		}
		upperASCII(b)
		if !seen[string(b)] {
			seen[string(b)] = true
			out = append(out, rawForm{bytes: b, short: len(b) < minBytes})
		}
	}
	for _, v := range []string{strings.ToUpper(f.text), strings.ToLower(f.text), f.text} {
		put([]byte(v))
		if b, err := charset.EncodeStringWithContext(ctx, v, source); err == nil {
			put(b)
		}
		if len(source) > 1 {
			for _, e := range source {
				if b, err := charset.EncodeStringWithContext(ctx, v, []string{e}); err == nil {
					put(b)
				}
			}
		}
	}
	return out
}

// stripEscapes removes ISO 2022 escape sequences (ECMA-35 13: ESC,
// intermediate bytes 0x20-0x2F, a final byte 0x30-0x7E) from b, so the
// bytes of a value written in a code extension match between the escapes
// the file wrote around a longer run (PS3.5 6.1.2.5.3).
func stripEscapes(b []byte) []byte {
	out := b[:0:0]
	for i := 0; i < len(b); i++ {
		if b[i] != 0x1B {
			out = append(out, b[i])
			continue
		}
		j := i + 1
		for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2F {
			j++
		}
		if j < len(b) && b[j] >= 0x30 && b[j] <= 0x7E {
			i = j
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// upperASCII changes the ASCII lower-case letters of b to upper case, in
// place.
func upperASCII(b []byte) {
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
}
