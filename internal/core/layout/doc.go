// Package layout is the one place that builds and parses the keys of a
// tenant's storage. No storage driver and no handler builds a key from a
// label itself, so a label reaches a storage only after this package has
// validated it.
//
// A storage holds two trees:
//
//	data/<subject>/<session>/<scan>/<SOP Instance UID>.dcm   one DICOM file per instance
//	meta/<subject>/<session>/<scan>/manifest.json            every file of the scan
//	meta/<subject>/<session>/<scan>/metadata.json            every instance's header
//
// The two trees are apart so every manifest of a storage can be found by
// listing meta/ alone, without paging past the instance files, which
// outnumber manifests by the number of instances in a scan; and a person
// can still read data/ as a tree of DICOM files that any tool opens.
//
// It is a package of its own, apart from the manifest that lists keys and
// the storage drivers that hold them, because both sides must agree on one
// form and the reverse (a key parsed back into its labels, as a listing or
// a rebuild of the catalog does) must refuse anything that form cannot
// produce. Parse refuses every key that does not build back to itself, byte
// for byte, so a key with a doubled slash, a different case of ".dcm", or a
// label that would not pass today is never taken as one of the archive's.
//
// # What this package must never do
//
// It must never do I/O, read the clock, or import anything of Magnetar's
// outside the core (.golangci.yml, depguard "core"). It must never return a
// key for parts it has not validated.
package layout
