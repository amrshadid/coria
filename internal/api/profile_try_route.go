package api

import (
	"context"
	"slices"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/deid/engine"
	"github.com/amrshadid/coria/internal/receive"
	"github.com/amrshadid/coria/internal/tenant"
)

// trialRouting applies a receiver's routes to the files a trial tries, so a
// trial agrees with what the receiver would do: a file its routing would
// not route is held as unrouted, and a file whose route names a profile
// version other than the one tried is tried with that version, under keys
// of its own made for the request, as every trial's are.
type trialRouting struct {
	p          tenant.Profiles
	target     trialTarget
	en         *engine.Engine
	keys       engine.Keys
	unassigned *int
	limit      int64
	// routed are the engines of the route's versions, made once each.
	routed map[tenant.ProfileRef]routedEngine
}

// routedEngine is the engine of a version a route names, with its keys.
type routedEngine struct {
	en   *engine.Engine
	keys engine.Keys
}

// trialFile is how one file is tried after its receiver's routing.
type trialFile struct {
	en       *engine.Engine
	keys     engine.Keys
	route    *receive.TrialRoute
	profile  *tenant.ProfileRef
	unrouted map[string]string
}

// of returns how in is tried: with the profile tried, or the version its
// route names; and what its routing decided.
func (tr *trialRouting) of(ctx context.Context, in trialInput) (trialFile, error) {
	out := trialFile{en: tr.en, keys: tr.keys}
	if in.receiver == nil || in.receiver.Routing.IsZero() {
		return out, nil
	}
	route, ok := receive.RouteTrial(*in.receiver, in.file, tr.limit,
		tenant.Caller{CallingAETitle: in.received.CallingAETitle, CalledAETitle: in.received.CalledAETitle})
	if !ok {
		return out, nil
	}
	out.route, out.unrouted = &route, route.Unrouted
	if route.Route == nil || route.Route.Profile.IsZero() {
		return out, nil
	}
	ref := route.Route.Profile
	if s := tr.target.stored; s != nil && s.Ref() == ref {
		return out, nil
	}
	re, ok := tr.routed[ref]
	if !ok {
		target, _, err := resolveTrial(ctx, tr.p, &v1.TrialProfile{Kind: &v1.TrialProfile_Version{
			Version: &v1.ProfileVersionName{Name: ref.Name, Version: ref.Version}}})
		if err != nil {
			return out, err
		}
		if re.en, re.keys, err = trialEngine(target, tr.unassigned); err != nil {
			return out, err
		}
		tr.routed[ref] = re
	}
	out.en, out.keys, out.profile = re.en, re.keys, &ref
	return out, nil
}

// hold makes t held as unrouted when the routing would not route its file,
// unless a check that runs before the routing at reception (the receiving
// checks, the labels) holds it already: those decide first, and
// de-identification's holds come after it.
func (f trialFile) hold(t *receive.Trial) {
	if f.unrouted == nil {
		return
	}
	if h := t.Hold; h != nil && slices.Contains(beforeRouting, h.Reason) {
		return
	}
	t.Hold = &receive.TrialHold{Reason: tenant.QuarantineUnrouted, Details: f.unrouted}
}

// beforeRouting are the reasons a file is held for before its study is
// routed at reception: when it lands, and when its labels are made.
var beforeRouting = []tenant.QuarantineReason{tenant.QuarantineNotDICOM, tenant.QuarantineHeaderTooLarge,
	tenant.QuarantineBadUID, tenant.QuarantineSOPClassNotAccepted, tenant.QuarantineBadLabel}

// mark writes on r the route chosen and the version the file was tried
// with when it is not the one asked for.
func (f trialFile) mark(r *v1.TrialResult) {
	if f.route != nil && f.route.Route != nil {
		r.Route = f.route.Route.Name
	}
	if f.unrouted != nil {
		r.Route = f.unrouted["route"]
	}
	if f.profile != nil {
		r.RoutedProfile = &v1.ProfileVersionName{Name: f.profile.Name, Version: f.profile.Version}
	}
}
