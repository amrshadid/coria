// Package sidecar is the format of a scan's metadata sidecar: one file on the
// storage beside the scan's manifest (layout.MetadataKey) that holds, for
// each instance of the scan, its header as the DICOM JSON Model (PS3.18
// Annex F) without its bulk data, and where its pixel data starts in the
// instance's file and how long it is. With it a client can search and show a
// scan, and fetch the pixels of one frame by range, without opening a DICOM
// file.
//
// # The format
//
// A sidecar is JSON, written in one form only:
//
//	{"version":1,"instances":[
//	{"sop_instance_uid":"1.2.3","key":"data/S/E/C/1.2.3.dcm","size":1234,"sha256":"<64 hex>","transfer_syntax":"1.2.840.10008.1.2.1","pixel_data":{"offset":1100,"length":134},"dataset":{...}},
//	...
//	]}
//
// one instance per line, its fields in that order, the instances sorted by
// SOP Instance UID with each UID once, and a final newline. transfer_syntax
// is the file's Transfer Syntax UID and is left out of a line written before
// it existed. pixel_data is null for an instance without pixel data and for
// one whose data set is deflated (PS3.5 A.5), where an offset counted in the
// inflated bytes is not a place in the file; its offset is the first byte of
// the element's tag in the file and its length runs to the first byte after
// the element. dataset is the data set's DICOM JSON, its members in the
// order of their tags; when that text is longer than MaxDataset it is null
// and "dataset_omitted":true follows it, so one instance with a huge header
// cannot make the sidecar unbounded or keep its scan from being archived.
//
// Because the form is fixed and sorted, a sidecar is written and merged as a
// stream, one line at a time: memory follows the longest line, not the scan.
//
// # What this package must never do
//
// It must never do I/O beyond the reader and writer it is given, and never
// put a value of a data set in an error's text.
package sidecar
