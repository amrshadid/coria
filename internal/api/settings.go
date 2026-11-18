package api

import (
	"context"
	"time"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/core/settings"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// messageNoSetting answers a name that is not in the list of settings the
// server knows.
const messageNoSetting = "no setting has this name"

// settingsService is SettingsService. The list of settings is the server's
// (package core/settings); the installation's values are in the master and a
// tenant's overrides in its own store, which only the registry hands out, and
// only for a tenant that is ready. The value that applies is the tenant's
// override, else the installation's value, else the built-in default.
type settingsService struct{ c *core }

var _ magnetarv1connect.SettingsServiceHandler = settingsService{}

// definition returns the setting named name, or NotFound when the server
// knows no such setting.
func (c *core) definition(name string) (settings.Definition, error) {
	def, ok := c.settings.Lookup(name)
	if !ok {
		return settings.Definition{}, fault.New(fault.NotFound, messageNoSetting)
	}
	return def, nil
}

// onInstallation returns the value of def that applies on the installation
// and where it comes from: the installation's value in the master, else the
// built-in default.
func (c *core) onInstallation(ctx context.Context, def settings.Definition) (string, v1.SettingSource, error) {
	v, err := c.master.Setting(ctx, def.Name)
	switch {
	case err == nil:
		return v, v1.SettingSource_SETTING_SOURCE_INSTALLATION, nil
	case fault.KindOf(err) == fault.NotFound:
		return def.Default, v1.SettingSource_SETTING_SOURCE_BUILT_IN, nil
	}
	return "", v1.SettingSource_SETTING_SOURCE_UNSPECIFIED, err
}

// keyLifetime returns the lifetime of a key made with no expiry: the
// installation's key.default-lifetime, else its built-in default. ok is
// false when the server's list has no such setting, and the access package's
// own default applies.
func (c *core) keyLifetime(ctx context.Context) (time.Duration, bool, error) {
	def, known := c.settings.Lookup(settings.KeyDefaultLifetime)
	if !known {
		return 0, false, nil
	}
	v, _, err := c.onInstallation(ctx, def)
	if err != nil {
		return 0, false, err
	}
	d, err := settings.ParseDuration(v)
	if err != nil || def.Validate(v) != nil {
		// Every value is validated before it is stored, so one that does
		// not read is the server's fault, not the caller's.
		return 0, false, fault.New(fault.Internal, "api: the stored "+settings.KeyDefaultLifetime+" does not read as a lifetime")
	}
	return d, true, nil
}

// settingMessage returns a setting as the contract writes it.
func settingMessage(name string, scope access.Scope, value string, source v1.SettingSource) *v1.Setting {
	return &v1.Setting{Name: name, Scope: scopeMessage(scope), Value: value, Source: source}
}

// tenantScoped returns the Invalid error of a tenant scope for a setting a
// tenant may not override, naming field, or nil.
func tenantScoped(field string, scope access.Scope, def settings.Definition) error {
	if !scope.IsInstallation() && !def.TenantOverride {
		return invalid(field, "is the installation: a tenant may not override "+def.Name)
	}
	return nil
}

// GetSetting answers the value of a setting that applies on a scope, and
// where it came from.
func (s settingsService) GetSetting(ctx context.Context, req *v1.GetSettingRequest) (*v1.GetSettingResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.SettingsServiceGetSettingProcedure)
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if err := settingName("name", name); err != nil {
		return nil, c.fail(d, err)
	}
	scope, err := scopeOf("scope", req.GetScope())
	if err != nil {
		return nil, c.fail(d, err)
	}
	def, err := c.definition(name)
	if err != nil {
		return nil, c.fail(d, err)
	}
	var st tenant.Store
	if !scope.IsInstallation() {
		// The tenant's store is needed even for a setting it may not
		// override: the read is recorded in the tenant's log, and a tenant
		// that is not ready is not read.
		if st, err = c.tenantStore(d, scope.Tenant); err != nil {
			return nil, c.fail(d, err)
		}
		if def.TenantOverride {
			v, err := st.Setting(ctx, name)
			switch {
			case err == nil:
				c.done(d, st)
				return &v1.GetSettingResponse{Setting: settingMessage(name, scope, v, v1.SettingSource_SETTING_SOURCE_TENANT)}, nil
			case fault.KindOf(err) != fault.NotFound:
				return nil, c.failIn(d, st, err)
			}
		}
	}
	v, source, err := c.onInstallation(ctx, def)
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	c.done(d, st)
	return &v1.GetSettingResponse{Setting: settingMessage(name, access.InstallationScope, v, source)}, nil
}

// SetSetting sets the installation's value of a setting or a tenant's
// override of it, with its audit entry in the same transaction. The entry
// holds the setting's name and scope, never its value.
func (s settingsService) SetSetting(ctx context.Context, req *v1.SetSettingRequest) (*v1.SetSettingResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.SettingsServiceSetSettingProcedure)
	if err != nil {
		return nil, err
	}
	in := req.GetSetting()
	if in == nil {
		return nil, c.fail(d, invalid("setting", "is required"))
	}
	name, value := in.GetName(), in.GetValue()
	if err := settingName("setting.name", name); err != nil {
		return nil, c.fail(d, err)
	}
	scope, err := scopeOf("setting.scope", in.GetScope())
	if err != nil {
		return nil, c.fail(d, err)
	}
	if err := text("setting.value", value, 0, master.MaxSettingValueLength); err != nil {
		return nil, c.fail(d, err)
	}
	def, err := c.definition(name)
	if err != nil {
		return nil, c.fail(d, err)
	}
	if err := tenantScoped("setting.scope", scope, def); err != nil {
		return nil, c.fail(d, err)
	}
	if err := def.Validate(value); err != nil {
		return nil, c.fail(d, invalid("setting.value", err.Error()))
	}
	if scope.IsInstallation() {
		if err := c.master.SetSetting(ctx, name, value, d.Entry(master.OutcomeDone, "", access.Facts{})); err != nil {
			return nil, c.fail(d, err)
		}
		return &v1.SetSettingResponse{Setting: settingMessage(name, scope, value, v1.SettingSource_SETTING_SOURCE_INSTALLATION)}, nil
	}
	st, err := c.tenantStore(d, scope.Tenant)
	if err != nil {
		return nil, c.fail(d, err)
	}
	if err := st.SetSetting(ctx, name, value, d.Entry(master.OutcomeDone, "", access.Facts{})); err != nil {
		return nil, c.failIn(d, st, err)
	}
	return &v1.SetSettingResponse{Setting: settingMessage(name, scope, value, v1.SettingSource_SETTING_SOURCE_TENANT)}, nil
}

// UnsetSetting removes a value set on a scope and answers what applies there
// again: for a tenant, what applies on the installation; for the
// installation, the built-in default. Removing a value that is not set
// succeeds.
func (s settingsService) UnsetSetting(ctx context.Context, req *v1.UnsetSettingRequest) (*v1.UnsetSettingResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.SettingsServiceUnsetSettingProcedure)
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if err := settingName("name", name); err != nil {
		return nil, c.fail(d, err)
	}
	scope, err := scopeOf("scope", req.GetScope())
	if err != nil {
		return nil, c.fail(d, err)
	}
	def, err := c.definition(name)
	if err != nil {
		return nil, c.fail(d, err)
	}
	if err := tenantScoped("scope", scope, def); err != nil {
		return nil, c.fail(d, err)
	}
	if scope.IsInstallation() {
		if _, err := c.master.UnsetSetting(ctx, name, d.Entry(master.OutcomeDone, "", access.Facts{})); err != nil {
			return nil, c.fail(d, err)
		}
		return &v1.UnsetSettingResponse{Setting: settingMessage(name, scope, def.Default, v1.SettingSource_SETTING_SOURCE_BUILT_IN)}, nil
	}
	st, err := c.tenantStore(d, scope.Tenant)
	if err != nil {
		return nil, c.fail(d, err)
	}
	if _, err := st.UnsetSetting(ctx, name, d.Entry(master.OutcomeDone, "", access.Facts{})); err != nil {
		return nil, c.failIn(d, st, err)
	}
	// The override is gone and its entry written; what applies now is read
	// for the answer. If that read fails the caller is told so, and may
	// repeat the call, which changes nothing more.
	v, source, err := c.onInstallation(ctx, def)
	if err != nil {
		return nil, c.toAPI(d, err)
	}
	return &v1.UnsetSettingResponse{Setting: settingMessage(name, access.InstallationScope, v, source)}, nil
}

// ListSettings answers a page of settings, ordered by name: on the
// installation, every setting the server knows with the value that applies
// there; on a tenant, its overrides.
func (s settingsService) ListSettings(ctx context.Context, req *v1.ListSettingsRequest) (*v1.ListSettingsResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.SettingsServiceListSettingsProcedure)
	if err != nil {
		return nil, err
	}
	scope, err := scopeOf("scope", req.GetScope())
	if err != nil {
		return nil, c.fail(d, err)
	}
	page, err := pageOf(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, c.fail(d, err)
	}
	if scope.IsInstallation() {
		out, err := c.installationSettings(ctx, page)
		if err != nil {
			return nil, c.fail(d, err)
		}
		c.done(d, nil)
		return out, nil
	}
	st, err := c.tenantStore(d, scope.Tenant)
	if err != nil {
		return nil, c.fail(d, err)
	}
	overrides, next, err := st.ListSettings(ctx, page)
	if err != nil {
		return nil, c.failIn(d, st, listError(err, page.Cursor))
	}
	c.done(d, st)
	out := &v1.ListSettingsResponse{Settings: make([]*v1.Setting, 0, len(overrides)), NextPageToken: next}
	for _, o := range overrides {
		out.Settings = append(out.Settings, settingMessage(o.Name, scope, o.Value, v1.SettingSource_SETTING_SOURCE_TENANT))
	}
	return out, nil
}

// installationSettings returns a page of every setting the server knows,
// each with the value that applies on the installation. It pages the
// server's list, not the master's rows, so a setting with no value set is
// listed with its built-in default; a row of a setting the server no longer
// knows is not listed.
func (c *core) installationSettings(ctx context.Context, page master.Page) (*v1.ListSettingsResponse, error) {
	after := ""
	if page.Cursor != "" {
		var err error
		if after, err = readAfterToken(page.Cursor, tokenInstallationSettings, "installation"); err != nil {
			return nil, err
		}
	}
	var names []string
	for _, n := range c.settings.Names() {
		if n > after {
			names = append(names, n)
		}
	}
	out := &v1.ListSettingsResponse{}
	if len(names) > page.Limit {
		names = names[:page.Limit]
		out.NextPageToken = afterToken(tokenInstallationSettings, "installation", names[len(names)-1])
	}
	out.Settings = make([]*v1.Setting, 0, len(names))
	for _, n := range names {
		def, _ := c.settings.Lookup(n)
		v, source, err := c.onInstallation(ctx, def)
		if err != nil {
			return nil, err
		}
		out.Settings = append(out.Settings, settingMessage(n, access.InstallationScope, v, source))
	}
	return out, nil
}
