// Package checkedread reads the bytes the archive holds and checks them
// against what the catalog records as they go.
//
// An instance's manifest holds its length and SHA-256. A read that passes
// bytes on without comparing them would spread damage the archive already
// knows about, so every reader of archived bytes (the data service of the
// API, the DICOMweb retrievals and a sender that passes instances to a peer)
// goes through the same three pieces:
//
//   - CopyChecked passes the bytes of one instance in pieces of a fixed size
//     and holds back the last piece until the count and the SHA-256 both
//     agree, so a body that differs from its manifest never reaches its
//     reader whole.
//   - Slots bounds how many reads one caller and the whole server hold open,
//     since each holds a storage's connection or file and two buffers.
//   - Stores opens the storages a read reaches, each once, for the tenant
//     whose pipeline it holds, and closes them at its end.
//
// The package knows nothing of HTTP, gRPC or DICOM: callers decide what to
// do with a mismatch.
package checkedread
