package tenant

import (
	"slices"
	"strconv"
	"time"

	"github.com/amrshadid/magnetar/internal/core/naming"
	"github.com/amrshadid/magnetar/internal/core/uid"
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

// The fault words a quarantine record's details carry under "fault", the API
// contract's, each listed with the reasons that use it (the label conflict
// faults are in catalog.go).
const (
	// FaultMissing, FaultEmpty and FaultBadUID: an attribute absent, empty,
	// or not a UID (QuarantineBadUID, QuarantineBadLabel).
	FaultMissing = "missing"
	FaultEmpty   = "empty"
	FaultBadUID  = "bad_uid"
	// FaultTooLong, FaultBadCharacter, FaultNoMatch and FaultBadLabel: a
	// value too long for a label, one with a character a label may not
	// hold, a pattern that does not match, a label made that is not valid
	// (QuarantineBadLabel).
	FaultTooLong      = "too_long"
	FaultBadCharacter = "bad_character"
	FaultNoMatch      = "no_match"
	FaultBadLabel     = "bad_label"
	// The storage faults (QuarantineStorageRefused), the contract's
	// StorageFault values in lowercase without their prefix.
	FaultUnreachable       = "unreachable"
	FaultTLSFailed         = "tls_failed"
	FaultRefused           = "refused"
	FaultNotFound          = "not_found"
	FaultWrongAnswer       = "wrong_answer"
	FaultAddressNotAllowed = "address_not_allowed"
)

// reasonDetails are the detail keys each reason may carry, the contract's,
// each with the check of its value. Every value is from a closed list, a
// number, a UID as received, a storage's name, a checksum, or an
// attribute's keyword or tag as the naming package parses it: nothing a
// file's value could be written into, so a detail never carries one.
var reasonDetails = map[QuarantineReason]map[string]func(string) bool{
	QuarantineNotDICOM:            {"offset": isNumber},
	QuarantineSOPClassNotAccepted: {"sop_class_uid": isUID},
	QuarantineTooLarge:            {"limit": isNumber},
	QuarantineBadUID: {
		"attribute": oneOf("SOPInstanceUID", "SOPClassUID", "StudyInstanceUID", "SeriesInstanceUID"),
		"fault":     oneOf(FaultMissing, FaultEmpty, FaultBadUID),
	},
	QuarantineBadLabel: {
		"label":     oneOf("subject", "session", "scan"),
		"attribute": func(v string) bool { return v == "" || isAttribute(v) },
		"fault":     oneOf(FaultMissing, FaultEmpty, FaultTooLong, FaultBadCharacter, FaultBadUID, FaultNoMatch, FaultBadLabel),
	},
	QuarantineLabelConflict: {
		"label": oneOf("subject", "session", "scan"),
		"fault": oneOf(FaultHeldByAnotherSession, FaultTwoSubjectsForOneStudy, FaultHeldByAnotherScan, FaultCaseCollision),
	},
	QuarantineDifferentFile:    {},
	QuarantineUIDElsewhere:     {"study_instance_uid": isUID, "series_instance_uid": isUID},
	QuarantineStorageRefused:   {"storage": isName, "fault": oneOf(FaultUnreachable, FaultTLSFailed, FaultRefused, FaultNotFound, FaultWrongAnswer, FaultAddressNotAllowed)},
	QuarantineReadBackMismatch: {"storage": isName, "read_sha256": isHex64},
}

// oneOf returns a check that a value is one of words.
func oneOf(words ...string) func(string) bool {
	return func(v string) bool { return slices.Contains(words, v) }
}

// isNumber reports whether v is a decimal count of at most 20 digits.
func isNumber(v string) bool {
	return v != "" && len(v) <= 20 && allBytes(v, func(b byte) bool { return b >= '0' && b <= '9' })
}

// isUID reports whether v is a UID (PS3.5 9.1).
func isUID(v string) bool { return uid.Valid(v) }

// isName reports whether v follows the tenant name rule.
func isName(v string) bool { return ValidateName("storage", v) == nil }

// isHex64 reports whether v is a SHA-256 in lowercase hexadecimal.
func isHex64(v string) bool {
	return len(v) == 64 && allBytes(v, func(b byte) bool { return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') })
}

// isAttribute reports whether v is an attribute a naming rule may name: a
// keyword of the naming package's table or a tag written (gggg,eeee),
// checked by the naming package's own parser.
func isAttribute(v string) bool {
	_, err := naming.ParseAttribute(v)
	return err == nil
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

// ReleaseAsIs runs it by the pipeline's rules now, the one mode of this
// version. Replacing an archived instance with a released file deletes
// archived data, which needs a recorded reason and proof of the copy that
// replaces it; it will be an operation of its own, not a mode of release.
const ReleaseAsIs ReleaseMode = "as_is"

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
		check, ok := allowed[k]
		if !ok {
			return invalid("the detail " + strconv.Quote(k) + " is not one the reason " + string(q.Reason) + " carries")
		}
		if !check(v) {
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
	if mode != ReleaseAsIs {
		return invalid("a release's mode is as_is")
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
