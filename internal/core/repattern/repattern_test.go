package repattern

import (
	"strings"
	"testing"
)

// TestCompile guards the bounds and the whole-value match: a pattern that
// would escape its anchors, a long text, a large repetition and a large
// class are refused; a good one matches only a whole value.
func TestCompile(t *testing.T) {
	re, cost, err := Compile(`CT|MR`)
	if err != nil || cost == 0 {
		t.Fatalf("Compile: %v, cost %d", err, cost)
	}
	for v, want := range map[string]bool{"CT": true, "MR": true, "CTX": false, "XMR": false, "": false} {
		if re.MatchString(v) != want {
			t.Errorf("%q: match %v, want %v", v, !want, want)
		}
	}
	for _, bad := range []string{`a)|(b`, `(`, strings.Repeat("a", MaxLength+1), `x{101}`, `\pL{100}`} {
		if _, _, err := Compile(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
