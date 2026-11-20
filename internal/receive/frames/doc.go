// Package frames is the frame tables of a scan's metadata sidecar: where
// each frame of an instance's pixel data lies in its file (Table), recorded
// when the instance is archived, and the pass that gives them to the scans
// archived before tables existed (Run).
//
// # The table
//
// go-dicom divides pixel data into frames (filereader.DICOMFile.FrameRanges)
// and package header hands its answer here as byte ranges of the file. For
// encapsulated pixel data (PS3.5 A.4) a frame is the contents of its
// fragments joined, so its ranges are those contents, without the item
// headers between them: what a WADO-RS request for a frame as stored is
// answered with (PS3.18 8.7.3.3.2). For native pixel data a frame is one
// range of Rows x Columns x Samples per Pixel samples. Each frame's
// SHA-256 is recorded beside its ranges, so a frame answered by range is
// checked as it is sent. A file whose frames
// are not byte ranges of it gets an empty table, so a reader knows it was
// looked at and reads the file's header for such a frame instead.
//
// # The pass
//
// A sidecar written before frame tables is of version 1 (package sidecar);
// one whose every line with pixel data has its table is of version 2. The
// pass lists the tenant's scans in label order, reads the first bytes of
// each scan's sidecar, and for one of version 1 reads, by bounded ranged
// reads of the storage (package readat), the header and the pixel data's
// item headers of each instance whose line has no table, then the file
// once more, streamed through one buffer, for each frame's checksum and
// the file's own, which must be the line's (Checksums), and rewrites the
// sidecar through sidecar.Complete with each line given its table and its
// checksums. The rewrite is a Put of the whole object, which a reader
// sees whole or not at all, made under the same lock the archive step takes
// for the scan, so a reception writing the scan at the same time loses
// nothing and is not lost.
//
// Its state is the sidecars themselves: a scan done is version 2, and a
// scan not done is version 1. Nothing is kept in memory that a restart
// needs: a pass stopped midway (a crash, a stop of the server) loses the
// work of the scan it was on, at most one batch of its instances, and the
// next pass, at the next start, reads the first bytes of each scan again
// and goes on from the first scan not done. A pass that finds every scan
// done changes nothing: running it again is reading one small range per
// scan. A batch of a scan's instances too large to hold at once is written
// with sidecar.Merge, which keeps the sidecar at version 1, and the last
// batch with sidecar.Complete, so a scan is version 2 only when all of it
// is done.
//
// It never starves reception: it works one instance at a time and waits
// after each for as long as the instance took, and for many times that
// while the pipeline has a step in flight or files arriving (Config.Busy).
// Its progress is reported as events (tenant.FramesAdded): one per scan
// rewritten, and one when a pass that rewrote any ends.
//
// # What this package must never do
//
// It must never hold a whole instance file in memory, never give a frame a
// checksum read from a file that is not the one its line names, never
// write a sidecar but through package sidecar, never rewrite a scan's
// sidecar without the scan's lock, never decide a frame's division itself (go-dicom does), and never
// put a value read from a file in an error, a log line or an event: events
// carry the scan's labels and counts only.
package frames
