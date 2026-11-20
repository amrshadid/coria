package profile

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// baseName is the rule of a base's name: a family, a name and an edition,
// such as "annex-e/basic:2026c". A new edition of a base is a new name, so
// a stored version that names one never changes meaning.
var baseName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}/[a-z][a-z0-9-]{0,47}:[a-z0-9][a-z0-9.]{0,15}$`)

// Base is a profile the server ships, which a document may name among its
// bases.
type Base struct {
	// Name is the base's name, such as "annex-e/basic:2026c".
	Name string
	// Description says what the base does, for a person.
	Description string
	// Document is the base as a document; its bases name other bases of the
	// same catalog. Nil for a base that is not available.
	Document *v1.ProfileDocument
	// Unavailable, when set, says why this build cannot expand the base: a
	// document that names it is refused with this reason.
	Unavailable string
}

// Catalog is a set of bases, checked whole when it is made: every base's
// document is valid, every base it names is in the catalog, and no base
// names itself through others. It is never changed after it is made, so
// one catalog is shared by every expansion.
type Catalog struct {
	bases map[string]*catalogBase
	names []string
}

// catalogBase is a base with its compiled rules, and why it cannot be used
// when it, or a base it names, is unavailable.
type catalogBase struct {
	base   Base
	rules  []rule
	pixels []pixelRule
	reason string
}

// NewCatalog checks bases and returns them as a catalog.
func NewCatalog(bases ...Base) (*Catalog, error) {
	c := &Catalog{bases: map[string]*catalogBase{}}
	for _, b := range bases {
		if !baseName.MatchString(b.Name) {
			return nil, fmt.Errorf("profile: %q is not a base's name: family/name:edition", b.Name)
		}
		if _, dup := c.bases[b.Name]; dup {
			return nil, fmt.Errorf("profile: the base %s is given twice", b.Name)
		}
		cb := &catalogBase{base: b, reason: b.Unavailable}
		switch {
		case b.Unavailable == "" && b.Document == nil:
			return nil, fmt.Errorf("profile: the base %s has neither a document nor a reason it is unavailable", b.Name)
		case b.Unavailable != "" && b.Document != nil:
			return nil, fmt.Errorf("profile: the base %s has a document and is unavailable", b.Name)
		case b.Document != nil:
			var list Problems
			cb.rules, cb.pixels = checkDocument(&list, b.Document)
			if len(list) > 0 {
				return nil, fmt.Errorf("profile: the base %s: %w", b.Name, list)
			}
			if len(b.Document.GetBases()) > MaxBases {
				return nil, fmt.Errorf("profile: the base %s names more than %d bases", b.Name, MaxBases)
			}
		}
		c.bases[b.Name] = cb
		c.names = append(c.names, b.Name)
	}
	slices.Sort(c.names)
	for _, n := range c.names {
		if err := c.resolve(c.bases[n], nil); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// errCycle is the cause of a catalog whose bases name each other in a
// circle.
var errCycle = errors.New("profile: bases name each other in a circle")

// resolve checks that every base cb names exists and that none leads back
// to a base on path, and sets cb's reason when a base it names cannot be
// used.
func (c *Catalog) resolve(cb *catalogBase, path []string) error {
	if slices.Contains(path, cb.base.Name) {
		return fmt.Errorf("%w: %v", errCycle, append(path, cb.base.Name))
	}
	path = append(path, cb.base.Name)
	for _, n := range cb.base.Document.GetBases() {
		inner, ok := c.bases[n]
		if !ok {
			return fmt.Errorf("profile: the base %s names %s, which the catalog does not hold", cb.base.Name, n)
		}
		if err := c.resolve(inner, path); err != nil {
			return err
		}
		if inner.reason != "" && cb.reason == "" {
			cb.reason = fmt.Sprintf("it is built on %s, which is not available: %s", n, inner.reason)
		}
	}
	return nil
}

// Names returns the names of the catalog's bases, in byte order.
func (c *Catalog) Names() []string { return slices.Clone(c.names) }

// Lookup returns a base by name, whether the catalog holds it, and why a
// document may not name it: empty when it may, else the reason it, or a
// base it is built on, is unavailable.
func (c *Catalog) Lookup(name string) (b Base, unavailable string, found bool) {
	cb, ok := c.bases[name]
	if !ok {
		return Base{}, "", false
	}
	return cb.base, cb.reason, true
}

// Proto returns the catalog as the API answers it, by name in byte order.
func (c *Catalog) Proto() []*v1.ProfileBase {
	out := make([]*v1.ProfileBase, 0, len(c.names))
	for _, n := range c.names {
		cb := c.bases[n]
		pb := &v1.ProfileBase{Name: n, Available: cb.reason == "", UnavailableReason: cb.reason, Description: cb.base.Description}
		if cb.reason == "" {
			pb.Document = cloneDocument(cb.base.Document)
		}
		out = append(out, pb)
	}
	return out
}

// The bases of PS3.15 Annex E: the basic profile and its options (PS3.15
// E.3, Table E.1-1). They are generated from the standard's tables, which
// the DICOM library holds; until it exports them, each is known by name and
// refused with this reason rather than built from a copy of the standard
// kept here, which would drift from the library's.
const (
	// AnnexEEdition is the edition of PS3.15 the Annex E bases follow.
	AnnexEEdition = "2026c"
	// reasonTable is why a base from Table E.1-1 is not available.
	reasonTable = "its rules are PS3.15 Table E.1-1, which this build does not carry yet"
	// reasonSafePrivate is why the safe private option is not available.
	reasonSafePrivate = "its rules are PS3.15 Table E.3.10-1, which this build does not carry yet"
	// reasonPixels is why the options that change pixels, which have no
	// column in Table E.1-1, are not available as bases.
	reasonPixels = "it changes pixels, not attributes, and is recorded in the de-identification method, which this build does not write yet"
)

// annexE lists each Annex E base: its name without the edition, what it
// is, and why it is not available.
var annexE = []struct{ name, description, reason string }{
	{"basic", "PS3.15 E.2, the Basic Application Level Confidentiality Profile: the actions of Table E.1-1's basic column.", reasonTable},
	{"clean-pixel-data", "PS3.15 E.3.1, Clean Pixel Data: burned-in identifying information is removed from the pixels.", reasonPixels},
	{"clean-recognizable-visual-features", "PS3.15 E.3.2, Clean Recognizable Visual Features.", reasonPixels},
	{"clean-graphics", "PS3.15 E.3.3, Clean Graphics: identifying text in graphics and overlays is removed.", reasonTable},
	{"clean-structured-content", "PS3.15 E.3.4, Clean Structured Content: identifying content items of structured reports are cleaned.", reasonTable},
	{"clean-descriptors", "PS3.15 E.3.5, Clean Descriptors: free-text descriptions are cleaned rather than kept or removed.", reasonTable},
	{"retain-longitudinal-full-dates", "PS3.15 E.3.6, Retain Longitudinal Temporal Information with Full Dates.", reasonTable},
	{"retain-longitudinal-modified-dates", "PS3.15 E.3.6, Retain Longitudinal Temporal Information with Modified Dates: dates are shifted, not removed.", reasonTable},
	{"retain-patient-characteristics", "PS3.15 E.3.7, Retain Patient Characteristics: sex, age, size, weight and the like are kept.", reasonTable},
	{"retain-device-identity", "PS3.15 E.3.8, Retain Device Identity.", reasonTable},
	{"retain-uids", "PS3.15 E.3.9, Retain UIDs: original UIDs are kept.", reasonTable},
	{"retain-safe-private", "PS3.15 E.3.10, Retain Safe Private: the private attributes Table E.3.10-1 lists as safe are kept.", reasonSafePrivate},
	{"retain-institution-identity", "PS3.15 E.3.11, Retain Institution Identity.", reasonTable},
}

// AnnexE returns the name of an Annex E base of this edition, such as
// AnnexE("basic") for "annex-e/basic:2026c".
func AnnexE(name string) string { return "annex-e/" + name + ":" + AnnexEEdition }

// shippedBases is every base the binary ships.
func shippedBases() []Base {
	var out []Base
	for _, b := range annexE {
		out = append(out, Base{Name: AnnexE(b.name), Description: b.description, Unavailable: b.reason})
	}
	// The data set as The Cancer Imaging Archive publishes it: the basic
	// profile with its six options, dates shifted so intervals between
	// studies survive.
	out = append(out, Base{
		Name:        "coria/longitudinal:1",
		Description: "The basic profile with clean pixel data, clean descriptors, retain longitudinal temporal information with modified dates, retain patient characteristics, retain device identity and retain safe private: dates are shifted per subject, so intervals between studies survive.",
		Document: &v1.ProfileDocument{Bases: []string{
			AnnexE("basic"),
			AnnexE("clean-pixel-data"),
			AnnexE("clean-descriptors"),
			AnnexE("retain-longitudinal-modified-dates"),
			AnnexE("retain-patient-characteristics"),
			AnnexE("retain-device-identity"),
			AnnexE("retain-safe-private"),
		}},
	})
	// The least that leaves a file useful: the cleaning options, every
	// date reduced to its year, and every element no rule names removed.
	// A VR rule is the least specific, so a date the bases name keeps the
	// bases' action; a date they leave is reduced to its year.
	out = append(out, Base{
		Name:        "coria/strict:1",
		Description: "The basic profile with clean descriptors, clean structured content and clean graphics; dates the bases leave are reduced to their year, private elements are removed, and an element no rule names is removed.",
		Document: &v1.ProfileDocument{
			Bases: []string{
				AnnexE("basic"),
				AnnexE("clean-descriptors"),
				AnnexE("clean-structured-content"),
				AnnexE("clean-graphics"),
			},
			Unnamed: v1.UnnamedElements_UNNAMED_ELEMENTS_REMOVE,
			Rules: []*v1.ProfileRule{
				{Name: "dates-to-year", Selector: &v1.ProfileSelector{Kind: &v1.ProfileSelector_Vr{Vr: "DA"}}, Action: &v1.ProfileAction{Kind: &v1.ProfileAction_Generalize{Generalize: &v1.GeneralizeAction{}}}, Reason: "a date the bases leave is reduced to its year"},
				{Name: "datetimes-to-year", Selector: &v1.ProfileSelector{Kind: &v1.ProfileSelector_Vr{Vr: "DT"}}, Action: &v1.ProfileAction{Kind: &v1.ProfileAction_Generalize{Generalize: &v1.GeneralizeAction{}}}, Reason: "a date and time the bases leave is reduced to its year"},
			},
		},
	})
	return out
}

// Shipped returns the catalog of the bases this binary ships.
var Shipped = sync.OnceValue(func() *Catalog {
	c, err := NewCatalog(shippedBases()...)
	if err != nil {
		// The shipped bases are fixed in the binary and checked by the
		// package's tests; a failure here is a defect of the build.
		panic(err)
	}
	return c
})
