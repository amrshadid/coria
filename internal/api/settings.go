package api

import (
	"context"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// messageNoSetting answers a setting name that names no setting.
const messageNoSetting = "no setting has this name"

// settingsService is SettingsService. The installation's defaults are in the
// master; a tenant's overrides in its own store, which only the registry
// hands out, and only for a tenant that is ready.
//
// The contract speaks of "the fixed list of settings the server knows". No
// package holds such a list yet, so a setting is one the installation holds
// a default for: setting an installation default is how a setting comes to
// exist, and a name with no default is not found everywhere else ("The
// installation holds a default for every setting; a tenant may override some
// of them").
type settingsService struct{ c *core }

var _ magnetarv1connect.SettingsServiceHandler = settingsService{}

// installationDefault returns the installation's default of a setting, or
// NotFound when the installation holds none.
func (c *core) installationDefault(ctx context.Context, name string) (string, error) {
	v, err := c.master.Setting(ctx, name)
	if fault.KindOf(err) == fault.NotFound {
		return "", fault.New(fault.NotFound, messageNoSetting)
	}
	return v, err
}

// settingMessage returns a setting as the contract writes it.
func settingMessage(name string, scope access.Scope, value string) *v1.Setting {
	return &v1.Setting{Name: name, Scope: scopeMessage(scope), Value: value}
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
	if scope.IsInstallation() {
		v, err := c.installationDefault(ctx, name)
		if err != nil {
			return nil, c.fail(d, err)
		}
		c.done(ctx, d, nil)
		return &v1.GetSettingResponse{Setting: settingMessage(name, scope, v)}, nil
	}
	st, err := c.tenantStore(d, scope.Tenant)
	if err != nil {
		return nil, c.fail(d, err)
	}
	from := scope
	v, err := st.Setting(ctx, name)
	if fault.KindOf(err) == fault.NotFound {
		from = access.InstallationScope
		v, err = c.installationDefault(ctx, name)
	}
	if err != nil {
		return nil, c.failIn(ctx, d, st, err)
	}
	c.done(ctx, d, st)
	return &v1.GetSettingResponse{Setting: settingMessage(name, from, v)}, nil
}

// SetSetting sets the installation's default of a setting or a tenant's
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
	if scope.IsInstallation() {
		if err := c.master.SetSetting(ctx, name, value, d.Entry(master.OutcomeDone, "")); err != nil {
			return nil, c.fail(d, err)
		}
		return &v1.SetSettingResponse{Setting: settingMessage(name, scope, value)}, nil
	}
	st, err := c.tenantStore(d, scope.Tenant)
	if err != nil {
		return nil, c.fail(d, err)
	}
	if _, err := c.installationDefault(ctx, name); err != nil {
		return nil, c.failIn(ctx, d, st, err)
	}
	if err := st.SetSetting(ctx, name, value, d.Entry(master.OutcomeDone, "")); err != nil {
		return nil, c.failIn(ctx, d, st, err)
	}
	return &v1.SetSettingResponse{Setting: settingMessage(name, scope, value)}, nil
}

// UnsetSetting removes a tenant's override of a setting and answers the
// installation's default, which applies again. Removing an override the
// tenant does not have succeeds; an installation scope is invalid.
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
	if scope.IsInstallation() {
		return nil, c.fail(d, invalid("scope", "is a tenant: the installation's default cannot be removed"))
	}
	st, err := c.tenantStore(d, scope.Tenant)
	if err != nil {
		return nil, c.fail(d, err)
	}
	def, err := c.installationDefault(ctx, name)
	if err != nil {
		return nil, c.failIn(ctx, d, st, err)
	}
	if _, err := st.UnsetSetting(ctx, name, d.Entry(master.OutcomeDone, "")); err != nil {
		return nil, c.failIn(ctx, d, st, err)
	}
	return &v1.UnsetSettingResponse{Setting: settingMessage(name, access.InstallationScope, def)}, nil
}

// ListSettings answers a page of the settings set on one scope, ordered by
// name: the installation's defaults, or a tenant's overrides.
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
	var settings []master.Setting
	var next string
	if scope.IsInstallation() {
		settings, next, err = c.master.ListSettings(ctx, page)
		if err != nil {
			return nil, c.fail(d, listError(err, page.Cursor))
		}
		c.done(ctx, d, nil)
	} else {
		st, err := c.tenantStore(d, scope.Tenant)
		if err != nil {
			return nil, c.fail(d, err)
		}
		settings, next, err = st.ListSettings(ctx, page)
		if err != nil {
			return nil, c.failIn(ctx, d, st, listError(err, page.Cursor))
		}
		c.done(ctx, d, st)
	}
	out := &v1.ListSettingsResponse{Settings: make([]*v1.Setting, 0, len(settings)), NextPageToken: next}
	for _, st := range settings {
		out.Settings = append(out.Settings, settingMessage(st.Name, scope, st.Value))
	}
	return out, nil
}
