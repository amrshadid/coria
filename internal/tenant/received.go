package tenant

import (
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// ReceivedFile is a file that has landed in the incoming area, synced, before
// its sender was answered.
type ReceivedFile struct {
	// ReceptionID names the reception it belongs to.
	ReceptionID string
	// SOPInstanceUID, SeriesUID and StudyUID are its UIDs as received.
	SOPInstanceUID, SeriesUID, StudyUID string
	// SOPClassUID and TransferSyntaxUID are its class and transfer syntax.
	SOPClassUID, TransferSyntaxUID string
	// Size is its size in bytes as written to the incoming area.
	Size int64
	// SHA256 is the checksum of the file as written: the received checksum.
	SHA256 SHA256
	// Incoming is where it is in the incoming area, relative to it.
	Incoming string
	// Caller is who sent it.
	Caller Caller
	// Association is the receiver's id of the association (or request)
	// that brought it, empty for a file no association brought (a release).
	Association string
	// Released says it came from a quarantine release.
	Released bool
	// Received is when it was recorded. Answered by a store.
	Received time.Time
}

// Validate returns an Invalid error unless f can be recorded. Its UIDs are
// checked by the core's rule, and the study UID must be its reception's,
// which the store checks.
func (f ReceivedFile) Validate() error {
	if err := master.ValidateID("reception id", f.ReceptionID); err != nil {
		return err
	}
	for _, u := range []struct{ what, v string }{
		{"SOP Instance UID", f.SOPInstanceUID}, {"Series Instance UID", f.SeriesUID},
		{"Study Instance UID", f.StudyUID}, {"SOP Class UID", f.SOPClassUID},
		{"Transfer Syntax UID", f.TransferSyntaxUID},
	} {
		if err := validateUID(u.what, u.v); err != nil {
			return err
		}
	}
	if f.Size < 0 {
		return invalid("a file's size is not negative")
	}
	if f.SHA256.IsZero() {
		return invalid("a received file has its checksum")
	}
	if err := validateIncoming(f.Incoming); err != nil {
		return err
	}
	if err := f.Caller.Validate(); err != nil {
		return err
	}
	if f.Association != "" {
		return master.ValidateID("association id", f.Association)
	}
	return nil
}

// ValidateReceivedBatch returns an Invalid error unless the batch is within
// MaxReceivedBatch and every file is valid.
func ValidateReceivedBatch(files []ReceivedFile, at time.Time) error {
	if len(files) > MaxReceivedBatch {
		return fault.New(fault.Invalid, "at most 1000 received files are recorded at once")
	}
	for _, f := range files {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	return master.ValidateTime("receive time", at)
}

// DuplicateQuery is one file whose SOP Instance UID is looked up among what
// the tenant holds.
type DuplicateQuery struct {
	// SOPInstanceUID, StudyUID and SeriesUID are the file's, as received.
	SOPInstanceUID, StudyUID, SeriesUID string
	// SHA256 is its received checksum.
	SHA256 SHA256
}

// Validate returns an Invalid error unless the query is well formed.
func (q DuplicateQuery) Validate() error {
	for _, u := range []struct{ what, v string }{
		{"SOP Instance UID", q.SOPInstanceUID}, {"Study Instance UID", q.StudyUID}, {"Series Instance UID", q.SeriesUID},
	} {
		if err := validateUID(u.what, u.v); err != nil {
			return err
		}
	}
	if q.SHA256.IsZero() {
		return invalid("a duplicate check is given the file's checksum")
	}
	return nil
}

// ValidateDuplicateBatch returns an Invalid error unless the batch is within
// MaxDuplicateBatch and every query is valid.
func ValidateDuplicateBatch(files []DuplicateQuery) error {
	if len(files) > MaxDuplicateBatch {
		return invalid("at most 1000 files are checked at once")
	}
	for _, q := range files {
		if err := q.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// DuplicateKind is how a file compares with what the tenant holds under its
// SOP Instance UID.
type DuplicateKind int

// The kinds, which the pipeline acts on: a new file goes on, the same file is
// ignored, and the two others are quarantined with the reason of their name.
const (
	// NotHeld is a SOP Instance UID the tenant does not hold.
	NotHeld DuplicateKind = iota
	// SameFile is the UID held with the same checksum, study and series:
	// the file is ignored as a duplicate.
	SameFile
	// DifferentFile is the UID held, in the same study and series, with
	// another checksum.
	DifferentFile
	// UIDElsewhere is the UID held under another study or series.
	UIDElsewhere
)

// Duplicate is the answer for one file of CheckDuplicates.
type Duplicate struct {
	// Kind is how it compares.
	Kind DuplicateKind
	// HeldSHA256, HeldStudyUID and HeldSeriesUID are the held instance's,
	// zero when NotHeld: the kept checksum and the details of the
	// quarantine record that follows.
	HeldSHA256                  SHA256
	HeldStudyUID, HeldSeriesUID string
}

// CompareHeld returns how a file compares with an instance the tenant holds
// under its UID. Every driver decides by it, so they agree: another study or
// series first, since an instance UID is unique in a tenant whatever the
// bytes, then the checksum.
func CompareHeld(q DuplicateQuery, held Duplicate) DuplicateKind {
	switch {
	case held.HeldStudyUID != q.StudyUID || held.HeldSeriesUID != q.SeriesUID:
		return UIDElsewhere
	case held.HeldSHA256 == q.SHA256:
		return SameFile
	}
	return DifferentFile
}
