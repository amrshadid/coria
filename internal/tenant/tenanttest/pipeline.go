package tenanttest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// OpenPipeline is Open for a store that implements tenant.Pipeline.
type OpenPipeline func(tb testing.TB, tenantID string) tenant.Pipeline

// OpenPipelinePair is OpenPair for stores that implement tenant.Pipeline.
type OpenPipelinePair func(tb testing.TB, tenantID string) (first, second tenant.Pipeline)

// RunPipeline runs the contract suite's pipeline groups (tenant.Pipeline)
// against the stores open makes, each test on its own store, in parallel. A
// driver that implements Pipeline calls it as well as Run.
func RunPipeline(t *testing.T, open OpenPipeline) {
	tests := []struct {
		name string
		fn   func(*penv)
	}{
		{"Storages", testStorages},
		{"StorageSetPartial", testStorageSetPartial},
		{"StorageInUse", testStorageInUse},
		{"Receivers", testReceivers},
		{"ReceiverNameHeld", testReceiverNameHeld},
		{"ConfigGuards", testConfigGuards},
		{"ConfigPagingWithDeletes", testConfigPagingWithDeletes},
		{"OpenReception", testOpenReception},
		{"OpenReceptionConcurrent", testOpenReceptionConcurrent},
		{"ReceptionSteps", testReceptionSteps},
		{"ReceptionStepRefusals", testReceptionStepRefusals},
		{"StepHook", testStepHook},
		{"ReceptionList", testReceptionList},
		{"ReceptionPagingWithInserts", testReceptionPagingWithInserts},
		{"RetryReception", testRetryReception},
		{"DiscardReception", testDiscardReception},
		{"ReceivedFiles", testReceivedFiles},
		{"ReceivedBatchRepeated", testReceivedBatchRepeated},
		{"ReceivedFilesClosed", testReceivedFilesClosed},
		{"Duplicates", testDuplicates},
		{"QuarantineHold", testQuarantineHold},
		{"QuarantineDetails", testQuarantineDetails},
		{"QuarantineRelease", testQuarantineRelease},
		{"QuarantineReject", testQuarantineReject},
		{"QuarantineTotals", testQuarantineTotals},
		{"QuarantineList", testQuarantineList},
		{"WorkClaim", testWorkClaim},
		{"WorkUniqueKey", testWorkUniqueKey},
		{"WorkFencing", testWorkFencing},
		{"WorkRetries", testWorkRetries},
		{"WorkConcurrentClaims", testWorkConcurrentClaims},
		{"WorkChain", testWorkChain},
		{"PipelineBounds", testPipelineBounds},
		{"PipelineTenantsApart", testPipelineTenantsApart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := open(t, SuiteTenant)
			tt.fn(&penv{env: &env{T: t, s: p, ctx: context.Background()}, p: p, open: open})
		})
	}
}

// RunPipelineShared runs the pipeline's tests that need two instances of
// the store on one database.
func RunPipelineShared(t *testing.T, open OpenPipelinePair) {
	tests := []struct {
		name string
		fn   func(e *penv, other tenant.Pipeline)
	}{
		{"ClaimsAcrossInstances", testClaimsShared},
		{"OpenAcrossInstances", testOpenShared},
		{"BatchRepeatedAcrossInstances", testBatchShared},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, second := open(t, SuiteTenant)
			tt.fn(&penv{env: &env{T: t, s: first, ctx: context.Background()}, p: first}, second)
		})
	}
}

// penv is a pipeline test's store and its helpers; it has env's too.
type penv struct {
	*env
	p    tenant.Pipeline
	open OpenPipeline
}

// at returns T0 plus seconds.
func at(seconds int) time.Time { return T0.Add(time.Duration(seconds) * time.Second) }

// uidOf returns a valid UID unique to n and kind (1 for studies, 2 for
// series, 3 for instances), under the 2.25 root of UUID-derived UIDs
// (PS3.5 B.2), so no real UID can match it.
func uidOf(kind, n int) string { return fmt.Sprintf("2.25.%d.%d", kind, n+1) }

// sum returns a checksum made from n, never zero.
func sum(n int) tenant.SHA256 {
	var s tenant.SHA256
	for i := range s {
		s[i] = byte((n + i + 1) & 0xff)
	}
	s[0] = byte((n>>8)&0x7f) | 0x80
	return s
}

// wantIs fails unless err holds target in its chain and has the kind.
func (e *penv) wantIs(what string, err error, target error, kind fault.Kind) {
	e.Helper()
	if !errors.Is(err, target) || fault.KindOf(err) != kind {
		e.Errorf("%s: %v (kind %v), want %v of kind %v", what, err, fault.KindOf(err), target, kind)
	}
}

// wantPrecondition fails unless err is a precondition failure.
func (e *penv) wantPrecondition(what string, err error) {
	e.Helper()
	e.wantIs(what, err, tenant.ErrPrecondition, fault.Conflict)
}

// dirStorage returns a directory storage named name under root "fast".
func dirStorage(name, path string) tenant.Storage {
	return tenant.Storage{Name: name, Type: tenant.StorageDirectory, Directory: tenant.DirectoryStorage{Root: "fast", Path: path}}
}

// s3Storage returns an S3 storage with a sealed secret.
func s3Storage(name, prefix string) tenant.Storage {
	return tenant.Storage{Name: name, Type: tenant.StorageS3, S3: tenant.S3Storage{
		Endpoint: "https://s3.example.org", Region: "us-east-1", Bucket: "lab-archive", Prefix: prefix,
		PathStyle: true, AccessKeyID: "AKIDEXAMPLE", SealedSecret: []byte("sealed-secret-bytes"),
	}}
}

// receiverOf returns a DIMSE receiver with the contract's defaults.
func receiverOf(name, storage string, port int) tenant.Receiver {
	return tenant.Receiver{
		Name: name, Type: tenant.ReceiverDIMSE, Storage: storage,
		DIMSE:       tenant.DIMSEReceiver{Port: port, AETitle: "ARCHIVE"},
		QuietPeriod: tenant.DefaultQuietPeriod, MaxOpenTime: tenant.DefaultMaxOpenTime, MaxFileBytes: tenant.DefaultMaxFileBytes,
	}
}

// addStorage adds a directory storage, failing the test on an error.
func (e *penv) addStorage(name string) tenant.Storage {
	e.Helper()
	s, err := e.p.AddStorage(e.ctx, dirStorage(name, name), T0, e.audit(), nil)
	e.ok("AddStorage", err)
	return s
}

// addReceiver adds a receiver on a storage it adds first when missing.
func (e *penv) addReceiver(name, storage string, port int) tenant.Receiver {
	e.Helper()
	if _, err := e.p.Storage(e.ctx, storage); fault.KindOf(err) == fault.NotFound {
		e.addStorage(storage)
	}
	r, err := e.p.AddReceiver(e.ctx, receiverOf(name, storage, port), T0, e.audit(), nil)
	e.ok("AddReceiver", err)
	return r
}

// openReception opens a reception of study n at the receiver.
func (e *penv) openReception(receiver string, study int, when time.Time) tenant.Reception {
	e.Helper()
	r, _, err := e.p.OpenReception(e.ctx, receiver, uidOf(1, study), e.id("rc"), when)
	e.ok("OpenReception", err)
	return r
}

// file returns received file n of study for the reception.
func file(r tenant.Reception, study, n int) tenant.ReceivedFile {
	return tenant.ReceivedFile{
		ReceptionID: r.ID, SOPInstanceUID: uidOf(3, n), SeriesUID: uidOf(2, study), StudyUID: r.StudyUID,
		SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", TransferSyntaxUID: "1.2.840.10008.1.2.1",
		Size: int64(1000 + n), SHA256: sum(n), Incoming: fmt.Sprintf("%s/f%06d.dcm", r.ID, n),
		Caller:      tenant.Caller{CallingAETitle: "SCANNER1", CalledAETitle: "ARCHIVE", Address: "192.0.2.7:51234"},
		Association: "assoc1",
	}
}

// path is the main line of a reception's steps after receiving: each event
// and the state it leads to.
var path = []struct {
	event reception.Event
	to    reception.State
}{
	{reception.Quiet, reception.Complete}, {reception.Identify, reception.Identified},
	{reception.Archive, reception.Archived}, {reception.Verify, reception.Verified}, {reception.Finish, reception.Done},
}

// stepTo moves a reception along the main line until it is in state to,
// identifying it with storage.
func (e *penv) stepTo(r tenant.Reception, to reception.State, storage string) tenant.Reception {
	e.Helper()
	from := reception.Receiving
	for _, s := range path {
		if r.State == to {
			return r
		}
		if r.State != from {
			from = s.to
			continue
		}
		from = s.to
		step := tenant.ReceptionStep{ID: r.ID, From: r.State, Event: s.event, At: r.LastFile.Add(time.Minute)}
		if s.event == reception.Identify {
			step.SubjectLabel, step.SessionLabel, step.Storage = "sub-01", "ses-"+r.ID, storage
		}
		var err error
		r, err = e.p.StepReception(e.ctx, step, nil)
		e.ok("StepReception "+string(s.event), err)
	}
	if r.State != to {
		e.Fatalf("the reception is %s, want %s", r.State, to)
	}
	return r
}

// failReception moves a reception from its state to failed.
func (e *penv) failReception(r tenant.Reception) tenant.Reception {
	e.Helper()
	r, err := e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: r.State, Event: reception.Fail,
		At: r.LastFile.Add(time.Hour), Failure: "storage unreachable"}, nil)
	e.ok("StepReception fail", err)
	return r
}

// allPages calls list until it returns no cursor, and returns every item.
func allPages[T any](e *penv, limit int, list func(master.Page) ([]T, string, error)) []T {
	e.Helper()
	var out []T
	page := master.Page{Limit: limit}
	for range 100000 {
		items, next, err := list(page)
		e.ok("list", err)
		out = append(out, items...)
		if next == "" {
			return out
		}
		page.Cursor = next
	}
	e.Fatal("a list did not end")
	return nil
}
