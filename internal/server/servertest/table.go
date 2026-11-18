package servertest

import (
	"google.golang.org/protobuf/proto"

	magnetarv1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/master"
)

// Entry is what one method needs in Table: a fixed requirement, or, when
// NewMsg is set, a requirement read from the request by Need.
type Entry struct {
	// Fixed is the requirement when it does not depend on the request.
	Fixed access.Requirement
	// NewMsg returns an empty request message; nil when Fixed applies.
	NewMsg func() proto.Message
	// Need maps a request to its requirement, when NewMsg is set.
	Need func(proto.Message) access.Requirement
}

// Table is a stand-in method-to-requirement table, keyed by full procedure
// name. It implements server.Methods for the server's tests and the
// stand-in program. It is test data: the table the binary uses belongs to
// package internal/api. Contract returns the contract's table, taken from
// the "Role:" line of each method in api/proto/magnetar/v1; its tests check
// it against the descriptors and those lines.
type Table map[string]Entry

// RequestFor returns an empty request message of procedure when its
// requirement depends on the request, nil when it does not, and false for a
// procedure the table does not have.
func (t Table) RequestFor(procedure string) (proto.Message, bool) {
	e, ok := t[procedure]
	if !ok {
		return nil, false
	}
	if e.NewMsg == nil {
		return nil, true
	}
	return e.NewMsg(), true
}

// Requirement returns what procedure needs for the request msg (nil for a
// fixed requirement), and no audit facts: the stand-in records none.
func (t Table) Requirement(procedure string, msg proto.Message) (access.Requirement, access.Request) {
	e, ok := t[procedure]
	if !ok {
		return access.Requirement{}, access.Request{}
	}
	if e.NewMsg == nil {
		return e.Fixed, access.Request{}
	}
	if msg == nil {
		msg = e.NewMsg()
	}
	return e.Need(msg), access.Request{}
}

// fixed is an entry with a requirement that does not depend on the request.
func fixed(req access.Requirement) Entry { return Entry{Fixed: req} }

// byMessage is an entry whose requirement is read from the request, typed as
// the method's request message M.
func byMessage[M proto.Message](newMsg func() M, need func(M) access.Requirement) Entry {
	return Entry{
		NewMsg: func() proto.Message { return newMsg() },
		Need: func(m proto.Message) access.Requirement {
			typed, ok := m.(M)
			if !ok {
				// The zero Requirement, which nobody meets, fails closed.
				return access.Requirement{}
			}
			return need(typed)
		},
	}
}

// onScope is the requirement of a role on a Scope as a request gives it. A
// request with no scope is held to the role on the installation: the check
// runs before validation, so a caller without that role is refused before it
// learns its request was malformed.
func onScope(s *magnetarv1.Scope, role master.Role) access.Requirement {
	if t, ok := s.GetKind().(*magnetarv1.Scope_Tenant); ok {
		return access.OnTenant(t.Tenant, role)
	}
	return access.OnInstallation(role)
}

// Contract returns the contract's method-to-requirement table, a new copy
// each call so a test may add to it.
func Contract() Table {
	admin := fixed(access.OnInstallation(master.RoleAdmin))
	return Table{
		magnetarv1connect.AccessServiceCreateKeyProcedure:   admin,
		magnetarv1connect.AccessServiceGetKeyProcedure:      admin,
		magnetarv1connect.AccessServiceListKeysProcedure:    admin,
		magnetarv1connect.AccessServiceRevokeKeyProcedure:   admin,
		magnetarv1connect.AccessServiceSetGrantProcedure:    admin,
		magnetarv1connect.AccessServiceListGrantsProcedure:  admin,
		magnetarv1connect.AccessServiceRemoveGrantProcedure: admin,
		// "Role: none; any caller the server authenticates."
		magnetarv1connect.AccessServiceWhoAmIProcedure: fixed(access.Authenticated()),

		magnetarv1connect.TenantServiceCreateTenantProcedure: admin,
		// "Role: reader on that tenant, or reader on the installation."
		magnetarv1connect.TenantServiceGetTenantProcedure: byMessage(
			func() *magnetarv1.GetTenantRequest { return &magnetarv1.GetTenantRequest{} },
			func(m *magnetarv1.GetTenantRequest) access.Requirement { return access.TenantRegistration(m.GetName()) },
		),
		// "Role: none beyond an authenticated caller."
		magnetarv1connect.TenantServiceListTenantsProcedure:  fixed(access.Authenticated()),
		magnetarv1connect.TenantServiceRemoveTenantProcedure: admin,

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
		magnetarv1connect.AuditServiceListAuditEntriesProcedure: byMessage(
			func() *magnetarv1.ListAuditEntriesRequest { return &magnetarv1.ListAuditEntriesRequest{} },
			func(m *magnetarv1.ListAuditEntriesRequest) access.Requirement {
				return onScope(m.GetScope(), master.RoleAdmin)
			},
		),
	}
}
