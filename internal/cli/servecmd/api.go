package servecmd

import (
	"context"
	"sync"
	"time"

	"github.com/amrshadid/coria/gen/coria/v1/coriav1connect"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/api"
	"github.com/amrshadid/coria/internal/checkedread"
	"github.com/amrshadid/coria/internal/core/settings"
	"github.com/amrshadid/coria/internal/driver"
	"github.com/amrshadid/coria/internal/events"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/receive/stow"
	"github.com/amrshadid/coria/internal/registry"
	"github.com/amrshadid/coria/internal/server"
	"github.com/amrshadid/coria/internal/settingvalue"
)

// The tenant registry's settings in serve. They are constants, not flags:
// none is something an operator has a reason to change yet, and each is sized
// so a dead database costs seconds, not minutes, while a slow but working one
// is not called dead. A flag can be added when a measured need shows.
const (
	// TenantCheckInterval is how often every tenant is checked, and the
	// registry reconciled with the master's list: 30 seconds, so a tenant
	// whose database comes back is served again within half a minute, at
	// the cost of one ping per tenant per interval.
	TenantCheckInterval = 30 * time.Second
	// TenantOpenTimeout bounds one tenant's open, ping and migration: 10
	// seconds, ample for a SQLite file or a reachable Postgres server and the
	// migrations this version ships, and the longest a dead database delays
	// the start of serve, since the tenants are opened in parallel.
	TenantOpenTimeout = 10 * time.Second
	// TenantPingTimeout bounds one check of an open tenant: 2 seconds, so a
	// database that stops answering is seen within one interval and a ping
	// never holds a check round for long.
	TenantPingTimeout = 2 * time.Second
	// TenantMaxOpening is how many tenants are opened at once: 16, enough to
	// open a thousand SQLite tenants in a few seconds without opening them
	// all in the same instant, against the process's limit of open files
	// and a Postgres server's limit of connections.
	TenantMaxOpening = 16
	// tenantNamesTTL is how long the names behind the tenants-by-state
	// metric are kept before the master is read again (see stateCounts).
	tenantNamesTTL = 10 * time.Second
	// tenantNamesTimeout bounds that read, so a scrape never waits long on a
	// master that does not answer.
	tenantNamesTimeout = 2 * time.Second
)

// APIServices builds the API over an open installation: the tenant registry,
// with the SQLite and Postgres tenant drivers under the database types a
// tenant's row records, then the handlers of package internal/api.
//
// The registry is started before the server listens: Start opens every
// tenant in parallel, TenantMaxOpening at a time, each under
// TenantOpenTimeout, and returns once each is ready, unreachable or failed.
// So serve answers its first request with every tenant already tried once;
// a tenant whose database did not answer in time is unreachable, answers
// UNAVAILABLE to a request into it while the others are served, and is
// tried again each TenantCheckInterval. The alternative, listening first and
// opening behind, would answer a tenant that is fine as unavailable for the
// first seconds of every start, and would let a creation race the start's
// own reading of the tenants.
func APIServices(d Deps) (Built, error) {
	reg, err := registry.New(registry.Config{
		Master: d.Master,
		Keys:   d.Keys,
		Drivers: map[string]registry.Driver{
			driver.SQLiteDriver:   driver.SQLiteTenants{DataDir: d.DataDir},
			driver.PostgresDriver: driver.PostgresTenants{},
		},
		Now:         time.Now,
		Interval:    TenantCheckInterval,
		OpenTimeout: TenantOpenTimeout,
		PingTimeout: TenantPingTimeout,
		MaxOpening:  TenantMaxOpening,
		Log:         d.Log,
		NewID:       server.NewID,
	})
	if err != nil {
		return Built{}, err
	}
	if err := reg.Start(context.Background()); err != nil {
		closeRegistry(reg)
		return Built{}, err
	}
	// The supervisor runs every tenant's receivers and reception pipeline,
	// and answers each receiver's health. The removal of discarded and
	// rejected files is left to the handlers' defaults: nothing is removed
	// from the incoming area by a handler (the pipeline removes it at its
	// next start). A quarantined file's header is read through the pipeline
	// that owns the incoming area.
	// One broadcaster wakes the streams of a tenant after a commit that
	// wrote its events; its timer covers a wake missed.
	feeds, err := events.New(events.Config{})
	if err != nil {
		closeRegistry(reg)
		return Built{}, err
	}
	// The running stow receivers, which the supervisor starts and stops
	// and the STOW-RS route finds by the caller's key.
	stowReceivers := stow.NewDirectory()
	sup, err := newReception(d, reg, feeds.Wake, stowReceivers)
	if err != nil {
		_ = feeds.Close()
		closeRegistry(reg)
		return Built{}, err
	}
	// The senders, which read archived bytes under the same count of reads
	// open as the API, so one bound covers both.
	reads := &checkedread.Slots{}
	senders, err := newSending(d, reg, feeds.Wake, reads)
	if err != nil {
		_ = feeds.Close()
		closeRegistry(reg)
		return Built{}, err
	}
	h, err := api.New(api.Config{Access: d.Access, Registry: reg, Master: d.Master, NewID: server.NewID, Log: d.Log, Keys: d.Keys, Now: time.Now,
		Receivers: receiverHealth{sup}, Live: receiverHealth{sup}, Feeds: feeds, Incoming: pipelineDecisions{sup}, Headers: heldHeaders{sup}, HeldFiles: heldHeaders{sup}, STOW: stowReceivers,
		Reads: reads, SenderHealths: senderHealths{senders}, SenderTests: senderTests{senders}})
	if err != nil {
		closeRegistry(reg)
		return Built{}, err
	}
	counts := &stateCounts{master: d.Master, registry: reg, now: time.Now}
	return Built{
		Register:       h.Register,
		Methods:        api.Methods{},
		WithDecision:   api.WithDecision,
		Registry:       reg,
		TenantsByState: counts.byState,
		// A tenant's networks live in its own database; each answer is kept
		// a few seconds, so a request into a tenant does not read it.
		TenantNetworks: access.CachedTenantNetworks(func(ctx context.Context, t access.TenantRef) (settings.Networks, error) {
			st, err := reg.Store(t.Name)
			if err != nil {
				return nil, err
			}
			return settingvalue.Reader{Master: d.Master}.OnTenant(st).TenantNetworks(ctx)
		}, tenantNetworksRefresh, time.Now),
		// The download of an instance over plain HTTP, and a tenant's
		// DICOMweb services (QIDO-RS, WADO-RS) on one route for their
		// subtree, each checked and audited as every route is.
		PatternRoutes: []server.PatternRoute{
			{Pattern: api.DownloadPattern, Need: api.DownloadNeed, Handler: h.Download},
			{Pattern: api.DICOMwebPattern, Need: api.DICOMwebNeed, Handler: h.DICOMweb},
			// STOW-RS, a POST on the same subtree, served by the tenant's
			// stow receiver that lists the caller's key.
			{Pattern: api.STOWPattern, Need: api.STOWNeed, Handler: h.STOW},
		},
		// No deadline applies to a read of a subject's instances, whose
		// stream may take hours; a client that stops reading is cut by the
		// server's stall bound instead. DICOMweb's one route carries both
		// retrieval, which may stream a whole study, and search, which is
		// bounded by its own row limits, so the route is long as a whole.
		// A store by STOW-RS may send a whole study in one request; a client
		// that stops sending is cut by the receiver's read stall bound.
		LongMethods: []string{coriav1connect.DataServiceReadFilesProcedure, api.DownloadPattern, api.DICOMwebPattern, api.STOWPattern},
		// Stopped last first: the event keeper, the senders (each giving its
		// unit back), then the receivers and pipelines, then the feeds.
		Reception: runners{closer{feeds}, sup, senders, events.NewKeeper(events.KeeperConfig{
			Tenants: keptFeeds{tenants: registryTenants{master: d.Master, registry: reg}, registry: reg, values: settingvalue.Reader{Master: d.Master}},
			Logger:  d.Log,
		})},
	}, nil
}

// closeRegistry closes a registry that will not be served, within the
// server's close deadline.
func closeRegistry(reg *registry.Registry) {
	ctx, cancel := context.WithTimeout(context.Background(), server.DefaultClose)
	defer cancel()
	_ = reg.Close(ctx)
}

// stateCounts counts the tenants in each state for /metrics. A tenant's
// state is the registry's current view, which costs no database read; only
// the names come from the master, since the registry does not list its
// tenants. They are read with one paged listing at most every
// tenantNamesTTL, so a scrape every few seconds costs the master nothing
// most of the time; a tenant created or removed shows within that time. A
// tenant the registry does not hold yet counts in the master's state.
type stateCounts struct {
	master   master.Store
	registry *registry.Registry
	now      func() time.Time

	mu    sync.Mutex
	rows  []master.Tenant
	until time.Time
}

// byState returns how many tenants are in each state.
func (s *stateCounts) byState() map[string]int {
	rows := s.names()
	out := make(map[string]int, 4)
	for _, row := range rows {
		state := row.State
		if v, ok := s.registry.State(row.Name); ok {
			state = v.State
		}
		out[string(state)]++
	}
	return out
}

// names returns the tenants as last read from the master, reading them
// again when the copy is older than tenantNamesTTL. A read that fails keeps
// the last copy, so a master that does not answer leaves the counts as they
// were rather than empty.
func (s *stateCounts) names() []master.Tenant {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Before(s.until) {
		return s.rows
	}
	ctx, cancel := context.WithTimeout(context.Background(), tenantNamesTimeout)
	defer cancel()
	var rows []master.Tenant
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		batch, next, err := s.master.ListTenants(ctx, page)
		if err != nil {
			return s.rows
		}
		rows = append(rows, batch...)
		if next == "" {
			break
		}
		page.Cursor = next
	}
	s.rows, s.until = rows, now.Add(tenantNamesTTL)
	return rows
}
