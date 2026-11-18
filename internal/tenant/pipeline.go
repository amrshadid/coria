package tenant

import (
	"context"
	"errors"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// Pipeline is a tenant's database with what the reception pipeline keeps in
// it: the tenant's storages and receivers, its receptions and the files they
// received, its quarantine, and the leased units of work that carry a
// reception through its steps. Everything the pipeline must not lose in a
// restart is here, so a server that stops at any moment starts again from
// what was committed.
//
// It is a Store and more. A driver implements it and proves it with the
// contract suite's pipeline groups (tenanttest.RunPipeline); once every
// driver does, its operations become part of Store itself.
//
// # Rules every driver keeps
//
// Store's rules hold, with these.
//
// A change a person or a key asks for (adding, changing or removing a
// storage or a receiver, retrying or discarding a reception, releasing or
// rejecting a quarantined file) takes its audit entry and a Guard, as
// Store's changes do. The pipeline's own work (opening and stepping a
// reception, recording received files, holding a file in quarantine,
// queuing, claiming and finishing work) is the server's and takes neither:
// it happens once per file or per step, far more often than any person
// acts, and its record is the reception's own row and, once they exist, its
// events.
//
// Times are given by the caller, always: a store never reads a clock. The
// clock that decides when a lease ends, when a unit of work is due, and
// when a reception opened or stepped is the clock of the server process
// that calls, handed over as "now". Correctness never rests on it: a lease
// that ends early or late changes who works a unit and when, but every
// write of a worker is fenced by its attempt number (WorkFence), so a
// worker whose lease was taken over changes nothing, whatever the clocks
// say. Several processes on one tenant (possible on a database that allows
// several writers) must keep their clocks in step to well under a lease,
// as any time service does, or a lease can be taken over early.
//
// A rule that depends on the state of what a request names (a storage that
// holds data, a reception that has not failed, a file already rejected) is
// refused with ErrPrecondition in the chain (Precondition), so a caller can
// answer it apart from a Conflict, which is a name or a key already taken.
//
// Every list is paged by a cursor bound to its list, its filter and the
// tenant, and every list, claim and lookup is answered from an index.
// Batches are bounded (MaxReceivedBatch, MaxQuarantineBatch,
// MaxDuplicateBatch); a larger one is Invalid.
type Pipeline interface {
	Store

	// AddStorage adds a storage, stored as given with Created and Updated
	// set to now, and returns it as a read would. A name taken by another
	// storage is Conflict. An S3 location on the endpoint and bucket of
	// another S3 storage whose prefix starts with this one's, or the other
	// way round, and a directory whose path is, contains or lies inside
	// another directory storage's under the same root (segments compared
	// with ASCII case folded), is a precondition failure.
	AddStorage(ctx context.Context, s Storage, now time.Time, audit master.AuditEntry, guard Guard) (Storage, error)
	// Storage returns the storage of that name, or NotFound. Its secret is
	// never returned: S3.SecretSet says whether one is held.
	Storage(ctx context.Context, name string) (Storage, error)
	// ListStorages returns a page of the storages in order of name.
	ListStorages(ctx context.Context, page master.Page) ([]Storage, string, error)
	// SetStorage changes the fields named in fields to their values in s,
	// whose Name names the storage, and no other; Updated becomes now. The
	// type cannot change, so a field of the other type's configuration is
	// Invalid; the access key id and the secret are named together or not
	// at all, and a secret named is not empty. Once a reception names the
	// storage (it may hold archived files) a change of where its files are
	// is a precondition failure. The rules of AddStorage apply to the
	// result.
	SetStorage(ctx context.Context, s Storage, fields []StorageField, now time.Time, audit master.AuditEntry, guard Guard) (Storage, error)
	// RemoveStorage removes the storage and returns it as it was. A storage
	// a receiver names, or one a reception names (it may hold archived
	// files), is a precondition failure whose message says which. A storage
	// not there is NotFound.
	RemoveStorage(ctx context.Context, name string, audit master.AuditEntry, guard Guard) (Storage, error)

	// AddReceiver adds a receiver, with Created and Updated set to now. A
	// name taken by a receiver, or still named by a reception or a
	// quarantined file of a receiver removed, is Conflict, so a name in a
	// record always means one receiver. A storage the tenant does not have,
	// and another receiver on the same port whose bind address is the same
	// or either is empty, are precondition failures.
	AddReceiver(ctx context.Context, r Receiver, now time.Time, audit master.AuditEntry, guard Guard) (Receiver, error)
	// Receiver returns the receiver of that name, or NotFound.
	Receiver(ctx context.Context, name string) (Receiver, error)
	// ListReceivers returns a page of the receivers in order of name.
	ListReceivers(ctx context.Context, page master.Page) ([]Receiver, string, error)
	// SetReceiver changes the fields named in fields to their values in r,
	// whose Name names the receiver; Updated becomes now. The rules of
	// AddReceiver apply to the result.
	SetReceiver(ctx context.Context, r Receiver, fields []ReceiverField, now time.Time, audit master.AuditEntry, guard Guard) (Receiver, error)
	// RemoveReceiver removes the receiver and returns it as it was. Its
	// receptions and quarantined files stay, naming it.
	RemoveReceiver(ctx context.Context, name string, audit master.AuditEntry, guard Guard) (Receiver, error)

	// OpenReception returns the reception of the study at the receiver that
	// is receiving, or makes one with the id given, the next arrival of the
	// study in the tenant, receiving since at; opened says which. A
	// receiver the tenant does not have is a precondition failure. Two
	// calls at once for one study and receiver open one reception.
	OpenReception(ctx context.Context, receiver, studyUID, id string, at time.Time) (r Reception, opened bool, err error)
	// Reception returns the reception with its study's arrivals, up to
	// MaxArrivals of the latest, in order of arrival number; or NotFound.
	Reception(ctx context.Context, id string) (Reception, []Arrival, error)
	// ListReceptions returns a page of the receptions the filter keeps,
	// newest first: by start time, then by id, both descending.
	ListReceptions(ctx context.Context, filter ReceptionFilter, page master.Page) ([]Reception, string, error)
	// StepReception moves a reception by one step, as reception.Step allows
	// it from step.From, comparing and setting the state: when the stored
	// state is not step.From nothing changes and the error holds
	// ErrStateChanged; a step the table refuses holds the
	// *reception.Refusal (errors.As), Already among them. Both are
	// Conflict. hook, when not nil, runs inside the step's transaction
	// after the reception is written and before the commit, with the
	// reception as it was and as it is; an error from it undoes the step
	// and is returned as it is. It is where the step's event is written.
	StepReception(ctx context.Context, step ReceptionStep, hook StepHook) (Reception, error)
	// RetryReception makes a new reception that runs the files of a failed
	// one again: at the receiver named, or the failed one's when receiver
	// is empty; the next arrival of its study, complete since now, with the
	// failed reception's received files and RetryOf set; and sets the
	// failed one's RetryReception. A reception already retried is returned
	// as it stands with the one made then, and nothing more is made. A
	// reception that has not failed or whose files were discarded, a
	// failed one's receiver removed with none named, and a receiver named
	// that does not exist, are precondition failures. The store adds the
	// detail DetailRetryReception, the id of the reception that runs the
	// files, to the audit entry.
	RetryReception(ctx context.Context, id, newID, receiver string, now time.Time, audit master.AuditEntry, guard Guard) (failed, retry Reception, err error)
	// DiscardReception records that a failed reception's files left in the
	// incoming area are removed, with the reason and now, and returns the
	// reception; the caller then removes the bytes of its received files
	// (ReceivedFiles), and a restart that finds a discarded reception's
	// files still there removes them. A
	// reception already discarded is returned as it stands with its first
	// reason. A reception that has not failed or that was retried is a
	// precondition failure. The store adds the details DetailFiles and
	// DetailBytes, the files and bytes received, to the audit entry.
	DiscardReception(ctx context.Context, id, reason string, now time.Time, audit master.AuditEntry, guard Guard) (Reception, error)

	// AddReceivedFiles records files that have landed in the incoming area,
	// up to MaxReceivedBatch, all or none, and returns how many were new.
	// A file already recorded with the same reception, SOP Instance UID and
	// checksum is skipped, so a batch written again after a crash changes
	// nothing. Each new file adds to its reception's received counts and
	// moves its last file time to at when later; its association, the
	// first time the reception sees it, adds to the reception's
	// associations and calling AE titles. A reception not there is
	// NotFound, and one that is not receiving is a precondition failure
	// with ErrReceptionClosed in the chain: its files belong to the next
	// arrival.
	AddReceivedFiles(ctx context.Context, files []ReceivedFile, at time.Time) (int, error)
	// ReceivedFiles returns a page of a reception's received files in order
	// of SOP Instance UID, then checksum.
	ReceivedFiles(ctx context.Context, receptionID string, page master.Page) ([]ReceivedFile, string, error)
	// CheckDuplicates says for each file, in order, up to
	// MaxDuplicateBatch, whether the tenant already holds an instance with
	// its SOP Instance UID, and how it compares (Duplicate).
	CheckDuplicates(ctx context.Context, files []DuplicateQuery) ([]Duplicate, error)

	// HoldFiles records files in quarantine, held, up to
	// MaxQuarantineBatch, all or none, and returns how many were new. A
	// record whose id is there already is skipped as it stands, so a batch
	// written again after a crash changes nothing. Each new record adds to
	// the quarantine's totals and, when it names a reception, to that
	// reception's quarantined counts, in the same transaction. A reception
	// named that is not there is a precondition failure.
	HoldFiles(ctx context.Context, files []QuarantinedFile) (int, error)
	// QuarantinedFile returns the record of that id, or NotFound.
	QuarantinedFile(ctx context.Context, id string) (QuarantinedFile, error)
	// ListQuarantined returns a page of the records the filter keeps,
	// newest first: by quarantine time, then by id, both descending.
	ListQuarantined(ctx context.Context, filter QuarantineFilter, page master.Page) ([]QuarantinedFile, string, error)
	// ReleaseQuarantined releases a held file with the mode given, through
	// the receiver named, or the one it first came to when receiver is
	// empty. A file with a valid study, series and instance UID and a
	// checksum joins that receiver's reception of its study that is
	// receiving, or a new one with receptionID opened at now, as a received
	// file counted released; the record names that reception. A file
	// already released with the same mode is returned as it stands. Mode
	// ReleaseOverwrite for a reason other than QuarantineDifferentFile is
	// Invalid; a rejected file, one released with the other mode, one whose
	// bytes were never kept, a removed receiver with none named, and a
	// receiver named that does not exist, are precondition failures.
	ReleaseQuarantined(ctx context.Context, id string, mode ReleaseMode, receiver, receptionID string, now time.Time, audit master.AuditEntry, guard Guard) (QuarantinedFile, error)
	// RejectQuarantined rejects a held file with the reason given: its
	// bytes are no longer kept. A file already rejected is returned as it
	// stands with its first reason; a released file is a precondition
	// failure. The store adds the details DetailReason, DetailSize and
	// DetailSHA256 to the audit entry.
	RejectQuarantined(ctx context.Context, id, reason string, now time.Time, audit master.AuditEntry, guard Guard) (QuarantinedFile, error)
	// QuarantineTotals returns the files held now and the bytes kept for
	// them, running totals kept in the transaction of every change, so
	// they equal a count over the records after every commit.
	QuarantineTotals(ctx context.Context) (files, bytes int64, err error)

	// EnqueueWork queues a unit of work, due at w.Due, and returns it with
	// created true; or, when w.UniqueKey is set and a unit with that key is
	// queued or leased, returns that one with created false and queues
	// nothing, so the same work is queued once.
	EnqueueWork(ctx context.Context, w Work, now time.Time) (unit Work, created bool, err error)
	// Work returns the unit of that id, or NotFound.
	Work(ctx context.Context, id string) (Work, error)
	// ClaimWork leases the next unit of the kind that is due at now, the
	// earliest due first, for lease, to owner, and returns it with its new
	// attempt number. A unit whose lease ended without being renewed is
	// due again; one whose last attempt was the last allowed is held as
	// failed instead. No unit due is NotFound. Two claims at once get
	// different units.
	ClaimWork(ctx context.Context, kind, owner string, now time.Time, lease time.Duration) (Work, error)
	// RenewWork extends a lease to now + lease. A unit whose attempt is no
	// longer the fence's, or that is not leased, changes nothing and is
	// ErrLeaseLost.
	RenewWork(ctx context.Context, fence WorkFence, now time.Time, lease time.Duration) (Work, error)
	// CompleteWork marks a leased unit done and, when next is not nil,
	// queues next in the same transaction, so a chain of steps never loses
	// its next one. Fenced as RenewWork.
	CompleteWork(ctx context.Context, fence WorkFence, now time.Time, next *Work) error
	// FailWork records a failed attempt with its reason: the unit is due
	// again after RetryDelay(attempt), or, after its last allowed attempt,
	// held as failed. Fenced as RenewWork. It returns the unit as written.
	FailWork(ctx context.Context, fence WorkFence, reason string, now time.Time) (Work, error)
}

// StepHook runs inside a reception step's transaction (Pipeline.StepReception)
// with the reception as it was and as it is now; an error undoes the step.
type StepHook func(ctx context.Context, before, after Reception) error

// ErrPrecondition is in the chain of every refusal that depends on the state
// of what a request names rather than on how it is written (Pipeline). Its
// kind is Conflict; the API answers it as a failed precondition.
var ErrPrecondition = errors.New("a precondition of the change does not hold")

// Precondition returns a refusal with ErrPrecondition in its chain and the
// message, which says which rule failed and is safe to show.
func Precondition(message string) error {
	return fault.Wrap(fault.Conflict, ErrPrecondition, message)
}

// ErrStateChanged is in the chain of a reception step whose reception is no
// longer in the state the step started from: another step came first.
var ErrStateChanged = errors.New("the reception is no longer in the state the step started from")

// ErrReceptionClosed is in the chain of received files refused because their
// reception is no longer receiving.
var ErrReceptionClosed = errors.New("the reception is no longer receiving")

// ErrLeaseLost is in the chain of a worker's write refused because the
// worker's lease was taken over or the unit is no longer leased. Its kind is
// Conflict. Nothing was changed.
var ErrLeaseLost = errors.New("the lease on this unit of work is lost")

// LeaseLost returns the refusal of a fenced write whose fence no longer
// holds.
func LeaseLost() error {
	return fault.Wrap(fault.Conflict, ErrLeaseLost, "the lease on this unit of work was lost; nothing was changed")
}

// The details a store adds to the audit entry of a change whose outcome only
// the store knows.
const (
	// DetailRetryReception is the id of the reception that runs a failed
	// reception's files.
	DetailRetryReception = "retry_reception"
	// DetailFiles is a count of files.
	DetailFiles = "files"
	// DetailBytes is a count of bytes.
	DetailBytes = "bytes"
	// DetailReason is a quarantined file's reason code.
	DetailReason = "reason"
	// DetailSize is a quarantined file's size in bytes.
	DetailSize = "size"
	// DetailSHA256 is a quarantined file's checksum in lowercase
	// hexadecimal, empty when it was not kept whole.
	DetailSHA256 = "sha256"
)
