package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/valuerep"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// Bounds of a data set the engine processes. A file past one is refused
// whole, never processed in part.
const (
	// MaxDepth is how deep sequences may nest (CauseTooDeep).
	MaxDepth = 64
	// MaxElements is how many elements a data set may hold, sequences'
	// items included (CauseTooLarge).
	MaxElements = 1 << 20
	// MaxValueLength is the longest value an action reads (CauseBadValue):
	// keep, remove and empty read no value and take any.
	MaxValueLength = 1 << 20
	// MinKeyLength is the shortest key the engine takes: the output size
	// of SHA-256, the least RFC 2104 section 3 recommends for HMAC, below
	// which an identifier could be found from its code by trying them.
	MinKeyLength = 32
	// maxMethodLength is the bound of De-identification Method (0012,0063),
	// an LO value (PS3.5 6.2).
	maxMethodLength = 64
)

// ErrPixelRules is returned by New for a profile with pixel rules: the
// engine changes no pixels, and a profile that says how pixels are cleaned
// must not run where they would silently stay as they are.
var ErrPixelRules = errors.New("engine: the profile has pixel rules, and this engine does not apply them; pixel data is never changed here")

// Keys are a tenant's secrets the actions are keyed with, as bytes; the
// engine keeps its own copies.
type Keys struct {
	// UID keys UID remapping (RemapUID).
	UID []byte
	// Date keys the subjects' date shifts (ShiftDays).
	Date []byte
	// Hash keys the hash and lookup actions and templates' placeholders:
	// the tenant's naming key, so a hashed Patient ID equals its subject's
	// label code.
	Hash []byte
}

// Reception is what a file's reception says of it, which conditions read.
// An empty field is absent.
type Reception struct {
	// Receiver is the receiver's name.
	Receiver string
	// CallingAETitle is the sender's AE title (PS3.8 9.3.2).
	CallingAETitle string
	// CalledAETitle is the AE title the sender called.
	CalledAETitle string
}

// Lookups reaches a tenant's lookup tables. A table is keyed by the keyed
// hash of a value (the naming rule's {#...} code of it), never the value.
type Lookups interface {
	// Code returns the code table holds for key, and whether it holds one.
	Code(ctx context.Context, table, key string) (code string, found bool, err error)
	// Assign returns the code table holds for key, giving key the table's
	// next code first when it holds none.
	Assign(ctx context.Context, table, key string) (string, error)
}

// Options are what an Engine takes besides its profile and keys.
type Options struct {
	// Lookups reaches the lookup tables; nil when the profile has no
	// lookup action, and Apply fails for one that does.
	Lookups Lookups
	// Types gives attributes' Types in their IODs, for the action that
	// removes an element unless its IOD requires it; nil for the DICOM
	// library's IOD tables (IODTypes).
	Types Types
	// Method, when set, is written into De-identification Method
	// (0012,0063): the profile and version that made the file, as the
	// caller names them. At most 64 characters of the default repertoire.
	Method string
}

// Engine applies one expanded profile version under one tenant's keys. It
// does no I/O but through Lookups, reads no clock and no random source,
// and is never changed after New, so one Engine serves every file at once
// and the same file always comes out the same.
type Engine struct {
	dec     Decider
	keys    Keys
	lookups Lookups
	types   Types
	method  string
	// received are the top-level elements templates read, as received.
	received []uint32
}

// New compiles an expanded profile, as profile.Expand made it or a stored
// version holds it, for Apply. It refuses a profile with pixel rules
// (ErrPixelRules), keys shorter than MinKeyLength, and a method that is not
// an LO value of the default repertoire.
func New(e *v1.ExpandedProfile, keys Keys, opts Options) (*Engine, error) {
	if len(e.GetPixelRules()) > 0 {
		return nil, ErrPixelRules
	}
	dec, err := newProgramDecider(e)
	if err != nil {
		return nil, err
	}
	return newEngine(dec, keys, opts)
}

// newEngine makes an Engine over any Decider.
func newEngine(dec Decider, keys Keys, opts Options) (*Engine, error) {
	for _, k := range []struct {
		name string
		key  []byte
	}{{"uid", keys.UID}, {"date", keys.Date}, {"hash", keys.Hash}} {
		if len(k.key) < MinKeyLength {
			return nil, fmt.Errorf("engine: the %s key is shorter than %d bytes", k.name, MinKeyLength)
		}
	}
	if opts.Method != "" {
		if len(opts.Method) > maxMethodLength || !isASCII(opts.Method) || valuerep.ValidateValue("LO", opts.Method) != nil {
			return nil, fmt.Errorf("engine: the method is not an LO value of at most %d characters of the default repertoire", maxMethodLength)
		}
	}
	en := &Engine{
		dec:     dec,
		keys:    Keys{UID: clone(keys.UID), Date: clone(keys.Date), Hash: clone(keys.Hash)},
		lookups: opts.Lookups,
		types:   opts.Types,
		method:  opts.Method,
	}
	if en.types == nil {
		en.types = defaultTypes
	}
	seen := map[uint32]bool{}
	for _, tmpl := range dec.Templates() {
		for _, t := range placeholders(tmpl) {
			if !seen[t] {
				seen[t] = true
				en.received = append(en.received, t)
			}
		}
	}
	return en, nil
}

// defaultTypes is the IOD Types every Engine without its own shares, so
// each answer is worked out once per process.
var defaultTypes = &IODTypes{}

// Apply de-identifies ds in place by the profile, and returns what it did
// to each element. Every element is visited: the top level, every item of
// every sequence, private blocks; each takes the action of the rule the
// profile's precedence chooses, or the default. Then the elements set
// rules make are added, and the de-identification is recorded in the data
// set: Patient Identity Removed (0012,0062) YES, De-identification Method
// (0012,0063) when Options.Method is set, Longitudinal Temporal Information
// Modified (0028,0303) MODIFIED when a date was shifted (PS3.15 E.1.1,
// E.3.6). Pixel data is never changed.
//
// Everything is worked out before the data set is changed. A file the
// profile cannot process returns a *Failure and leaves ds as it was, never
// half processed; any other error (a lookup table that does not answer, a
// canceled ctx) says nothing of the file and leaves it as it was too.
func (en *Engine) Apply(ctx context.Context, ds *dataset.Dataset, rec Reception) (*Record, error) {
	if ds == nil {
		return nil, errors.New("engine: no data set")
	}
	r := &run{en: en, ctx: ctx, top: ds, record: &Record{PixelsUntouched: true}}
	r.facts = readFacts(ds, rec)
	r.received = map[uint32]string{}
	for _, t := range en.received {
		if v, ok := topText(ds, t); ok {
			r.received[t] = v
		}
	}
	if err := r.walk(ds, nil, "", scope{charset: charsetOf(ds, charsetAbsent)}); err != nil {
		return nil, err
	}
	if err := r.planSets(); err != nil {
		return nil, err
	}
	if err := r.finishCleans(); err != nil {
		return nil, err
	}
	r.planMethod()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.apply()
	return r.record, nil
}

// clone copies a key.
func clone(b []byte) []byte { return append([]byte(nil), b...) }
