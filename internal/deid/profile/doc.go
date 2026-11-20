// Package profile is the language of de-identification profiles: a profile
// as data, its checks, its expansion into a frozen version, and the
// precedence by which one rule decides what happens to each element.
//
// A profile is the API's ProfileDocument, and its file form is that
// message's protobuf JSON (ParseDocument, FormatDocument), so a file, a
// request and a stored document are one thing. A document names bases,
// profiles the binary ships (Catalog, Shipped), and then its own rules: a
// selector, conditions and an action each, and pixel rules for images
// whose pixels may carry text.
//
// Checks. Expand checks every field of a document and answers every
// problem at once (Problems), each naming the rule by its position and
// name, the field by its path in the JSON names, and a fixed code a
// program can test. Nothing that cannot work is stored: a keyword the data
// dictionary does not know, a path through an element that is not a
// sequence, a regular expression that does not compile, an action that
// does not fit the VR its selector selects (a UID remapped in a date, a
// date shifted in a name), a constant that is not a value of its VR (PS3.5
// 6.2), a template that reads a value as received, a base that is not
// available. The elements no rule may name are refused: the file meta
// information, which is written anew for every file (PS3.10 7.1), items
// and group lengths, and pixel data, which only pixel rules change. There
// is no action that runs outside code or calls an address, since each would
// be a way out for the values a profile removes.
//
// Expansion. A version stores the document expanded: each base's rules
// copied in, its own bases first, then the document's rules; every keyword,
// in a selector, a path or a template, written as its tag; the defaults
// made explicit. Its canonical text (Canonical) has a SHA-256, the version's
// checksum. So a stored version never depends on what a later build ships:
// a new edition of a base is a new base name, and the data dictionary of a
// later library cannot change which element a stored rule names. The same
// document expands to the same text, and so the same checksum, in every
// run.
//
// Precedence. Of the rules whose selector matches an element and whose
// conditions all hold, the most specific selector decides (Tier: a path or
// a content item, then a tag or keyword, a private element, a private
// creator's block, a group, a VR); between equally specific ones the later
// rule, so a document's rules win over its bases', and a later base over an
// earlier, as an option of PS3.15 Annex E overrides the basic profile
// (PS3.15 E.3). An element no rule decides is removed when it is private
// (PS3.5 7.8), and otherwise follows the profile's unnamed default. Program
// decides this for the engine; matching an element reads only a Location
// and the Facts the conditions need, never a file.
//
// Versions. A Version is immutable: a change is a new version with the next
// number. Load reads a stored version and refuses one whose expanded text
// does not match its checksum.
//
// The bases of PS3.15 Annex E come from the standard's tables, which the
// DICOM library holds. Until it exports them, each Annex E base is in the
// catalog by name and refused as not available, rather than built from a
// copy of the standard kept here, which would drift from the library's.
// The composite bases built on them are refused for the same reason.
//
// The package does no I/O and reads no clock and no random source, so a
// profile means the same thing on every server and in every test.
package profile
