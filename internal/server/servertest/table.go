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
				return settingChange(m.GetSetting().GetName(), m.GetSetting().GetScope())
			},
		),
		magnetarv1connect.SettingsServiceUnsetSettingProcedure: byMessage(
			func() *magnetarv1.UnsetSettingRequest { return &magnetarv1.UnsetSettingRequest{} },
			func(m *magnetarv1.UnsetSettingRequest) access.Requirement {
				return settingChange(m.GetName(), m.GetScope())
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

		// StorageService: "admin on the tenant." to change, "reader on the
		// tenant." to read.
		magnetarv1connect.StorageServiceAddStorageProcedure:    onTenant(func() *magnetarv1.AddStorageRequest { return &magnetarv1.AddStorageRequest{} }, master.RoleAdmin),
		magnetarv1connect.StorageServiceGetStorageProcedure:    onTenant(func() *magnetarv1.GetStorageRequest { return &magnetarv1.GetStorageRequest{} }, master.RoleReader),
		magnetarv1connect.StorageServiceListStoragesProcedure:  onTenant(func() *magnetarv1.ListStoragesRequest { return &magnetarv1.ListStoragesRequest{} }, master.RoleReader),
		magnetarv1connect.StorageServiceSetStorageProcedure:    onTenant(func() *magnetarv1.SetStorageRequest { return &magnetarv1.SetStorageRequest{} }, master.RoleAdmin),
		magnetarv1connect.StorageServiceRemoveStorageProcedure: onTenant(func() *magnetarv1.RemoveStorageRequest { return &magnetarv1.RemoveStorageRequest{} }, master.RoleAdmin),
		magnetarv1connect.StorageServiceTestStorageProcedure:   onTenant(func() *magnetarv1.TestStorageRequest { return &magnetarv1.TestStorageRequest{} }, master.RoleAdmin),

		// ReceiverService: the same lines.
		magnetarv1connect.ReceiverServiceAddReceiverProcedure:    onTenant(func() *magnetarv1.AddReceiverRequest { return &magnetarv1.AddReceiverRequest{} }, master.RoleAdmin),
		magnetarv1connect.ReceiverServiceGetReceiverProcedure:    onTenant(func() *magnetarv1.GetReceiverRequest { return &magnetarv1.GetReceiverRequest{} }, master.RoleReader),
		magnetarv1connect.ReceiverServiceListReceiversProcedure:  onTenant(func() *magnetarv1.ListReceiversRequest { return &magnetarv1.ListReceiversRequest{} }, master.RoleReader),
		magnetarv1connect.ReceiverServiceSetReceiverProcedure:    onTenant(func() *magnetarv1.SetReceiverRequest { return &magnetarv1.SetReceiverRequest{} }, master.RoleAdmin),
		magnetarv1connect.ReceiverServiceRemoveReceiverProcedure: onTenant(func() *magnetarv1.RemoveReceiverRequest { return &magnetarv1.RemoveReceiverRequest{} }, master.RoleAdmin),

		// ReceptionService: reader to read, operator to filter by Study
		// Instance UID and to settle a failed reception.
		magnetarv1connect.ReceptionServiceListReceptionsProcedure: byMessage(
			func() *magnetarv1.ListReceptionsRequest { return &magnetarv1.ListReceptionsRequest{} },
			func(m *magnetarv1.ListReceptionsRequest) access.Requirement {
				return tenantRole(m.GetTenant(), master.RoleReader, m.GetStudyInstanceUid() != "")
			},
		),
		magnetarv1connect.ReceptionServiceGetReceptionProcedure:     onTenant(func() *magnetarv1.GetReceptionRequest { return &magnetarv1.GetReceptionRequest{} }, master.RoleReader),
		magnetarv1connect.ReceptionServiceRetryReceptionProcedure:   onTenant(func() *magnetarv1.RetryReceptionRequest { return &magnetarv1.RetryReceptionRequest{} }, master.RoleOperator),
		magnetarv1connect.ReceptionServiceDiscardReceptionProcedure: onTenant(func() *magnetarv1.DiscardReceptionRequest { return &magnetarv1.DiscardReceptionRequest{} }, master.RoleOperator),

		// QuarantineService: reader to read, operator for a UID filter, a
		// header, a release and a rejection.
		magnetarv1connect.QuarantineServiceListQuarantinedFilesProcedure: byMessage(
			func() *magnetarv1.ListQuarantinedFilesRequest { return &magnetarv1.ListQuarantinedFilesRequest{} },
			func(m *magnetarv1.ListQuarantinedFilesRequest) access.Requirement {
				return tenantRole(m.GetTenant(), master.RoleReader, m.GetStudyInstanceUid() != "" || m.GetSopInstanceUid() != "")
			},
		),
		magnetarv1connect.QuarantineServiceGetQuarantinedFileProcedure: byMessage(
			func() *magnetarv1.GetQuarantinedFileRequest { return &magnetarv1.GetQuarantinedFileRequest{} },
			func(m *magnetarv1.GetQuarantinedFileRequest) access.Requirement {
				return tenantRole(m.GetTenant(), master.RoleReader, m.GetIncludeHeader())
			},
		),
		magnetarv1connect.QuarantineServiceReleaseQuarantinedFileProcedure: onTenant(func() *magnetarv1.ReleaseQuarantinedFileRequest { return &magnetarv1.ReleaseQuarantinedFileRequest{} }, master.RoleOperator),
		magnetarv1connect.QuarantineServiceRejectQuarantinedFileProcedure:  onTenant(func() *magnetarv1.RejectQuarantinedFileRequest { return &magnetarv1.RejectQuarantinedFileRequest{} }, master.RoleOperator),

		// MonitorService: "reader on the tenant asked for; operator on the
		// installation for every tenant."
		magnetarv1connect.MonitorServiceGetStatusProcedure: byMessage(
			func() *magnetarv1.GetStatusRequest { return &magnetarv1.GetStatusRequest{} },
			func(m *magnetarv1.GetStatusRequest) access.Requirement { return onMonitorScope(m.GetScope()) },
		),
		magnetarv1connect.MonitorServiceWatchProcedure: byMessage(
			func() *magnetarv1.WatchRequest { return &magnetarv1.WatchRequest{} },
			func(m *magnetarv1.WatchRequest) access.Requirement { return onMonitorScope(m.GetScope()) },
		),
	}
}

// onMonitorScope is the requirement of a Monitor method: reader on the
// tenant the scope names, operator on the installation for every tenant
// or a malformed scope.
func onMonitorScope(s *magnetarv1.Scope) access.Requirement {
	if t, ok := s.GetKind().(*magnetarv1.Scope_Tenant); ok && master.ValidateTenantName(t.Tenant) == nil {
		return access.OnTenant(t.Tenant, master.RoleReader)
	}
	return access.OnInstallation(master.RoleOperator)
}

// tenanted is a request into one tenant, named by its field "tenant".
type tenanted interface {
	proto.Message
	GetTenant() string
}

// onTenant is an entry whose requirement is role on the tenant the request
// names by its field "tenant".
func onTenant[M tenanted](newMsg func() M, role master.Role) Entry {
	return byMessage(newMsg, func(m M) access.Requirement { return tenantRole(m.GetTenant(), role, false) })
}

// tenantRole is the requirement of role on the tenant named name, or of
// operator when operator is set. A request with no tenant is held to the
// role on the installation, as onScope holds one with no scope.
func tenantRole(name string, role master.Role, operator bool) access.Requirement {
	if operator {
		role = master.RoleOperator
	}
	if name == "" {
		return access.OnInstallation(role)
	}
	return access.OnTenant(name, role)
}

// installationSet is the list of settings an admin on the installation
// alone sets on a tenant: SetSetting and UnsetSetting of one need admin on
// the installation, whatever the scope. Restated from the server's list of
// settings, so the table stays test data and its test compares the two.
var installationSet = map[string]bool{"receiver.ports": true, "incoming.limit": true}

// settingChange is the requirement of a change of the setting named name
// on scope.
func settingChange(name string, scope *magnetarv1.Scope) access.Requirement {
	if installationSet[name] {
		return access.OnInstallation(master.RoleAdmin)
	}
	return onScope(scope, master.RoleAdmin)
}
