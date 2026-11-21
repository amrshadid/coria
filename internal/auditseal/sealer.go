package auditseal

import (
	"context"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/amrshadid/coria/internal/core/auditchain"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/master"
)

// DefaultInterval is the pass's interval when the setting cannot be read:
// audit.seal-interval's built-in default.
const DefaultInterval = 10 * time.Minute

// maxSealsPerLog bounds the checkpoints one pass makes in one log, so a log
// written faster than it is sealed does not hold the pass for ever; what is
// left waits for the next pass. 100 checkpoints of 10,000 entries are a
// million entries a log a pass.
const maxSealsPerLog = 100

// maxRetries bounds how often a pass reads a log again after a seal it made
// was refused as Conflict (another sealer sealed first).
const maxRetries = 3

// ServerUser is the local user of the entries the server writes for itself,
// on no request: a pruning's entry.
const ServerUser = "server"

// TenantLog names one tenant's log: by its id, which the chain's signed
// texts name and a move keeps, and by its name, which the registry opens it
// by.
type TenantLog struct {
	// ID is the tenant's id: the log's name in its checkpoints.
	ID string
	// Name is the tenant's name.
	Name string
}

// Tenants is what a pass needs of the installation's tenants.
type Tenants interface {
	// List returns every tenant now.
	List(ctx context.Context) ([]TenantLog, error)
	// Log returns a tenant's open store, or an error when it is not open
	// (not ready, unreachable): the pass skips it until the next.
	Log(ctx context.Context, name string) (master.AuditSeals, error)
}

// Config is what New needs.
type Config struct {
	// Installation is the installation's id, which every checkpoint names.
	Installation string
	// Master is the master's store.
	Master master.AuditSeals
	// Tenants lists the tenants and opens their logs; nil seals the
	// master's log alone.
	Tenants Tenants
	// Signer signs the checkpoints: the installation's audit signer
	// (secret.Keys.Derive(secret.AuditSeal), then Signer).
	Signer auditchain.Signer
	// Interval returns audit.seal-interval as it is now; nil, or one that
	// fails, is DefaultInterval.
	Interval func(ctx context.Context) (time.Duration, error)
	// Retention returns audit.retention as it is now, 0 to keep everything;
	// nil keeps everything, and one that fails prunes nothing that pass.
	Retention func(ctx context.Context) (time.Duration, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// NewID makes the id of a pruning's audit entry.
	NewID func() string
	// Log receives a line per checkpoint made and per log a pass could not
	// seal; nil discards them.
	Log *slog.Logger
}

// Sealer runs the sealing pass on a timer and at stop. Make it with New.
type Sealer struct {
	cfg  Config
	log  *slog.Logger
	stop context.CancelFunc
	done sync.WaitGroup
	// mu keeps two passes from running at once in one process: the timer's
	// and the one Stop runs.
	mu sync.Mutex
}

// New returns a Sealer that is not yet running.
func New(cfg Config) (*Sealer, error) {
	if cfg.Master == nil || cfg.Signer == nil || cfg.NewID == nil {
		return nil, fault.New(fault.Internal, "auditseal: a sealer needs the master, a signer and ids")
	}
	if err := master.ValidateID("installation id", cfg.Installation); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Sealer{cfg: cfg, log: log}, nil
}

// Start runs a pass at once and then one every interval, read again after
// each pass, in a goroutine that Stop ends.
func (s *Sealer) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	s.done.Go(func() {
		for {
			s.Pass(ctx)
			t := time.NewTimer(s.interval(ctx))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	})
	return nil
}

// Stop ends the timer, waits for a pass running, then runs one last pass
// within ctx, so the entries written up to the stop are sealed.
func (s *Sealer) Stop(ctx context.Context) error {
	if s.stop != nil {
		s.stop()
	}
	s.done.Wait()
	s.Pass(ctx)
	return nil
}

// interval returns the pass's interval now.
func (s *Sealer) interval(ctx context.Context) time.Duration {
	if s.cfg.Interval == nil {
		return DefaultInterval
	}
	d, err := s.cfg.Interval(ctx)
	if err != nil || d <= 0 {
		return DefaultInterval
	}
	return d
}

// Pass seals every log once, anchors the master and the tenants to each
// other, and prunes what retention makes due. Start runs it on a timer and
// Stop once more; a test calls it directly. A log that fails is logged and
// left for the next pass.
func (s *Sealer) Pass(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	masterLast, masterOK := s.seal(ctx, s.cfg.Master, master.MasterAuditLog, "")
	var tenants []TenantLog
	if s.cfg.Tenants != nil {
		var err error
		if tenants, err = s.cfg.Tenants.List(ctx); err != nil {
			s.log.Warn("auditseal: the tenants could not be listed; only the installation's log was sealed", "reason", fault.Message(err))
			tenants = nil
		}
	}
	retention := s.retention(ctx)
	for _, t := range tenants {
		if ctx.Err() != nil {
			return
		}
		st, err := s.cfg.Tenants.Log(ctx, t.Name)
		if err != nil {
			continue
		}
		last, ok := s.seal(ctx, st, t.ID, t.Name)
		if masterOK {
			s.anchor(ctx, st, t.Name, masterLast)
		}
		if ok {
			s.anchor(ctx, s.cfg.Master, "", last)
		}
		s.prune(ctx, st, t.Name, retention)
	}
	s.prune(ctx, s.cfg.Master, "", retention)
}

// retention returns audit.retention now, or 0 (prune nothing) when it is
// not set up or cannot be read.
func (s *Sealer) retention(ctx context.Context) time.Duration {
	if s.cfg.Retention == nil {
		return 0
	}
	d, err := s.cfg.Retention(ctx)
	if err != nil {
		s.log.Warn("auditseal: audit.retention could not be read; nothing is pruned this pass", "reason", fault.Message(err))
		return 0
	}
	return d
}

// logger returns the pass's logger for one log.
func (s *Sealer) logger(name, tenantName string) *slog.Logger {
	if tenantName == "" {
		return s.log.With("log", name)
	}
	return s.log.With("log", name, "tenant", tenantName)
}

// seal seals one log as far as it goes, and returns its latest checkpoint
// and whether it has one.
func (s *Sealer) seal(ctx context.Context, st master.AuditSeals, name, tenantName string) (master.AuditCheckpoint, bool) {
	log := s.logger(name, tenantName)
	last, ok, err := SealLog(ctx, st, s.cfg.Installation, name, s.cfg.Signer, s.cfg.Now, func(c master.AuditCheckpoint) {
		log.Info("auditseal: checkpoint made", "from_seq", c.From, "to_seq", c.To,
			"digest", hex.EncodeToString(c.Digest[:]), "key_version", c.KeyVersion)
	})
	if err != nil {
		log.Warn("auditseal: the log could not be sealed; it is tried again next pass", "reason", fault.Message(err))
	}
	return last, ok
}

// SealLog seals one log as far as it goes: the entries without a seq, in
// checkpoints of at most master.MaxAuditSealBatch, at most maxSealsPerLog
// of them. made is told of each checkpoint kept. It returns the log's
// latest checkpoint afterward and whether it has one, also when it fails
// part way, with what it kept.
func SealLog(ctx context.Context, st master.AuditSeals, installation, name string, signer auditchain.Signer,
	now func() time.Time, made func(master.AuditCheckpoint)) (master.AuditCheckpoint, bool, error) {
	last, ok, err := latest(ctx, st)
	if err != nil {
		return last, ok, err
	}
	retries := 0
	for seals := 0; seals < maxSealsPerLog; {
		entries, err := st.UnsealedAudit(ctx, master.AuditPosition{}, master.MaxAuditSealBatch)
		if err != nil {
			return last, ok, err
		}
		if len(entries) == 0 {
			return last, ok, nil
		}
		from := auditchain.State{}
		if ok {
			from = stateAfter(last)
		}
		chained := make([]auditchain.Entry, len(entries))
		ids := make([]string, len(entries))
		for i, e := range entries {
			chained[i] = chainEntry(int64(from.Seq)+int64(i)+1, e) //nolint:gosec // G115: a seq read from a store.
			ids[i] = e.ID
		}
		c, _, err := auditchain.Seal(signer, installation, name, from, chained, now().UTC())
		if err != nil {
			return last, ok, fault.Wrap(fault.Internal, err, "auditseal: the entries cannot be sealed")
		}
		digest, err := c.Digest()
		if err != nil {
			return last, ok, fault.Wrap(fault.Internal, err, "auditseal: the checkpoint cannot be digested")
		}
		kept := storeCheckpoint(c, digest)
		err = st.SealAudit(ctx, master.AuditSeal{IDs: ids, Checkpoint: kept})
		if fault.KindOf(err) == fault.Conflict && retries < maxRetries {
			// Sealed by another since it was read: read the log again.
			retries++
			if last, ok, err = latest(ctx, st); err != nil {
				return last, ok, err
			}
			continue
		}
		if err != nil {
			return last, ok, err
		}
		// The store keeps the time to the microsecond; so does the signed
		// text, so the checkpoint as read back is this one.
		kept.Time = time.UnixMicro(kept.Time.UnixMicro()).UTC()
		last, ok, retries = kept, true, 0
		seals++
		if made != nil {
			made(kept)
		}
		if len(entries) < master.MaxAuditSealBatch {
			return last, ok, nil
		}
	}
	return last, ok, nil
}

// latest returns a log's latest checkpoint and whether it has one.
func latest(ctx context.Context, st master.AuditSealReads) (master.AuditCheckpoint, bool, error) {
	cs, err := st.RecentAuditCheckpoints(ctx, 0, 1)
	if err != nil || len(cs) == 0 {
		return master.AuditCheckpoint{}, false, err
	}
	return cs[0], true, nil
}

// anchor keeps c, the latest checkpoint of another log, in st. An anchor
// the store refuses as going back means the log c is of went back to an
// earlier point, which is what anchors are kept to show: it is logged as an
// error.
func (s *Sealer) anchor(ctx context.Context, st master.AuditSeals, tenantName string, c master.AuditCheckpoint) {
	err := st.SetAuditAnchor(ctx, master.AuditAnchor{Log: c.Log, To: c.To, Digest: c.Digest})
	switch {
	case err == nil:
	case fault.KindOf(err) == fault.Conflict:
		s.logger(c.Log, tenantName).Error("auditseal: a log went back from the checkpoint the other log keeps of it",
			"anchored_log", c.Log, "to_seq", c.To, "reason", fault.Message(err))
	default:
		s.logger(c.Log, tenantName).Warn("auditseal: the anchor could not be kept; it is tried again next pass", "reason", fault.Message(err))
	}
}

// prune removes what retention makes due in one log, a pruning call at a
// time, until none is due or ctx ends.
func (s *Sealer) prune(ctx context.Context, st master.AuditSeals, tenantName string, retention time.Duration) {
	if retention <= 0 {
		return
	}
	now := s.cfg.Now().UTC()
	before := now.Add(-retention)
	for ctx.Err() == nil {
		got, err := st.PruneAudit(ctx, before, master.MaxAuditPruneBatch, master.AuditEntry{
			ID: s.cfg.NewID(), Time: now, LocalUser: ServerUser, RemoteAddress: "local", Method: "AuditRetention",
			TenantName: tenantName, Object: "audit", Outcome: master.OutcomeDone,
		})
		if err != nil {
			s.log.Warn("auditseal: the log could not be pruned; it is tried again next pass", "tenant", tenantName, "reason", fault.Message(err))
			return
		}
		if got.Checkpoints > 0 {
			s.log.Info("auditseal: entries pruned by audit.retention", "tenant", tenantName,
				"checkpoints", got.Checkpoints, "entries", got.Entries)
		}
		if !got.More {
			return
		}
	}
}
