package server

import (
	"google.golang.org/protobuf/proto"

	magnetarv1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/master"
)

// The procedures of the standard gRPC health service
// (grpc/health/v1/health.proto). They are the only methods anyone may call
// without a key: a load balancer or an orchestrator asks them, and they
// answer only "serving" or "not serving".
const (
	HealthCheckProcedure = "/grpc.health.v1.Health/Check"
	HealthWatchProcedure = "/grpc.health.v1.Health/Watch"
)

// rule is what one method needs of its caller. Exactly one of public,
// static and need says it:
//
//   - public: no credential at all; only the health service;
//   - static: a requirement that does not depend on the request, checked
//     before the request's message is read, so a refused caller's message is
//     never read at all;
//   - need: a requirement that depends on the request, such as a role on
//     the tenant the request names. The first message is read (bounded by
//     the 4 MiB limit) and need maps it to the requirement; newMsg makes the
//     empty message to read it into.
type rule struct {
	public bool
	static access.Requirement
	need   func(proto.Message) access.Requirement
	newMsg func() proto.Message
}

// readsMessage reports whether the rule needs the request's message.
func (r rule) readsMessage() bool { return r.need != nil }

// fallback is the requirement used when the message the rule needs cannot be
// read (malformed, too large): the requirement of the empty message, which
// for a scope is the role on the installation. The order of checks stays
// "authenticated, then the role, then the request": a caller learns that its
// message was malformed only after passing the strictest form of the role
// check.
func (r rule) fallback() access.Requirement {
	if r.need == nil {
		return r.static
	}
	return r.need(r.newMsg())
}

// on returns a rule with a fixed requirement.
func on(req access.Requirement) rule { return rule{static: req} }

// byMessage returns a rule whose requirement is read from the request,
// typed as the method's request message M.
func byMessage[M proto.Message](newMsg func() M, need func(M) access.Requirement) rule {
	return rule{
		newMsg: func() proto.Message { return newMsg() },
		need: func(m proto.Message) access.Requirement {
			typed, ok := m.(M)
			if !ok {
				// A message of another type is a bug of the server: the zero
				// Requirement, which nobody meets, fails the check closed.
				return access.Requirement{}
			}
			return need(typed)
		},
	}
}

// onScope is the requirement of a role on a Scope as a request gives it: on
// the tenant it names, or on the installation. A request with no scope is
// invalid, but the check runs before validation, so it is held to the role
// on the installation: a caller without that role is refused before it can
// learn its request was malformed.
func onScope(s *magnetarv1.Scope, role master.Role) access.Requirement {
	if t, ok := s.GetKind().(*magnetarv1.Scope_Tenant); ok {
		return access.OnTenant(t.Tenant, role)
	}
	return access.OnInstallation(role)
}

// adminOnInstallation is the rule of every method whose comment says "Role:
// admin on the installation".
var adminOnInstallation = on(access.OnInstallation(master.RoleAdmin))

// rules is the method-to-requirement table: every method of the contract,
// keyed by its full procedure name, with the requirement its "Role:" line in
// api/proto/magnetar/v1 states. TestRulesMatchContract walks the generated
// descriptors and the proto files: every method has an entry, every entry
// names a method, and each entry gives the requirement its Role line says.
// A method missing here is refused at start (New), never served unchecked.
var rules = map[string]rule{
	// access.proto: AccessService. Every method needs admin on the
	// installation in this version, except WhoAmI.
	magnetarv1connect.AccessServiceCreateKeyProcedure:   adminOnInstallation,
	magnetarv1connect.AccessServiceGetKeyProcedure:      adminOnInstallation,
	magnetarv1connect.AccessServiceListKeysProcedure:    adminOnInstallation,
	magnetarv1connect.AccessServiceRevokeKeyProcedure:   adminOnInstallation,
	magnetarv1connect.AccessServiceSetGrantProcedure:    adminOnInstallation,
	magnetarv1connect.AccessServiceListGrantsProcedure:  adminOnInstallation,
	magnetarv1connect.AccessServiceRemoveGrantProcedure: adminOnInstallation,
	// "Role: none; any caller the server authenticates."
	magnetarv1connect.AccessServiceWhoAmIProcedure: on(access.Authenticated()),

	// tenant.proto: TenantService.
	magnetarv1connect.TenantServiceCreateTenantProcedure: adminOnInstallation,
	// "Role: reader on that tenant, or reader on the installation."
	magnetarv1connect.TenantServiceGetTenantProcedure: byMessage(
		func() *magnetarv1.GetTenantRequest { return &magnetarv1.GetTenantRequest{} },
		func(m *magnetarv1.GetTenantRequest) access.Requirement { return access.TenantRegistration(m.GetName()) },
	),
	// "Role: none beyond an authenticated caller; the caller's grants
	// decide what is listed."
	magnetarv1connect.TenantServiceListTenantsProcedure:  on(access.Authenticated()),
	magnetarv1connect.TenantServiceRemoveTenantProcedure: adminOnInstallation,

	// settings.proto: SettingsService. Each role is on the scope the
	// request names.
	magnetarv1connect.SettingsServiceGetSettingProcedure: byMessage(
		func() *magnetarv1.GetSettingRequest { return &magnetarv1.GetSettingRequest{} },
		func(m *magnetarv1.GetSettingRequest) access.Requirement {
			return onScope(m.GetScope(), master.RoleReader)
		},
	),
	magnetarv1connect.SettingsServiceSetSettingProcedure: byMessage(
		func() *magnetarv1.SetSettingRequest { return &magnetarv1.SetSettingRequest{} },
		func(m *magnetarv1.SetSettingRequest) access.Requirement {
			return onScope(m.GetSetting().GetScope(), master.RoleAdmin)
		},
	),
	magnetarv1connect.SettingsServiceUnsetSettingProcedure: byMessage(
		func() *magnetarv1.UnsetSettingRequest { return &magnetarv1.UnsetSettingRequest{} },
		func(m *magnetarv1.UnsetSettingRequest) access.Requirement {
			return onScope(m.GetScope(), master.RoleAdmin)
		},
	),
	magnetarv1connect.SettingsServiceListSettingsProcedure: byMessage(
		func() *magnetarv1.ListSettingsRequest { return &magnetarv1.ListSettingsRequest{} },
		func(m *magnetarv1.ListSettingsRequest) access.Requirement {
			return onScope(m.GetScope(), master.RoleReader)
		},
	),

	// audit.proto: AuditService. "Role: admin on the scope asked for."
	magnetarv1connect.AuditServiceListAuditEntriesProcedure: byMessage(
		func() *magnetarv1.ListAuditEntriesRequest { return &magnetarv1.ListAuditEntriesRequest{} },
		func(m *magnetarv1.ListAuditEntriesRequest) access.Requirement {
			return onScope(m.GetScope(), master.RoleAdmin)
		},
	),

	// The standard health service: no key.
	HealthCheckProcedure: {public: true},
	HealthWatchProcedure: {public: true},
}
