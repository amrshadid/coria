package receive

import (
	"context"
	"strconv"

	"github.com/amrshadid/magnetar/internal/fault"
)

// Start reconciles the incoming area with the database (Recover), queues the
// units a crash left missing, and starts the batcher and the workers. Files
// are accepted from then on. It returns what Recover found. A second Start
// is an error.
func (p *Pipeline) Start(ctx context.Context) (RecoverReport, error) {
	if p.running.Load() {
		return RecoverReport{}, fault.New(fault.Internal, "the reception pipeline is already running")
	}
	p.started = p.now()
	p.workCtx, p.stopWork = context.WithCancel(context.WithoutCancel(ctx))
	p.stepCtx, p.abort = context.WithCancel(context.WithoutCancel(ctx))
	rep, err := p.Recover(ctx)
	if err != nil {
		return rep, err
	}
	if err := p.repairWork(ctx); err != nil {
		return rep, err
	}
	stop := make(chan struct{})
	p.batchStop = stop
	p.wg.Add(1 + p.cfg.Workers)
	go func() {
		defer p.wg.Done()
		p.batch.run(stop)
	}()
	for i := range p.cfg.Workers {
		go p.worker(p.cfg.Owner + "-" + strconv.Itoa(i))
	}
	p.running.Store(true)
	if rep.Missing > 0 || rep.Rechecked > 0 || rep.Removed > 0 {
		p.log.Info("the incoming area was reconciled", "rechecked", rep.Rechecked, "renamed", rep.Renamed,
			"removed", rep.Removed, "temp", rep.Temp, "missing", rep.Missing, "strangers", rep.Strangers)
	}
	return rep, nil
}

// Stop stops taking files, waits for every Accept in flight, writes the
// batches held, and stops the workers: a step in flight is let finish while
// ctx lasts, and is cut short when ctx ends (its unit stays leased and is
// taken up after its lease, by this process's next start or another). It
// returns when every goroutine the pipeline started has ended, then closes
// the incoming area.
func (p *Pipeline) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	p.mu.Unlock()
	p.accepting.Wait()
	if !p.running.Load() {
		return p.area.Close()
	}
	close(p.batchStop)
	p.stopWork()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		p.abort()
		<-done
	}
	p.abort()
	p.running.Store(false)
	return p.area.Close()
}
