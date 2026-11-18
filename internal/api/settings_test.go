package api_test

import (
	"strings"
	"testing"

	connect "connectrpc.com/connect/v2"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/master"
)

// settingsFixture is the fixture with a default for audit.retention and a
// tenant, down, whose database does not answer.
func settingsFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.setDefault("audit.retention", "365d")
	f.pg.down.Store(true)
	o := call(f.as(local).tenants.CreateTenant, &v1.CreateTenantRequest{Name: "down", DatabaseType: pgType, DatabaseAddress: pgAddress})
	if o.err != nil {
		t.Fatal(o.err)
	}
	return f
}

// getSetting returns a call of GetSetting.
func getSetting(name string, s *v1.Scope) func(clients) outcome {
	return func(c clients) outcome {
		return call(c.settings.GetSetting, &v1.GetSettingRequest{Name: name, Scope: s})
	}
}

// setSetting returns a call of SetSetting.
func setSetting(name string, s *v1.Scope, value string) func(clients) outcome {
	return func(c clients) outcome {
		return call(c.settings.SetSetting, &v1.SetSettingRequest{Setting: &v1.Setting{Name: name, Scope: s, Value: value}})
	}
}

// wantSetting returns a check that the answer's setting is name, from the
// scope from, with value.
func wantSetting(name, from, value string) func(t *testing.T, o outcome) {
	return func(t *testing.T, o outcome) {
		t.Helper()
		var s *v1.Setting
		switch r := o.res.(type) {
		case *v1.GetSettingResponse:
			s = r.GetSetting()
		case *v1.SetSettingResponse:
			s = r.GetSetting()
		case *v1.UnsetSettingResponse:
			s = r.GetSetting()
		}
		got := "installation"
		if s.GetScope().GetTenant() != "" {
			got = s.GetScope().GetTenant()
		}
		if s.GetName() != name || got != from || s.GetValue() != value {
			t.Errorf("got %s from %s = %q, want %s from %s = %q", s.GetName(), got, s.GetValue(), name, from, value)
		}
	}
}

// testGetSetting guards GetSetting: reader on the scope asked for (a role on
// the installation gives nothing inside a tenant); the value that applies and
// where it came from; a setting with no default not found; a tenant that
// does not exist, Denied or NotFound by who asks; a tenant not ready
// unavailable; the entry in the log of the scope asked for.
func testGetSetting(t *testing.T) {
	f := settingsFixture(t)
	f.run(t, []tcase{
		{name: "no key", as: nobody, call: getSetting("audit.retention", installation()), code: connect.CodeUnauthenticated},
		{name: "installation, just below the role", as: f.noGrant, call: getSetting("audit.retention", installation()), code: connect.CodePermissionDenied},
		{name: "installation, at the role", as: f.instReader, call: getSetting("audit.retention", installation()),
			audit: &wantAudit{outcome: master.OutcomeDone, object: "audit.retention", details: map[string]string{"scope": "installation"}},
			check: wantSetting("audit.retention", "installation", "365d")},
		{name: "tenant, an installation admin", as: f.instAdmin, call: getSetting("audit.retention", tenantScope("lab1")), code: connect.CodePermissionDenied},
		{name: "tenant, just below the role", as: f.noGrant, call: getSetting("audit.retention", tenantScope("lab1")), code: connect.CodePermissionDenied},
		{name: "tenant, at the role, the default applies", as: f.lab1Reader, call: getSetting("audit.retention", tenantScope("lab1")),
			audit: &wantAudit{log: "lab1", outcome: master.OutcomeDone, tenant: "lab1", object: "audit.retention", details: map[string]string{"scope": "tenant/lab1"}},
			check: wantSetting("audit.retention", "installation", "365d")},
		{name: "tenant, the override applies", as: f.lab1Admin, call: func(c clients) outcome {
			if o := setSetting("audit.retention", tenantScope("lab1"), "30d")(c); o.err != nil {
				return o
			}
			return getSetting("audit.retention", tenantScope("lab1"))(c)
		}, check: wantSetting("audit.retention", "lab1", "30d")},
		{name: "no such setting", as: f.instReader, call: getSetting("no.such", installation()), code: connect.CodeNotFound, msg: "no setting", audit: failed("no setting")},
		{name: "no such setting on a tenant", as: f.lab1Reader, call: getSetting("no.such", tenantScope("lab1")), code: connect.CodeNotFound,
			audit: &wantAudit{log: "lab1", outcome: master.OutcomeFailed, reason: "no setting"}},
		{name: "name at 128", as: f.instReader, call: getSetting(strings.Repeat("n", 128), installation()), code: connect.CodeNotFound},
		{name: "name at 129", as: f.instReader, call: getSetting(strings.Repeat("n", 129), installation()), code: connect.CodeInvalidArgument, msg: "name"},
		{name: "a malformed name", as: f.instReader, call: getSetting("Audit", installation()), code: connect.CodeInvalidArgument, msg: "name", audit: failed("name")},
		{name: "no scope, may read the installation", as: f.instReader, call: getSetting("audit.retention", nil), code: connect.CodeInvalidArgument, msg: "scope"},
		{name: "no scope, may not", as: f.lab1Reader, call: getSetting("audit.retention", nil), code: connect.CodePermissionDenied},
		{name: "no such tenant, no right to know", as: f.lab1Reader, call: getSetting("audit.retention", tenantScope("nolab")), code: connect.CodePermissionDenied},
		{name: "no such tenant, may list", as: local, call: getSetting("audit.retention", tenantScope("nolab")), code: connect.CodeNotFound},
		{name: "a malformed tenant, may list", as: f.instReader, call: getSetting("audit.retention", tenantScope("Lab1")), code: connect.CodeInvalidArgument, msg: "scope.tenant"},
		{name: "a tenant that is not ready", as: local, call: getSetting("audit.retention", tenantScope("down")), code: connect.CodeUnavailable, msg: "unreachable", audit: failed("unreachable")},
	})
}

// testSetSetting guards SetSetting: admin on the scope set; the change and
// its entry in the scope's log, without the value; the value's limit; a
// tenant's override of a setting with no default not found; a tenant not
// ready unavailable.
func testSetSetting(t *testing.T) {
	f := settingsFixture(t)
	value := "v-" + strings.Repeat("x", 20)
	f.run(t, []tcase{
		{name: "no key", as: nobody, call: setSetting("audit.retention", installation(), "1d"), code: connect.CodeUnauthenticated},
		{name: "installation, just below the role", as: f.instOperator, call: setSetting("audit.retention", installation(), "1d"), code: connect.CodePermissionDenied, audit: refused()},
		{name: "installation, at the role", as: f.instAdmin, call: setSetting("audit.retention", installation(), value),
			audit: &wantAudit{outcome: master.OutcomeDone, object: "audit.retention", details: map[string]string{"scope": "installation"}},
			check: wantSetting("audit.retention", "installation", value)},
		{name: "installation, a new setting", as: f.instAdmin, call: setSetting("receive.quiet-period", installation(), "60s")},
		{name: "tenant, an installation admin", as: f.instAdmin, call: setSetting("audit.retention", tenantScope("lab1"), "1d"), code: connect.CodePermissionDenied},
		{name: "tenant, just below the role", as: f.lab1Operator, call: setSetting("audit.retention", tenantScope("lab1"), "1d"), code: connect.CodePermissionDenied},
		{name: "tenant, at the role", as: f.lab1Admin, call: setSetting("audit.retention", tenantScope("lab1"), value),
			audit: &wantAudit{log: "lab1", outcome: master.OutcomeDone, tenant: "lab1", object: "audit.retention", details: map[string]string{"scope": "tenant/lab1"}},
			check: wantSetting("audit.retention", "lab1", value)},
		{name: "tenant, repeated", as: f.lab1Admin, call: setSetting("audit.retention", tenantScope("lab1"), value), check: wantSetting("audit.retention", "lab1", value)},
		{name: "tenant, no default", as: f.lab1Admin, call: setSetting("no.such", tenantScope("lab1"), "1"), code: connect.CodeNotFound, msg: "no setting",
			audit: &wantAudit{log: "lab1", outcome: master.OutcomeFailed}},
		{name: "value at 64 KiB", as: f.instAdmin, call: setSetting("big", installation(), strings.Repeat("v", 64<<10))},
		{name: "value at 64 KiB and one", as: f.instAdmin, call: setSetting("big", installation(), strings.Repeat("v", 64<<10+1)), code: connect.CodeInvalidArgument, msg: "setting.value", audit: failed("setting.value")},
		{name: "an empty value", as: f.instAdmin, call: setSetting("empty", installation(), ""), check: wantSetting("empty", "installation", "")},
		{name: "no setting", as: f.instAdmin, call: func(c clients) outcome { return call(c.settings.SetSetting, &v1.SetSettingRequest{}) }, code: connect.CodeInvalidArgument, msg: "setting"},
		{name: "a malformed name", as: f.instAdmin, call: setSetting("A", installation(), "1"), code: connect.CodeInvalidArgument, msg: "setting.name"},
		{name: "no scope", as: f.instAdmin, call: setSetting("audit.retention", nil, "1"), code: connect.CodeInvalidArgument, msg: "setting.scope"},
		{name: "no such tenant, may list", as: local, call: setSetting("audit.retention", tenantScope("nolab"), "1"), code: connect.CodeNotFound},
		{name: "no such tenant, no right to know", as: f.lab1Admin, call: setSetting("audit.retention", tenantScope("nolab"), "1"), code: connect.CodePermissionDenied},
		{name: "a tenant that is not ready", as: local, call: setSetting("audit.retention", tenantScope("down"), "1"), code: connect.CodeUnavailable},
	})
	// The value is in no entry of either log.
	for _, en := range append(f.masterLog(), f.tenantLog("lab1")...) {
		if strings.Contains(en.Reason+strings.Join(mapValues(en.Details), " ")+en.Object, value) {
			t.Errorf("an audit entry holds a setting's value: %+v", en)
		}
	}
	if v, err := f.master.Setting(ctx, "audit.retention"); err != nil || v != value {
		t.Errorf("the master holds %q, %v", v, err)
	}
}

// testUnsetSetting guards UnsetSetting: admin on the tenant; the default
// answered; a repeat that succeeds; an installation scope invalid; a setting
// with no default not found; the entry in the tenant's log.
func testUnsetSetting(t *testing.T) {
	f := settingsFixture(t)
	unset := func(name string, s *v1.Scope) func(clients) outcome {
		return func(c clients) outcome {
			return call(c.settings.UnsetSetting, &v1.UnsetSettingRequest{Name: name, Scope: s})
		}
	}
	call(f.as(f.lab1Admin).settings.SetSetting, &v1.SetSettingRequest{Setting: &v1.Setting{Name: "audit.retention", Scope: tenantScope("lab1"), Value: "30d"}})
	f.run(t, []tcase{
		{name: "no key", as: nobody, call: unset("audit.retention", tenantScope("lab1")), code: connect.CodeUnauthenticated},
		{name: "just below the role", as: f.lab1Operator, call: unset("audit.retention", tenantScope("lab1")), code: connect.CodePermissionDenied},
		{name: "at the role", as: f.lab1Admin, call: unset("audit.retention", tenantScope("lab1")),
			audit: &wantAudit{log: "lab1", outcome: master.OutcomeDone, tenant: "lab1", object: "audit.retention"},
			check: wantSetting("audit.retention", "installation", "365d")},
		{name: "the default applies again", as: f.lab1Reader, call: getSetting("audit.retention", tenantScope("lab1")), check: wantSetting("audit.retention", "installation", "365d")},
		{name: "repeated", as: f.lab1Admin, call: unset("audit.retention", tenantScope("lab1")), check: wantSetting("audit.retention", "installation", "365d")},
		{name: "the installation", as: f.instAdmin, call: unset("audit.retention", installation()), code: connect.CodeInvalidArgument, msg: "scope", audit: failed("scope")},
		{name: "the installation, below its admin", as: f.instOperator, call: unset("audit.retention", installation()), code: connect.CodePermissionDenied},
		{name: "no default", as: f.lab1Admin, call: unset("no.such", tenantScope("lab1")), code: connect.CodeNotFound},
		{name: "a malformed name", as: f.lab1Admin, call: unset("", tenantScope("lab1")), code: connect.CodeInvalidArgument, msg: "name"},
		{name: "no such tenant, no right to know", as: f.lab1Admin, call: unset("audit.retention", tenantScope("nolab")), code: connect.CodePermissionDenied},
		{name: "no such tenant, may list", as: local, call: unset("audit.retention", tenantScope("nolab")), code: connect.CodeNotFound},
		{name: "a tenant that is not ready", as: local, call: unset("audit.retention", tenantScope("down")), code: connect.CodeUnavailable},
	})
}

// testListSettings guards ListSettings: reader on the scope; the
// installation's defaults or only the tenant's overrides; paging and a token
// refused with another scope.
func testListSettings(t *testing.T) {
	f := settingsFixture(t)
	f.setDefault("a.one", "1")
	f.setDefault("b.two", "2")
	call(f.as(f.lab1Admin).settings.SetSetting, &v1.SetSettingRequest{Setting: &v1.Setting{Name: "a.one", Scope: tenantScope("lab1"), Value: "x"}})
	list := func(s *v1.Scope, size int32, token string) func(clients) outcome {
		return func(c clients) outcome {
			return call(c.settings.ListSettings, &v1.ListSettingsRequest{Scope: s, PageSize: size, PageToken: token})
		}
	}
	names := func(o outcome) string {
		var out []string
		for _, s := range o.res.(*v1.ListSettingsResponse).GetSettings() {
			out = append(out, s.GetName()+"="+s.GetValue()+"@"+s.GetScope().GetTenant())
		}
		return strings.Join(out, ",")
	}
	var token string
	f.run(t, []tcase{
		{name: "no key", as: nobody, call: list(installation(), 0, ""), code: connect.CodeUnauthenticated},
		{name: "installation, just below the role", as: f.noGrant, call: list(installation(), 0, ""), code: connect.CodePermissionDenied},
		{name: "installation, at the role", as: f.instReader, call: list(installation(), 0, ""),
			audit: &wantAudit{outcome: master.OutcomeDone, details: map[string]string{"scope": "installation"}},
			check: func(t *testing.T, o outcome) {
				if got := names(o); got != "a.one=1@,audit.retention=365d@,b.two=2@" {
					t.Errorf("got %q", got)
				}
			}},
		{name: "installation, a page of 2", as: f.instReader, call: list(installation(), 2, ""),
			check: func(t *testing.T, o outcome) {
				token = o.res.(*v1.ListSettingsResponse).GetNextPageToken()
				if token == "" {
					t.Error("no token")
				}
			}},
		{name: "installation, the next page", as: f.instReader, call: func(c clients) outcome { return list(installation(), 2, token)(c) },
			check: func(t *testing.T, o outcome) {
				if got := names(o); got != "b.two=2@" {
					t.Errorf("got %q", got)
				}
			}},
		{name: "a token with another scope", as: f.lab1Reader, call: func(c clients) outcome { return list(tenantScope("lab1"), 2, token)(c) },
			code: connect.CodeInvalidArgument, msg: "page_token"},
		{name: "tenant, just below the role", as: f.noGrant, call: list(tenantScope("lab1"), 0, ""), code: connect.CodePermissionDenied},
		{name: "tenant, at the role, overrides only", as: f.lab1Reader, call: list(tenantScope("lab1"), 0, ""),
			audit: &wantAudit{log: "lab1", outcome: master.OutcomeDone, details: map[string]string{"scope": "tenant/lab1"}},
			check: func(t *testing.T, o outcome) {
				if got := names(o); got != "a.one=x@lab1" {
					t.Errorf("got %q", got)
				}
			}},
		{name: "page_size at 1001", as: f.instReader, call: list(installation(), 1001, ""), code: connect.CodeInvalidArgument, msg: "page_size", audit: failed("page_size")},
		{name: "no scope", as: f.instReader, call: list(nil, 0, ""), code: connect.CodeInvalidArgument, msg: "scope"},
		{name: "no such tenant, may list", as: local, call: list(tenantScope("nolab"), 0, ""), code: connect.CodeNotFound},
		{name: "no such tenant, no right to know", as: f.lab1Reader, call: list(tenantScope("nolab"), 0, ""), code: connect.CodePermissionDenied},
		{name: "a tenant that is not ready", as: local, call: list(tenantScope("down"), 0, ""), code: connect.CodeUnavailable},
	})
}
