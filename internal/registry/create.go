package registry

import (
	"context"
	"crypto/subtle"
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

// Create creates a tenant, whole or not at all, as the API contract's
// CreateTenant says. It writes the tenant's row as upgrading with the
// caller's audit entry (both or neither, as the master writes every change),
// then opens and migrates its database, then records it ready and returns
// it.
//
// When the database cannot be opened or upgraded, Create removes the row it
// wrote, in one master transaction with a system audit entry saying the
// creation was undone and why, unregisters the tenant, and returns the
// failure: Unavailable when the database does not answer or its tables
// cannot be made, Invalid when it refuses the address or its file, Internal
// for the rest (an address that does not open with this key file, a driver
// that panicked). If the row cannot be removed (the master does not answer),
// the tenant stays registered in the state the master has, and the error
// says so; the next round or start retries it.
//
// It is safe to repeat. Only a crash after the row is written leaves an
// upgrading row, which the next start or round opens; a Create again with
// the same id, name, database type and address finishes the first one's
// work and records its own request with AppendAudit. A tenant of that name
// with another id, type or address is Conflict. A repeat that cannot open
// the database returns the failure and leaves the row, which this call did
// not write.
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
	// an address the driver refuses never leaves a row behind. The driver's
	// error is replaced by a fixed message of its kind: whatever it says,
	// it was handed the address.
	if _, err := drv.Location(nt.Name, nt.Address); err != nil {
		return master.Tenant{}, fault.New(fault.KindOf(err), "the database address is not one the database type "+nt.Driver+" takes")
	}
	audit = withDetails(audit, map[string]string{DetailDatabaseType: nt.Driver})
	r.listing.RLock()
	defer r.listing.RUnlock()
	defer r.names.lock(nt.Name)()

	row, wrote, err := r.findOrWrite(ctx, nt, audit)
	if err != nil {
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
		return master.Tenant{}, fault.New(fault.Conflict, "a tenant named "+nt.Name+" is being replaced; try again")
	}
	e.op.Lock()
	defer e.op.Unlock()
	if e.removed {
		return master.Tenant{}, fault.New(fault.NotFound, "the tenant was removed while it was being created")
	}
	if e.store == nil || e.view.Load().state != master.StateReady {
		r.refresh(e, true)
	}
	v := e.view.Load()
	if v.state == master.StateReady {
		row.State, row.StateReason = v.state, v.reason
		return row, nil
	}
	kind := e.lastKind
	if kind == fault.Internal && v.state == master.StateUnreachable {
		kind = fault.Unavailable
	}
	if !wrote {
		return master.Tenant{}, fault.New(kind, "the tenant's database cannot be used: "+v.reason)
	}
	if err := r.undo(ctx, e, nt.Driver, v.reason); err != nil {
		return master.Tenant{}, fault.New(kind, "the tenant was not created, and its row could not be removed ("+
			fault.Message(err)+"); it is "+string(v.state)+": "+v.reason)
	}
	return master.Tenant{}, fault.New(kind, "the tenant was not created: "+v.reason)
}

// findOrWrite returns the tenant nt names, and whether this call wrote its
// row. A tenant that exists with the same id, type and address is a repeat:
// its row is returned, and the request is recorded with AppendAudit. A row
// whose write returned an error but committed (the master answered late) is
// found by reading the master again, so it is not left behind unopened.
func (r *Registry) findOrWrite(ctx context.Context, nt NewTenant, audit master.AuditEntry) (master.Tenant, bool, error) {
	row, err := r.cfg.Master.TenantByName(ctx, nt.Name)
	switch {
	case err == nil:
		if err := r.repeat(row, nt); err != nil {
			return master.Tenant{}, false, err
		}
		return row, false, r.recordRepeat(ctx, audit)
	case fault.KindOf(err) != fault.NotFound:
		return master.Tenant{}, false, err
	}
	sealed, err := r.cfg.Keys.Seal(addressContext(nt.ID), []byte(nt.Address))
	if err != nil {
		return master.Tenant{}, false, err
	}
	row = master.Tenant{
		ID:            nt.ID,
		Name:          nt.Name,
		Driver:        nt.Driver,
		SealedAddress: sealed,
		State:         master.StateUpgrading,
		Created:       r.cfg.Now(),
	}
	werr := r.cfg.Master.CreateTenant(ctx, row, audit)
	if werr == nil {
		return row, true, nil
	}
	existing, rerr := r.cfg.Master.TenantByName(ctx, nt.Name)
	if rerr != nil {
		return master.Tenant{}, false, werr
	}
	if fault.KindOf(werr) == fault.Conflict {
		// Another creation of the same name wrote first.
		if err := r.repeat(existing, nt); err != nil {
			return master.Tenant{}, false, err
		}
		return existing, false, r.recordRepeat(ctx, audit)
	}
	if existing.ID == nt.ID {
		// The write committed although the call failed: this call wrote it.
		return existing, true, nil
	}
	return master.Tenant{}, false, werr
}

// repeat returns nil when row is the tenant nt asks for, so a creation that
// finds it is a repeat, and Conflict otherwise. The addresses are compared
// in constant time, after the stored one is opened, so a repeat with another
// address is refused instead of silently keeping the old one.
func (r *Registry) repeat(row master.Tenant, nt NewTenant) error {
	if row.ID != nt.ID || row.Driver != nt.Driver {
		return fault.New(fault.Conflict, "a tenant named "+nt.Name+" exists")
	}
	stored, err := r.cfg.Keys.Open(addressContext(row.ID), row.SealedAddress)
	if err != nil {
		return fault.New(fault.Internal, "the database address of the tenant "+nt.Name+" does not open with this key file")
	}
	defer clear(stored)
	if subtle.ConstantTimeCompare(stored, []byte(nt.Address)) != 1 {
		return fault.New(fault.Conflict, "a tenant named "+nt.Name+" exists with another database address")
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

// undo removes the row a failed creation wrote, in one master transaction
// with a system audit entry saying why, and retires the entry. The caller
// holds e.op. On an error the entry stays registered, as the master still
// has its row.
func (r *Registry) undo(ctx context.Context, e *entry, driver, reason string) error {
	audit := master.AuditEntry{
		ID:         r.cfg.NewID(),
		Time:       r.cfg.Now(),
		Method:     UndoMethod,
		TenantName: e.name,
		Object:     "tenant/" + e.name,
		Outcome:    master.OutcomeFailed,
		Reason:     fitAuditReason("the creation was undone: " + reason),
		Details:    map[string]string{DetailActor: ActorSystem, DetailDatabaseType: driver},
	}
	err := r.cfg.Master.RemoveTenant(ctx, e.id, audit)
	if err != nil && fault.KindOf(err) != fault.NotFound {
		r.log.Warn("a failed creation could not be undone", "tenant", e.name,
			"kind", fault.KindOf(err).String(), "message", fault.Message(err))
		return err
	}
	r.unregister(e)
	r.retireLocked(e)
	return nil
}

// fitAuditReason cuts a text to the length an audit entry's reason may have.
func fitAuditReason(s string) string {
	if len(s) <= master.MaxAuditReasonLength {
		return s
	}
	return fitReason(s[:master.MaxAuditReasonLength])
}

// Remove unregisters the tenant named name and removes its row and grants
// from the master, with the caller's audit entry, to which it adds the
// database type and the data's location. It leaves the tenant's data where
// it is (D36) and says where. Its store is closed after the row is gone; a
// request holding it fails as Unavailable from then on. When the master has
// no tenant of that name, which a removal that committed and then failed
// leaves behind, any entry of that name is unregistered and closed too, and
// the answer is NotFound.
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
		if fault.KindOf(err) == fault.NotFound {
			r.retireName(name)
		}
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
		if fault.KindOf(err) == fault.NotFound {
			r.retireName(name)
		}
		return Removed{}, err
	}
	r.retireName(name)
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
