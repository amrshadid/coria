package receive

import (
	"context"
	"errors"
	"time"

	"github.com/amrshadid/coria/internal/blob"
	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/core/layout"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/receive/frames"
	"github.com/amrshadid/coria/internal/tenant"
)

// framePassRetry is how long the pass of frame tables waits after a pass
// that stopped on a failure (a storage or the database not answering)
// before it passes again from the first scan.
const framePassRetry = time.Minute

// framePass gives the metadata sidecars written before frame tables
// existed their tables (frames.Run), once a start: every sidecar written
// after it starts has its tables already, because the archive step writes
// them, so one pass that ends leaves none to do. A pass that stops on a
// failure is run again after framePassRetry, and the next start runs one
// again, which finds the scans done by reading one small range of each.
// It runs while the workers run and ends with them.
func (p *Pipeline) framePass() {
	defer p.wg.Done()
	ctx := p.workCtx
	cfg := frames.Config{
		Catalog: p.store,
		OpenStorage: func(ctx context.Context, name string) (blob.Store, error) {
			return p.cfg.OpenStorage(ctx, name)
		},
		Lock: func(storage string, path label.Path) func() {
			key, err := layout.ManifestKey(path)
			if err != nil {
				// A scan the catalog lists has labels that make keys; the
				// lock is then still a lock of its own, by the labels.
				key = path.Subject + "/" + path.Session + "/" + path.Scan
			}
			return p.lockScan(storage, key)
		},
		HeaderLimit: p.headerLimit,
		Busy:        p.busy,
		Wake:        p.cfg.Wake,
		Clock:       p.cfg.Clock,
		Logger:      p.log,
	}
	if ev, ok := p.store.(tenant.Events); ok {
		cfg.Events = ev
	}
	for ctx.Err() == nil {
		rep, err := frames.Run(ctx, cfg)
		if rep.ScansDone > 0 {
			p.log.Info("frame tables were added to scans archived before them", "scans", rep.ScansDone,
				"instances", rep.InstancesDone, "checked", rep.ScansChecked)
		}
		if err == nil {
			return
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		p.log.Warn("the pass of frame tables stopped; it passes again later", "reason", fault.Message(err))
		select {
		case <-ctx.Done():
			return
		case <-p.cfg.Clock.After(framePassRetry):
		}
	}
}

// busy reports whether reception is at work: a step in flight, or a file
// landed since the last time it was asked.
func (p *Pipeline) busy() bool {
	landed := p.stats.Landed.Load()
	moved := p.landedSeen.Swap(landed) != landed
	return moved || p.stepping.Load() > 0
}
