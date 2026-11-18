package api_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/encoding/protojson"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/master"
)

// everyMethod returns one call of each method of the four services, each
// with a request that would succeed for the local operator on the fixture.
func everyMethod(f *fixture) map[string]func(clients) outcome {
	return map[string]func(clients) outcome{
		"CreateKey": createKey(&v1.CreateKeyRequest{Holder: "a"}),
		"GetKey":    func(c clients) outcome { return call(c.access.GetKey, &v1.GetKeyRequest{Id: f.noGrantID}) },
		"ListKeys":  func(c clients) outcome { return call(c.access.ListKeys, &v1.ListKeysRequest{}) },
		"RevokeKey": func(c clients) outcome { return call(c.access.RevokeKey, &v1.RevokeKeyRequest{Id: f.noGrantID}) },
		"SetGrant": func(c clients) outcome {
			return call(c.access.SetGrant, &v1.SetGrantRequest{KeyId: f.noGrantID, Scope: installation(), Role: v1.Role_ROLE_READER})
		},
		"ListGrants": func(c clients) outcome {
			return call(c.access.ListGrants, &v1.ListGrantsRequest{Filter: &v1.ListGrantsRequest_Scope{Scope: installation()}})
		},
		"RemoveGrant": func(c clients) outcome {
			return call(c.access.RemoveGrant, &v1.RemoveGrantRequest{KeyId: f.lab1ReaderID, Scope: tenantScope("lab1")})
		},
		"WhoAmI":       func(c clients) outcome { return call(c.access.WhoAmI, &v1.WhoAmIRequest{}) },
		"CreateTenant": createTenant("lab9", sqliteType, ""),
		"GetTenant":    func(c clients) outcome { return call(c.tenants.GetTenant, &v1.GetTenantRequest{Name: "lab1"}) },
		"ListTenants":  func(c clients) outcome { return call(c.tenants.ListTenants, &v1.ListTenantsRequest{}) },
		"RemoveTenant": func(c clients) outcome {
			return call(c.tenants.RemoveTenant, &v1.RemoveTenantRequest{Name: "lab1", ConfirmName: "lab1"})
		},
		"GetSetting": getSetting("audit.retention", tenantScope("lab1")),
		"SetSetting": setSetting("audit.retention", tenantScope("lab1"), "1d"),
		"UnsetSetting": func(c clients) outcome {
			return call(c.settings.UnsetSetting, &v1.UnsetSettingRequest{Name: "audit.retention", Scope: tenantScope("lab1")})
		},
		"ListSettings": func(c clients) outcome {
			return call(c.settings.ListSettings, &v1.ListSettingsRequest{Scope: installation()})
		},
		"ListAuditEntries": func(c clients) outcome {
			return call(c.audit.ListAuditEntries, &v1.ListAuditEntriesRequest{Scope: installation()})
		},
	}
}

// TestNoDecisionIsInternal guards the rule that a handler never decides by
// itself: when the server did not put a Decision in the context, every
// method answers INTERNAL with the fixed message, logs it, and changes
// nothing, even for the local operator, who would be allowed everything.
func TestNoDecisionIsInternal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.setDefault("audit.retention", "365d")
	f.decide.Store(false)
	methods := everyMethod(f)
	if len(methods) != len(contractMethods()) {
		t.Fatalf("everyMethod has %d methods, the contract %d", len(methods), len(contractMethods()))
	}
	for name, m := range methods {
		o := m(f.as(local))
		if o.code() != connect.CodeInternal || o.message() != "internal error" {
			t.Errorf("%s without a decision: %v, %q", name, o.code(), o.message())
		}
	}
	if n := strings.Count(f.log.String(), "without an access decision"); n != len(methods) {
		t.Errorf("%d log lines about a missing decision, want %d", n, len(methods))
	}
	if _, err := f.master.TenantByName(ctx, "lab9"); err == nil {
		t.Error("CreateTenant acted without a decision")
	}
	if _, ok := f.registry.State("lab1"); !ok {
		t.Error("RemoveTenant acted without a decision")
	}
	if k, err := f.master.Key(ctx, f.noGrantID); err != nil || !k.Revoked.IsZero() {
		t.Error("RevokeKey acted without a decision")
	}
}

// TestEveryMethodWithTheMasterDown guards the unavailable line of every
// method: with the master database closed, each answers UNAVAILABLE, never a
// refusal or an internal error. The local operator calls, so the check reads
// nothing for most methods and the handler meets the closed master itself;
// WhoAmI is called with a key, since the local operator's WhoAmI reads
// nothing at all.
func TestEveryMethodWithTheMasterDown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.setDefault("audit.retention", "365d")
	if err := f.master.Close(); err != nil {
		t.Fatal(err)
	}
	for name, m := range everyMethod(f) {
		as := local
		if name == "WhoAmI" {
			as = f.instAdmin
		}
		if o := m(f.as(as)); o.code() != connect.CodeUnavailable {
			t.Errorf("%s with the master down: %v (%v)", name, o.code(), o.err)
		}
	}
}

// TestLaterLookupDoesNotUndoTheAccessRule guards the rule that a lookup
// after the check that finds no tenant answers as the check would: a key
// whose grant names a tenant the registry does not hold (here a row written
// to the master behind the registry's back) is denied with the message of
// no grant, and only a caller who may list every tenant is told not found.
func TestLaterLookupDoesNotUndoTheAccessRule(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.setDefault("audit.retention", "365d")
	row := master.Tenant{ID: "ghost-id", Name: "ghost", Driver: "sqlite", SealedAddress: []byte{1}, State: master.StateReady, Created: t0}
	if err := f.master.CreateTenant(ctx, row, f.setupEntry()); err != nil {
		t.Fatal(err)
	}
	admin, _ := f.key(master.Grant{Scope: master.TenantScope("ghost-id"), Role: master.RoleAdmin})
	calls := map[string]func(clients) outcome{
		"GetSetting": getSetting("audit.retention", tenantScope("ghost")),
		"SetSetting": setSetting("audit.retention", tenantScope("ghost"), "1d"),
		"UnsetSetting": func(c clients) outcome {
			return call(c.settings.UnsetSetting, &v1.UnsetSettingRequest{Name: "audit.retention", Scope: tenantScope("ghost")})
		},
		"ListSettings": func(c clients) outcome {
			return call(c.settings.ListSettings, &v1.ListSettingsRequest{Scope: tenantScope("ghost")})
		},
		"ListAuditEntries": func(c clients) outcome {
			return call(c.audit.ListAuditEntries, &v1.ListAuditEntriesRequest{Scope: tenantScope("ghost")})
		},
	}
	for name, m := range calls {
		if o := m(f.as(admin)); o.code() != connect.CodePermissionDenied || o.message() != access.MessageDenied {
			t.Errorf("%s by a key that may not list every tenant: %v %q, want permission denied %q", name, o.code(), o.message(), access.MessageDenied)
		}
		if o := m(f.as(local)); o.code() != connect.CodeNotFound || o.message() != access.MessageNoTenant {
			t.Errorf("%s by the local operator: %v %q, want not found", name, o.code(), o.message())
		}
	}
}

// TestReadsDoNotFailForTheirEntry guards the rule that a read never fails
// because its audit entry could not be queued: with the access package's
// audit writer closed, so every read's entry is dropped, reads still answer.
func TestReadsDoNotFailForTheirEntry(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.setDefault("audit.retention", "365d")
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.access.Close(cctx); err != nil {
		t.Fatal(err)
	}
	reads := map[string]func(clients) outcome{
		"GetTenant":   func(c clients) outcome { return call(c.tenants.GetTenant, &v1.GetTenantRequest{Name: "lab1"}) },
		"ListTenants": func(c clients) outcome { return call(c.tenants.ListTenants, &v1.ListTenantsRequest{}) },
		"GetSetting":  getSetting("audit.retention", installation()),
		"ListSettings": func(c clients) outcome {
			return call(c.settings.ListSettings, &v1.ListSettingsRequest{Scope: installation()})
		},
		"ListAuditEntries": func(c clients) outcome {
			return call(c.audit.ListAuditEntries, &v1.ListAuditEntriesRequest{Scope: installation()})
		},
	}
	for name, m := range reads {
		if o := m(f.as(f.instAdmin)); o.err != nil {
			t.Errorf("%s with the audit writer closed: %v", name, o.err)
		}
	}
	if f.access.AuditStats().Dropped == 0 {
		t.Error("no entry was dropped, so the test showed nothing")
	}
}

// TestInternalCauseIsLoggedNotReturned guards the rule for an internal
// fault: the answer says "internal error" and nothing more, and the log has
// the cause under the request's id. The fault is an id source that makes a
// tenant id the master would refuse, which must not come back as the
// caller's invalid argument.
func TestInternalCauseIsLoggedNotReturned(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.badTenantID.Store(true)
	o := createTenant("lab2", sqliteType, "")(f.as(f.instAdmin))
	if o.code() != connect.CodeInternal || o.message() != "internal error" {
		t.Fatalf("got %v %q", o.code(), o.message())
	}
	log := f.log.String()
	if !strings.Contains(log, "request_id="+o.requestID) || !strings.Contains(log, "id source made a tenant id") {
		t.Errorf("the log does not hold the cause under the request id:\n%s", log)
	}
	en := entriesOf(f.masterLog(), o.requestID)
	if len(en) != 1 || en[0].Outcome != master.OutcomeFailed || strings.Contains(en[0].Reason, "id source") {
		t.Errorf("the entry is %+v", en)
	}
}

// TestTenantLifecycle is acceptance point 4: a tenant created over the API
// is ready and has its database file; a tenant setting is set, read, unset
// and found in that tenant's audit log; after removal the registration is
// gone and the file is still there.
func TestTenantLifecycle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.setDefault("audit.retention", "365d")
	admin := e.as(local)
	o := call(admin.tenants.CreateTenant, &v1.CreateTenantRequest{Name: "study", DatabaseType: sqliteType})
	if o.err != nil || o.res.(*v1.CreateTenantResponse).GetTenant().GetState() != v1.TenantState_TENANT_STATE_READY {
		t.Fatalf("create: %v %v", o.err, o.res)
	}
	file := filepath.Join(e.data, "tenants", "study.db")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the tenant's file: %v", err)
	}
	set := call(admin.settings.SetSetting, &v1.SetSettingRequest{Setting: &v1.Setting{Name: "audit.retention", Scope: tenantScope("study"), Value: "30d"}})
	get := call(admin.settings.GetSetting, &v1.GetSettingRequest{Name: "audit.retention", Scope: tenantScope("study")})
	unset := call(admin.settings.UnsetSetting, &v1.UnsetSettingRequest{Name: "audit.retention", Scope: tenantScope("study")})
	for name, o := range map[string]outcome{"set": set, "get": get, "unset": unset} {
		if o.err != nil {
			t.Fatalf("%s: %v", name, o.err)
		}
	}
	if v := get.res.(*v1.GetSettingResponse).GetSetting(); v.GetValue() != "30d" || v.GetScope().GetTenant() != "study" {
		t.Errorf("get: %v", v)
	}
	if v := unset.res.(*v1.UnsetSettingResponse).GetSetting(); v.GetValue() != "365d" || v.GetScope().GetInstallation() == nil {
		t.Errorf("unset: %v", v)
	}
	log := e.tenantLog("study")
	for name, o := range map[string]outcome{"set": set, "get": get, "unset": unset} {
		if got := entriesOf(log, o.requestID); len(got) != 1 || got[0].Outcome != master.OutcomeDone || got[0].Object != "audit.retention" {
			t.Errorf("%s: the tenant's log holds %+v", name, got)
		}
	}
	rm := call(admin.tenants.RemoveTenant, &v1.RemoveTenantRequest{Name: "study", ConfirmName: "study"})
	if rm.err != nil {
		t.Fatal(rm.err)
	}
	if _, err := e.master.TenantByName(ctx, "study"); err == nil {
		t.Error("the master still holds the tenant")
	}
	if _, ok := e.registry.State("study"); ok {
		t.Error("the registry still holds the tenant")
	}
	if o := call(admin.tenants.GetTenant, &v1.GetTenantRequest{Name: "study"}); o.code() != connect.CodeNotFound {
		t.Errorf("get after remove: %v", o.code())
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the file is gone after remove: %v", err)
	}
}

// TestDatabaseAddressIsFoundNowhere is acceptance point 5: a database
// address given to CreateTenant, with a password, user and host that are
// each searched for, is in no response, no audit entry of either log, no
// log line, no error, and in the master's files only sealed. The test's
// driver repeats the address in its own error when the database is down, so
// the path where a driver is careless is covered too.
func TestDatabaseAddressIsFoundNowhere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const (
		password = "Hunter2-Secret-9f3k"
		user     = "aliceuser7q"
		host     = "db-secret-host.internal"
	)
	address := "postgres://" + user + ":" + password + "@" + host + ":5432/lab?sslmode=verify-full"
	// Each secret is reported by its label only, so a failing run prints no
	// secret in the test's output.
	secrets := map[string]string{"the address": address, "the password": password, "the user": user, "the host": host}

	var seen []string
	record := func(o outcome) {
		if o.res != nil {
			b, err := protojson.Marshal(o.res)
			if err != nil {
				t.Fatal(err)
			}
			seen = append(seen, string(b))
		}
		if o.err != nil {
			seen = append(seen, o.err.Error(), o.message())
		}
	}
	admin := f.as(f.instAdmin)
	create := func(name, addr string, typ v1.DatabaseType) outcome {
		return call(admin.tenants.CreateTenant, &v1.CreateTenantRequest{Name: name, DatabaseType: typ, DatabaseAddress: addr})
	}
	o := create("pg1", address, pgType)
	if o.err != nil || o.res.(*v1.CreateTenantResponse).GetTenant().GetState() != v1.TenantState_TENANT_STATE_READY {
		t.Fatalf("create: %v %v", o.err, o.res)
	}
	record(o)
	record(create("pg1", address, pgType))        // conflict
	record(create("pg-bad", address, sqliteType)) // invalid
	f.pg.down.Store(true)
	record(create("pg2", address, pgType)) // created unreachable; the driver's error held the address
	record(call(admin.tenants.GetTenant, &v1.GetTenantRequest{Name: "pg2"}))
	record(call(admin.tenants.ListTenants, &v1.ListTenantsRequest{}))
	record(call(f.as(local).settings.GetSetting, &v1.GetSettingRequest{Name: "x", Scope: tenantScope("pg2")}))
	f.pg.down.Store(false)
	record(call(admin.tenants.GetTenant, &v1.GetTenantRequest{Name: "pg1"}))
	record(call(f.as(local).settings.SetSetting, &v1.SetSettingRequest{Setting: &v1.Setting{Name: "x", Scope: installation(), Value: "1"}}))
	record(call(f.as(local).settings.SetSetting, &v1.SetSettingRequest{Setting: &v1.Setting{Name: "x", Scope: tenantScope("pg1"), Value: "1"}}))
	record(call(f.as(local).audit.ListAuditEntries, &v1.ListAuditEntriesRequest{Scope: installation(), PageSize: 1000}))
	record(call(f.as(local).audit.ListAuditEntries, &v1.ListAuditEntriesRequest{Scope: tenantScope("pg1"), PageSize: 1000}))
	rm := call(admin.tenants.RemoveTenant, &v1.RemoveTenantRequest{Name: "pg1", ConfirmName: "pg1"})
	if rm.err != nil || rm.res.(*v1.RemoveTenantResponse).GetDataLocation() != "pg.location.test:5432/pg1" {
		t.Errorf("remove: %v %v", rm.err, rm.res)
	}
	record(rm)

	for _, en := range f.masterLog() {
		seen = append(seen, fmt.Sprintf("%+v", en))
	}
	seen = append(seen, f.log.String())
	for _, name := range []string{"master.db", "master.db-wal"} {
		b, err := os.ReadFile(filepath.Join(f.data, name)) //nolint:gosec // G304: a file of the test's own directory.
		if err == nil {
			seen = append(seen, string(b))
		}
	}
	if len(seen) < 15 {
		t.Fatalf("only %d texts were gathered", len(seen))
	}
	for label, s := range secrets {
		for i, text := range seen {
			if strings.Contains(text, s) {
				t.Errorf("text %d of %d gathered holds %s", i, len(seen), label)
			}
		}
	}
	if !strings.Contains(strings.Join(seen, "\n"), "pg.location.test") {
		t.Error("the location is in nothing gathered, so the search may have looked at the wrong texts")
	}
}
