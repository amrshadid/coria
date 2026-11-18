package api_test

import (
	"context"
	"testing"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectinprocess"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/api"
	"github.com/amrshadid/magnetar/internal/master"
)

// inProcess returns clients that reach e's handlers without the network:
// the same connect.Server and stand-in, through the in-process transport, so
// a benchmark measures the check and the handler, not HTTP.
func (e *env) inProcess(b *testing.B, c caller) clients {
	b.Helper()
	h, err := api.New(api.Config{Access: e.access, Registry: e.registry, Master: e.master, NewID: e.newID, Settings: e.known})
	if err != nil {
		b.Fatal(err)
	}
	srv := connect.NewServer(e.standIn)
	h.Register(srv)
	set := func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			if info, ok := connect.CallInfoForClientContext(ctx); ok && c.key != "" {
				info.RequestHeader().Set("Authorization", "Bearer "+c.key)
			}
			return next(ctx, spec)
		}
	}
	cl := connect.NewClient(connectinprocess.New(srv), set)
	return clients{
		tenants:  magnetarv1connect.NewTenantServiceClient(cl),
		access:   magnetarv1connect.NewAccessServiceClient(cl),
		settings: magnetarv1connect.NewSettingsServiceClient(cl),
		audit:    magnetarv1connect.NewAuditServiceClient(cl),
	}
}

// BenchmarkGetTenant measures one read, GetTenant by a key with reader on
// the tenant, through the access check and the handler without the network:
// the check's reads of the key, the tenant and the roles, the handler's read
// of the tenant, and its audit entry handed to the audit writer.
func BenchmarkGetTenant(b *testing.B) {
	e := newEnv(b)
	e.tenant("lab1")
	reader, _ := e.key(e.onTenant("lab1", master.RoleReader))
	cl := e.inProcess(b, reader)
	req := &v1.GetTenantRequest{Name: "lab1"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cl.tenants.GetTenant(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSetSetting measures one change, SetSetting on the installation by
// an installation admin, through the check and the handler without the
// network: the change and its audit entry are one synced transaction of the
// SQLite master, which is most of the cost.
func BenchmarkSetSetting(b *testing.B) {
	e := newEnv(b)
	admin, _ := e.key(onInstallation(master.RoleAdmin))
	cl := e.inProcess(b, admin)
	req := &v1.SetSettingRequest{Setting: &v1.Setting{Name: testRetention, Scope: installation(), Value: "90d"}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cl.settings.SetSetting(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetTenantSetting measures a read on a tenant's scope, whose audit
// entry is written to the tenant's log on the request's path (the tenant
// store has no batching writer), so its cost is a synced commit of the
// tenant's database, unlike a read of the installation.
func BenchmarkGetTenantSetting(b *testing.B) {
	e := newEnv(b)
	e.tenant("lab1")
	reader, _ := e.key(e.onTenant("lab1", master.RoleReader))
	cl := e.inProcess(b, reader)
	req := &v1.GetSettingRequest{Name: testRetention, Scope: tenantScope("lab1")}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cl.settings.GetSetting(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSetTenantSetting is BenchmarkSetSetting on a tenant's override,
// by the tenant's admin: the change goes to the tenant's database, after a
// read of the installation's default from the master.
func BenchmarkSetTenantSetting(b *testing.B) {
	e := newEnv(b)
	e.tenant("lab1")
	admin, _ := e.key(e.onTenant("lab1", master.RoleAdmin))
	cl := e.inProcess(b, admin)
	req := &v1.SetSettingRequest{Setting: &v1.Setting{Name: testRetention, Scope: tenantScope("lab1"), Value: "90d"}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cl.settings.SetSetting(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}
