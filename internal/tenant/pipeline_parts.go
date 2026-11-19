package tenant

import (
	"context"
	"time"

	"github.com/amrshadid/magnetar/internal/master"
)

// The pipeline's narrower interfaces, each one concern, which Pipeline
// embeds (PipelineParts). A caller that needs only one depends on it alone;
// each is proved by the contract suite's pipeline groups.

// StorageSecrets hands back what a storage needs to be opened and Storage
// never answers.
type StorageSecrets interface {
	// StorageSecret returns the sealed secret access key of the S3 storage
	// of that name, the bytes AddStorage or SetStorage was given, still
	// sealed: the store neither opens nor logs them, and only the caller
	// that holds the installation key can. A storage not there, or not of
	// type S3, is NotFound.
	StorageSecret(ctx context.Context, name string) ([]byte, error)
}

// QuarantineReleases releases a held file with what a release records.
type QuarantineReleases interface {
	// ReleaseFile is ReleaseQuarantined with the reason the person gave,
	// kept on the record (QuarantinedFile.ReleaseReason). Its rules are
	// ReleaseQuarantined's, with these: an empty reason records none, and
	// one given follows ValidateDiscardReason's rule (Invalid otherwise);
	// a file already released is returned as it stands, with the reason
	// of its first release. The store adds the detail
	// DetailReleaseReception, the reception the file joined, to the audit
	// entry when it joined one, also when the release is answered as it
	// stands.
	ReleaseFile(ctx context.Context, r Release, audit master.AuditEntry, guard Guard) (QuarantinedFile, error)
}

// Release is what a release of a held file asks (QuarantineReleases).
type Release struct {
	// ID names the record.
	ID string
	// Mode is how the file runs again: ReleaseAsIs.
	Mode ReleaseMode
	// Receiver is the receiver to run it by, or empty for the one it came
	// to.
	Receiver string
	// ReceptionID is the id of the reception it opens when it joins none
	// that is receiving.
	ReceptionID string
	// Reason is the person's reason, or empty for none.
	Reason string
	// Now is when the release is made.
	Now time.Time
}

// Validate returns an Invalid error unless the release is well formed.
func (r Release) Validate() error {
	if err := ValidateRelease(r.ID, r.Mode, r.Receiver, r.ReceptionID, r.Now); err != nil {
		return err
	}
	if r.Reason != "" {
		return validateReason("release reason", r.Reason)
	}
	return nil
}

// FencedFiles records files under a worker's lease.
type FencedFiles interface {
	// AddReceivedFilesFenced is AddReceivedFiles made only while fence,
	// when not nil, holds: a worker whose lease was taken over records
	// nothing and is answered ErrLeaseLost. The fence is checked first in
	// the batch's transaction, before any reception is read.
	AddReceivedFilesFenced(ctx context.Context, files []ReceivedFile, at time.Time, fence *WorkFence) (int, error)
	// HoldFilesFenced is HoldFiles made only while fence, when not nil,
	// holds, as AddReceivedFilesFenced.
	HoldFilesFenced(ctx context.Context, files []QuarantinedFile, fence *WorkFence) (int, error)
}

// WorkClaims claims work and says what the claim did on the way.
type WorkClaims interface {
	// ClaimNextWork is ClaimWork that also says which units it held as
	// failed on the way (their last attempt's lease ended) and which
	// receptions that failed, so the caller can report them and act on
	// their bytes at once rather than when it next looks. Nothing due is
	// not an error: the claim's Found is false, and GaveUp still lists
	// what was held as failed, which is committed either way.
	ClaimNextWork(ctx context.Context, kind, owner string, now time.Time, lease time.Duration) (WorkClaim, error)
}

// WorkClaim is the answer of ClaimNextWork.
type WorkClaim struct {
	// Work is the unit leased, with its new attempt; zero when Found is
	// false.
	Work Work
	// Found says a unit was leased.
	Found bool
	// GaveUp lists the units held as failed on the way, in the order the
	// claim met them, at most MaxGaveUp.
	GaveUp []GaveUpWork
}

// MaxGaveUp bounds the units one claim holds as failed on the way, and so
// its GaveUp list: a claim that meets more leaves the rest for the next.
const MaxGaveUp = 1024

// GaveUpWork is one unit a claim held as failed.
type GaveUpWork struct {
	// WorkID names the unit.
	WorkID string
	// ReceptionID names the reception the unit carried, empty for none.
	ReceptionID string
	// ReceptionFailed says that reception took the Fail step
	// (FailureWorkGaveUp) in the claim's transaction; false when it had
	// ended already or is not there.
	ReceptionFailed bool
}

// ScanContents lists what a scan of the catalog holds.
type ScanContents interface {
	// ScanInstances returns a page of the instances held in the scan of
	// the series, in order of SOP Instance UID, whichever reception
	// confirmed each: the catalog's own list of the scan, from which a
	// manifest is written and against which a reception checks that no
	// instance is missing from it. A claim not confirmed is never listed.
	// A series with no scan has none, and its list is empty. The cursor is
	// bound to the series and the tenant.
	ScanInstances(ctx context.Context, seriesUID string, page master.Page) ([]ScanInstance, string, error)
}

// ScanInstance is one instance a scan holds, as the catalog keeps it.
type ScanInstance struct {
	// SOPInstanceUID names the instance.
	SOPInstanceUID string
	// ReceptionID is the reception that confirmed it.
	ReceptionID string
	// Storage is the storage its file is on, its session's.
	Storage string
	// Key, Offset and Length are where it is on the storage.
	Key            string
	Offset, Length int64
	// Size is the file's size in bytes.
	Size int64
	// SHA256 is the checksum it was received with; ArchivedSHA256 that of
	// what was written and read back.
	SHA256, ArchivedSHA256 SHA256
	// Held is when its reception confirmed it.
	Held time.Time
}

// ValidateSeriesUID returns an Invalid error unless s is a Series Instance
// UID (PS3.5 9.1).
func ValidateSeriesUID(s string) error { return validateUID("Series Instance UID", s) }

// PipelineParts is every narrower interface of the pipeline, which Pipeline
// embeds, so a driver implements them whole.
type PipelineParts interface {
	StorageSecrets
	QuarantineReleases
	FencedFiles
	WorkClaims
	ScanContents
}
