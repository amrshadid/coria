package engine

import (
	"slices"
	"testing"
)

// TestDateFormsAreTheWrittenOnes guards the written forms of a date that
// clean removes and the check after searches for: without leading zeros,
// with a two-digit year, the month's name with a dot and the day as an
// ordinal, the Japanese form; bounded where they are ambiguous; and a
// year and month only at their precision.
func TestDateFormsAreTheWrittenOnes(t *testing.T) {
	forms, partial := DateForms("20240305101500")
	if partial {
		t.Fatal("a date and time taken as partial")
	}
	got := map[string]bool{}
	for _, f := range forms {
		got[f.Text] = f.Bounded
	}
	for text, bounded := range map[string]bool{
		"20240305": false, "2024-03-05": false, "05/03/2024": false,
		"3/5/2024": true, "5.3.2024": true, "2024-3-5": true, "03/05/24": true, "Mar. 5, 2024": true,
		"5th March 2024": true, "March 5th, 2024": true, "2024年3月5日": true, "5Mar2024": true,
	} {
		b, ok := got[text]
		if !ok || b != bounded {
			t.Errorf("%s: present %v, bounded %v", text, ok, b)
		}
	}
	ym, partial := DateForms("195703")
	if !partial || !slices.ContainsFunc(ym, func(f WrittenForm) bool { return f.Text == "1957年3月" && f.Bounded }) {
		t.Errorf("year and month: %v %v", partial, ym)
	}
	if f, _ := DateForms("20241305"); f != nil {
		t.Errorf("a month 13: %v", f)
	}
}

// TestFoldTakesFullWidthAsASCII guards that full-width letters and digits,
// written in East Asian text, fold to their ASCII ones, and case is
// folded beyond ASCII.
func TestFoldTakesFullWidthAsASCII(t *testing.T) {
	for in, want := range map[rune]rune{'ｄ': 'D', 'Ｄ': 'D', '１': '1', 'é': 'É', 'K': 'K', '王': '王'} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCleanRemovesTheNewWrittenForms guards clean on the written forms
// people use: a birth date without leading zeros, with the month's name
// and a dot, in Japanese, and a year beside a Japanese letter; full-width
// letters of a name.
func TestCleanRemovesTheNewWrittenForms(t *testing.T) {
	terms, ok := cleanTerms([]Needle{{Kind: NeedleDate, Value: "20240305"}, {Kind: NeedleDate, Value: "1957"},
		{Kind: NeedleName, Value: "DOE^JOHN"}})
	if !ok {
		t.Fatal("too many terms")
	}
	c := newCleaner(terms, maxCleanWork)
	for in, want := range map[string]string{
		"on 3/5/2024.":       "on .",
		"seen Mar. 5, 2024":  "seen ",
		"2024年3月5日に":         "に",
		"1957年生まれ":           "年生まれ",
		"ＤＯＥ ＪＯＨＮ came":      " came",
		"13/5/2024 stays":    "13/5/2024 stays",
		"born 1957-03 stays": "born 1957-03 stays",
	} {
		got, err := c.clean(t.Context(), in)
		if err != nil || got != want {
			t.Errorf("clean(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
