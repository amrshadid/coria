package profile_test

import (
	"strings"
	"testing"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/deid/profile"
)

// TestShippedCatalog guards the bases the binary ships: the catalog is
// made without error; every base of PS3.15 Annex E is known by name, of
// this edition, and refused with its reason rather than built from a copy
// of the standard; the composite bases are refused because they are built
// on them; and a document naming any of them is refused, naming the base.
func TestShippedCatalog(t *testing.T) {
	cat := profile.Shipped()
	names := cat.Names()
	want := []string{
		"annex-e/basic:2026c", "annex-e/clean-descriptors:2026c", "annex-e/clean-graphics:2026c",
		"annex-e/clean-pixel-data:2026c", "annex-e/clean-recognizable-visual-features:2026c",
		"annex-e/clean-structured-content:2026c", "annex-e/retain-device-identity:2026c",
		"annex-e/retain-institution-identity:2026c", "annex-e/retain-longitudinal-full-dates:2026c",
		"annex-e/retain-longitudinal-modified-dates:2026c", "annex-e/retain-patient-characteristics:2026c",
		"annex-e/retain-safe-private:2026c", "annex-e/retain-uids:2026c",
		"coria/longitudinal:1", "coria/strict:1",
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("bases %v, want %v", names, want)
	}
	for _, n := range names {
		b, unavailable, found := cat.Lookup(n)
		if !found || unavailable == "" || b.Description == "" {
			t.Errorf("%s: found %v, unavailable %q, description %q", n, found, unavailable, b.Description)
		}
		if strings.HasPrefix(n, "annex-e/") && b.Document != nil {
			t.Errorf("%s has rules of its own, where the standard's tables belong", n)
		}
		_, problems := profile.Expand(&v1.ProfileDocument{Bases: []string{n}}, cat)
		if len(problems) != 1 || problems[0].Code != profile.CodeBaseUnavailable || !strings.Contains(problems[0].Message, n) {
			t.Errorf("a document naming %s: %v", n, problems)
		}
	}
	if _, unavailable, _ := cat.Lookup("coria/strict:1"); !strings.Contains(unavailable, "annex-e/basic:2026c") {
		t.Errorf("a composite's reason does not name the base it waits for: %q", unavailable)
	}
	listed := cat.Proto()
	if len(listed) != len(want) {
		t.Fatalf("%d bases listed", len(listed))
	}
	for _, b := range listed {
		if b.GetAvailable() || b.GetUnavailableReason() == "" || b.GetDocument() != nil {
			t.Errorf("listed %v", b)
		}
	}
	// A document of its own rules needs no base.
	if _, problems := profile.Expand(&v1.ProfileDocument{Rules: []*v1.ProfileRule{rl("", kw("PatientName"), remove())}}, cat); len(problems) > 0 {
		t.Errorf("own rules over the shipped catalog: %v", problems)
	}
}

// TestShippedCompositesAreValid guards the composite bases' own rules and
// bases: with Annex E bases that are available (empty here, standing in for
// the tables), each composite expands, so the composites are ready the day
// the tables are.
func TestShippedCompositesAreValid(t *testing.T) {
	var bases []profile.Base
	for _, n := range profile.Shipped().Names() {
		b, _, _ := profile.Shipped().Lookup(n)
		if strings.HasPrefix(n, "annex-e/") {
			b = profile.Base{Name: n, Document: &v1.ProfileDocument{}}
		}
		bases = append(bases, b)
	}
	cat, err := profile.NewCatalog(bases...)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"coria/longitudinal:1", "coria/strict:1"} {
		e := mustExpand(t, &v1.ProfileDocument{Bases: []string{n}}, cat)
		if e.GetBases()[0] != profile.AnnexE("basic") || e.GetBases()[len(e.GetBases())-1] != n {
			t.Errorf("%s expands over %v", n, e.GetBases())
		}
	}
	strict := mustExpand(t, &v1.ProfileDocument{Bases: []string{"coria/strict:1"}}, cat)
	if strict.GetUnnamed() != v1.UnnamedElements_UNNAMED_ELEMENTS_REMOVE || len(strict.GetRules()) != 2 {
		t.Errorf("strict: %v", strict)
	}
}

// TestNewCatalogRefuses guards what a catalog refuses: a malformed or
// repeated name, a base with neither rules nor a reason, a base naming one
// the catalog does not hold, bases naming each other in a circle, and a
// base whose own document has a problem.
func TestNewCatalogRefuses(t *testing.T) {
	doc := func(bases ...string) *v1.ProfileDocument { return &v1.ProfileDocument{Bases: bases} }
	for _, c := range []struct {
		name  string
		bases []profile.Base
	}{
		{"malformed name", []profile.Base{{Name: "basic", Document: doc()}}},
		{"name without edition", []profile.Base{{Name: "test/basic", Document: doc()}}},
		{"repeated name", []profile.Base{{Name: "test/a:1", Document: doc()}, {Name: "test/a:1", Document: doc()}}},
		{"neither rules nor reason", []profile.Base{{Name: "test/a:1"}}},
		{"rules and reason", []profile.Base{{Name: "test/a:1", Document: doc(), Unavailable: "why"}}},
		{"unknown inner base", []profile.Base{{Name: "test/a:1", Document: doc("test/b:1")}}},
		{"circle", []profile.Base{{Name: "test/a:1", Document: doc("test/b:1")}, {Name: "test/b:1", Document: doc("test/a:1")}}},
		{"itself", []profile.Base{{Name: "test/a:1", Document: doc("test/a:1")}}},
		{"bad rule", []profile.Base{{Name: "test/a:1", Document: &v1.ProfileDocument{Rules: []*v1.ProfileRule{rl("", kw("Nobody"), remove())}}}}},
	} {
		if _, err := profile.NewCatalog(c.bases...); err == nil {
			t.Errorf("%s: made", c.name)
		}
	}
}
