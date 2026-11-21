package servecmd

import (
	"context"

	"github.com/amrshadid/coria/internal/auditseal"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/registry"
	"github.com/amrshadid/coria/internal/secret"
	"github.com/amrshadid/coria/internal/server"
	"github.com/amrshadid/coria/internal/settingvalue"
)

// newSealer returns the sealing pass of every audit log of the installation
// (package auditseal), signed with the audit key derived from the key
// file's newest version, run every audit.seal-interval and once more at
// stop.
func newSealer(d Deps, reg *registry.Registry) (*auditseal.Sealer, error) {
	id, err := server.ReadInstallationID(d.DataDir)
	if err != nil {
		return nil, err
	}
	derived, err := d.Keys.Derive(secret.AuditSeal)
	if err != nil {
		return nil, err
	}
	signer, err := derived.Signer()
	if err != nil {
		return nil, err
	}
	values := settingvalue.Reader{Master: d.Master}
	return auditseal.New(auditseal.Config{
		Installation: id,
		Master:       d.Master,
		Tenants:      sealedTenants{master: d.Master, registry: reg},
		Signer:       signer,
		Interval:     values.AuditSealInterval,
		Retention:    values.AuditRetention,
		NewID:        server.NewID,
		Log:          d.Log,
	})
}

// sealedTenants is the sealing pass's view of the tenants: their ids and
// names from the master, each one's open store from the registry.
type sealedTenants struct {
	master   master.Store
	registry *registry.Registry
}

var _ auditseal.Tenants = sealedTenants{}

// List implements auditseal.Tenants, reading the master page after page.
func (s sealedTenants) List(ctx context.Context) ([]auditseal.TenantLog, error) {
	var out []auditseal.TenantLog
	page := master.Page{Limit: master.MaxPageLimit}
	for {
		batch, next, err := s.master.ListTenants(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, t := range batch {
			out = append(out, auditseal.TenantLog{ID: t.ID, Name: t.Name})
		}
		if next == "" {
			return out, nil
		}
		page.Cursor = next
	}
}

// Log implements auditseal.Tenants.
func (s sealedTenants) Log(_ context.Context, name string) (master.AuditSeals, error) {
	return s.registry.Store(name)
}
