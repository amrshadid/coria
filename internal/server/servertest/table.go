package servertest

import (
	"google.golang.org/protobuf/proto"

	coriav1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/gen/coria/v1/coriav1connect"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/master"
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
// the "Role:" line of each method in api/proto/coria/v1; its tests check
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
func onScope(s *coriav1.Scope, role master.Role) access.Requirement {
	if t, ok := s.GetKind().(*coriav1.Scope_Tenant); ok {
		return access.OnTenant(t.Tenant, role)
	}
	return access.OnInstallation(role)
}

// Contract returns the contract's method-to-requirement table, a new copy
// each call so a test may add to it.
func Contract() Table {
	admin := fixed(access.OnInstallation(master.RoleAdmin))
	return Table{
		coriav1connect.AccessServiceCreateKeyProcedure:   admin,
		coriav1connect.AccessServiceGetKeyProcedure:      admin,
		coriav1connect.AccessServiceListKeysProcedure:    admin,
		coriav1connect.AccessServiceRevokeKeyProcedure:   admin,
		coriav1connect.AccessServiceSetGrantProcedure:    admin,
		coriav1connect.AccessServiceListGrantsProcedure:  admin,
		coriav1connect.AccessServiceRemoveGrantProcedure: admin,
		coriav1connect.AccessServiceSetKeyRulesProcedure: admin,
		// "Role: admin on the installation; or the key being rotated
		// itself": the handler decides which, after the check.
		coriav1connect.AccessServiceRotateKeyProcedure: fixed(access.Authenticated()),
		// "Role: none; any caller the server authenticates."
		coriav1connect.AccessServiceWhoAmIProcedure: fixed(access.Authenticated()),

		coriav1connect.TenantServiceCreateTenantProcedure: admin,
		// "Role: reader on that tenant, or reader on the installation."
		coriav1connect.TenantServiceGetTenantProcedure: byMessage(
			func() *coriav1.GetTenantRequest { return &coriav1.GetTenantRequest{} },
			func(m *coriav1.GetTenantRequest) access.Requirement { return access.TenantRegistration(m.GetName()) },
		),
		// "Role: none beyond an authenticated caller."
		coriav1connect.TenantServiceListTenantsProcedure:  fixed(access.Authenticated()),
		coriav1connect.TenantServiceRemoveTenantProcedure: admin,

		coriav1connect.SettingsServiceGetSettingProcedure: byMessage(
			func() *coriav1.GetSettingRequest { return &coriav1.GetSettingRequest{} },
			func(m *coriav1.GetSettingRequest) access.Requirement {
				return onScope(m.GetScope(), master.RoleReader)
			},
		),
		coriav1connect.SettingsServiceSetSettingProcedure: byMessage(
			func() *coriav1.SetSettingRequest { return &coriav1.SetSettingRequest{} },
			func(m *coriav1.SetSettingRequest) access.Requirement {
				return settingChange(m.GetSetting().GetName(), m.GetSetting().GetScope())
			},
		),
		coriav1connect.SettingsServiceUnsetSettingProcedure: byMessage(
			func() *coriav1.UnsetSettingRequest { return &coriav1.UnsetSettingRequest{} },
			func(m *coriav1.UnsetSettingRequest) access.Requirement {
				return settingChange(m.GetName(), m.GetScope())
			},
		),
		coriav1connect.SettingsServiceListSettingsProcedure: byMessage(
			func() *coriav1.ListSettingsRequest { return &coriav1.ListSettingsRequest{} },
			func(m *coriav1.ListSettingsRequest) access.Requirement {
				return onScope(m.GetScope(), master.RoleReader)
			},
		),
		coriav1connect.AuditServiceListAuditEntriesProcedure: byMessage(
			func() *coriav1.ListAuditEntriesRequest { return &coriav1.ListAuditEntriesRequest{} },
			func(m *coriav1.ListAuditEntriesRequest) access.Requirement {
				return onScope(m.GetScope(), master.RoleAdmin)
			},
		),

		// StorageService: "admin on the tenant." to change, "reader on the
		// tenant." to read.
		coriav1connect.StorageServiceAddStorageProcedure:    onTenant(func() *coriav1.AddStorageRequest { return &coriav1.AddStorageRequest{} }, master.RoleAdmin),
		coriav1connect.StorageServiceGetStorageProcedure:    onTenant(func() *coriav1.GetStorageRequest { return &coriav1.GetStorageRequest{} }, master.RoleReader),
		coriav1connect.StorageServiceListStoragesProcedure:  onTenant(func() *coriav1.ListStoragesRequest { return &coriav1.ListStoragesRequest{} }, master.RoleReader),
		coriav1connect.StorageServiceSetStorageProcedure:    onTenant(func() *coriav1.SetStorageRequest { return &coriav1.SetStorageRequest{} }, master.RoleAdmin),
		coriav1connect.StorageServiceRemoveStorageProcedure: onTenant(func() *coriav1.RemoveStorageRequest { return &coriav1.RemoveStorageRequest{} }, master.RoleAdmin),
		coriav1connect.StorageServiceTestStorageProcedure:   onTenant(func() *coriav1.TestStorageRequest { return &coriav1.TestStorageRequest{} }, master.RoleAdmin),

		// ReceiverService: the same lines.
		coriav1connect.ReceiverServiceAddReceiverProcedure:    onTenant(func() *coriav1.AddReceiverRequest { return &coriav1.AddReceiverRequest{} }, master.RoleAdmin),
		coriav1connect.ReceiverServiceGetReceiverProcedure:    onTenant(func() *coriav1.GetReceiverRequest { return &coriav1.GetReceiverRequest{} }, master.RoleReader),
		coriav1connect.ReceiverServiceListReceiversProcedure:  onTenant(func() *coriav1.ListReceiversRequest { return &coriav1.ListReceiversRequest{} }, master.RoleReader),
		coriav1connect.ReceiverServiceSetReceiverProcedure:    onTenant(func() *coriav1.SetReceiverRequest { return &coriav1.SetReceiverRequest{} }, master.RoleAdmin),
		coriav1connect.ReceiverServiceRemoveReceiverProcedure: onTenant(func() *coriav1.RemoveReceiverRequest { return &coriav1.RemoveReceiverRequest{} }, master.RoleAdmin),

		// ReceptionService: reader to read, operator to filter by Study
		// Instance UID and to settle a failed reception.
		coriav1connect.ReceptionServiceListReceptionsProcedure: byMessage(
			func() *coriav1.ListReceptionsRequest { return &coriav1.ListReceptionsRequest{} },
			func(m *coriav1.ListReceptionsRequest) access.Requirement {
				return tenantRole(m.GetTenant(), master.RoleReader, m.GetStudyInstanceUid() != "")
			},
		),
		coriav1connect.ReceptionServiceGetReceptionProcedure:     onTenant(func() *coriav1.GetReceptionRequest { return &coriav1.GetReceptionRequest{} }, master.RoleReader),
		coriav1connect.ReceptionServiceRetryReceptionProcedure:   onTenant(func() *coriav1.RetryReceptionRequest { return &coriav1.RetryReceptionRequest{} }, master.RoleOperator),
		coriav1connect.ReceptionServiceDiscardReceptionProcedure: onTenant(func() *coriav1.DiscardReceptionRequest { return &coriav1.DiscardReceptionRequest{} }, master.RoleOperator),

		// QuarantineService: reader to read, operator for a UID filter, a
		// header, a release and a rejection.
		coriav1connect.QuarantineServiceListQuarantinedFilesProcedure: byMessage(
			func() *coriav1.ListQuarantinedFilesRequest { return &coriav1.ListQuarantinedFilesRequest{} },
			func(m *coriav1.ListQuarantinedFilesRequest) access.Requirement {
				return tenantRole(m.GetTenant(), master.RoleReader, m.GetStudyInstanceUid() != "" || m.GetSopInstanceUid() != "")
			},
		),
		coriav1connect.QuarantineServiceGetQuarantinedFileProcedure: byMessage(
			func() *coriav1.GetQuarantinedFileRequest { return &coriav1.GetQuarantinedFileRequest{} },
			func(m *coriav1.GetQuarantinedFileRequest) access.Requirement {
				return tenantRole(m.GetTenant(), master.RoleReader, m.GetIncludeHeader())
			},
		),
		coriav1connect.QuarantineServiceReleaseQuarantinedFileProcedure: onTenant(func() *coriav1.ReleaseQuarantinedFileRequest { return &coriav1.ReleaseQuarantinedFileRequest{} }, master.RoleOperator),
		coriav1connect.QuarantineServiceRejectQuarantinedFileProcedure:  onTenant(func() *coriav1.RejectQuarantinedFileRequest { return &coriav1.RejectQuarantinedFileRequest{} }, master.RoleOperator),

		// MetadataService: reader on the tenant for a scan's or an
		// instance's header.
		coriav1connect.MetadataServiceGetScanMetadataProcedure:     onTenant(func() *coriav1.GetScanMetadataRequest { return &coriav1.GetScanMetadataRequest{} }, master.RoleReader),
		coriav1connect.MetadataServiceGetInstanceMetadataProcedure: onTenant(func() *coriav1.GetInstanceMetadataRequest { return &coriav1.GetInstanceMetadataRequest{} }, master.RoleReader),

		// CatalogService: reader on the tenant for every list and get.
		coriav1connect.CatalogServiceListSubjectsProcedure: onTenant(func() *coriav1.ListSubjectsRequest { return &coriav1.ListSubjectsRequest{} }, master.RoleReader),
		coriav1connect.CatalogServiceGetSubjectProcedure:   onTenant(func() *coriav1.GetSubjectRequest { return &coriav1.GetSubjectRequest{} }, master.RoleReader),
		coriav1connect.CatalogServiceListSessionsProcedure: onTenant(func() *coriav1.ListSessionsRequest { return &coriav1.ListSessionsRequest{} }, master.RoleReader),
		coriav1connect.CatalogServiceGetSessionProcedure:   onTenant(func() *coriav1.GetSessionRequest { return &coriav1.GetSessionRequest{} }, master.RoleReader),
		coriav1connect.CatalogServiceListScansProcedure:    onTenant(func() *coriav1.ListScansRequest { return &coriav1.ListScansRequest{} }, master.RoleReader),
		coriav1connect.CatalogServiceGetScanProcedure:      onTenant(func() *coriav1.GetScanRequest { return &coriav1.GetScanRequest{} }, master.RoleReader),

		// DataService: reader on the tenant for a stream of files.
		coriav1connect.DataServiceReadFilesProcedure: onTenant(func() *coriav1.ReadFilesRequest { return &coriav1.ReadFilesRequest{} }, master.RoleReader),

		// SenderService: "admin on the tenant." to change or test a sender,
		// "reader on the tenant." to read one. JobService: "operator on the
		// tenant." to make, cancel and retry a job, "reader on the tenant."
		// to read jobs and their failures.
		coriav1connect.SenderServiceAddSenderProcedure:    onTenant(func() *coriav1.AddSenderRequest { return &coriav1.AddSenderRequest{} }, master.RoleAdmin),
		coriav1connect.SenderServiceGetSenderProcedure:    onTenant(func() *coriav1.GetSenderRequest { return &coriav1.GetSenderRequest{} }, master.RoleReader),
		coriav1connect.SenderServiceListSendersProcedure:  onTenant(func() *coriav1.ListSendersRequest { return &coriav1.ListSendersRequest{} }, master.RoleReader),
		coriav1connect.SenderServiceSetSenderProcedure:    onTenant(func() *coriav1.SetSenderRequest { return &coriav1.SetSenderRequest{} }, master.RoleAdmin),
		coriav1connect.SenderServiceRemoveSenderProcedure: onTenant(func() *coriav1.RemoveSenderRequest { return &coriav1.RemoveSenderRequest{} }, master.RoleAdmin),
		coriav1connect.SenderServiceTestSenderProcedure:   onTenant(func() *coriav1.TestSenderRequest { return &coriav1.TestSenderRequest{} }, master.RoleAdmin),
		coriav1connect.JobServiceCreateJobProcedure:       onTenant(func() *coriav1.CreateJobRequest { return &coriav1.CreateJobRequest{} }, master.RoleOperator),
		coriav1connect.JobServiceGetJobProcedure:          onTenant(func() *coriav1.GetJobRequest { return &coriav1.GetJobRequest{} }, master.RoleReader),
		coriav1connect.JobServiceListJobsProcedure:        onTenant(func() *coriav1.ListJobsRequest { return &coriav1.ListJobsRequest{} }, master.RoleReader),
		coriav1connect.JobServiceCancelJobProcedure:       onTenant(func() *coriav1.CancelJobRequest { return &coriav1.CancelJobRequest{} }, master.RoleOperator),
		coriav1connect.JobServiceRetryJobProcedure:        onTenant(func() *coriav1.RetryJobRequest { return &coriav1.RetryJobRequest{} }, master.RoleOperator),
		coriav1connect.JobServiceListJobFailuresProcedure: onTenant(func() *coriav1.ListJobFailuresRequest { return &coriav1.ListJobFailuresRequest{} }, master.RoleReader),

		// ProfileService: "admin on the tenant." to make a version,
		// "operator on the tenant." to read, check, try, explain and
		// compare; the bases need only an authenticated caller.
		coriav1connect.ProfileServiceCreateProfileVersionProcedure: onTenant(func() *coriav1.CreateProfileVersionRequest { return &coriav1.CreateProfileVersionRequest{} }, master.RoleAdmin),
		coriav1connect.ProfileServiceGetProfileVersionProcedure:    onTenant(func() *coriav1.GetProfileVersionRequest { return &coriav1.GetProfileVersionRequest{} }, master.RoleOperator),
		coriav1connect.ProfileServiceListProfilesProcedure:         onTenant(func() *coriav1.ListProfilesRequest { return &coriav1.ListProfilesRequest{} }, master.RoleOperator),
		coriav1connect.ProfileServiceListProfileVersionsProcedure:  onTenant(func() *coriav1.ListProfileVersionsRequest { return &coriav1.ListProfileVersionsRequest{} }, master.RoleOperator),
		coriav1connect.ProfileServiceValidateProfileProcedure:      onTenant(func() *coriav1.ValidateProfileRequest { return &coriav1.ValidateProfileRequest{} }, master.RoleOperator),
		coriav1connect.ProfileServiceListProfileBasesProcedure:     fixed(access.Authenticated()),
		coriav1connect.ProfileServiceTryProfileProcedure:           onTenant(func() *coriav1.TryProfileRequest { return &coriav1.TryProfileRequest{} }, master.RoleOperator),
		coriav1connect.ProfileServiceExplainProfileProcedure:       onTenant(func() *coriav1.ExplainProfileRequest { return &coriav1.ExplainProfileRequest{} }, master.RoleOperator),
		coriav1connect.ProfileServiceDiffProfileVersionsProcedure:  onTenant(func() *coriav1.DiffProfileVersionsRequest { return &coriav1.DiffProfileVersionsRequest{} }, master.RoleOperator),

		// MonitorService: "reader on the tenant asked for; operator on the
		// installation for every tenant."
		coriav1connect.MonitorServiceGetStatusProcedure: byMessage(
			func() *coriav1.GetStatusRequest { return &coriav1.GetStatusRequest{} },
			func(m *coriav1.GetStatusRequest) access.Requirement { return onMonitorScope(m.GetScope()) },
		),
		coriav1connect.MonitorServiceWatchProcedure: byMessage(
			func() *coriav1.WatchRequest { return &coriav1.WatchRequest{} },
			func(m *coriav1.WatchRequest) access.Requirement { return onMonitorScope(m.GetScope()) },
		),
	}
}

// onMonitorScope is the requirement of a Monitor method: reader on the
// tenant the scope names, operator on the installation for every tenant
// or a malformed scope.
func onMonitorScope(s *coriav1.Scope) access.Requirement {
	if t, ok := s.GetKind().(*coriav1.Scope_Tenant); ok && master.ValidateTenantName(t.Tenant) == nil {
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
var installationSet = map[string]bool{"receiver.ports": true, "incoming.limit": true, "sender.tenant-max-associations": true,
	"receiver.allow-as-received": true}

// settingChange is the requirement of a change of the setting named name
// on scope.
func settingChange(name string, scope *coriav1.Scope) access.Requirement {
	if installationSet[name] {
		return access.OnInstallation(master.RoleAdmin)
	}
	return onScope(scope, master.RoleAdmin)
}
