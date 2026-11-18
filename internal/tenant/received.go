package tenant

import (
	"strconv"
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

// DuplicateKind is how a file compares with the instance the tenant holds,
// or a reception claims, under its SOP Instance UID: what CheckDuplicates and
// ClaimInstances answer for each file.
type DuplicateKind int

// The kinds. The pipeline goes on with a file claimed now or before, ignores
// the same file, quarantines a different file and a UID elsewhere with the
// reason of their name, and tries a file claimed by another reception again
// later, through its leased work, until it resolves to one of the others.
const (
	// NotHeld is a SOP Instance UID no instance row has: nothing holds or
	// claims it (CheckDuplicates only).
	NotHeld DuplicateKind = iota
	// SameFile is the UID held, in the same study and series, with the
	// same checksum: the file is ignored as a duplicate.
	SameFile
	// DifferentFile is the UID held, in the same study and series, with
	// another checksum; or claimed by this same reception with another
	// checksum, when one reception received two files under one UID: the
	// first claimed is kept and the next is different.
	DifferentFile
	// UIDElsewhere is the UID held or claimed under another study or
	// series: an instance UID is one instance in a tenant.
	UIDElsewhere
	// Claimed is the UID claimed by another reception that has not
	// confirmed it yet. The caller tries the file again later (its step is
	// retried through the leased work), when the claim is confirmed (one
	// of the held answers) or released (then it can be claimed).
	Claimed
	// ClaimedNow is the UID claimed by this call for this reception
	// (ClaimInstances only): the file goes on to the storage. With
	// Overwrite, the held instance stays held until the claim is confirmed.
	ClaimedNow
	// ClaimedBefore is the UID claimed by this reception before, with the
	// same checksum, as when a step runs again after a crash
	// (ClaimInstances only): the file goes on.
	ClaimedBefore
)

// String returns the kind's name, for messages and logs.
func (k DuplicateKind) String() string {
	switch k {
	case NotHeld:
		return "not held"
	case SameFile:
		return "same file"
	case DifferentFile:
		return "different file"
	case UIDElsewhere:
		return "UID elsewhere"
	case Claimed:
		return "claimed by another reception"
	case ClaimedNow:
		return "claimed now"
	case ClaimedBefore:
		return "claimed before"
	}
	return "DuplicateKind(" + strconv.Itoa(int(k)) + ")"
}

// Duplicate is the answer for one file of CheckDuplicates or
// ClaimInstances.
type Duplicate struct {
	// Kind is how it compares.
	Kind DuplicateKind
	// HeldSHA256, HeldStudyUID and HeldSeriesUID are those of the instance
	// held under the UID, or of the claim the answer is about (the first
	// claim of a reception that received two files under one UID; another
	// reception's claim): the kept checksum and the details of the
	// quarantine record that follows. Zero for NotHeld, and for ClaimedNow
	// without Overwrite.
	HeldSHA256                  SHA256
	HeldStudyUID, HeldSeriesUID string
	// HeldBy is the reception that confirmed the held instance, or that
	// claims it for Claimed and DifferentFile of a claim.
	HeldBy string
}

// CompareHeld returns how a file compares with an instance of the tenant
// under its UID, by study and series first, since an instance UID is unique
// in a tenant whatever the bytes, then by checksum. It is the one place the
// comparison is defined: CheckDuplicates and ClaimInstances both decide by
// it, in every driver.
func CompareHeld(q DuplicateQuery, held Duplicate) DuplicateKind {
	switch {
	case held.HeldStudyUID != q.StudyUID || held.HeldSeriesUID != q.SeriesUID:
		return UIDElsewhere
	case held.HeldSHA256 == q.SHA256:
		return SameFile
	}
	return DifferentFile
}

// InstanceClaim is one file a reception claims before it writes anything to
// a storage.
type InstanceClaim struct {
	// SOPInstanceUID, StudyUID and SeriesUID are the file's, as received.
	SOPInstanceUID, StudyUID, SeriesUID string
	// SHA256 is its received checksum.
	SHA256 SHA256
	// Overwrite claims a UID held with another checksum, for a file
	// released to overwrite (QuarantineDifferentFile): the held instance
	// stays held until this claim is confirmed, and then is replaced.
	Overwrite bool
}

// Validate returns an Invalid error unless the claim is well formed.
func (c InstanceClaim) Validate() error {
	return DuplicateQuery{SOPInstanceUID: c.SOPInstanceUID, StudyUID: c.StudyUID, SeriesUID: c.SeriesUID, SHA256: c.SHA256}.Validate()
}

// Query returns the claim as the query CompareHeld takes.
func (c InstanceClaim) Query() DuplicateQuery {
	return DuplicateQuery{SOPInstanceUID: c.SOPInstanceUID, StudyUID: c.StudyUID, SeriesUID: c.SeriesUID, SHA256: c.SHA256}
}

// InstanceRow is what a driver read of the instance row under a UID: the
// instance held, the claim pending, or both (a claim to overwrite).
type InstanceRow struct {
	// StudyUID and SeriesUID are the instance's, as received.
	StudyUID, SeriesUID string
	// HeldSHA256 is the held instance's checksum, zero when none is held;
	// HeldBy the reception that confirmed it.
	HeldSHA256 SHA256
	HeldBy     string
	// ClaimSHA256 is the pending claim's checksum, zero when none;
	// ClaimedBy the reception that claims it.
	ClaimSHA256 SHA256
	ClaimedBy   string
}

// ClaimAction is what a driver writes for one file of a claim.
type ClaimAction int

// The actions.
const (
	// ClaimNothing writes nothing: the answer is about the row as it is.
	ClaimNothing ClaimAction = iota
	// ClaimInsert inserts the row, claimed by the reception.
	ClaimInsert
	// ClaimTakeOver sets the claim on a held row whose checksum is the
	// answer's HeldSHA256, to overwrite it.
	ClaimTakeOver
)

// DecideClaim returns the answer of a claim by reception receptionID for c,
// given the row under its UID (nil when there is none), and what to write.
// Every driver decides by it, so the claim's rules are stated once:
//
//   - no row: claimed now (insert);
//   - another study or series, held or claimed: UID elsewhere;
//   - claimed by this reception: claimed before with the same checksum,
//     and a different file with another (the first claimed is kept);
//   - claimed by another reception: claimed, tried again later;
//   - held with the same checksum: the same file;
//   - held with another: a different file, or claimed now to overwrite
//     when the claim says so.
func DecideClaim(receptionID string, c InstanceClaim, row *InstanceRow) (Duplicate, ClaimAction) {
	if row == nil {
		return Duplicate{Kind: ClaimedNow}, ClaimInsert
	}
	q := c.Query()
	if row.ClaimedBy != "" {
		pending := Duplicate{HeldSHA256: row.ClaimSHA256, HeldStudyUID: row.StudyUID, HeldSeriesUID: row.SeriesUID, HeldBy: row.ClaimedBy}
		switch k := CompareHeld(q, pending); {
		case k == UIDElsewhere:
			pending.Kind = UIDElsewhere
		case row.ClaimedBy != receptionID:
			pending.Kind = Claimed
		case k == SameFile:
			pending.Kind = ClaimedBefore
		default:
			pending.Kind = DifferentFile
		}
		return pending, ClaimNothing
	}
	held := Duplicate{HeldSHA256: row.HeldSHA256, HeldStudyUID: row.StudyUID, HeldSeriesUID: row.SeriesUID, HeldBy: row.HeldBy}
	held.Kind = CompareHeld(q, held)
	if held.Kind == DifferentFile && c.Overwrite {
		held.Kind = ClaimedNow
		return held, ClaimTakeOver
	}
	return held, ClaimNothing
}

// JudgeRow returns CheckDuplicates's answer for q given the row under its
// UID (nil when none): what the tenant holds decides, a pending claim only
// when nothing is held.
func JudgeRow(q DuplicateQuery, row *InstanceRow) Duplicate {
	if row == nil {
		return Duplicate{}
	}
	if row.HeldSHA256.IsZero() {
		d := Duplicate{HeldSHA256: row.ClaimSHA256, HeldStudyUID: row.StudyUID, HeldSeriesUID: row.SeriesUID, HeldBy: row.ClaimedBy, Kind: Claimed}
		if CompareHeld(q, d) == UIDElsewhere {
			d.Kind = UIDElsewhere
		}
		return d
	}
	d := Duplicate{HeldSHA256: row.HeldSHA256, HeldStudyUID: row.StudyUID, HeldSeriesUID: row.SeriesUID, HeldBy: row.HeldBy}
	d.Kind = CompareHeld(q, d)
	return d
}

// ValidateClaimBatch returns an Invalid error unless the batch is within
// MaxClaimBatch, every claim is valid, and no UID appears twice with the
// same checksum (a UID twice with two checksums is two received files, and
// the claim answers the second as different).
func ValidateClaimBatch(receptionID string, files []InstanceClaim, now time.Time) error {
	if err := master.ValidateID("reception id", receptionID); err != nil {
		return err
	}
	if len(files) > MaxClaimBatch {
		return invalid("at most " + strconv.Itoa(MaxClaimBatch) + " instances are claimed at once")
	}
	type key struct {
		uid string
		sum SHA256
	}
	seen := make(map[key]bool, len(files))
	for _, c := range files {
		if err := c.Validate(); err != nil {
			return err
		}
		k := key{c.SOPInstanceUID, c.SHA256}
		if seen[k] {
			return invalid("a claim names one file twice")
		}
		seen[k] = true
	}
	return master.ValidateTime("claim time", now)
}
