package receive

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/amrshadid/go-dicom/dataset"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/core/naming"
	"github.com/amrshadid/coria/internal/deid/check"
	"github.com/amrshadid/coria/internal/deid/engine"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/receive/header"
	"github.com/amrshadid/coria/internal/tenant"
)

// Trial is what de-identifying one file would do at a receiver, worked out
// in memory with nothing stored: the steps of the reception pipeline (the
// receiving side's checks of the header when a receiver is named, its
// labels, the engine, the placing UIDs, the writer, the check after), the
// file written to memory instead of the incoming area. It holds the file's
// values (Before, After and the record's needles), so it is answered only
// to a caller who may read them and never logged.
type Trial struct {
	// Record is the engine's record; nil when the file was held before
	// the engine finished.
	Record *engine.Record
	// Before is the data set as received; nil when it could not be read.
	Before *dataset.Dataset
	// After is the de-identified file read back as written; nil when the
	// file was held before it was written.
	After *dataset.Dataset
	// Written is the de-identified file's bytes; nil when it was held
	// before it was written.
	Written []byte
	// Report is the check after's report; zero when it did not run.
	Report check.Report
	// Hold is why a receiver would hold the file; nil when it would
	// archive it, or when Undecided.
	Hold *TrialHold
	// Undecided reports that the verdict depends on a lookup table: a
	// lookup rule whose value has no code is held at a receiver, and a
	// trial reads no table (Hold is then nil).
	Undecided bool
	// Receiver is the receiver whose receiving checks ran; empty when
	// none did and the verdict is de-identification's only.
	Receiver string
	// Pixels is what was done to the file's pixels (Record.Pixels); nil
	// when it has none or was held before the engine finished.
	Pixels *engine.PixelWork
}

// TrialHold is why a receiver would hold a file: the quarantine reason and
// the record's details (cause, path, rule, kind, label, attribute, fault),
// never a value.
type TrialHold struct {
	// Reason is the quarantine reason.
	Reason tenant.QuarantineReason
	// Details are the record's details.
	Details map[string]string
}

// TrialOptions are what a trial runs with besides its engine and file.
type TrialOptions struct {
	// Keys are the keys the engine was made with, which the placing UIDs
	// are checked against and the labels hashed with. A trial is given
	// keys of its own, never a tenant's, so nothing keyed it answers can
	// be matched with the archive.
	Keys engine.Keys
	// HeaderLimit bounds the header, as receiver.header-limit does.
	HeaderLimit int64
	// PixelLimit bounds an image decoded that the work on pixels takes,
	// as deid.pixel-limit does; zero or less is DefaultPixelLimit.
	PixelLimit int64
	// Receiver, when not nil, is the receiver the file is tried as
	// arriving at: its receiving checks (headerHold, classHold), its
	// naming rule and the label check run as at reception, and the check
	// after reads within its bound. Nil tries de-identification alone.
	Receiver *tenant.Receiver
	// Check makes the check after for a file bound, as Config.Check does.
	Check func(maxFileBytes int64) Checker
}

// TryFile de-identifies file in memory by en as a receiver that names en's
// profile version would, checks it, and answers what it would archive or
// why it would hold it. An error other than a hold says nothing of the
// file (a canceled ctx); its message is fixed and carries nothing of the
// file.
func TryFile(ctx context.Context, en *engine.Engine, file []byte, rec engine.Reception, o TrialOptions) (Trial, error) {
	var t Trial
	held := func(reason tenant.QuarantineReason, details map[string]string) (Trial, error) {
		t.Hold = &TrialHold{Reason: reason, Details: details}
		return t, nil
	}
	h, err := header.ReadWithin(bytes.NewReader(file), int64(len(file)), o.HeaderLimit)
	var big *header.TooLargeError
	switch {
	case errors.As(err, &big):
		return held(tenant.QuarantineHeaderTooLarge, map[string]string{"limit": itoa(big.Limit)})
	case err != nil:
		return held(tenant.QuarantineNotDICOM, map[string]string{"cause": "unreadable"})
	}
	var path labelsFor
	if rcv := o.Receiver; rcv != nil {
		t.Receiver = rcv.Name
		if reason, details := headerHold(h, true); reason != "" {
			return held(reason, details)
		}
		if reason, details := classHold(*rcv, h); reason != "" {
			return held(reason, details)
		}
		if path, err = trialLabels(*rcv, h, o.Keys); err != nil {
			return t, err
		}
		if path.fault != nil {
			return held(tenant.QuarantineBadLabel, path.fault.details())
		}
	}
	ds, source, littleEndian, err := h.Dataset(bytes.NewReader(file))
	if err != nil {
		return held(tenant.QuarantineDeidFailed, map[string]string{"cause": "unreadable"})
	}
	// A second read of the same bytes, for the values before: the engine
	// changes ds in place.
	t.Before, _, _, _ = h.Dataset(bytes.NewReader(file))
	rec.CharacterSet = h.CharacterSet()
	limit := o.PixelLimit
	if limit <= 0 {
		limit = DefaultPixelLimit
	}
	record, err := en.ApplyImage(ctx, ds, rec, image(h, bytes.NewReader(file), source, limit))
	var failure *engine.Failure
	if errors.As(err, &failure) {
		if failure.Cause == engine.CauseLookupMissing {
			t.Undecided = true
			return t, nil
		}
		return held(engineHold(failure))
	}
	if err != nil {
		return t, fault.New(fault.Unavailable, "a file could not be de-identified now")
	}
	t.Record, t.Pixels = record, record.Pixels
	if o.Receiver != nil {
		if level, kind := labelResidual(*o.Receiver, path.path, record.Needles); level != "" {
			return held(tenant.QuarantineResidualValue, map[string]string{"path": level, "kind": kind})
		}
	}
	sop, _ := h.UID(header.SOPInstanceUID)
	study, _ := h.UID(header.StudyUID)
	series, _ := h.UID(header.SeriesUID)
	if dsUID(ds, dsSOPInstanceUID) != remapPlacingUID(o.Keys, sop) || dsUID(ds, dsStudyUID) != remapPlacingUID(o.Keys, study) ||
		dsUID(ds, dsSeriesUID) != remapPlacingUID(o.Keys, series) {
		return held(tenant.QuarantineDeidFailed, map[string]string{"cause": "uid_not_remapped"})
	}
	mem := &memFile{}
	err = writeDataset(mem, ds, writtenSyntax(h.TransferSyntaxUID, ds, record), source, littleEndian)
	var we errWrite
	if errors.Is(err, engine.ErrPixelsUndecodable) {
		return held(tenant.QuarantineDeidFailed, pixelHold)
	}
	if errors.As(err, &we) {
		return held(tenant.QuarantineDeidFailed, map[string]string{"cause": "unwritable"})
	}
	if err != nil {
		return t, fault.New(fault.Internal, "a de-identified file could not be written to memory")
	}
	out := mem.b
	bound := 2*int64(len(file)) + o.HeaderLimit + grown(record)
	if o.Receiver != nil {
		bound = o.Receiver.MaxFileBytes + checkHeadroom + grown(record)
	}
	t.Report, err = o.Check(bound).Check(ctx, bytes.NewReader(out), int64(len(out)), record.Needles, h.CharacterSet())
	if reason, details, isHold := checkHold(err); isHold {
		return held(reason, details)
	} else if err != nil {
		return t, fault.New(fault.Unavailable, "a de-identified file could not be checked now")
	}
	t.Written = out
	after, err := header.ReadWithin(bytes.NewReader(out), int64(len(out)), int64(len(out))+o.HeaderLimit)
	if err == nil {
		t.After, _, _, err = after.Dataset(bytes.NewReader(out))
	}
	if err != nil {
		// The check after read it, by a reader of its own; this reader
		// could not. Never answered as passed with no values to show.
		t.Written = nil
		return held(tenant.QuarantineDeidFailed, map[string]string{"cause": "after_unreadable"})
	}
	return t, nil
}

// labelsFor is a file's labels by a receiver's rule, or why there are
// none.
type labelsFor struct {
	path  label.Path
	fault *labelFault
}

// trialLabels runs the receiver's naming rule over the header, its hashed
// placeholders under the trial's own hash key.
func trialLabels(rcv tenant.Receiver, h *header.Header, keys engine.Keys) (labelsFor, error) {
	rule, err := ruleOf(rcv)
	if err != nil {
		return labelsFor{}, fault.New(fault.Internal, "the receiver's naming rule does not parse")
	}
	hasher, err := naming.NewHasher(keys.Hash)
	if err != nil {
		return labelsFor{}, fault.New(fault.Internal, "a trial's hash key is not a key")
	}
	path, err := rule.Labels(h, hasher)
	if err == nil {
		return labelsFor{path: path}, nil
	}
	lf, err := labelFaultOf(err)
	if err != nil {
		return labelsFor{}, fault.New(fault.Internal, "the naming rule failed")
	}
	return labelsFor{fault: lf}, nil
}

// ExplainFile answers for each element of file why en treats it as it does
// (engine.Explain), with nothing stored, or why the engine refuses the
// file (a hold, with Explain's cause).
func ExplainFile(ctx context.Context, en *engine.Engine, file []byte, rec engine.Reception, headerLimit int64) ([]engine.Explanation, *TrialHold, error) {
	h, err := header.ReadWithin(bytes.NewReader(file), int64(len(file)), headerLimit)
	if err != nil {
		return nil, &TrialHold{Reason: tenant.QuarantineNotDICOM, Details: map[string]string{"cause": "unreadable"}}, nil
	}
	ds, _, _, err := h.Dataset(bytes.NewReader(file))
	if err != nil {
		return nil, &TrialHold{Reason: tenant.QuarantineDeidFailed, Details: map[string]string{"cause": "unreadable"}}, nil
	}
	rec.CharacterSet = h.CharacterSet()
	out, err := en.Explain(ctx, ds, rec)
	var failure *engine.Failure
	if errors.As(err, &failure) {
		reason, details := engineHold(failure)
		if failure.Cause == engine.CauseLookupMissing {
			details["cause"] = TrialCauseLookupTable
		}
		return nil, &TrialHold{Reason: reason, Details: details}, nil
	}
	if err != nil {
		return nil, nil, fault.New(fault.Unavailable, "a file could not be explained now")
	}
	return out, nil, nil
}

// TrialCauseLookupTable is the cause of a trial whose verdict depends on a
// lookup table it does not read.
const TrialCauseLookupTable = "lookup_table"

// memFile is an io.WriteSeeker over memory, which the writer writes a
// tried file into: it seeks back to fill in a length it learns late.
type memFile struct {
	b   []byte
	off int64
}

// Write writes p at the offset, growing the file as needed.
func (m *memFile) Write(p []byte) (int, error) {
	end := m.off + int64(len(p))
	if end > int64(len(m.b)) {
		m.b = append(m.b, make([]byte, end-int64(len(m.b)))...)
	}
	copy(m.b[m.off:end], p)
	m.off = end
	return len(p), nil
}

// Seek moves the offset; it never moves before the start.
func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	var at int64
	switch whence {
	case io.SeekStart:
		at = offset
	case io.SeekCurrent:
		at = m.off + offset
	case io.SeekEnd:
		at = int64(len(m.b)) + offset
	default:
		return 0, errors.New("memfile: unknown whence")
	}
	if at < 0 {
		return 0, errors.New("memfile: seek before the start")
	}
	m.off = at
	return at, nil
}
