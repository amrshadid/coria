// Package engine de-identifies a DICOM data set by a profile: a pure
// function from a data set, an expanded profile version and a tenant's
// keys to the data set changed in place and a record of what was done to
// each element and why.
//
// The data set is the DICOM library's (go-dicom's dataset.Dataset), read
// and changed only through its exported calls; this package parses and
// encodes no DICOM. The profile comes from the profile language
// (internal/deid/profile) through one small boundary, Decider: the
// decision for an element (the action with its arguments, the rule, its
// origin and reason, the VRs its selector was checked for), the set rules
// and the templates.
//
// Every element is visited: the top level, every item of every sequence
// (to MaxDepth), every private block, whose creator is resolved in the
// data set the element is in and compared exactly once its padding is
// removed. A private creator element follows its block: it is kept when an
// element of its block is, and removed otherwise. A content item is any
// item with a Value Type (0040,A040), wherever it sits, and its elements
// carry its concept name and value type to the profile's content item
// rules. Some elements are treated by fixed rules whatever the profile
// says: group lengths and file meta information are removed (written anew
// with the file); pixel data is kept, since only pixel rules change it and
// this engine applies none; Original Attributes, Encrypted Attributes,
// digital signatures, MAC parameters and trailing padding are removed
// (PS3.15 E.1.1); and Specific Character Set (0008,0005) is removed at
// every depth and written once at the top level after every change:
// ISO_IR 192 when any text left is outside the default repertoire, none
// otherwise. The library holds the text it decoded in UTF-8; text under
// another declaration, or that is not UTF-8, refuses the file.
//
// The floor (floor.go). The attributes that name or identify the person
// (Patient's Name and ID, other IDs, birth date and time, addresses and
// telephone numbers, and every element of VR PN) are never kept, whatever
// the profile decides: an action that would keep one becomes
// remove-unless-required with a dummy. Their values are always needles.
//
// Clean removes the file's needles from free text: each value, each part
// of a name (of one or two letters only as a whole word; of a script
// written without spaces at any length) and each date in its common
// written forms, in any case, rune by rune, again until nothing more is
// found. Its work for a file is bounded, past which the file is refused.
// A value of VR UN the engine cannot see into (one that begins with an
// item, or not in memory) is never kept: it refuses the file.
//
// Keyed actions. A UID is remapped to "2.25." and a version 8 UUID from
// HMAC-SHA-256 of it under the tenant's UID key (RemapUID); a UID the
// standard registers is kept (Registered). A date is moved back by the
// subject's shift, from 1 to the profile's most days, keyed by the
// tenant's date key over the Issuer of Patient ID and the Patient ID
// (ShiftDays); the time of day is kept, and the shift is written nowhere.
// A hash and a template's "{#X}" are the naming rule's keyed hash of the
// value (naming.HashText) under the tenant's naming key, so a hashed
// Patient ID equals the subject's label code.
//
// Fails closed. Everything is worked out before the data set is changed:
// a file the profile cannot process returns a *Failure with a fixed Cause,
// and the data set is left as it was, never half processed; a panic is
// recovered as a Failure, the changes made so far undone; a lookup table
// assigns a code only once the plan has succeeded. A value an action
// writes is checked against its VR (PS3.5 6.2) first.
//
// What happens to an element whose VR in the file is not one its rule was
// checked for (the VRs validation stored with the rule, never this
// build's dictionary): keep, remove, empty, dummy and the IOD-dependent
// removal apply to any VR; an action that reads or writes the value
// refuses the file. An element the file holds as UN is treated as the
// data dictionary's VR for a standard tag.
//
// PS3.15 Table E.1-1's codes and their resolution by an attribute's Type
// in the IOD of the file's SOP class are here (Resolve, Types, IODTypes);
// the table itself comes from the DICOM library, and no copy of it is
// kept in this package.
//
// The package does no I/O but through the Lookups the caller passes, reads
// no clock and no random source: the same file under the same profile and
// keys always comes out byte for byte the same.
package engine
