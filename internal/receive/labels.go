package receive

import (
	"context"
	"errors"
	"slices"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/core/naming"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/receive/header"
	"github.com/amrshadid/coria/internal/tenant"
)

// series is the received files of one series of a reception, in order of
// SOP Instance UID.
type series struct {
	uid   string
	files []tenant.ReceivedFile
}

// receivedBySeries returns a reception's received files grouped by series,
// the series in order of UID. Its memory is the files' rows, a few hundred
// bytes each, never their bytes.
func (p *Pipeline) receivedBySeries(ctx context.Context, receptionID string) ([]series, error) {
	by := map[string][]tenant.ReceivedFile{}
	err := p.eachReceived(ctx, receptionID, func(f tenant.ReceivedFile) error {
		by[f.SeriesUID] = append(by[f.SeriesUID], f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]series, 0, len(by))
	for uid, files := range by {
		out = append(out, series{uid: uid, files: files})
	}
	slices.SortFunc(out, func(a, b series) int {
		switch {
		case a.uid < b.uid:
			return -1
		case a.uid > b.uid:
			return 1
		}
		return 0
	})
	return out, nil
}

// eachReceived calls fn for each received file of a reception, a page at a
// time.
func (p *Pipeline) eachReceived(ctx context.Context, receptionID string, fn func(tenant.ReceivedFile) error) error {
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		files, next, err := p.store.ReceivedFiles(ctx, receptionID, page)
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := fn(f); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.Cursor = next
	}
}

// catalogLabels returns what the catalog holds for the study and the series,
// asked in bounded batches.
func (p *Pipeline) catalogLabels(ctx context.Context, study string, groups []series) (tenant.CatalogLabels, error) {
	out := tenant.CatalogLabels{Scans: map[string]string{}}
	uids := make([]string, len(groups))
	for i, g := range groups {
		uids[i] = g.uid
	}
	for start := 0; start == 0 || start < len(uids); start += tenant.MaxSeriesLookup {
		part := uids[start:min(start+tenant.MaxSeriesLookup, len(uids))]
		c, err := p.store.CatalogLabels(ctx, study, part)
		if err != nil {
			return out, err
		}
		if c.Session != nil {
			out.Session = c.Session
		}
		for k, v := range c.Scans {
			out.Scans[k] = v
		}
		if len(uids) == 0 {
			break
		}
	}
	return out, nil
}

// ruleFor returns the receiver's naming rule, the default when it has none
// or when the receiver was removed.
func (p *Pipeline) ruleFor(ctx context.Context, receiverName string) (*naming.Rule, error) {
	rcv, err := p.receivers.get(ctx, receiverName)
	if fault.KindOf(err) == fault.NotFound || (err == nil && rcv.Naming == nil) {
		return naming.Default(), nil
	}
	if err != nil {
		return nil, err
	}
	rule, err := naming.Parse(*rcv.Naming)
	if err != nil {
		return nil, fault.Wrap(fault.Internal, err, "the receiver's naming rule does not parse")
	}
	return rule, nil
}

// labelFault is why a file's labels could not be made: the level, the
// attribute when it is one a quarantine record may name, and the fault
// word, all from the closed lists of the quarantine's details.
type labelFault struct {
	level, attribute, fault string
}

// record returns the details of a bad_label record.
func (f *labelFault) details() map[string]string {
	d := map[string]string{"label": f.level, "fault": f.fault}
	if f.attribute != "" {
		d["attribute"] = f.attribute
	}
	return d
}

// labelsOf runs the rule over the header of the first file of g whose bytes
// are in the incoming area. A file's fault is a *labelFault; anything else
// (no file readable, a hasher missing) is an error of the server.
func (p *Pipeline) labelsOf(ctx context.Context, rule *naming.Rule, g series) (label.Path, *labelFault, error) {
	var lastErr error
	for _, f := range g.files {
		h, err := p.readHeader(ctx, f.Incoming)
		if err != nil {
			lastErr = err
			continue
		}
		path, err := rule.Labels(h, p.cfg.Hasher)
		if err == nil {
			return path, nil, nil
		}
		var ne *naming.Error
		if !errors.As(err, &ne) {
			return label.Path{}, nil, fault.Wrap(fault.Internal, err, "the naming rule failed")
		}
		lf := &labelFault{level: ne.Level}
		switch ne.Fault {
		case naming.Missing:
			lf.fault = tenant.FaultMissing
		case naming.Empty:
			lf.fault = tenant.FaultEmpty
		case naming.TooLong:
			lf.fault = tenant.FaultTooLong
		case naming.BadCharacter:
			lf.fault = tenant.FaultBadCharacter
		case naming.BadUID:
			lf.fault = tenant.FaultBadUID
		case naming.NoMatch:
			lf.fault = tenant.FaultNoMatch
		case naming.BadLabel:
			lf.fault = tenant.FaultBadLabel
		default:
			// NoHasher and BadHash are the server's mistakes, never a
			// file's.
			return label.Path{}, nil, fault.New(fault.Internal, "the naming rule needs the tenant's hasher: "+ne.Fault.String())
		}
		if _, perr := naming.ParseAttribute(ne.Attribute); perr == nil {
			lf.attribute = ne.Attribute
		}
		return label.Path{}, lf, nil
	}
	if lastErr == nil {
		lastErr = fault.New(fault.Internal, "a series has no file")
	}
	return label.Path{}, nil, fault.Wrap(fault.KindOf(lastErr), lastErr, "no file of the series could be read to name it")
}

// readHeader reads the header of a file in the incoming area, within the
// bound the check that recorded it read it within or a raised one.
func (p *Pipeline) readHeader(ctx context.Context, path string) (*header.Header, error) {
	f, size, err := p.area.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h, err := header.ReadWithin(f, size, p.headerLimit(ctx))
	if err != nil {
		return nil, fault.Wrap(fault.Internal, err, "a recorded file's header cannot be read")
	}
	return h, nil
}

// holdRecord returns the quarantine record of a received file of reception
// r. Its id is made from the reception, the UID and the checksum, so one
// file is held at most once by one reception, however many runs of a step
// hold it: the store skips a record it holds.
func (p *Pipeline) holdRecord(r tenant.Reception, f tenant.ReceivedFile, reason tenant.QuarantineReason, details map[string]string) tenant.QuarantinedFile {
	return tenant.QuarantinedFile{
		ID: derivedID("q", "reception", r.ID, f.SOPInstanceUID, f.SHA256.String()), Reason: reason, Details: details,
		Receiver: r.Receiver, ReceptionID: r.ID, Caller: f.Caller, Size: f.Size, SHA256: f.SHA256,
		SOPClassUID: f.SOPClassUID, SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID,
		TransferSyntaxUID: f.TransferSyntaxUID, Incoming: f.Incoming, ContentKept: true, Quarantined: p.now(),
		Deidentified: f.Deidentified,
	}
}

// hold writes quarantine records in bounded batches, under the fence of the
// unit w the worker holds (HoldFilesFenced): a worker whose lease was taken
// over holds nothing and is answered that its lease is lost. A record
// written before (a step run again) is skipped by the store.
func (p *Pipeline) hold(ctx context.Context, w tenant.Work, records []tenant.QuarantinedFile) error {
	fence := w.Fence()
	for start := 0; start < len(records); start += tenant.MaxQuarantineBatch {
		part := records[start:min(start+tenant.MaxQuarantineBatch, len(records))]
		p.at("step.before-hold")
		if _, err := p.store.HoldFilesFenced(ctx, part, &fence); err != nil {
			return err
		}
		p.wake()
	}
	return nil
}
