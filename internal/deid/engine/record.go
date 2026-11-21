package engine

import (
	"fmt"
	"strconv"
)

// Cause is the fixed reason a file could not be de-identified. A program
// tests it; a person reads Failure's text, which never holds a value of the
// file.
type Cause string

// The causes. Each is a closed word the quarantine records as the detail of
// its reason, so a list of held files can be grouped by it.
const (
	// CauseNoSubject: a date had to be shifted and the file has no Patient
	// ID (0010,0020) to find its subject's offset by.
	CauseNoSubject Cause = "no_subject"
	// CauseLookupMissing: a lookup table holds no code for a value and the
	// rule says to quarantine.
	CauseLookupMissing Cause = "lookup_missing"
	// CauseBadValue: a value the action must read or write is not one of
	// its VR (PS3.5 6.2), is too long to process, is not in memory, or the
	// action does not fit the VR the file gives the element.
	CauseBadValue Cause = "bad_value"
	// CauseTooDeep: sequences nest deeper than MaxDepth.
	CauseTooDeep Cause = "too_deep"
	// CauseTooLarge: the data set holds more than MaxElements elements,
	// sequences' items included.
	CauseTooLarge Cause = "too_large"
	// CauseRuleQuarantine: a rule whose action is quarantine applies to an
	// element the file holds; Failure.Rule names it.
	CauseRuleQuarantine Cause = "rule"
	// CauseInternal: the engine itself failed on the file (a panic,
	// recovered), and every change it had made was undone. The file is
	// held like any other refusal, so a fault of the code is never a file
	// archived half de-identified.
	CauseInternal Cause = "internal"
)

// The reasons a quarantine records for a failure.
const (
	// ReasonFailed is the reason of every cause but a rule's quarantine.
	ReasonFailed = "deid_failed"
	// ReasonRefused is the reason of a rule's quarantine.
	ReasonRefused = "profile_refused"
)

// Failure is a file the profile cannot process. It is never a fault of the
// server: the same file under the same profile fails the same way, and the
// pipeline holds it in quarantine with Reason and Detail. Its text names
// where in the data set and which rule, never a value.
type Failure struct {
	// Cause is why.
	Cause Cause
	// Path is the element where it failed, as Entry.Path writes it; empty
	// when the failure is of the whole file.
	Path string
	// Rule is the name of the rule whose action failed or quarantined,
	// or its position ("rule 3") when it has no name; empty for a default.
	Rule string
}

// Error says what failed, where and by which rule.
func (f *Failure) Error() string {
	s := "deid: " + string(f.Cause)
	if f.Path != "" {
		s += " at " + f.Path
	}
	if f.Rule != "" {
		s += " (" + f.Rule + ")"
	}
	return s
}

// Reason returns the quarantine reason of the failure.
func (f *Failure) Reason() string {
	if f.Cause == CauseRuleQuarantine {
		return ReasonRefused
	}
	return ReasonFailed
}

// Detail returns the quarantine detail: the rule's name for a rule's
// quarantine, the cause otherwise.
func (f *Failure) Detail() string {
	if f.Cause == CauseRuleQuarantine {
		return f.Rule
	}
	return string(f.Cause)
}

// Entry is what happened to one element of the data set, and why.
type Entry struct {
	// Path is where the element is: its tag as eight hexadecimal digits,
	// after the sequences and item numbers it is inside, such as
	// "00081115[0]/0020000E".
	Path string
	// Tag is the element's tag.
	Tag uint32
	// VR is the element's value representation as the engine read it.
	VR string
	// Action is what was done: keep, remove, empty, replace, set, hash,
	// uid, shift, generalize, lookup, clean, dummy, remove_unless_required,
	// or create for an element a set rule made. An element the floor
	// covers and the profile would have kept has Rule -1 and a Reason that
	// says so.
	Action string
	// Rule is the position of the deciding rule in the expanded profile,
	// from 0; -1 when no rule decided.
	Rule int
	// RuleName is the deciding rule's name; empty when it has none or no
	// rule decided.
	RuleName string
	// Origin is the base the deciding rule came from; empty for the
	// profile's own rules and the defaults.
	Origin string
	// Reason is the rule's reason, or why the default or a fixed rule of
	// the engine applied.
	Reason string
	// Changed reports whether the element's value or presence changed.
	Changed bool
}

// NeedleKind is what a removed value was, which says how a later search
// for it may match its written forms.
type NeedleKind int

// The kinds of value.
const (
	// NeedleText is an identifier, as text.
	NeedleText NeedleKind = iota + 1
	// NeedleName is a person's name (VR PN), or one of its parts.
	NeedleName
	// NeedleDate is a date or a date and time (VR DA or DT).
	NeedleDate
	// NeedleUID is a UID (VR UI).
	NeedleUID
)

// Needle is a value the profile removed or replaced that can identify
// someone by itself (needles.go), kept in memory so the check after can
// search the de-identified file for it.
type Needle struct {
	// Kind is what the value was.
	Kind NeedleKind
	// Value is the value as received, its padding removed.
	Value string
	// Path is the element it was removed from.
	Path string
}

// Needles is the values a profile removed from a file. It holds patient
// data, so it is never printed: its text is only its count.
type Needles []Needle

// String returns the count, never a value.
func (n Needles) String() string { return strconv.Itoa(len(n)) + " values" }

// GoString returns the count, so %#v prints no value either.
func (n Needles) GoString() string { return n.String() }

// Format prints the count for every verb, so no formatting of a Record or
// of its needles reaches a value.
func (n Needles) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(n.String())) }

// Record is what the engine did to a file: an entry per element, in the
// order the data set is walked, and the values it removed.
type Record struct {
	// Entries lists every element of the data set as received, with what
	// was done to it, then each element a set rule made.
	Entries []Entry
	// Needles are the values removed or replaced. They stay in memory for
	// the check after and are never stored, logged or answered.
	Needles Needles
	// DatesShifted reports whether any date was moved by the subject's
	// shift. The shift itself is in no field: it is a secret of the subject.
	DatesShifted bool
	// PixelsUntouched is always true here: the engine changes no pixel
	// data. Whether an image may carry burned-in text is decided by the
	// pipeline's gate, not by this record.
	PixelsUntouched bool
}

// formatTag writes a tag as eight upper-case hexadecimal digits, the form a
// profile's selectors use.
func formatTag(t uint32) string { return fmt.Sprintf("%08X", t) }
