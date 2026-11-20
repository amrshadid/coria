package engine

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The written forms of removed values. Clean removes them from free text
// and the check after de-identification searches the result for them,
// from this one list, so what one removes and the other looks for cannot
// drift apart.

// WrittenForm is one way people write a value in free text.
type WrittenForm struct {
	// Text is the form.
	Text string
	// Bounded is set for a form that is ambiguous out of context (a year,
	// a year and month, a two-digit year, a day or month without its
	// leading zero, a month's name): it is taken for the value only where
	// it stands alone as a date, with no ASCII letter or digit next to it
	// and no date's separator ("-", "/", ".") with a digit beyond it, so
	// "2024" is not the year of 2024-03-15 and "5 March 2024" is not inside
	// "15 March 2024". A letter of another script next to it does not
	// count: "1957年" is the year 1957.
	Bounded bool
}

// NameForms returns the forms of a person's name (PS3.5 6.2.1): the value;
// each component group (split by "="); each component of a group (split by
// "^") and each word of a component, since a component often holds more
// than one ("van der Berg", "山田 太郎"); and the family and given names as
// people write them: "JOHN DOE", "DOE JOHN", "DOE, JOHN", "DOE,JOHN",
// "JOHN PAUL DOE" with the middle name, and every component in order with
// spaces.
func NameForms(pn string) []string {
	out := []string{pn}
	for _, group := range strings.Split(pn, "=") {
		var parts []string
		for _, c := range strings.Split(group, "^") {
			if c = strings.TrimSpace(c); c != "" {
				parts = append(parts, c)
			}
		}
		out = append(out, group)
		for _, p := range parts {
			out = append(out, p)
			if words := strings.Fields(p); len(words) > 1 {
				out = append(out, words...)
			}
		}
		if len(parts) >= 2 {
			fam, given := parts[0], parts[1]
			out = append(out, given+" "+fam, fam+" "+given, fam+", "+given, fam+","+given, strings.Join(parts, " "))
			if len(parts) >= 3 {
				out = append(out, given+" "+parts[2]+" "+fam)
			}
		}
	}
	return out
}

// months are the English names of the months, by number from 1.
var months = [...]string{"January", "February", "March", "April", "May", "June", "July",
	"August", "September", "October", "November", "December"}

// DateForms returns the forms the date of a DA or DT value (PS3.5 6.2) is
// written in by people. For a full date, from its first eight digits:
// 20240305, 2024-03-05, 2024/03/05, 2024.03.05, 05/03/2024, 03/05/2024,
// 05.03.2024 and 05-03-2024; and bounded: the same without leading zeros
// (2024-3-5, 3/5/2024, 5.3.2024), with a two-digit year (05/03/24,
// 3/5/24), with the month's name, its three letters and those with a dot,
// the day with and without its zero and as an ordinal ("5 March 2024",
// "March 5th, 2024", "Mar. 5, 2024", "05-Mar-2024", "5MAR2024"), and
// 2024年3月5日. For a value of only a year, or a year and month, partial
// is true and the forms are bounded ones of that precision only (2024;
// 202403, 2024-03, 03/2024, 3/2024, "March 2024", 2024年3月): the value
// itself is then no form of its own. Nil for a value whose leading digits
// are not four, six or eight, or whose month or day is not one.
func DateForms(v string) (forms []WrittenForm, partial bool) {
	n := 0
	for n < len(v) && n < 8 && v[n] >= '0' && v[n] <= '9' {
		n++
	}
	switch {
	case n == 4 && (len(v) == 4 || !isDigitByte(v[4])):
		return []WrittenForm{{Text: v[:4], Bounded: true}}, true
	case n == 6 && (len(v) == 6 || !isDigitByte(v[6])):
		return monthForms(v[:4], v[4:6]), true
	case n == 8:
		return dayForms(v[:4], v[4:6], v[6:8]), false
	}
	return nil, false
}

// monthForms returns the bounded forms of a year and month.
func monthForms(y, m string) []WrittenForm {
	mi, _ := strconv.Atoi(m)
	if mi < 1 || mi > 12 {
		return nil
	}
	mm := strconv.Itoa(mi)
	var out []WrittenForm
	for _, t := range []string{y + m, y + "-" + m, y + "/" + m, y + "." + m, m + "/" + y, m + "." + y, m + "-" + y,
		y + "-" + mm, mm + "/" + y, months[mi-1] + " " + y, months[mi-1][:3] + " " + y, months[mi-1][:3] + ". " + y,
		y + "年" + mm + "月"} {
		out = append(out, WrittenForm{Text: t, Bounded: true})
	}
	return dedupe(out)
}

// dayForms returns the forms of a full date.
func dayForms(y, m, d string) []WrittenForm {
	mi, _ := strconv.Atoi(m)
	di, _ := strconv.Atoi(d)
	if mi < 1 || mi > 12 || di < 1 || di > 31 {
		return nil
	}
	out := []WrittenForm{
		{Text: y + m + d}, {Text: y + "-" + m + "-" + d}, {Text: y + "/" + m + "/" + d}, {Text: y + "." + m + "." + d},
		{Text: d + "/" + m + "/" + y}, {Text: m + "/" + d + "/" + y}, {Text: d + "." + m + "." + y}, {Text: d + "-" + m + "-" + y},
	}
	mm, dd, yy := strconv.Itoa(mi), strconv.Itoa(di), y[2:]
	bounded := []string{
		y + "-" + mm + "-" + dd, y + "/" + mm + "/" + dd, y + "." + mm + "." + dd,
		dd + "/" + mm + "/" + y, mm + "/" + dd + "/" + y, dd + "." + mm + "." + y, dd + "-" + mm + "-" + y,
		d + "/" + m + "/" + yy, m + "/" + d + "/" + yy, d + "." + m + "." + yy, d + "-" + m + "-" + yy,
		dd + "/" + mm + "/" + yy, mm + "/" + dd + "/" + yy, dd + "." + mm + "." + yy,
		y + "年" + mm + "月" + dd + "日",
	}
	full, abbr := months[mi-1], months[mi-1][:3]
	for _, day := range []string{d, dd, dd + ordinal(di)} {
		for _, name := range []string{full, abbr, abbr + "."} {
			bounded = append(bounded, day+" "+name+" "+y, name+" "+day+", "+y, name+" "+day+" "+y)
		}
	}
	for _, day := range []string{d, dd} {
		bounded = append(bounded, day+"-"+abbr+"-"+y, day+abbr+y)
	}
	for _, t := range bounded {
		out = append(out, WrittenForm{Text: t, Bounded: true})
	}
	return dedupe(out)
}

// ordinal returns the English ordinal suffix of a day: st, nd, rd or th.
func ordinal(d int) string {
	switch {
	case d >= 11 && d <= 13:
		return "th"
	case d%10 == 1:
		return "st"
	case d%10 == 2:
		return "nd"
	case d%10 == 3:
		return "rd"
	}
	return "th"
}

// dedupe drops forms whose text an earlier one has, keeping the first.
func dedupe(forms []WrittenForm) []WrittenForm {
	seen := make(map[string]bool, len(forms))
	out := forms[:0]
	for _, f := range forms {
		if !seen[f.Text] {
			seen[f.Text] = true
			out = append(out, f)
		}
	}
	return out
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// Unspaced reports whether s holds a letter of a script written without
// spaces between words, or whose names are a syllable or two (Han,
// Hiragana, Katakana, Hangul): a name part of it is a name at any length.
func Unspaced(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// Fold returns the rune every rune of r's case folding orbit maps to
// (unicode.SimpleFold), its least, after a full-width form of an ASCII
// character (U+FF01 to U+FF5E, written in East Asian text) is taken as
// that character: "K", "k", "ｋ" and the Kelvin sign are one rune, and
// "１２３" is "123". It maps a rune to a rune, so positions in a folded
// text are positions in the text.
func Fold(r rune) rune {
	if r >= 0xFF01 && r <= 0xFF5E {
		r -= 0xFEE0
	}
	if r < utf8.RuneSelf {
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}
	least := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		least = min(least, f)
	}
	return least
}
