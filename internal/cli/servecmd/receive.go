package servecmd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/amrshadid/magnetar/internal/api"
	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/blob/dir"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/place"
	"github.com/amrshadid/magnetar/internal/receive"
	"github.com/amrshadid/magnetar/internal/receive/dimse"
	"github.com/amrshadid/magnetar/internal/receive/supervise"
	"github.com/amrshadid/magnetar/internal/registry"
	"github.com/amrshadid/magnetar/internal/settingvalue"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// The directories of the data directory the receivers use.
const (
	// IncomingDirName holds the incoming area: <data-dir>/incoming/<tenant>.
	IncomingDirName = "incoming"
	// TLSDirName holds each tenant's receiver certificates:
	// <data-dir>/tls/<tenant> (receiver.proto, ReceiverTls).
	TLSDirName = "tls"
)

// Runner is a part of the server that runs beside the API: started once the
// server listens, stopped before the server stops, so it never uses a
// tenant's database after the registry has closed it.
type Runner interface {
	// Start starts it; it returns without waiting for what it starts.
	Start(ctx context.Context) error
	// Stop stops it within ctx.
	Stop(ctx context.Context) error
}

// newReception returns the supervisor of every tenant's receivers and
// reception pipeline, over the registry's tenants.
func newReception(d Deps, reg *registry.Registry, wake func(tenantName string)) (*supervise.Supervisor, error) {
	incomingRoot := filepath.Join(d.DataDir, IncomingDirName)
	if err := os.MkdirAll(incomingRoot, 0o700); err != nil {
		return nil, fault.Wrap(fault.Unavailable, err, "the incoming area cannot be made in the data directory")
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	values := settingvalue.Reader{Master: d.Master}
	// The pipeline keeps a storage open across steps (tenantStorages), so
	// the S3 driver's cleanup of unfinished uploads runs once per open; the
	// supervisor's look at a storage's space opens and closes it each time,
	// so it asks for none.
	pipelineOpener := place.Opener{Values: values, Keys: d.Keys}
	spaceOpener := place.Opener{Values: values, Keys: d.Keys, StaleUploadAge: -1}
	return supervise.New(supervise.Config{
		Tenants:      registryTenants{master: d.Master, registry: reg},
		Installation: d.Master,
		StartPipeline: func(ctx context.Context, name string, st tenant.Pipeline, g supervise.Gates) (supervise.Pipeline, error) {
			// The tenant's keyed hash for its naming rules: without it a
			// default rule names nothing, so the pipeline does not start
			// and the supervisor shows the receivers down until it can.
			hasher, err := reg.NamingHasher(ctx, name)
			if err != nil {
				return nil, err
			}
			storages := newTenantStorages(pipelineOpener, d.Master, name, st)
			p, err := receive.New(receive.Config{
				Tenant: name, Store: st, IncomingRoot: incomingRoot, Logger: log,
				OpenStorage: storages.Open, Hasher: hasher, HeaderLimit: values.HeaderLimit,
				LowDisk: g.LowDisk, OverLimit: g.OverLimit, Wake: func() { wake(name) },
			})
			if err != nil {
				return nil, err
			}
			if _, err := p.Start(ctx); err != nil {
				_ = storages.Close()
				return nil, err
			}
			return pipelineRun{Pipeline: p, storages: storages}, nil
		},
		OpenStorage: func(ctx context.Context, tenantName string, st tenant.Pipeline, storage string) (blob.Store, error) {
			t, err := d.Master.TenantByName(ctx, tenantName)
			if err != nil {
				return nil, err
			}
			row, err := st.Storage(ctx, storage)
			if err != nil {
				return nil, err
			}
			return spaceOpener.Open(ctx, place.TenantOf(t.ID, tenantName, st), row)
		},
		NewListener: func(spec supervise.ListenerSpec) (supervise.Listener, error) {
			return dimse.New(dimse.Config{
				Receiver: spec.Receiver, BindAddress: spec.BindAddress, Sink: spec.Sink,
				TLSDir:  filepath.Join(d.DataDir, TLSDirName, spec.Tenant),
				Refusal: dimse.Refusal(spec.Refusal), Report: dimse.Report(spec.Report),
				Logger: log.With("tenant", spec.Tenant),
			})
		},
		IncomingUsage: func(ctx context.Context) (blob.Usage, error) {
			s, err := dir.Open(incomingRoot)
			if err != nil {
				return blob.Usage{}, err
			}
			defer func() { _ = s.Close() }()
			return s.Usage(ctx)
		},
		Wake:   wake,
		Logger: log,
	})
}

// pipelineRun is one tenant's running pipeline with the storages it keeps
// open, closed once it has stopped.
type pipelineRun struct {
	*receive.Pipeline
	storages *tenantStorages
}

// Stop stops the pipeline, then closes its storages: no step holds one
// once Stop has returned.
func (r pipelineRun) Stop(ctx context.Context) error {
	err := r.Pipeline.Stop(ctx)
	return errors.Join(err, r.storages.Close())
}

// registryTenants is the supervisor's view of the installation's tenants:
// their names from the master, their databases from the registry.
type registryTenants struct {
	master   master.Store
	registry *registry.Registry
}

// Names reads every tenant's name from the master, page after page.
func (r registryTenants) Names(ctx context.Context) ([]string, error) {
	var out []string
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		batch, next, err := r.master.ListTenants(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, t := range batch {
			out = append(out, t.Name)
		}
		if next == "" {
			return out, nil
		}
		page.Cursor = next
	}
}

// Store returns the tenant's open database from the registry.
func (r registryTenants) Store(name string) (tenant.Store, error) { return r.registry.Store(name) }

// receiverHealth answers the API's question of a receiver's health from
// the supervisor, in the API's terms, and passes the API's notice of a
// change of receivers to it.
type receiverHealth struct{ s *supervise.Supervisor }

var _ api.ReceiverChanges = receiverHealth{}

// downReasons maps the contract's down reasons to the API's.
var downReasons = map[tenant.ReceiverDownReason]api.DownReason{
	tenant.DownDisabled: api.DownDisabled, tenant.DownAddressInUse: api.DownAddressInUse,
	tenant.DownPermissionDenied: api.DownPermissionDenied, tenant.DownAddressNotAvailable: api.DownAddressNotAvailable,
	tenant.DownTLS: api.DownTLS, tenant.DownNearlyFull: api.DownNearlyFull, tenant.DownTenantNotReady: api.DownTenantNotReady,
	tenant.DownStopping: api.DownStopping, tenant.DownOther: api.DownOther, tenant.DownIncomingLimit: api.DownIncomingLimit,
}

// ReceiversChanged tells the supervisor at once that a tenant's receivers
// changed (api.ReceiverChanges), so a receiver added, changed or removed is
// started, restarted or stopped without waiting for its periodic read.
func (h receiverHealth) ReceiversChanged(tenantName string) { h.s.Poke(tenantName) }

// Health returns the supervisor's health of the receiver.
func (h receiverHealth) Health(tenantName string, r tenant.Receiver) api.Health {
	s := h.s.Health(tenantName, r)
	out := api.Health{Detail: s.Detail, Since: s.Since}
	switch s.State {
	case tenant.ReceiverStarting:
		out.State = api.HealthStarting
	case tenant.ReceiverUp:
		out.State = api.HealthUp
	default:
		out.State = api.HealthDown
		out.Reason = downReasons[s.Reason]
		if out.Reason == api.DownNone {
			out.Reason = api.DownOther
		}
	}
	return out
}

var _ api.Live = receiverHealth{}

// TenantLive returns what the supervisor holds of the tenant, as the
// handlers' status reads it.
func (h receiverHealth) TenantLive(tenantName string) (api.TenantLive, bool) {
	v, ok := h.s.View(tenantName)
	if !ok {
		return api.TenantLive{}, false
	}
	out := api.TenantLive{
		Storages: make(map[string]api.StorageSpace, len(v.Storages)),
		Throughput: api.Throughput{FilesReceived: v.Throughput.FilesReceivedPerSecond, BytesReceived: v.Throughput.BytesReceivedPerSecond,
			FilesArchived: v.Throughput.FilesArchivedPerSecond, BytesArchived: v.Throughput.BytesArchivedPerSecond},
	}
	for _, r := range v.Receivers {
		out.Receivers = append(out.Receivers, r.Receiver)
	}
	for name, s := range v.Storages {
		out.Storages[name] = api.StorageSpace{FreeBytes: s.FreeBytes, NearlyFull: s.NearlyFull}
	}
	return out, true
}

// pipelineDecisions is the API's Incoming: a release and a retry run
// through the tenant's running reception pipeline, which gives a released
// file's bytes their landed name before the store records the release and
// queues a retry's first unit at once; the removal of a rejected or
// discarded file's bytes is handed to it too. With no pipeline running (the
// tenant's database not ready), a release or a retry is refused as
// unavailable, so nothing is recorded that the bytes do not follow, and a
// removal is left to the pipeline's next start, which finds the decision
// recorded.
type pipelineDecisions struct{ s *supervise.Supervisor }

var _ api.Incoming = pipelineDecisions{}

// errNoPipeline answers a release or a retry of a tenant whose reception
// pipeline does not run.
var errNoPipeline = fault.New(fault.Unavailable, "the tenant's reception is not running now; try again shortly")

// Release releases through the tenant's pipeline.
func (d pipelineDecisions) Release(ctx context.Context, tenantName string, _ tenant.Pipeline, rel tenant.Release, audit master.AuditEntry, guard tenant.Guard) (tenant.QuarantinedFile, error) {
	p, ok := d.s.Decisions(tenantName)
	if !ok {
		return tenant.QuarantinedFile{}, errNoPipeline
	}
	return p.Release(ctx, rel, audit, guard)
}

// Retry retries through the tenant's pipeline, which reads its own clock.
func (d pipelineDecisions) Retry(ctx context.Context, tenantName string, _ tenant.Pipeline, id, newReceptionID, receiver string, _ time.Time, audit master.AuditEntry, guard tenant.Guard) (tenant.Reception, tenant.Reception, error) {
	p, ok := d.s.Decisions(tenantName)
	if !ok {
		return tenant.Reception{}, tenant.Reception{}, errNoPipeline
	}
	return p.Retry(ctx, id, newReceptionID, receiver, audit, guard)
}

// RemoveDiscarded hands the removal to the tenant's pipeline, or leaves it
// to its next start.
func (d pipelineDecisions) RemoveDiscarded(ctx context.Context, tenantName, receptionID string) error {
	if p, ok := d.s.Decisions(tenantName); ok {
		return p.RemoveDiscarded(ctx, receptionID)
	}
	return nil
}

// RemoveRejected hands the removal to the tenant's pipeline, or leaves it
// to its next start.
func (d pipelineDecisions) RemoveRejected(_ context.Context, tenantName, incoming string) error {
	if p, ok := d.s.Decisions(tenantName); ok {
		return p.RemoveRejected(incoming)
	}
	return nil
}
