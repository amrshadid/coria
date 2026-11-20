package servecmd

import (
	"context"
	"sync"
	"time"

	"github.com/amrshadid/coria/internal/api"
	"github.com/amrshadid/coria/internal/driver"
	"github.com/amrshadid/coria/internal/events"
	"github.com/amrshadid/coria/internal/master"
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
	// rejected files and a quarantined file's header are left to the
	// handlers' defaults: nothing is removed from the incoming area by a
	// handler (the pipeline removes it at its next start), no header is
	// read.
	// One broadcaster wakes the streams of a tenant after a commit that
	// wrote its events; its timer covers a wake missed.
	feeds, err := events.New(events.Config{})
	if err != nil {
		closeRegistry(reg)
		return Built{}, err
	}
	sup, err := newReception(d, reg, feeds.Wake)
	if err != nil {
		_ = feeds.Close()
		closeRegistry(reg)
		return Built{}, err
	}
	h, err := api.New(api.Config{Access: d.Access, Registry: reg, Master: d.Master, NewID: server.NewID, Log: d.Log, Keys: d.Keys, Now: time.Now,
		Receivers: receiverHealth{sup}, Live: receiverHealth{sup}, Feeds: feeds, Incoming: pipelineDecisions{sup}})
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
		// The download of an instance over plain HTTP: checked and audited
		// as the contract says, and answered 501 until its handler is
		// written.
		PatternRoutes: []server.PatternRoute{{Pattern: api.DownloadPattern, Need: api.DownloadNeed, Handler: server.NotImplemented}},
		Reception: runners{closer{feeds}, sup, events.NewKeeper(events.KeeperConfig{
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
