package settingvalue

import (
	"context"

	"github.com/amrshadid/magnetar/internal/core/settings"
	"github.com/amrshadid/magnetar/internal/fault"
)

// Source answers the value stored for a setting: master.Store for the
// installation, tenant.Store for a tenant.
type Source interface {
	// Setting returns the value stored for name, NotFound when none is.
	Setting(ctx context.Context, name string) (string, error)
}

// Reader reads the value of a setting that applies. Its zero Table is the
// server's built-in list.
type Reader struct {
	// Table is the list of settings the server knows; nil is
	// settings.MustBuiltin.
	Table *settings.Table
	// Master is the installation's store of settings, the master database.
	Master Source
}

// table returns the reader's list of settings.
func (r Reader) table() *settings.Table {
	if r.Table == nil {
		return builtin
	}
	return r.Table
}

// builtin is the server's own list, made once: it never changes.
var builtin = settings.MustBuiltin()

// definition returns the setting named name, or an Internal fault: a name
// the server reads that its own list does not hold is a mistake of the code.
func (r Reader) definition(name string) (settings.Definition, error) {
	def, ok := r.table().Lookup(name)
	if !ok {
		return settings.Definition{}, fault.New(fault.Internal, "settings: the server's list of settings has no "+name)
	}
	return def, nil
}

// errBroken is the fault of a stored value its rule refuses: every value is
// checked before it is stored, so one that fails now is the server's fault.
func errBroken(where, name string) error {
	return fault.New(fault.Internal, "settings: the "+where+"stored "+name+" does not follow its rule")
}

// Installation returns the value of the setting named name that applies on
// the installation: its stored value, else the built-in default.
func (r Reader) Installation(ctx context.Context, name string) (string, error) {
	def, err := r.definition(name)
	if err != nil {
		return "", err
	}
	return r.onInstallation(ctx, def)
}

// onInstallation is Installation for a definition already looked up.
func (r Reader) onInstallation(ctx context.Context, def settings.Definition) (string, error) {
	if r.Master == nil {
		return "", fault.New(fault.Internal, "settings: the reader is given no installation store")
	}
	v, err := r.Master.Setting(ctx, def.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		v = def.Default
	case err != nil:
		return "", err
	}
	if def.Validate(v) != nil {
		return "", errBroken("", def.Name)
	}
	return v, nil
}

// Tenant returns the value of the setting named name that applies on the
// tenant whose store is st: its own, else the installation's (none for a
// setting with no installation value), else the built-in default.
func (r Reader) Tenant(ctx context.Context, st Source, name string) (string, error) {
	def, err := r.definition(name)
	if err != nil {
		return "", err
	}
	v, err := st.Setting(ctx, name)
	switch {
	case fault.KindOf(err) == fault.NotFound && def.TenantOnly:
		v = def.Default
	case fault.KindOf(err) == fault.NotFound:
		return r.onInstallation(ctx, def)
	case err != nil:
		return "", err
	}
	if def.Validate(v) != nil {
		return "", errBroken("tenant's ", name)
	}
	return v, nil
}

// typed parses v, the value read of the setting named name (or the error
// of reading it), with parse; a value read that does not parse is the
// server's fault.
func typed[T any](name string, v string, err error, parse func(string) (T, error)) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	out, err := parse(v)
	if err != nil {
		return zero, errBroken("", name)
	}
	return out, nil
}

// DirectoryRoots returns the installation's storage.directory-roots.
func (r Reader) DirectoryRoots(ctx context.Context) (settings.DirectoryRoots, error) {
	v, err := r.Installation(ctx, settings.StorageDirectoryRoots)
	return typed(settings.StorageDirectoryRoots, v, err, settings.ParseDirectoryRoots)
}

// S3Endpoints returns the installation's storage.s3-endpoints.
func (r Reader) S3Endpoints(ctx context.Context) (settings.S3Endpoints, error) {
	v, err := r.Installation(ctx, settings.StorageS3Endpoints)
	return typed(settings.StorageS3Endpoints, v, err, settings.ParseS3Endpoints)
}

// BindAddresses returns the installation's receiver.bind-addresses.
func (r Reader) BindAddresses(ctx context.Context) (settings.BindAddresses, error) {
	v, err := r.Installation(ctx, settings.ReceiverBindAddresses)
	return typed(settings.ReceiverBindAddresses, v, err, settings.ParseBindAddresses)
}

// Reserve returns the installation's storage.reserve.
func (r Reader) Reserve(ctx context.Context) (settings.Reserve, error) {
	v, err := r.Installation(ctx, settings.StorageReserve)
	return typed(settings.StorageReserve, v, err, settings.ParseReserve)
}

// HeaderLimit returns the installation's receiver.header-limit in bytes.
func (r Reader) HeaderLimit(ctx context.Context) (int64, error) {
	v, err := r.Installation(ctx, settings.ReceiverHeaderLimit)
	return typed(settings.ReceiverHeaderLimit, v, err, settings.ParseHeaderLimit)
}

// OnTenant returns the reader of the settings that apply on the tenant
// whose store is st.
func (r Reader) OnTenant(st Source) Tenant { return Tenant{r: r, st: st} }

// Tenant reads the settings that apply on one tenant.
type Tenant struct {
	r  Reader
	st Source
}

// Ports returns receiver.ports as it applies on the tenant.
func (t Tenant) Ports(ctx context.Context) (settings.Ports, error) {
	v, err := t.r.Tenant(ctx, t.st, settings.ReceiverPorts)
	return typed(settings.ReceiverPorts, v, err, settings.ParsePorts)
}

// BindAddresses returns the installation's receiver.bind-addresses, which
// no tenant overrides.
func (t Tenant) BindAddresses(ctx context.Context) (settings.BindAddresses, error) {
	return t.r.BindAddresses(ctx)
}

// IncomingLimit returns incoming.limit as it applies on the tenant, in
// bytes; 0 is no limit.
func (t Tenant) IncomingLimit(ctx context.Context) (int64, error) {
	v, err := t.r.Tenant(ctx, t.st, settings.IncomingLimit)
	return typed(settings.IncomingLimit, v, err, settings.ParseSize)
}
