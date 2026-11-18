package receive

import (
	"context"

	"github.com/amrshadid/magnetar/internal/core/naming"
	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// identify gives a complete reception its subject and session labels and its
// storage, and holds the files whose labels cannot be made.
//
// Order of effects, each safe to repeat or to stop after:
//
//  1. Read the reception. One past complete took this step before a crash:
//     its unit is completed with the unit its state needs.
//  2. Read what the catalog holds for the study and its series
//     (CatalogLabels): a study or series it knows keeps its labels and
//     storage, so a later arrival lands beside the first. The rest are named
//     by the receiver's rule over the header of a file of each, the same
//     file every run (the first of its series by UID whose bytes are here),
//     so a run again makes the same labels.
//  3. Hold, with the label reason, every file of a series whose scan label
//     cannot be made, or every file when the subject or session label cannot
//     be. A record's id is made from the reception and the file, so a run
//     again writes nothing new.
//  4. Take the identify step with the labels and the storage (the session's
//     own when it has one, else the receiver's), or the quarantine step when
//     no file can go on.
//  5. Complete the unit with the archive unit, in one transaction. A crash
//     between 4 and 5 is taken up by 1.
//
// A label that collides with one the catalog holds for another study or
// series is found by the archive step's commit, which holds those files
// then: the catalog decides that in its own transaction.
func (p *Pipeline) identify(ctx context.Context, w tenant.Work) error {
	r, _, err := p.store.Reception(ctx, w.ReceptionID)
	if err != nil {
		return err
	}
	switch {
	case r.State.Ended():
		return p.complete(ctx, w, nil)
	case r.State != reception.Complete:
		return p.completeFor(ctx, w, r)
	}
	groups, err := p.receivedBySeries(ctx, r.ID)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return p.quarantineAll(ctx, w, r)
	}
	catalog, err := p.catalogLabels(ctx, r.StudyUID, groups)
	if err != nil {
		return err
	}
	rule, err := p.ruleFor(ctx, r.Receiver)
	if err != nil {
		return err
	}
	var subject, session, storage string
	if s := catalog.Session; s != nil {
		subject, session, storage = s.SubjectLabel, s.SessionLabel, s.Storage
	} else {
		// The subject and session labels come from the first series whose
		// file the rule names whole. A fault at the subject or session
		// level is the study's: no session can be made, and every file is
		// held. A fault at the scan level is that series' alone, and the
		// next series is tried.
		for _, g := range groups {
			path, lf, err := p.labelsOf(rule, g)
			if err != nil {
				return err
			}
			if lf != nil && lf.level != "scan" {
				return p.holdEverything(ctx, w, r, groups, lf)
			}
			if lf == nil {
				subject, session = path.Subject, path.Session
				break
			}
		}
		if subject == "" {
			// Every series failed its scan label: hold each with its
			// fault, and nothing is left.
			if _, err := p.holdBadScans(ctx, r, groups, catalog, rule); err != nil {
				return err
			}
			return p.quarantineAll(ctx, w, r)
		}
		rcv, err := p.receivers.get(ctx, r.Receiver)
		if err != nil {
			if fault.KindOf(err) == fault.NotFound {
				return fault.Wrap(fault.Conflict, err, "the reception's receiver was removed, and its study has no session to name a storage")
			}
			return err
		}
		storage = rcv.Storage
	}
	left, err := p.holdBadScans(ctx, r, groups, catalog, rule)
	if err != nil {
		return err
	}
	if left == 0 {
		return p.quarantineAll(ctx, w, r)
	}
	p.at("identify.before-step")
	if _, err := p.step(ctx, w, tenant.ReceptionStep{ID: r.ID, From: reception.Complete, Event: reception.Identify,
		SubjectLabel: subject, SessionLabel: session, Storage: storage}); err != nil {
		return err
	}
	p.at("identify.after-step")
	next := unitFor(kindArchive, r.ID, p.now())
	return p.complete(ctx, w, &next)
}

// holdBadScans holds the files of each series whose scan label cannot be
// made, and returns how many series can go on.
func (p *Pipeline) holdBadScans(ctx context.Context, r tenant.Reception, groups []series, catalog tenant.CatalogLabels, rule *naming.Rule) (int, error) {
	left := 0
	var records []tenant.QuarantinedFile
	for _, g := range groups {
		if _, ok := catalog.Scans[g.uid]; ok {
			left++
			continue
		}
		_, lf, err := p.labelsOf(rule, g)
		if err != nil {
			return 0, err
		}
		if lf != nil {
			for _, f := range g.files {
				records = append(records, p.holdRecord(r, f, tenant.QuarantineBadLabel, lf.details()))
			}
			continue
		}
		left++
	}
	return left, p.hold(ctx, records)
}

// holdEverything holds every file of the reception with the label fault,
// then ends it as quarantined.
func (p *Pipeline) holdEverything(ctx context.Context, w tenant.Work, r tenant.Reception, groups []series, lf *labelFault) error {
	var records []tenant.QuarantinedFile
	for _, g := range groups {
		for _, f := range g.files {
			records = append(records, p.holdRecord(r, f, tenant.QuarantineBadLabel, lf.details()))
		}
	}
	if err := p.hold(ctx, records); err != nil {
		return err
	}
	return p.quarantineAll(ctx, w, r)
}

// quarantineAll ends a reception none of whose files can go on, and
// completes its unit with nothing after it.
func (p *Pipeline) quarantineAll(ctx context.Context, w tenant.Work, r tenant.Reception) error {
	p.at("step.before-quarantine")
	if _, err := p.step(ctx, w, tenant.ReceptionStep{ID: r.ID, From: r.State, Event: reception.Quarantine}); err != nil {
		return err
	}
	return p.complete(ctx, w, nil)
}
