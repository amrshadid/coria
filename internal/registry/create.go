package registry

import (
	"context"
	"errors"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// NewTenant is a tenant to create.
type NewTenant struct {
	// ID is the tenant's id, made by the caller, so a repeated request
	// names the same tenant.
	ID string
	// Name is the tenant's name (master.ValidateTenantName).
	Name string
	// Driver is the database type, one of Config.Drivers.
	Driver string
	// Address is the database's address with its password; empty for a
	// type that needs none (SQLite). It is sealed before it is stored and
	// appears in nothing the registry returns, logs or records.
	Address string
}

// Create creates a tenant: it writes the tenant's row as upgrading with the
// caller's audit entry (both or neither, as the master writes every change),
// then opens and migrates its database, then records its state, and returns
// the tenant in that state. A tenant whose database does not answer or does
// not open is still created, unreachable or failed, as the API contract's
// CreateTenant answers.
//
// It is safe to repeat. A crash after the row is written leaves an
// upgrading row that the next Start opens; a Create again with the same id,
// name and database type finishes the first one's work and records its own
// request with AppendAudit. A tenant of that name with another id or type
// is Conflict.
func (r *Registry) Create(ctx context.Context, nt NewTenant, audit master.AuditEntry) (master.Tenant, error) {
	if err := r.enter(); err != nil {
		return master.Tenant{}, err
	}
	defer r.wg.Done()
	if err := master.ValidateID("tenant id", nt.ID); err != nil {
		return master.Tenant{}, err
	}
	if err := master.ValidateTenantName(nt.Name); err != nil {
		return master.Tenant{}, err
	}
	drv, ok := r.cfg.Drivers[nt.Driver]
	if !ok {
		return master.Tenant{}, fault.New(fault.Invalid, "this server has no driver for the database type "+nt.Driver)
	}
	// The address is checked by its driver before anything is written, so
	// an address the driver refuses never leaves a row behind.
	if _, err := drv.Location(nt.Name, nt.Address); err != nil {
		return master.Tenant{}, err
	}
	audit = withDetails(audit, map[string]string{DetailDatabaseType: nt.Driver})
	r.listing.RLock()
	defer r.listing.RUnlock()
	defer r.names.lock(nt.Name)()

	row, err := r.cfg.Master.TenantByName(ctx, nt.Name)
	switch {
	case err == nil:
		if err := repeat(row, nt); err != nil {
			return master.Tenant{}, err
		}
		if err := r.recordRepeat(ctx, audit); err != nil {
			return master.Tenant{}, err
		}
	case fault.KindOf(err) == fault.NotFound:
		row, err = r.writeRow(ctx, nt, audit)
		if err != nil {
			return master.Tenant{}, err
		}
	default:
		return master.Tenant{}, err
	}
	if r.afterRow != nil {
		if err := r.afterRow(); err != nil {
			return master.Tenant{}, err
		}
	}

	e := r.register(row)
	if e == nil {
		if r.closed.Load() {
			return master.Tenant{}, fault.New(fault.Unavailable, "the server is shutting down")
		}
		return master.Tenant{}, fault.New(fault.Conflict, "a tenant named "+nt.Name+" is being removed; try again")
	}
	// The same order as every round: a slot first, then the tenant's lock,
	// so a full round and a creation never wait for each other.
	select {
	case r.slots <- struct{}{}:
	case <-r.ctx.Done():
		return master.Tenant{}, fault.New(fault.Unavailable, "the server is shutting down")
	}
	defer func() { <-r.slots }()
	e.op.Lock()
	defer e.op.Unlock()
	if e.removed {
		return master.Tenant{}, fault.New(fault.NotFound, "the tenant was removed while it was being created")
	}
	if e.store == nil || e.view.Load().state != master.StateReady {
		r.refresh(e, true)
	}
	v := e.view.Load()
	row.State, row.StateReason = v.state, v.reason
	return row, nil
}

// writeRow seals the address and writes the tenant's row as upgrading. When
// another creation of the same name wrote its row first, it is treated as a
// repeat if it is the same tenant.
func (r *Registry) writeRow(ctx context.Context, nt NewTenant, audit master.AuditEntry) (master.Tenant, error) {
	sealed, err := r.cfg.Keys.Seal(addressContext(nt.ID), []byte(nt.Address))
	if err != nil {
		return master.Tenant{}, err
	}
	row := master.Tenant{
		ID:            nt.ID,
		Name:          nt.Name,
		Driver:        nt.Driver,
		SealedAddress: sealed,
		State:         master.StateUpgrading,
		Created:       r.cfg.Now(),
	}
	err = r.cfg.Master.CreateTenant(ctx, row, audit)
	if fault.KindOf(err) != fault.Conflict {
		return row, err
	}
	existing, rerr := r.cfg.Master.TenantByName(ctx, nt.Name)
	if rerr != nil {
		return master.Tenant{}, err
	}
	if rerr := repeat(existing, nt); rerr != nil {
		return master.Tenant{}, rerr
	}
	return existing, r.recordRepeat(ctx, audit)
}

// repeat returns nil when row is the tenant nt asks for, so a creation that
// finds it is a repeat, and Conflict otherwise.
func repeat(row master.Tenant, nt NewTenant) error {
	if row.ID != nt.ID || row.Driver != nt.Driver {
		return fault.New(fault.Conflict, "a tenant named "+nt.Name+" exists")
	}
	return nil
}

// recordRepeat writes the audit entry of a repeated creation, which changed
// nothing in the master. An entry whose id is taken was written by the
// request being repeated (master.ErrDuplicateAuditID), so it is already
// recorded.
func (r *Registry) recordRepeat(ctx context.Context, audit master.AuditEntry) error {
	err := r.cfg.Master.AppendAudit(ctx, audit)
	if errors.Is(err, master.ErrDuplicateAuditID) {
		return nil
	}
	return err
}

// Remove unregisters the tenant named name and removes its row and grants
// from the master, with the caller's audit entry, to which it adds the
// database type and the data's location. It leaves the tenant's data where
// it is (D36) and says where. Its store is closed after the row is gone; a
// request holding it fails as Unavailable from then on.
func (r *Registry) Remove(ctx context.Context, name string, audit master.AuditEntry) (Removed, error) {
	if err := r.enter(); err != nil {
		return Removed{}, err
	}
	defer r.wg.Done()
	if err := master.ValidateTenantName(name); err != nil {
		return Removed{}, err
	}
	r.listing.RLock()
	defer r.listing.RUnlock()
	defer r.names.lock(name)()
	row, err := r.cfg.Master.TenantByName(ctx, name)
	if err != nil {
		return Removed{}, err
	}
	out := Removed{Driver: row.Driver}
	if drv, ok := r.cfg.Drivers[row.Driver]; ok {
		if address, err := r.cfg.Keys.Open(addressContext(row.ID), row.SealedAddress); err == nil {
			if loc, err := drv.Location(name, string(address)); err == nil {
				out.Location = loc
			}
			clear(address)
		}
	}
	details := map[string]string{DetailDatabaseType: row.Driver}
	if len(out.Location) <= master.MaxAuditDetailValueLength {
		details[DetailLocation] = out.Location
	}
	if err := r.cfg.Master.RemoveTenant(ctx, row.ID, withDetails(audit, details)); err != nil {
		return Removed{}, err
	}

	e := (*r.entries.Load())[name]
	if e == nil || e.id != row.ID {
		return out, nil
	}
	r.unregister(e)
	e.cancel() // Ends an open in progress, so the lock below is not held long.
	e.op.Lock()
	defer e.op.Unlock()
	e.removed = true
	e.view.Store(&view{state: master.StateUnreachable, reason: "the tenant was removed"})
	if err := e.closeStore(); err != nil {
		r.log.Warn("a removed tenant's database did not close cleanly", "tenant", name, "error", err.Error())
	}
	return out, nil
}

// withDetails returns a copy of audit whose details also hold add, without
// replacing a detail the caller set and without going past the bounds of an
// entry, so the master never refuses the change for a detail the registry
// added.
func withDetails(audit master.AuditEntry, add map[string]string) master.AuditEntry {
	details := make(map[string]string, len(audit.Details)+len(add))
	for k, v := range audit.Details {
		details[k] = v
	}
	for k, v := range add {
		if _, set := details[k]; !set && len(details) < master.MaxAuditDetails {
			details[k] = v
		}
	}
	audit.Details = details
	return audit
}
