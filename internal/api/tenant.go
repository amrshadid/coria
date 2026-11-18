package api

import (
	"context"
	"slices"
	"strconv"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/registry"
)

// maxGrantPages bounds how many pages of a key's grants ListTenants reads
// for a caller who may not list every tenant: a thousand a page, so a
// hundred thousand grants, past any installation's tenants. A store that
// keeps answering a cursor fails the call rather than loop.
const maxGrantPages = 100

// tenantService is TenantService. A tenant's registration and state are in
// the master; it is created and removed through the registry, which holds
// its open store.
type tenantService struct{ c *core }

var _ magnetarv1connect.TenantServiceHandler = tenantService{}

// CreateTenant registers a tenant and makes its tables in its database.
func (s tenantService) CreateTenant(ctx context.Context, req *v1.CreateTenantRequest) (*v1.CreateTenantResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.TenantServiceCreateTenantProcedure)
	if err != nil {
		return nil, err
	}
	name, address := req.GetName(), req.GetDatabaseAddress()
	if err := tenantName("name", name); err != nil {
		return nil, c.fail(d, err)
	}
	var drv string
	switch req.GetDatabaseType() {
	case v1.DatabaseType_DATABASE_TYPE_SQLITE:
		if address != "" {
			return nil, c.fail(d, invalid("database_address", "is empty for a SQLite tenant: its file is made in the data directory"))
		}
		drv = DriverSQLite
	case v1.DatabaseType_DATABASE_TYPE_POSTGRES:
		if address == "" {
			return nil, c.fail(d, invalid("database_address", "is required for a Postgres tenant"))
		}
		if len(address) > MaxDatabaseAddressLength {
			return nil, c.fail(d, invalid("database_address", "is at most 4 KiB"))
		}
		drv = DriverPostgres
	default:
		return nil, c.fail(d, invalid("database_type", "is required: sqlite or postgres"))
	}
	id := c.newID()
	if master.ValidateID("tenant id", id) != nil {
		// The id source is the server's, so a bad id is the server's fault,
		// not the caller's: it must not come back as INVALID_ARGUMENT.
		return nil, c.fail(d, fault.New(fault.Internal, "api: the id source made a tenant id that is not valid"))
	}
	// The registry seals the address under "tenant/<id>/database" before it
	// writes the row, and adds the database type to the entry's details; the
	// address is in nothing the registry returns, logs or records.
	entry := d.Entry(master.OutcomeDone, "")
	entry.TenantName, entry.Object = name, tenantObject(name)
	row, err := c.reg.Create(ctx, registry.NewTenant{ID: id, Name: name, Driver: drv, Address: address}, entry)
	if err != nil {
		return nil, c.fail(d, err)
	}
	return &v1.CreateTenantResponse{Tenant: tenantMessage(row)}, nil
}

// GetTenant answers one tenant, whatever its state: its registration from
// the master and its state from the registry (withState), without touching
// the tenant's database. The row is read here rather than taken from the
// Decision, which is to carry only the tenant's id and name; a row gone
// since the check is answered as the check would have answered it.
func (s tenantService) GetTenant(ctx context.Context, req *v1.GetTenantRequest) (*v1.GetTenantResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.TenantServiceGetTenantProcedure)
	if err != nil {
		return nil, err
	}
	if err := tenantName("name", req.GetName()); err != nil {
		return nil, c.fail(d, err)
	}
	t, err := c.master.TenantByName(ctx, req.GetName())
	if fault.KindOf(err) == fault.NotFound {
		// Removed since the check: answered as the check would have.
		err = noTenant(d)
	}
	if err != nil {
		return nil, c.fail(d, err)
	}
	c.done(ctx, d, nil)
	return &v1.GetTenantResponse{Tenant: tenantMessage(c.withState(t))}, nil
}

// ListTenants answers the tenants the caller may see, ordered by name: every
// tenant for a caller who may list them all, otherwise the tenants its key
// holds a grant on.
func (s tenantService) ListTenants(ctx context.Context, req *v1.ListTenantsRequest) (*v1.ListTenantsResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.TenantServiceListTenantsProcedure)
	if err != nil {
		return nil, err
	}
	page, err := pageOf(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, c.fail(d, err)
	}
	var rows []master.Tenant
	var next string
	if d.Principal().CanListTenants() {
		rows, next, err = c.master.ListTenants(ctx, page)
		err = listError(err, page.Cursor)
	} else {
		rows, next, err = c.grantedTenants(ctx, d.Principal().KeyID(), page)
	}
	if err != nil {
		return nil, c.fail(d, err)
	}
	c.done(ctx, d, nil)
	out := &v1.ListTenantsResponse{Tenants: make([]*v1.Tenant, 0, len(rows)), NextPageToken: next}
	for _, t := range rows {
		out.Tenants = append(out.Tenants, tenantMessage(c.withState(t)))
	}
	return out, nil
}

// grantedTenants returns a page of the tenants the key holds a grant on,
// ordered by name, and the token of the next page. It reads the key's grants,
// which name their tenants, and then only the tenants of the page, never
// every tenant of the installation: a key holds grants on few tenants, and
// the installation may have thousands. The token is the last name answered,
// bound to the key, so it is refused with another key's list, and a tenant
// granted or removed between two pages neither repeats nor shifts the rest.
func (c *core) grantedTenants(ctx context.Context, keyID string, page master.Page) ([]master.Tenant, string, error) {
	after := ""
	if page.Cursor != "" {
		var err error
		if after, err = readAfterToken(page.Cursor, tokenGrantedTenants, keyID); err != nil {
			return nil, "", err
		}
		if master.ValidateTenantName(after) != nil {
			return nil, "", invalid("page_token", "is not one this list returned for these arguments")
		}
	}
	if keyID == "" {
		// Only a key holds grants; a caller who is not a key and may not list
		// every tenant sees none.
		return nil, "", nil
	}
	ids := map[string]string{}
	cursor := ""
	for i := 0; ; i++ {
		if i == maxGrantPages {
			return nil, "", fault.New(fault.Internal, "api: a key's grants did not end after "+strconv.Itoa(maxGrantPages)+" pages")
		}
		grants, nextCursor, err := c.master.GrantsByKey(ctx, keyID, master.Page{Cursor: cursor, Limit: master.MaxPageLimit})
		if err != nil {
			return nil, "", err
		}
		for _, g := range grants {
			if !g.Scope.IsInstallation() && g.TenantName > after {
				ids[g.TenantName] = g.Scope.TenantID
			}
		}
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}
	names := make([]string, 0, len(ids))
	for name := range ids {
		names = append(names, name)
	}
	// Names are ASCII, so byte order is the master's order of names.
	slices.Sort(names)
	next := ""
	if len(names) > page.Limit {
		names = names[:page.Limit]
		next = afterToken(tokenGrantedTenants, keyID, names[len(names)-1])
	}
	rows := make([]master.Tenant, 0, len(names))
	for _, name := range names {
		t, err := c.master.Tenant(ctx, ids[name])
		if fault.KindOf(err) == fault.NotFound {
			continue // Removed since its grant was read; its grants went with it.
		}
		if err != nil {
			return nil, "", err
		}
		rows = append(rows, t)
	}
	return rows, next, nil
}

// RemoveTenant unregisters a tenant and says where its data was left. The
// tenant's name is sent twice; the two must be equal.
func (s tenantService) RemoveTenant(ctx context.Context, req *v1.RemoveTenantRequest) (*v1.RemoveTenantResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.TenantServiceRemoveTenantProcedure)
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if err := tenantName("name", name); err != nil {
		return nil, c.fail(d, err)
	}
	if req.GetConfirmName() != name {
		return nil, c.fail(d, invalid("confirm_name", "must equal name exactly"))
	}
	entry := d.Entry(master.OutcomeDone, "")
	entry.TenantName, entry.Object = name, tenantObject(name)
	removed, err := c.reg.Remove(ctx, name, entry)
	if fault.KindOf(err) == fault.NotFound {
		err = noTenant(d)
	}
	if err != nil {
		return nil, c.fail(d, err)
	}
	return &v1.RemoveTenantResponse{DatabaseType: databaseType(removed.Driver), DataLocation: removed.Location}, nil
}

// withState returns t with the state and reason the registry sees now, which
// is what a request into the tenant meets. The master's row may lag behind
// it: the registry writes a change between ready and unreachable at most once
// a minute per tenant. A tenant the registry does not hold yet (for a moment
// during start or creation) keeps the master's state. It reads no database.
func (c *core) withState(t master.Tenant) master.Tenant {
	if st, ok := c.reg.State(t.Name); ok {
		t.State, t.StateReason = st.State, st.Reason
	}
	return t
}
