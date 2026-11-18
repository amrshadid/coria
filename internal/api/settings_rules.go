package api

import (
	"context"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/core/settings"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// placeSettings are the settings that say where the installation lets data
// go: a change of one is checked against what every tenant's storages and
// receivers use, under the write lock of core.places.
var placeSettings = map[string]bool{
	settings.StorageDirectoryRoots: true,
	settings.StorageS3Endpoints:    true,
	settings.ReceiverBindAddresses: true,
	settings.ReceiverPorts:         true,
}

// checkSettingValue applies the rules of a setting's value that need more
// than its own text (settings.Definition.Validate has that): each root's
// path absolute on this server's system, and each S3 endpoint one a storage
// could use, with no two entries spelling one endpoint. The errors are
// Invalid, naming field.
func checkSettingValue(field, name, value string) error {
	switch name {
	case settings.StorageDirectoryRoots:
		roots, err := settings.ParseDirectoryRoots(value)
		if err != nil {
			return invalid(field, err.Error())
		}
		for _, r := range roots {
			if !filepath.IsAbs(r.Path) {
				return invalid(field, "has a path for the root "+r.Name+" that is not absolute on this server's system")
			}
		}
	case settings.StorageS3Endpoints:
		entries, err := settings.ParseS3Endpoints(value)
		if err != nil {
			return invalid(field, err.Error())
		}
		seen := map[string]bool{}
		for i, e := range entries {
			n, err := tenant.NormalizeEndpoint(e.URL)
			if err != nil {
				return invalid(field, "has, on line "+strconv.Itoa(i+1)+", an endpoint a storage cannot use: "+fault.Message(err))
			}
			if seen[n] {
				return invalid(field, "lists, on line "+strconv.Itoa(i+1)+", an endpoint another line lists in another spelling")
			}
			seen[n] = true
		}
	}
	return nil
}

// takenAway refuses, as a precondition, a value of a place setting that
// would take away what a tenant's storage or receiver uses, or a tenant's
// ports that overlap another tenant's; value is what would apply on scope
// after the change. A tenant that is not ready cannot be read, so whether
// it uses the value cannot be known: the change is refused as Unavailable
// until it is. The caller holds core.places for writing, so no storage or
// receiver is added meanwhile on what is being taken away.
func (c *core) takenAway(ctx context.Context, scope access.Scope, name, value string) error {
	switch name {
	case settings.StorageDirectoryRoots:
		old, err := c.directoryRoots(ctx)
		if err != nil {
			return err
		}
		next, err := settings.ParseDirectoryRoots(value)
		if err != nil {
			return invalid("setting.value", err.Error())
		}
		return c.eachTenant(ctx, func(name string, p tenant.Pipeline) error {
			storages, err := tenantStorages(ctx, p)
			if err != nil {
				return err
			}
			for _, s := range storages {
				if s.Type != tenant.StorageDirectory {
					continue
				}
				was, _ := old.Lookup(s.Directory.Root)
				now, ok := next.Lookup(s.Directory.Root)
				if !ok || now.Path != was.Path {
					return precondition("the root " + s.Directory.Root + " holds the storage " + s.Name + " of the tenant " + name + "; move or remove the storage before the root is changed or removed")
				}
			}
			return nil
		})
	case settings.StorageS3Endpoints:
		next, err := settings.ParseS3Endpoints(value)
		if err != nil {
			return invalid("setting.value", err.Error())
		}
		return c.eachTenant(ctx, func(name string, p tenant.Pipeline) error {
			storages, err := tenantStorages(ctx, p)
			if err != nil {
				return err
			}
			for _, s := range storages {
				if s.Type == tenant.StorageS3 && !endpointListed(next, s.S3.Endpoint) {
					return precondition("the S3 storage " + s.Name + " of the tenant " + name + " uses an endpoint this value no longer allows; move or remove the storage first")
				}
			}
			return nil
		})
	case settings.ReceiverBindAddresses:
		next, err := settings.ParseBindAddresses(value)
		if err != nil {
			return invalid("setting.value", err.Error())
		}
		return c.eachTenant(ctx, func(name string, p tenant.Pipeline) error {
			receivers, err := tenantReceivers(ctx, p)
			if err != nil {
				return err
			}
			for _, r := range receivers {
				if r.DIMSE.BindAddress == "" {
					continue
				}
				a, err := netip.ParseAddr(r.DIMSE.BindAddress)
				if err != nil || !next.Contains(a) {
					return precondition("the receiver " + r.Name + " of the tenant " + name + " listens on an address this value no longer lists; move or remove the receiver first")
				}
			}
			return nil
		})
	case settings.ReceiverPorts:
		if scope.IsInstallation() {
			return nil
		}
		return c.portsTakenAway(ctx, scope.Tenant, value)
	}
	return nil
}

// portsTakenAway refuses ports for the tenant named name that leave out a
// port its receivers use, or that overlap the ports another tenant holds.
func (c *core) portsTakenAway(ctx context.Context, name, value string) error {
	next, err := settings.ParsePorts(value)
	if err != nil {
		return invalid("setting.value", err.Error())
	}
	return c.eachTenant(ctx, func(other string, p tenant.Pipeline) error {
		if other == name {
			receivers, err := tenantReceivers(ctx, p)
			if err != nil {
				return err
			}
			for _, r := range receivers {
				if r.Type == tenant.ReceiverDIMSE && !next.Contains(r.DIMSE.Port) {
					return precondition("the receiver " + r.Name + " of the tenant " + name + " uses the port " + strconv.Itoa(r.DIMSE.Port) + ", which this value leaves out; move or remove the receiver first")
				}
			}
			return nil
		}
		v, err := p.Setting(ctx, settings.ReceiverPorts)
		switch {
		case fault.KindOf(err) == fault.NotFound:
			return nil
		case err != nil:
			return err
		}
		held, err := settings.ParsePorts(v)
		if err != nil {
			return fault.New(fault.Internal, "api: the stored "+settings.ReceiverPorts+" of a tenant does not follow its rule")
		}
		if port, overlap := next.Overlap(held); overlap {
			return precondition("the port " + strconv.Itoa(port) + " is allotted to the tenant " + other + "; two tenants' receivers never share a port")
		}
		return nil
	})
}

// endpointListed reports whether a storage's endpoint, kept in the port's
// one spelling, is in entries, and allowed plain http when it is http.
func endpointListed(entries settings.S3Endpoints, endpoint string) bool {
	for _, e := range entries {
		if n, err := tenant.NormalizeEndpoint(e.URL); err == nil && n == endpoint {
			return !strings.HasPrefix(endpoint, "http://") || e.PlainHTTP
		}
	}
	return false
}

// eachTenant calls fn with the name and pipeline of every tenant the master
// lists, in order of name, and stops at its first error. A tenant removed
// meanwhile is passed over; one that is not ready fails the walk as
// Unavailable, naming it: the caller is an admin on the installation, who
// may list every tenant.
func (c *core) eachTenant(ctx context.Context, fn func(name string, p tenant.Pipeline) error) error {
	page := master.Page{Limit: master.MaxPageLimit}
	for i := 0; ; i++ {
		if i == maxGrantPages {
			return fault.New(fault.Internal, "api: the master's tenants did not end")
		}
		rows, next, err := c.master.ListTenants(ctx, page)
		if err != nil {
			return err
		}
		for _, row := range rows {
			st, err := c.reg.Store(row.Name)
			switch kind := fault.KindOf(err); {
			case kind == fault.NotFound:
				continue
			case kind == fault.Unavailable:
				return fault.Wrap(fault.Unavailable, err, "the tenant "+row.Name+" is not ready, so whether it uses this value cannot be checked; try again once it is")
			case err != nil:
				return err
			}
			p, err := c.pipeline(st)
			if err != nil {
				return err
			}
			if err := fn(row.Name, p); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.Cursor = next
	}
}
