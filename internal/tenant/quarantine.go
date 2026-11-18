package tenant

import (
	"strconv"
	"time"

	"github.com/amrshadid/magnetar/internal/master"
)

// QuarantineReason is why a file was quarantined, from the API contract's
// fixed list. Its text is what a store keeps.
type QuarantineReason string

// The reasons.
const (
	QuarantineNotDICOM            QuarantineReason = "not_dicom"
	QuarantineSOPClassNotAccepted QuarantineReason = "sop_class_not_accepted"
	QuarantineTooLarge            QuarantineReason = "too_large"
	QuarantineBadUID              QuarantineReason = "bad_uid"
	QuarantineBadLabel            QuarantineReason = "bad_label"
	QuarantineLabelConflict       QuarantineReason = "label_conflict"
	QuarantineDifferentFile       QuarantineReason = "different_file"
	QuarantineUIDElsewhere        QuarantineReason = "uid_elsewhere"
	QuarantineStorageRefused      QuarantineReason = "storage_refused"
	QuarantineReadBackMismatch    QuarantineReason = "read_back_mismatch"
)

// reasonDetails are the detail keys each reason may carry, the contract's,
// with the shape of each key's value. A value is checked by its shape so
// that a detail can never carry an attribute's value from the file: a UID,
// as received, is the only thing from a file a detail may hold.
var reasonDetails = map[QuarantineReason]map[string]detailShape{
	QuarantineNotDICOM:            {"offset": digits},
	QuarantineSOPClassNotAccepted: {"sop_class_uid": uidShape},
	QuarantineTooLarge:            {"limit": digits},
	QuarantineBadUID:              {"attribute": keyword, "fault": word},
	QuarantineBadLabel:            {"label": levelShape, "attribute": attributeOrPart, "fault": word},
	QuarantineLabelConflict:       {"label": levelShape, "fault": word},
	QuarantineDifferentFile:       {},
	QuarantineUIDElsewhere:        {"study_instance_uid": uidShape, "series_instance_uid": uidShape},
	QuarantineStorageRefused:      {"storage": nameShape, "fault": word},
	QuarantineReadBackMismatch:    {"storage": nameShape, "read_sha256": hexShape},
}

// detailShape is what a detail's value may look like.
type detailShape int

const (
	digits detailShape = iota
	uidShape
	keyword
	word
	levelShape
	attributeOrPart
	nameShape
	hexShape
)

// valid reports whether v has the shape.
func (s detailShape) valid(v string) bool {
	switch s {
	case digits:
		return v != "" && len(v) <= 20 && allBytes(v, func(b byte) bool { return b >= '0' && b <= '9' })
	case uidShape:
		return validateUID("UID", v) == nil
	case keyword:
		return v != "" && len(v) <= 64 && allBytes(v, isAlnum)
	case word:
		return v != "" && len(v) <= 32 && allBytes(v, func(b byte) bool { return (b >= 'a' && b <= 'z') || b == '_' })
	case levelShape:
		return v == "subject" || v == "session" || v == "scan"
	case attributeOrPart:
		// A keyword, a tag as "(gggg,eeee)", a pattern's part, or empty
		// for a fault in the label as a whole.
		return len(v) <= 64 && allBytes(v, func(b byte) bool { return isAlnum(b) || b == '_' || b == '(' || b == ')' || b == ',' })
	case nameShape:
		return ValidateName("storage", v) == nil
	case hexShape:
		return len(v) == 64 && allBytes(v, func(b byte) bool { return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') })
	}
	return false
}

// Valid reports whether r is one of the reasons.
func (r QuarantineReason) Valid() bool {
	_, ok := reasonDetails[r]
	return ok
}

// QuarantineState is what has been decided about a quarantined file.
type QuarantineState string

// The states.
const (
	QuarantineHeld     QuarantineState = "held"
	QuarantineReleased QuarantineState = "released"
	QuarantineRejected QuarantineState = "rejected"
)

// ReleaseMode is how a released file runs through the pipeline again.
type ReleaseMode string

// The modes.
const (
	// ReleaseAsIs runs it by the pipeline's rules now.
	ReleaseAsIs ReleaseMode = "as_is"
	// ReleaseOverwrite also replaces the different file the tenant holds
	// under its SOP Instance UID; only for QuarantineDifferentFile.
	ReleaseOverwrite ReleaseMode = "overwrite"
)

// MaxQuarantineDetails bounds a record's details, the contract's.
const MaxQuarantineDetails = 16

// QuarantinedFile is the record of one quarantined file.
type QuarantinedFile struct {
	// ID is the record's id, given by the caller.
	ID string
	// Reason is why it was quarantined.
	Reason QuarantineReason
	// Details are more about the reason, under the keys the reason allows.
	Details map[string]string
	// State is what has been decided. A record is held when made.
	State QuarantineState
	// Receiver names the receiver it arrived at.
	Receiver string
	// ReceptionID names its reception, empty when its study was not known.
	ReceptionID string
	// Caller is who sent it.
	Caller Caller
	// Size is its size in bytes as written, or read before the bound.
	Size int64
	// SHA256 is its checksum, zero when its content was not kept whole.
	SHA256 SHA256
	// KeptSHA256 is the checksum of the file held under its UID, for
	// QuarantineDifferentFile only.
	KeptSHA256 SHA256
	// The UIDs as received, each empty when it could not be read.
	SOPClassUID, SOPInstanceUID, StudyUID, SeriesUID, TransferSyntaxUID string
	// Incoming is where its bytes are in the incoming area, empty when
	// they were never kept.
	Incoming string
	// ContentKept says its bytes are held: true while held with Incoming
	// set, false once rejected.
	ContentKept bool
	// Quarantined is when it was quarantined.
	Quarantined time.Time
	// Decided is when it was released or rejected, zero while held.
	Decided time.Time
	// ReleaseMode, ReleaseReception and ReleaseReceiver are set when it
	// is released.
	ReleaseMode ReleaseMode
	// ReleaseReception is the reception its release joined.
	ReleaseReception string
	// ReleaseReceiver is the receiver its release ran through.
	ReleaseReceiver string
	// RejectReason is why it was rejected.
	RejectReason string
	// ReleasedFrom names the record whose release quarantined it again.
	ReleasedFrom string
}

// Validate returns an Invalid error unless q can be recorded as a new held
// file.
func (q QuarantinedFile) Validate() error {
	if err := master.ValidateID("quarantine id", q.ID); err != nil {
		return err
	}
	allowed, ok := reasonDetails[q.Reason]
	if !ok {
		return invalid("a quarantine reason is one of the contract's codes")
	}
	if len(q.Details) > MaxQuarantineDetails {
		return invalid("a quarantine record has at most " + strconv.Itoa(MaxQuarantineDetails) + " details")
	}
	for k, v := range q.Details {
		shape, ok := allowed[k]
		if !ok {
			return invalid("the detail " + strconv.Quote(k) + " is not one the reason " + string(q.Reason) + " carries")
		}
		if !shape.valid(v) {
			return invalid("the detail " + strconv.Quote(k) + " does not have the form its reason states")
		}
	}
	if q.State != "" && q.State != QuarantineHeld {
		return invalid("a quarantined file is recorded held")
	}
	if err := ValidateName("receiver", q.Receiver); err != nil {
		return err
	}
	if q.ReceptionID != "" {
		if err := master.ValidateID("reception id", q.ReceptionID); err != nil {
			return err
		}
	}
	if err := q.Caller.Validate(); err != nil {
		return err
	}
	if q.Size < 0 {
		return invalid("a file's size is not negative")
	}
	if !q.KeptSHA256.IsZero() && q.Reason != QuarantineDifferentFile {
		return invalid("a kept checksum is recorded for a different file only")
	}
	if q.Reason == QuarantineDifferentFile && (q.KeptSHA256.IsZero() || q.SHA256.IsZero()) {
		return invalid("a different file is recorded with both checksums")
	}
	for _, u := range []struct{ what, v string }{
		{"SOP Class UID", q.SOPClassUID}, {"SOP Instance UID", q.SOPInstanceUID}, {"Study Instance UID", q.StudyUID},
		{"Series Instance UID", q.SeriesUID}, {"Transfer Syntax UID", q.TransferSyntaxUID},
	} {
		if u.v != "" {
			if err := validateUID(u.what, u.v); err != nil {
				return err
			}
		}
	}
	if q.ContentKept != (q.Incoming != "") {
		return invalid("a quarantined file whose content is kept says where it is, and only then")
	}
	if q.Incoming != "" {
		if err := validateIncoming(q.Incoming); err != nil {
			return err
		}
	}
	if q.Reason == QuarantineTooLarge && q.ContentKept {
		return invalid("a file too large is never kept")
	}
	if q.ReleasedFrom != "" {
		if err := master.ValidateID("released-from id", q.ReleasedFrom); err != nil {
			return err
		}
	}
	return master.ValidateTime("quarantine time", q.Quarantined)
}

// ValidateQuarantineBatch returns an Invalid error unless the batch is within
// MaxQuarantineBatch and every record is valid.
func ValidateQuarantineBatch(files []QuarantinedFile) error {
	if len(files) > MaxQuarantineBatch {
		return invalid("at most 1000 quarantined files are recorded at once")
	}
	for _, q := range files {
		if err := q.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// HeldBytes is what a record adds to the quarantine's held bytes: its size
// while its content is kept, nothing otherwise.
func (q QuarantinedFile) HeldBytes() int64 {
	if q.State == QuarantineHeld && q.ContentKept {
		return q.Size
	}
	return 0
}

// QuarantineFilter narrows ListQuarantined. A zero field keeps every record.
type QuarantineFilter struct {
	State          QuarantineState
	Reason         QuarantineReason
	Receiver       string
	ReceptionID    string
	StudyUID       string
	SOPInstanceUID string
	// Since and Until bound the quarantine time: at or after, and before.
	Since, Until time.Time
}

// Validate returns an Invalid error when f cannot be applied.
func (f QuarantineFilter) Validate() error {
	switch f.State {
	case "", QuarantineHeld, QuarantineReleased, QuarantineRejected:
	default:
		return invalid("a quarantine state is held, released or rejected")
	}
	if f.Reason != "" && !f.Reason.Valid() {
		return invalid("a quarantine reason is one of the contract's codes")
	}
	if f.Receiver != "" {
		if err := ValidateName("receiver", f.Receiver); err != nil {
			return err
		}
	}
	if f.ReceptionID != "" {
		if err := master.ValidateID("reception id", f.ReceptionID); err != nil {
			return err
		}
	}
	for _, u := range []struct{ what, v string }{{"Study Instance UID", f.StudyUID}, {"SOP Instance UID", f.SOPInstanceUID}} {
		if u.v != "" {
			if err := validateUID(u.what, u.v); err != nil {
				return err
			}
		}
	}
	return validateRange(f.Since, f.Until)
}

// ValidateRelease returns an Invalid error unless the arguments of
// ReleaseQuarantined are well formed.
func ValidateRelease(id string, mode ReleaseMode, receiver, receptionID string, now time.Time) error {
	if err := master.ValidateID("quarantine id", id); err != nil {
		return err
	}
	if mode != ReleaseAsIs && mode != ReleaseOverwrite {
		return invalid("a release's mode is as_is or overwrite")
	}
	if receiver != "" {
		if err := ValidateName("receiver", receiver); err != nil {
			return err
		}
	}
	if err := master.ValidateID("reception id", receptionID); err != nil {
		return err
	}
	return master.ValidateTime("release time", now)
}
