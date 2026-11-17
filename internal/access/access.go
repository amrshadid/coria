package access

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// The defaults of the sizes and the interval in Config, used when a field is
// zero.
const (
	// DefaultMarkQueue is how many last-use marks wait for their worker. A
	// mark is a key id and a time, so the queue costs a few kilobytes; it is
	// deep enough to ride out a slow write of the master and shallow enough
	// that a stuck master shows in the drop count, not in memory.
	DefaultMarkQueue = 256
	// DefaultAuditQueue is how many audit entries of refusals and reads
	// wait for the audit writer. An entry is at most a few kilobytes, so the
	// queue is bounded to some tens of megabytes at worst, and holds several
	// seconds of a busy server's requests while a batch is written.
	DefaultAuditQueue = 8192
	// DefaultAuditBatch is the most entries one append writes. One synced
	// commit costs milliseconds whatever its size, so a batch of a thousand
	// costs about what one entry did.
	DefaultAuditBatch = 1000
	// DefaultAuditInterval is the longest an entry waits in a batch that is
	// not full: the window in which a killed server loses entries.
	DefaultAuditInterval = 200 * time.Millisecond
)

// Config is what New needs. Store, Clock and NewID are required; the rest
// have defaults.
type Config struct {
	// Store is the master database.
	Store Store
	// Clock returns the current time, for every audit entry and every
	// expiry. It is given, not read from the system, so a test moves it to
	// expire a key without sleeping.
	Clock func() time.Time
	// NewID returns a fresh id for an audit entry: 1 to 64 of A-Z, a-z,
	// 0-9, "_" and "-", unique among entries. A check given an id that is
	// not valid fails as Internal rather than lose its audit entry.
	NewID func() string
	// MarkQueue is how many last-use marks may wait; zero is
	// DefaultMarkQueue. When it is full, a new mark is dropped.
	MarkQueue int
	// AuditQueue is how many audit entries of refusals and reads may wait
	// for the audit writer; zero is DefaultAuditQueue. When it is full, a new
	// entry is dropped and counted.
	AuditQueue int
	// AuditBatch is the most entries one append writes, 1 to
	// master.MaxAuditBatch; zero is DefaultAuditBatch.
	AuditBatch int
	// AuditInterval is the longest an entry waits before its batch is
	// written; zero is DefaultAuditInterval.
	AuditInterval time.Duration
	// Log receives what the package cannot return to anyone: a last-use
	// write or an audit append that failed. Nil discards. No line carries a
	// secret.
	Log *slog.Logger
}

// Access is the check and the management of keys and grants over one master
// database. It owns two goroutines, the last-use worker and the audit
// writer, which Close stops. Its methods are safe for concurrent use.
type Access struct {
	store Store
	clock func() time.Time
	newID func() string
	log   *slog.Logger

	closing atomic.Bool
	once    sync.Once
	stop    chan struct{}
	// workCtx is the context of every write the two workers make; Close
	// cancels it when its own deadline passes, so a write in flight does not
	// outlive Close.
	workCtx    context.Context
	workCancel context.CancelFunc

	marks       chan mark
	marksDone   chan struct{}
	markWritten atomic.Uint64
	markDropped atomic.Uint64
	markFailed  atomic.Uint64

	audit         chan master.AuditEntry
	auditFlush    chan chan struct{}
	auditDone     chan struct{}
	auditBatch    int
	auditInterval time.Duration
	auditWritten  atomic.Uint64
	auditDropped  atomic.Uint64
	auditFailed   atomic.Uint64
}

// New returns an Access over cfg.Store and starts its two workers. The
// caller must call Close when done, which waits for both.
func New(cfg Config) (*Access, error) {
	if cfg.Store == nil || cfg.Clock == nil || cfg.NewID == nil {
		return nil, fault.New(fault.Internal, "access: a store, a clock and an id source are required")
	}
	if cfg.MarkQueue < 0 || cfg.AuditQueue < 0 || cfg.AuditInterval < 0 || cfg.AuditBatch < 0 || cfg.AuditBatch > master.MaxAuditBatch {
		return nil, fault.New(fault.Internal, "access: queue sizes and the interval are not negative, and a batch is at most "+strconv.Itoa(master.MaxAuditBatch))
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &Access{
		store:         cfg.Store,
		clock:         cfg.Clock,
		newID:         cfg.NewID,
		log:           log,
		stop:          make(chan struct{}),
		workCtx:       ctx,
		workCancel:    cancel,
		marks:         make(chan mark, orDefault(cfg.MarkQueue, DefaultMarkQueue)),
		marksDone:     make(chan struct{}),
		audit:         make(chan master.AuditEntry, orDefault(cfg.AuditQueue, DefaultAuditQueue)),
		auditFlush:    make(chan chan struct{}),
		auditDone:     make(chan struct{}),
		auditBatch:    orDefault(cfg.AuditBatch, DefaultAuditBatch),
		auditInterval: orDefault(cfg.AuditInterval, DefaultAuditInterval),
	}
	go a.markWorker()
	go a.auditWriter()
	return a, nil
}

// orDefault returns v, or def when v is zero.
func orDefault[T int | time.Duration](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}

// Close stops both workers. Each first writes what it already holds, until
// ctx is done; then the writes in flight are canceled, what is left is
// dropped and counted, and Close waits for both workers to return. It
// returns ctx's error when anything was left. Checks made after Close still
// work, but their marks and their refusals' and reads' audit entries are
// dropped and counted. Close does not close the store, which belongs to the
// caller.
func (a *Access) Close(ctx context.Context) error {
	a.once.Do(func() {
		a.closing.Store(true)
		close(a.stop)
	})
	var err error
	for _, done := range []chan struct{}{a.marksDone, a.auditDone} {
		select {
		case <-done:
		case <-ctx.Done():
			err = ctx.Err()
			a.workCancel()
			<-done
		}
	}
	a.workCancel()
	return err
}
