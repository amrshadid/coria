package tenant

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// EventType is the kind of an event, the API contract's EventType in
// lowercase without its prefix. Its text is what a store keeps.
type EventType string

// The event types.
const (
	// EventAssociationFailed is a receiver turning callers away.
	EventAssociationFailed EventType = "association_failed"
	// EventReceptionStep is a reception reaching a step.
	EventReceptionStep EventType = "reception_step"
	// EventFileQuarantined is a file quarantined.
	EventFileQuarantined EventType = "file_quarantined"
	// EventQuarantineDecided is a quarantined file released or rejected.
	EventQuarantineDecided EventType = "quarantine_decided"
	// EventReceiverHealth is a receiver's state changing.
	EventReceiverHealth EventType = "receiver_health"
	// EventStorageSpace is a storage becoming nearly full or recovering.
	EventStorageSpace EventType = "storage_space"
)

// EventTypes returns every event type, in the contract's order.
func EventTypes() []EventType {
	return []EventType{EventAssociationFailed, EventReceptionStep, EventFileQuarantined, EventQuarantineDecided,
		EventReceiverHealth, EventStorageSpace}
}

// Valid reports whether t is one of EventTypes.
func (t EventType) Valid() bool { return slices.Contains(EventTypes(), t) }

// ServerEventType reports whether events of type t are the server's to
// report through AppendEvents: what is not a row of the pipeline. The
// others are written by the store with the change they report.
func ServerEventType(t EventType) bool {
	return t == EventAssociationFailed || t == EventReceiverHealth || t == EventStorageSpace
}

// ValidateEventTypes returns an Invalid error unless types is a filter a
// read accepts: at most MaxEventTypes, each one EventTypes lists.
func ValidateEventTypes(types []EventType) error {
	if len(types) > MaxEventTypes {
		return invalid("a filter names at most " + strconv.Itoa(MaxEventTypes) + " event types")
	}
	for _, t := range types {
		if !t.Valid() {
			return invalid("an event type is one of association_failed, reception_step, file_quarantined, quarantine_decided, receiver_health, storage_space")
		}
	}
	return nil
}

// EventPayload is what an event says happened: one of the payload types of
// this file, each holding exactly what the API contract's message of its
// type holds. A payload carries the archive's labels, ids, names, counts and
// words from closed lists; never a UID, an attribute's value or anything else
// read from a file.
type EventPayload interface {
	// EventType is the type of the events that carry it.
	EventType() EventType
	// validate returns an Invalid error unless every field has its form.
	validate() error
}

// EventRecord is an event as a store writes it: its type, its time, and its
// payload in the one form every driver stores (JSON, with the fields in the
// order of their struct). Only NewEvent and the functions that call it make
// one, so a store writes nothing that was not validated, and every driver
// writes the same bytes for the same event.
type EventRecord struct {
	typ     EventType
	at      time.Time
	payload string
}

// Type returns the event's type.
func (r EventRecord) Type() EventType { return r.typ }

// Time returns the time of the change it reports.
func (r EventRecord) Time() time.Time { return r.at }

// Payload returns its payload as stored.
func (r EventRecord) Payload() string { return r.payload }

// Validate returns an Invalid error unless r was made by NewEvent.
func (r EventRecord) Validate() error {
	if !r.typ.Valid() || r.payload == "" {
		return invalid("an event is made by the tenant package's event functions")
	}
	return master.ValidateTime("event time", r.at)
}

// NewEvent validates p and returns the event that carries it, at the time
// given.
func NewEvent(p EventPayload, at time.Time) (EventRecord, error) {
	if p == nil {
		return EventRecord{}, invalid("an event has a payload")
	}
	if err := master.ValidateTime("event time", at); err != nil {
		return EventRecord{}, err
	}
	if h, ok := p.(*ReceiverHealthChanged); ok {
		// A time is written as JSON in its location and to the nanosecond;
		// held to UTC and microseconds, as every time a store keeps, the
		// same health makes the same bytes whatever clock made it.
		c := *h
		c.Since = c.Since.UTC().Truncate(time.Microsecond)
		p = &c
	}
	if err := p.validate(); err != nil {
		return EventRecord{}, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return EventRecord{}, fault.Wrap(fault.Internal, err, "an event's payload cannot be written")
	}
	return EventRecord{typ: p.EventType(), at: at, payload: string(b)}, nil
}

// DecodeEvent reads a payload a store kept for an event of type t, ignoring
// fields this version does not know, so a later version may add fields to a
// type. It never fails: a type this version does not know answers nil and
// true, and a payload of a known type that cannot be read back as valid (not
// JSON, a value outside this version's lists, a field the type requires
// missing) answers nil and false, which a store counts on the page
// (EventPage.Unreadable). A reader skips both and takes their cursor, so one
// row never stops a feed.
func DecodeEvent(t EventType, payload string) (EventPayload, bool) {
	var p EventPayload
	switch t {
	case EventAssociationFailed:
		p = &AssociationFailed{}
	case EventReceptionStep:
		p = &ReceptionStepReached{}
	case EventFileQuarantined:
		p = &FileQuarantined{}
	case EventQuarantineDecided:
		p = &QuarantineDecided{}
	case EventReceiverHealth:
		p = &ReceiverHealthChanged{}
	case EventStorageSpace:
		p = &StorageSpaceChanged{}
	default:
		return nil, true
	}
	if err := json.Unmarshal([]byte(payload), p); err != nil || p.validate() != nil {
		return nil, false
	}
	return p, true
}

// ReceptionStepReached is a reception reaching a step, the contract's
// message of that name.
type ReceptionStepReached struct {
	// Reception is the reception's id.
	Reception string `json:"reception"`
	// Receiver names the receiver it arrived at.
	Receiver string `json:"receiver"`
	// Arrival is its arrival number.
	Arrival int `json:"arrival"`
	// Step is the step it reached.
	Step reception.State `json:"step"`
	// PreviousStep is the step it left; empty for a reception's first
	// step (receiving, or complete for one made by RetryReception).
	PreviousStep reception.State `json:"previous_step"`
	// Counts are its totals when it reached the step.
	Counts EventCounts `json:"counts"`
	// SubjectLabel and SessionLabel are its labels, empty before it is
	// identified.
	SubjectLabel string `json:"subject_label"`
	// SessionLabel is its session's label, empty before it is identified.
	SessionLabel string `json:"session_label"`
	// RetryOf is the failed reception it runs again, empty unless it was
	// made by RetryReception.
	RetryOf string `json:"retry_of"`
}

// EventCounts are a reception's totals as an event carries them, the
// contract's ReceptionCounts. Its fields are ReceptionCounts' own, in the
// same order, so one converts to the other and a count added to one and
// not the other does not compile.
type EventCounts struct {
	FilesReceived    int64 `json:"files_received"`
	BytesReceived    int64 `json:"bytes_received"`
	FilesArchived    int64 `json:"files_archived"`
	BytesArchived    int64 `json:"bytes_archived"`
	FilesDuplicate   int64 `json:"files_duplicate"`
	BytesDuplicate   int64 `json:"bytes_duplicate"`
	FilesQuarantined int64 `json:"files_quarantined"`
	BytesQuarantined int64 `json:"bytes_quarantined"`
	FilesReleased    int64 `json:"files_released"`
}

// EventType implements EventPayload.
func (*ReceptionStepReached) EventType() EventType { return EventReceptionStep }

// validate implements EventPayload.
func (p *ReceptionStepReached) validate() error {
	if err := master.ValidateID("reception id", p.Reception); err != nil {
		return err
	}
	if err := ValidateName("receiver", p.Receiver); err != nil {
		return err
	}
	if p.Arrival < 1 {
		return invalid("an arrival number is at least 1")
	}
	if !knownState(p.Step) || (p.PreviousStep != "" && !knownState(p.PreviousStep)) {
		return invalid("a step is one of " + statesText())
	}
	c := p.Counts
	for _, n := range []int64{c.FilesReceived, c.BytesReceived, c.FilesArchived, c.BytesArchived, c.FilesDuplicate,
		c.BytesDuplicate, c.FilesQuarantined, c.BytesQuarantined, c.FilesReleased} {
		if n < 0 {
			return invalid("a reception's counts are not negative")
		}
	}
	for _, l := range []string{p.SubjectLabel, p.SessionLabel} {
		if l != "" && !label.Valid(l) {
			return invalid("an event's labels are valid labels")
		}
	}
	if p.RetryOf != "" {
		return master.ValidateID("retried reception id", p.RetryOf)
	}
	return nil
}

// ReceptionStepEvent returns the event of a reception as it is after a
// step from the state previous (empty for its first), at the step's time.
// Its study's UID is not in it.
func ReceptionStepEvent(previous reception.State, r Reception) (EventRecord, error) {
	return NewEvent(&ReceptionStepReached{
		Reception: r.ID, Receiver: r.Receiver, Arrival: r.Arrival, Step: r.State, PreviousStep: previous, Counts: EventCounts(r.Counts),
		SubjectLabel: r.SubjectLabel, SessionLabel: r.SessionLabel, RetryOf: r.RetryOf,
	}, r.StepTime)
}

// FileQuarantined is one file quarantined, the contract's message of that
// name.
type FileQuarantined struct {
	// File is the record's id.
	File string `json:"file"`
	// Reception is its reception's id, empty when it had none.
	Reception string `json:"reception"`
	// Receiver names the receiver it arrived at.
	Receiver string `json:"receiver"`
	// Reason is why it was quarantined.
	Reason QuarantineReason `json:"reason"`
	// SizeBytes is its size.
	SizeBytes int64 `json:"size_bytes"`
	// HeldFiles and HeldBytes are the quarantine's totals with it.
	HeldFiles int64 `json:"held_files"`
	// HeldBytes are the bytes kept for the files held, as
	// QuarantineTotals counts them.
	HeldBytes int64 `json:"held_bytes"`
}

// EventType implements EventPayload.
func (*FileQuarantined) EventType() EventType { return EventFileQuarantined }

// validate implements EventPayload.
func (p *FileQuarantined) validate() error {
	if err := master.ValidateID("quarantine id", p.File); err != nil {
		return err
	}
	if p.Reception != "" {
		if err := master.ValidateID("reception id", p.Reception); err != nil {
			return err
		}
	}
	if err := ValidateName("receiver", p.Receiver); err != nil {
		return err
	}
	if !p.Reason.Valid() {
		return invalid("a quarantine reason is one of the contract's codes")
	}
	if p.SizeBytes < 0 || p.HeldFiles < 1 || p.HeldBytes < 0 {
		return invalid("a quarantined file's size and totals are not negative, and the files held count it")
	}
	return nil
}

// FileQuarantinedEvent returns the event of a record just held, with the
// quarantine's totals after it, at its quarantine time.
func FileQuarantinedEvent(q QuarantinedFile, heldFiles, heldBytes int64) (EventRecord, error) {
	return NewEvent(&FileQuarantined{File: q.ID, Reception: q.ReceptionID, Receiver: q.Receiver, Reason: q.Reason,
		SizeBytes: q.Size, HeldFiles: heldFiles, HeldBytes: heldBytes}, q.Quarantined)
}

// QuarantineDecided is a quarantined file released or rejected, the
// contract's message of that name.
type QuarantineDecided struct {
	// File is the record's id.
	File string `json:"file"`
	// State is what was decided: released or rejected.
	State QuarantineState `json:"state"`
	// ReleaseMode is how it was released; empty for a rejection.
	ReleaseMode ReleaseMode `json:"release_mode"`
	// ReleaseReception is the reception its release joined; empty for a
	// rejection, and for a release of a file that joined none.
	ReleaseReception string `json:"release_reception"`
	// HeldFiles and HeldBytes are the quarantine's totals after it.
	HeldFiles int64 `json:"held_files"`
	// HeldBytes are the bytes kept for the files held after it.
	HeldBytes int64 `json:"held_bytes"`
}

// EventType implements EventPayload.
func (*QuarantineDecided) EventType() EventType { return EventQuarantineDecided }

// validate implements EventPayload.
func (p *QuarantineDecided) validate() error {
	if err := master.ValidateID("quarantine id", p.File); err != nil {
		return err
	}
	switch p.State {
	case QuarantineReleased:
		if p.ReleaseMode != ReleaseAsIs {
			return invalid("a release's mode is as_is")
		}
		if p.ReleaseReception != "" {
			if err := master.ValidateID("reception id", p.ReleaseReception); err != nil {
				return err
			}
		}
	case QuarantineRejected:
		if p.ReleaseMode != "" || p.ReleaseReception != "" {
			return invalid("a rejection has no release mode or reception")
		}
	default:
		return invalid("a decision is released or rejected")
	}
	if p.HeldFiles < 0 || p.HeldBytes < 0 {
		return invalid("the quarantine's totals are not negative")
	}
	return nil
}

// QuarantineDecidedEvent returns the event of a record just released or
// rejected, with the quarantine's totals after it, at its decision time.
func QuarantineDecidedEvent(q QuarantinedFile, heldFiles, heldBytes int64) (EventRecord, error) {
	return NewEvent(&QuarantineDecided{File: q.ID, State: q.State, ReleaseMode: q.ReleaseMode,
		ReleaseReception: q.ReleaseReception, HeldFiles: heldFiles, HeldBytes: heldBytes}, q.Decided)
}

// AssociationFailure is why a receiver turned a caller away, the contract's
// AssociationFailure in lowercase without its prefix.
type AssociationFailure string

// The failures.
const (
	FailureCallerNotAllowed           AssociationFailure = "caller_not_allowed"
	FailureCalledAETitleNotRecognized AssociationFailure = "called_ae_title_not_recognized"
	FailureNoAcceptableContext        AssociationFailure = "no_acceptable_context"
	FailureNearlyFull                 AssociationFailure = "nearly_full"
	FailureTenantNotReady             AssociationFailure = "tenant_not_ready"
	FailureTLSFailed                  AssociationFailure = "tls_failed"
	FailureProtocolError              AssociationFailure = "protocol_error"
	FailureIncomingLimit              AssociationFailure = "incoming_limit"
)

// associationFailures is the closed list of failures.
var associationFailures = []AssociationFailure{FailureCallerNotAllowed, FailureCalledAETitleNotRecognized,
	FailureNoAcceptableContext, FailureNearlyFull, FailureTenantNotReady, FailureTLSFailed, FailureProtocolError,
	FailureIncomingLimit}

// GroupCaller is who called, in an event that groups connections: the AE
// titles when they were read, and the host's address without its port.
type GroupCaller struct {
	// CallingAETitle is the caller's AE title, empty when not read.
	CallingAETitle string `json:"calling_ae_title"`
	// CalledAETitle is the AE title it called, empty when not read.
	CalledAETitle string `json:"called_ae_title"`
	// Address is the host's address alone, as "192.0.2.7" or
	// "2001:db8::7".
	Address string `json:"address"`
}

// AssociationFailed is a receiver turning callers away, grouped by
// receiver, failure and host as the contract's message of that name says;
// the grouping is the server's, which appends the events.
type AssociationFailed struct {
	// Receiver names the receiver.
	Receiver string `json:"receiver"`
	// Caller is who called; nil for the event that folds the failures of
	// many hosts together.
	Caller *GroupCaller `json:"caller,omitempty"`
	// Failure is why the caller was turned away.
	Failure AssociationFailure `json:"failure"`
	// Count is how many failures the event stands for, at least 1.
	Count int64 `json:"count"`
	// Hosts is how many distinct hosts it stands for: 1 with a caller.
	Hosts int64 `json:"hosts"`
}

// EventType implements EventPayload.
func (*AssociationFailed) EventType() EventType { return EventAssociationFailed }

// validate implements EventPayload.
func (p *AssociationFailed) validate() error {
	if err := ValidateName("receiver", p.Receiver); err != nil {
		return err
	}
	if !slices.Contains(associationFailures, p.Failure) {
		return invalid("an association's failure is one of the contract's AssociationFailure values")
	}
	if p.Count < 1 || p.Hosts < 1 {
		return invalid("a failed association's count and hosts are at least 1")
	}
	if c := p.Caller; c != nil {
		if p.Hosts != 1 {
			return invalid("an event with a caller stands for one host")
		}
		for _, ae := range []string{c.CallingAETitle, c.CalledAETitle} {
			if ae != "" {
				if err := ValidateAETitle(ae); err != nil {
					return err
				}
			}
		}
		a, err := netip.ParseAddr(c.Address)
		if err != nil || a.Zone() != "" || a.String() != c.Address {
			return invalid("a grouped caller's address is an IP address without its port, written as Go writes it")
		}
	}
	return nil
}

// validate implements EventPayload.
func (p *ReceiverHealthChanged) validate() error {
	if err := ValidateName("receiver", p.Receiver); err != nil {
		return err
	}
	switch p.State {
	case ReceiverStarting, ReceiverUp:
		if p.DownReason != "" || p.Detail != "" {
			return invalid("a receiver that is not down has no down reason and no detail")
		}
	case ReceiverDown:
		phrases, ok := healthPhrases[p.DownReason]
		if !ok {
			return invalid("a down receiver's reason is one of the contract's")
		}
		if p.Detail != "" && !slices.Contains(phrases, p.Detail) {
			return invalid("a health's detail is one of the server's phrases for its reason")
		}
	default:
		return invalid("a receiver's state is starting, up or down")
	}
	return master.ValidateTime("health time", p.Since)
}

// StorageSpaceState is whether a storage has room, the contract's
// StorageSpaceState in lowercase without its prefix.
type StorageSpaceState string

// The space states.
const (
	StorageNearlyFull StorageSpaceState = "nearly_full"
	StorageRecovered  StorageSpaceState = "recovered"
)

// StorageSpaceChanged is a storage becoming nearly full or recovering, the
// contract's message of that name.
type StorageSpaceChanged struct {
	// Storage names the storage.
	Storage string `json:"storage"`
	// State is whether it is nearly full or recovered.
	State StorageSpaceState `json:"state"`
	// UsedBytes are its used bytes then.
	UsedBytes int64 `json:"used_bytes"`
	// FreeBytes are its free bytes then; nil when unknown.
	FreeBytes *int64 `json:"free_bytes,omitempty"`
}

// EventType implements EventPayload.
func (*StorageSpaceChanged) EventType() EventType { return EventStorageSpace }

// validate implements EventPayload.
func (p *StorageSpaceChanged) validate() error {
	if err := ValidateName("storage", p.Storage); err != nil {
		return err
	}
	if p.State != StorageNearlyFull && p.State != StorageRecovered {
		return invalid("a storage's space is nearly_full or recovered")
	}
	if p.UsedBytes < 0 || (p.FreeBytes != nil && *p.FreeBytes < 0) {
		return invalid("a storage's bytes are not negative")
	}
	return nil
}

// ValidateEventAppends returns an Invalid error unless the batch is within
// MaxEventBatch and each event is one the server reports, made by NewEvent,
// with a well formed key.
func ValidateEventAppends(events []EventAppend) error {
	if len(events) > MaxEventBatch {
		return invalid("at most " + strconv.Itoa(MaxEventBatch) + " events are appended at once")
	}
	for _, e := range events {
		if err := master.ValidateID("event key", e.Key); err != nil {
			return err
		}
		if err := e.Event.Validate(); err != nil {
			return err
		}
		if !ServerEventType(e.Event.Type()) {
			return invalid("an event of type " + string(e.Event.Type()) + " is written with the change it reports, not appended")
		}
	}
	return nil
}

// ValidateEventRead returns an Invalid error unless the filter and limit of
// a read are well formed.
func ValidateEventRead(types []EventType, limit int) error {
	if limit < 1 || limit > MaxEventPage {
		return invalid("a read answers 1 to " + strconv.Itoa(MaxEventPage) + " events")
	}
	return ValidateEventTypes(types)
}

// ValidatePrune returns an Invalid error unless the arguments of
// PruneEvents are well formed.
func ValidatePrune(before time.Time, limit int) error {
	if limit < 1 || limit > MaxPruneBatch {
		return invalid("a pruning removes 1 to " + strconv.Itoa(MaxPruneBatch) + " events")
	}
	return master.ValidateTime("pruning time", before)
}

// InProgressSteps returns, from a count of receptions per step, the entries
// of the steps that have not ended and have any, in the order of
// reception.States: what a status lists.
func InProgressSteps(counts map[reception.State]int64) []StepCount {
	var out []StepCount
	for _, s := range reception.States() {
		if n := counts[s]; n > 0 && !s.Ended() {
			out = append(out, StepCount{Step: s, Receptions: n})
		}
	}
	return out
}
