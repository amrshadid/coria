package receive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/core/layout"
	"github.com/amrshadid/magnetar/internal/core/manifest"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/receive/sidecar"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// scanResult is what archiving one scan decided.
type scanResult struct {
	// kept counts the instances this reception holds in the scan after
	// the run: confirmed now or before.
	kept int
	// dupFiles and dupBytes count the files ignored as the same file the
	// tenant holds.
	dupFiles, dupBytes int64
	// claimed says another reception in flight claims some instances.
	claimed bool
}

// written is one file put on the storage.
type written struct {
	f   tenant.ReceivedFile
	key string
}

// archiveScan archives the files of one series into its scan. A file is
// written to the storage and read back before its catalog row is committed:
// the catalog never says an instance is held before its bytes on the storage
// were found to be the bytes received.
//
// Order of effects, each safe to repeat or to stop after:
//
//  1. Claim the files' instances (ClaimInstances, under the fence, in
//     batches). The answers decide each file, as the port documents: claimed
//     now or before, written; archived before, already done; the same file,
//     a duplicate; a different file or the UID elsewhere, held; claimed by
//     another reception, left for a later run. A claim run again answers
//     ClaimedBefore or ArchivedBefore and changes nothing. A file claimed
//     before that a held record of this reception keeps (a read-back
//     mismatch of an earlier run) is not written again: it stays held.
//  2. Hold the files the claims decided to hold. A record's id is made from
//     the reception and the file, so a run again writes nothing new. Until
//     HoldFiles takes a fence, that id is what keeps a worker whose lease
//     was taken over from writing a second record.
//  3. Put each file to be written to its layout key. A key already holding
//     an object (a run again) is replaced whole by the port's rule, with the
//     same bytes, so the object is what it was.
//  4. Read each one back from the storage as a stream through SHA-256 and
//     compare with the checksum it was received with. A mismatch, or an
//     object that is not there, holds the file with the read-back reason; it
//     keeps its incoming copy and goes no further. Its instance stays claimed
//     by this reception, unconfirmed, until the reception ends, when the
//     store releases every claim the reception did not confirm (by the
//     finish, quarantine or fail step). The object written under its key is
//     one no catalog row explains; it is not deleted (nothing is deleted
//     from a storage without a recorded reason and a checksum proving the
//     copy that replaces it), and a later correct file of the same instance,
//     by a retry or a release, is put to the same key and replaces it whole.
//  5. Put the scan's metadata sidecar, then its manifest, with the files that
//     read back equal: each made from what the storage holds for the scan,
//     every entry under a UID this reception claims taken out and its own
//     put in its place, each replaced whole by the port.
//  6. Commit the scan's catalog rows with the instances that read back equal
//     (CommitScan, under the fence, at most MaxScanCommit per call).
//
// A crash after 3, 4 or 5 and before 6 leaves objects on the storage that the
// catalog does not list: the data objects of instances this reception still
// claims (nobody else can write those keys, since nobody else holds the
// claim), and a manifest and sidecar that list them. The next run of this
// step, which the unit's lease guarantees, puts and reads back the same
// objects again and commits. If the reception fails instead, its claims are
// released and the objects stay unlisted until the retry of the reception
// writes them again.
//
// The sidecar and manifest are read, merged and replaced under a lock held
// by this process per storage and scan, so two receptions of one process
// that write one scan never lose each other's entries. Two processes on one
// tenant (a database with several writers) are not serialized here: the
// catalog then holds every instance, and only the manifest can miss one
// until the scan's next commit rewrites it.
func (p *Pipeline) archiveScan(ctx context.Context, w tenant.Work, r tenant.Reception, store blob.Store, path label.Path, g series, kept map[string]bool) (scanResult, error) {
	var res scanResult
	fence := w.Fence()
	var toWrite []tenant.ReceivedFile
	var holds []tenant.QuarantinedFile
	for start := 0; start < len(g.files); start += tenant.MaxClaimBatch {
		part := g.files[start:min(start+tenant.MaxClaimBatch, len(g.files))]
		claims := make([]tenant.InstanceClaim, len(part))
		for i, f := range part {
			claims[i] = tenant.InstanceClaim{SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SHA256: f.SHA256}
		}
		p.at("archive.before-claim")
		answers, err := p.store.ClaimInstances(ctx, r.ID, claims, p.now(), &fence)
		if err != nil {
			return res, err
		}
		p.at("archive.after-claim")
		if len(answers) != len(part) {
			return res, fault.New(fault.Internal, "a claim answered another number of files than it was given")
		}
		for i, d := range answers {
			f := part[i]
			switch d.Kind {
			case tenant.ClaimedNow, tenant.ClaimedBefore:
				if !kept[f.Incoming] {
					toWrite = append(toWrite, f)
				}
			case tenant.ArchivedBefore:
				res.kept++
			case tenant.SameFile:
				res.dupFiles++
				res.dupBytes += f.Size
			case tenant.DifferentFile:
				q := p.holdRecord(r, f, tenant.QuarantineDifferentFile, nil)
				q.KeptSHA256 = d.HeldSHA256
				holds = append(holds, q)
			case tenant.UIDElsewhere:
				details := map[string]string{}
				if validUID(d.HeldStudyUID) && validUID(d.HeldSeriesUID) {
					details["study_instance_uid"], details["series_instance_uid"] = d.HeldStudyUID, d.HeldSeriesUID
				}
				holds = append(holds, p.holdRecord(r, f, tenant.QuarantineUIDElsewhere, details))
			case tenant.Claimed:
				res.claimed = true
			default:
				return res, fault.New(fault.Internal, "a claim gave an answer this version does not know: "+d.Kind.String())
			}
		}
	}
	if err := p.hold(ctx, holds); err != nil {
		return res, err
	}
	if len(toWrite) == 0 {
		return res, nil
	}
	manifestKey, err := layout.ManifestKey(path)
	if err != nil {
		return res, fault.Wrap(fault.Internal, err, "the scan's labels make no key")
	}
	metadataKey, err := layout.MetadataKey(path)
	if err != nil {
		return res, fault.Wrap(fault.Internal, err, "the scan's labels make no key")
	}
	unlock := p.scanLocks.lock(r.Storage + "\x00" + manifestKey)
	defer unlock()

	puts := make([]written, 0, len(toWrite))
	for _, f := range toWrite {
		key, err := layout.InstanceKey(path, f.SOPInstanceUID)
		if err != nil {
			return res, fault.Wrap(fault.Internal, err, "an instance makes no key")
		}
		if err := p.putIncoming(ctx, store, key, f); err != nil {
			return res, err
		}
		puts = append(puts, written{f: f, key: key})
	}

	// Every UID this reception claims and writes: its entries already on
	// the storage are not the catalog's (no committed instance has a UID
	// while a claim on it stands), whether its file reads back or not.
	ours := make(map[string]bool, len(puts))
	for _, wr := range puts {
		ours[wr.f.SOPInstanceUID] = true
	}
	good := puts[:0:0]
	var mismatched []tenant.QuarantinedFile
	for _, wr := range puts {
		p.at("archive.before-readback")
		sum, err := readSum(ctx, store, wr.key)
		switch {
		case fault.KindOf(err) == fault.NotFound:
			mismatched = append(mismatched, p.holdRecord(r, wr.f, tenant.QuarantineReadBackMismatch, map[string]string{"storage": r.Storage}))
		case err != nil:
			return res, err
		case sum != wr.f.SHA256:
			mismatched = append(mismatched, p.holdRecord(r, wr.f, tenant.QuarantineReadBackMismatch,
				map[string]string{"storage": r.Storage, "read_sha256": hex.EncodeToString(sum[:])}))
			p.log.Warn("an archived file read back with another checksum; it is held, not committed, and its incoming copy kept",
				"reception", r.ID, "storage", r.Storage)
		default:
			good = append(good, wr)
		}
	}
	p.at("archive.after-readback")
	if err := p.hold(ctx, mismatched); err != nil {
		return res, err
	}
	puts = good
	if len(puts) == 0 {
		return res, nil
	}
	files := make([]manifest.File, len(puts))
	for i, wr := range puts {
		files[i] = manifest.File{Key: wr.key, Length: wr.f.Size, Size: wr.f.Size, SHA256: wr.f.SHA256.String(), SOPInstanceUID: wr.f.SOPInstanceUID}
	}
	old, err := readManifest(ctx, store, manifestKey)
	if err != nil {
		return res, err
	}
	keep := old.Files[:0:0]
	for _, mf := range old.Files {
		// An entry under a UID this reception claims is not the catalog's:
		// no committed instance has that UID while the claim stands. It is
		// what a reception that failed before its commit left, and this
		// reception's entry takes its place.
		if !ours[mf.SOPInstanceUID] {
			keep = append(keep, mf)
		}
	}
	merged, _, err := manifest.Merge(manifest.Manifest{Files: keep}, files)
	if err != nil {
		return res, fault.Wrap(fault.Internal, err, "the scan's manifest cannot be merged")
	}
	encoded, err := manifest.Encode(merged)
	if err != nil {
		return res, fault.Wrap(fault.Internal, err, "the scan's manifest cannot be written")
	}
	inManifest := make(map[string]bool, len(merged.Files))
	for _, mf := range merged.Files {
		inManifest[mf.SOPInstanceUID] = true
	}
	p.at("archive.before-sidecar")
	if err := p.putSidecar(ctx, store, metadataKey, puts, func(u string) bool { return !inManifest[u] || ours[u] }); err != nil {
		return res, err
	}
	p.at("archive.before-manifest")
	if _, err := store.Put(ctx, manifestKey, bytes.NewReader(encoded)); err != nil {
		return res, storageError(err, "the scan's manifest cannot be written")
	}
	p.at("archive.after-manifest")
	manifestSum := tenant.SHA256(sha256.Sum256(encoded))

	for start := 0; start < len(puts); start += tenant.MaxScanCommit {
		part := puts[start:min(start+tenant.MaxScanCommit, len(puts))]
		c := tenant.ScanCommit{
			ReceptionID: r.ID, StudyUID: r.StudyUID, SeriesUID: g.uid,
			SubjectLabel: path.Subject, SessionLabel: path.Session, ScanLabel: path.Scan,
			Storage: r.Storage, ManifestSHA256: manifestSum, At: p.now(), Fence: &fence,
		}
		for _, wr := range part {
			c.Instances = append(c.Instances, tenant.ConfirmedInstance{
				SOPInstanceUID: wr.f.SOPInstanceUID, Key: wr.key, Length: wr.f.Size, Size: wr.f.Size, ArchivedSHA256: wr.f.SHA256,
			})
		}
		p.at("archive.before-commit")
		_, err := p.store.CommitScan(ctx, c)
		var lc *tenant.LabelConflict
		switch {
		case errors.As(err, &lc):
			return res, p.holdWritten(ctx, r, puts[start:], tenant.QuarantineLabelConflict, map[string]string{"label": lc.Label, "fault": lc.Fault})
		case errors.Is(err, tenant.ErrSeriesElsewhere):
			return res, p.holdWritten(ctx, r, puts[start:], tenant.QuarantineUIDElsewhere, nil)
		case err != nil:
			return res, err
		}
		p.wake()
		p.at("archive.after-commit")
		res.kept += len(part)
	}
	return res, nil
}

// holdWritten holds files whose commit the catalog refused. Their objects on
// the storage stay, unlisted by the catalog; nothing is deleted from a
// storage without a recorded reason, and the files keep their incoming
// copies for a person to decide.
func (p *Pipeline) holdWritten(ctx context.Context, r tenant.Reception, puts []written, reason tenant.QuarantineReason, details map[string]string) error {
	records := make([]tenant.QuarantinedFile, len(puts))
	for i, wr := range puts {
		records[i] = p.holdRecord(r, wr.f, reason, details)
	}
	return p.hold(ctx, records)
}

// putIncoming puts a received file's incoming copy to key, and checks that
// what the storage counted is what was received: a copy changed on the disk
// since it landed is not archived.
func (p *Pipeline) putIncoming(ctx context.Context, store blob.Store, key string, f tenant.ReceivedFile) error {
	in, _, err := p.area.Open(f.Incoming)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	p.at("archive.before-put")
	wr, err := store.Put(ctx, key, in)
	if err != nil {
		return storageError(err, "a file cannot be written to the storage")
	}
	p.at("archive.after-put")
	if wr.Size != f.Size || tenant.SHA256(wr.SHA256) != f.SHA256 {
		return fault.New(fault.Internal, "an incoming copy no longer has the checksum it was received with; it is not archived")
	}
	return nil
}

// readManifest reads a scan's manifest from the storage, or an empty one
// when there is none. One that cannot be decoded is an error: it is not
// replaced by a manifest that would drop what it lists.
func readManifest(ctx context.Context, store blob.Store, key string) (manifest.Manifest, error) {
	rc, err := store.Read(ctx, key, 0, -1)
	if fault.KindOf(err) == fault.NotFound {
		return manifest.Manifest{}, nil
	}
	if err != nil {
		return manifest.Manifest{}, storageError(err, "the scan's manifest cannot be read")
	}
	defer func() { _ = rc.Close() }()
	m, err := manifest.Decode(rc)
	if err != nil {
		return manifest.Manifest{}, fault.Wrap(fault.Internal, err, "the scan's manifest on the storage cannot be read; it is left as it is")
	}
	return m, nil
}

// putSidecar writes the scan's metadata sidecar: the sidecar the storage
// holds, merged with this reception's instances, streamed to the storage as
// it is made, each instance's header read from its incoming copy only when
// its line is written.
func (p *Pipeline) putSidecar(ctx context.Context, store blob.Store, key string, puts []written, drop func(string) bool) error {
	var old io.ReadCloser
	rc, err := store.Read(ctx, key, 0, -1)
	switch {
	case err == nil:
		old = rc
		defer func() { _ = rc.Close() }()
	case fault.KindOf(err) != fault.NotFound:
		return storageError(err, "the scan's metadata cannot be read")
	}
	i := 0
	added := func() (sidecar.Instance, bool, error) {
		if i == len(puts) {
			return sidecar.Instance{}, false, nil
		}
		wr := puts[i]
		i++
		h, err := p.readHeader(wr.f.Incoming)
		if err != nil {
			return sidecar.Instance{}, false, err
		}
		in := sidecar.Instance{SOPInstanceUID: wr.f.SOPInstanceUID, Key: wr.key, Size: wr.f.Size, SHA256: hex.EncodeToString(wr.f.SHA256[:])}
		if h.HasPixelData {
			in.PixelData = &sidecar.PixelData{Offset: h.PixelData.Offset, Length: h.PixelData.Length}
		}
		if js, err := h.DICOMJSON(); err == nil {
			in.Dataset = js
		}
		return in, true, nil
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		var oldReader io.Reader
		if old != nil {
			oldReader = old
		}
		_, err := sidecar.Merge(pw, oldReader, added, drop)
		_ = pw.CloseWithError(err)
		done <- err
	}()
	_, perr := store.Put(ctx, key, pr)
	_ = pr.CloseWithError(errors.New("the storage stopped reading"))
	merr := <-done
	if merr != nil && !errors.Is(merr, io.ErrClosedPipe) {
		if fault.KindOf(merr) == fault.Internal {
			return fault.Wrap(fault.Internal, merr, "the scan's metadata cannot be made")
		}
		return merr
	}
	if perr != nil {
		return storageError(perr, "the scan's metadata cannot be written")
	}
	return nil
}

// storageError keeps a storage's failure with its kind, and a message for
// the log and the unit's last error that says which write failed.
func storageError(err error, message string) error {
	k := fault.KindOf(err)
	if k == fault.Internal {
		k = fault.Unavailable
	}
	return fault.Wrap(k, err, message+": "+safe(err))
}
