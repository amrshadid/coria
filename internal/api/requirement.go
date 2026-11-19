package api

import (
	"time"

	"google.golang.org/protobuf/proto"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/core/settings"
	"github.com/amrshadid/magnetar/internal/master"
)

// The keys of the details this package puts in audit entries.
const (
	detailScope   = "scope"
	detailKeyID   = "key_id"
	detailOutcome = "outcome"
	detailStart   = "start_time"
	detailEnd     = "end_time"
)

// Methods is the method-to-requirement table of the services: for each
// method, what a request needs of its caller and the facts of its audit
// entry, each line from the Role line of the method's comment. The server
// asks it about every request before the handler runs, and makes the access
// check with what it answers. Its zero value is ready to use.
//
// A requirement that depends on the request (a role on the tenant or the
// scope the request names) reads the request only for that scope. A request
// whose scope or tenant name is malformed, or that could not be read at all,
// is checked for the same role on the installation, and the handler then
// answers it as invalid (doc.go).
type Methods struct{}

// RequestFor reports whether procedure, a full procedure name such as
// "/magnetar.v1.TenantService/GetTenant", is a method of the table, and
// returns a new empty request message of its input type when its
// requirement depends on the request, or nil when the requirement is fixed,
// so the server need not read the body of a request it may refuse.
func (Methods) RequestFor(procedure string) (msg proto.Message, known bool) {
	r, ok := rules[procedure]
	if !ok {
		return nil, false
	}
	if r.newMsg == nil {
		return nil, true
	}
	return r.newMsg(), true
}

// Requirement returns what a request to procedure needs of its caller, and
// its audit facts: Object and Details (the server fills Method,
// RemoteAddress and RequestID). msg is the request read into the message
// RequestFor returned, nil for a fixed requirement, or an empty message when
// the request could not be read, which is held to the role on the
// installation. A message of another type is read as an empty one, so a
// mistake in the server's wiring is held to the strictest requirement and
// never panics. An unknown procedure gets the zero Requirement, which
// nobody meets.
func (Methods) Requirement(procedure string, msg proto.Message) (access.Requirement, access.Request) {
	r, ok := rules[procedure]
	if !ok {
		return access.Requirement{}, access.Request{}
	}
	return r.need(msg)
}

// rule is one line of the table. newMsg is nil when the requirement does
// not depend on the request; need takes the request, or nil.
type rule struct {
	newMsg func() proto.Message
	need   func(msg proto.Message) (access.Requirement, access.Request)
}

// fixed is a line whose requirement does not depend on the request. Its
// audit facts are none: the request is not read before the check, and the
// handler, or package access, adds the object and details to the entries it
// writes.
func fixed(need access.Requirement) rule {
	return rule{need: func(proto.Message) (access.Requirement, access.Request) { return need, access.Request{} }}
}

// byMessage is a line whose requirement is read from the request, of type
// *M. A nil message, or one of another type, is read as the empty message.
func byMessage[M any, P interface {
	*M
	proto.Message
}](f func(P) (access.Requirement, access.Request)) rule {
	return rule{
		newMsg: func() proto.Message { return P(new(M)) },
		need: func(msg proto.Message) (access.Requirement, access.Request) {
			m, ok := msg.(P)
			if !ok || m == nil {
				m = P(new(M))
			}
			return f(m)
		},
	}
}

// installationAdmin is the requirement of every method that needs admin on
// the installation.
var installationAdmin = access.OnInstallation(master.RoleAdmin)

// onScope returns the requirement of role on a scope a request names: on the
// tenant when it names a valid one, and otherwise on the installation, which
// is also the requirement for a malformed scope (doc.go).
func onScope(s *v1.Scope, role master.Role) access.Requirement {
	if t, ok := s.GetKind().(*v1.Scope_Tenant); ok && master.ValidateTenantName(t.Tenant) == nil {
		return access.OnTenant(t.Tenant, role)
	}
	return access.OnInstallation(role)
}

// onMonitorScope is the requirement of a Monitor method: reader on the
// tenant the scope names, operator on the installation for every tenant (a
// malformed scope is held to the installation's, as onScope does).
func onMonitorScope(s *v1.Scope) access.Requirement {
	if t, ok := s.GetKind().(*v1.Scope_Tenant); ok && master.ValidateTenantName(t.Tenant) == nil {
		return access.OnTenant(t.Tenant, master.RoleReader)
	}
	return access.OnInstallation(master.RoleOperator)
}

// scopeDetail returns a scope as an audit entry's details write it,
// "installation" or "tenant/<name>", and "none" for a malformed one.
func scopeDetail(s *v1.Scope) string {
	switch k := s.GetKind().(type) {
	case *v1.Scope_Installation:
		return access.InstallationScope.String()
	case *v1.Scope_Tenant:
		return access.TenantNamed(k.Tenant).String()
	}
	return "none"
}

// tenantObject is the object of an entry about a tenant itself, as the
// registry writes it on its own entries.
func tenantObject(name string) string { return "tenant/" + name }

// rules is the table. A method of the contract missing here is unknown to
// RequestFor, so the server refuses to start, and the descriptor test fails
// until it is added.
var rules = map[string]rule{
	// AccessService: admin on the installation, but WhoAmI. Package access
	// writes each entry's object and details itself.
	magnetarv1connect.AccessServiceCreateKeyProcedure:   fixed(installationAdmin),
	magnetarv1connect.AccessServiceGetKeyProcedure:      fixed(installationAdmin),
	magnetarv1connect.AccessServiceListKeysProcedure:    fixed(installationAdmin),
	magnetarv1connect.AccessServiceRevokeKeyProcedure:   fixed(installationAdmin),
	magnetarv1connect.AccessServiceSetGrantProcedure:    fixed(installationAdmin),
	magnetarv1connect.AccessServiceListGrantsProcedure:  fixed(installationAdmin),
	magnetarv1connect.AccessServiceRemoveGrantProcedure: fixed(installationAdmin),
	// "Role: none; any caller the server authenticates."
	magnetarv1connect.AccessServiceWhoAmIProcedure: fixed(access.Authenticated()),

	// TenantService.
	magnetarv1connect.TenantServiceCreateTenantProcedure: fixed(installationAdmin),
	// "Role: reader on that tenant, or reader on the installation."
	magnetarv1connect.TenantServiceGetTenantProcedure: byMessage(func(r *v1.GetTenantRequest) (access.Requirement, access.Request) {
		if master.ValidateTenantName(r.GetName()) != nil {
			return access.OnInstallation(master.RoleReader), access.Request{}
		}
		return access.TenantRegistration(r.GetName()), access.Request{}
	}),
	// "Role: none beyond an authenticated caller."
	magnetarv1connect.TenantServiceListTenantsProcedure:  fixed(access.Authenticated()),
	magnetarv1connect.TenantServiceRemoveTenantProcedure: fixed(installationAdmin),

	// SettingsService: reader to read, admin to change, on the scope asked
	// for. The object is the setting's name, never its value.
	magnetarv1connect.SettingsServiceGetSettingProcedure: byMessage(func(r *v1.GetSettingRequest) (access.Requirement, access.Request) {
		return onScope(r.GetScope(), master.RoleReader), settingFacts(r.GetName(), r.GetScope())
	}),
	// "Role: admin on the scope set; for a setting set on a tenant by the
	// installation's admin only, admin on the installation, whatever the
	// scope."
	magnetarv1connect.SettingsServiceSetSettingProcedure: byMessage(func(r *v1.SetSettingRequest) (access.Requirement, access.Request) {
		s := r.GetSetting()
		return settingChange(s.GetName(), s.GetScope()), settingFacts(s.GetName(), s.GetScope())
	}),
	magnetarv1connect.SettingsServiceUnsetSettingProcedure: byMessage(func(r *v1.UnsetSettingRequest) (access.Requirement, access.Request) {
		return settingChange(r.GetName(), r.GetScope()), settingFacts(r.GetName(), r.GetScope())
	}),
	magnetarv1connect.SettingsServiceListSettingsProcedure: byMessage(func(r *v1.ListSettingsRequest) (access.Requirement, access.Request) {
		return onScope(r.GetScope(), master.RoleReader), access.Request{Details: map[string]string{detailScope: scopeDetail(r.GetScope())}}
	}),

	// AuditService: admin on the scope asked for.
	magnetarv1connect.AuditServiceListAuditEntriesProcedure: byMessage(func(r *v1.ListAuditEntriesRequest) (access.Requirement, access.Request) {
		return onScope(r.GetScope(), master.RoleAdmin), access.Request{Details: auditFilterDetails(r)}
	}),

	// MonitorService: reader on the tenant asked for, operator on the
	// installation for every tenant.
	magnetarv1connect.MonitorServiceGetStatusProcedure: byMessage(func(r *v1.GetStatusRequest) (access.Requirement, access.Request) {
		return onMonitorScope(r.GetScope()), access.Request{Details: map[string]string{detailScope: scopeDetail(r.GetScope())}}
	}),
	magnetarv1connect.MonitorServiceWatchProcedure: byMessage(func(r *v1.WatchRequest) (access.Requirement, access.Request) {
		return onMonitorScope(r.GetScope()), access.Request{Details: watchDetails(r)}
	}),

	// StorageService: admin on the tenant to change, reader to read. The
	// object is the storage's name.
	magnetarv1connect.StorageServiceAddStorageProcedure: byMessage(func(r *v1.AddStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetStorage().GetName(), Details: map[string]string{detailType: storageType(r.GetStorage())}}
	}),
	magnetarv1connect.StorageServiceGetStorageProcedure: byMessage(func(r *v1.GetStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: r.GetName()}
	}),
	magnetarv1connect.StorageServiceListStoragesProcedure: byMessage(func(r *v1.ListStoragesRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{}
	}),
	magnetarv1connect.StorageServiceSetStorageProcedure: byMessage(func(r *v1.SetStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetStorage().GetName(), Details: maskDetails(r.GetUpdateMask().GetPaths())}
	}),
	magnetarv1connect.StorageServiceRemoveStorageProcedure: byMessage(func(r *v1.RemoveStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetName()}
	}),
	magnetarv1connect.StorageServiceTestStorageProcedure: byMessage(func(r *v1.TestStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetName()}
	}),

	// ReceiverService: admin on the tenant to change, reader to read. The
	// object is the receiver's name.
	magnetarv1connect.ReceiverServiceAddReceiverProcedure: byMessage(func(r *v1.AddReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetReceiver().GetName(), Details: requestReceiverDetails(r.GetReceiver())}
	}),
	magnetarv1connect.ReceiverServiceGetReceiverProcedure: byMessage(func(r *v1.GetReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: r.GetName()}
	}),
	magnetarv1connect.ReceiverServiceListReceiversProcedure: byMessage(func(r *v1.ListReceiversRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{}
	}),
	magnetarv1connect.ReceiverServiceSetReceiverProcedure: byMessage(func(r *v1.SetReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetReceiver().GetName(), Details: maskDetails(r.GetUpdateMask().GetPaths())}
	}),
	magnetarv1connect.ReceiverServiceRemoveReceiverProcedure: byMessage(func(r *v1.RemoveReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetName()}
	}),

	// ReceptionService: reader to read, operator to settle a failed
	// reception, and operator to look a study up by its UID.
	magnetarv1connect.ReceptionServiceListReceptionsProcedure: byMessage(func(r *v1.ListReceptionsRequest) (access.Requirement, access.Request) {
		role := master.RoleReader
		if r.GetStudyInstanceUid() != "" {
			role = master.RoleOperator
		}
		return onTenantNamed(r.GetTenant(), role), access.Request{Details: receptionFilterDetails(r)}
	}),
	magnetarv1connect.ReceptionServiceGetReceptionProcedure: byMessage(func(r *v1.GetReceptionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: r.GetId()}
	}),
	magnetarv1connect.ReceptionServiceRetryReceptionProcedure: byMessage(func(r *v1.RetryReceptionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: r.GetId()}
	}),
	magnetarv1connect.ReceptionServiceDiscardReceptionProcedure: byMessage(func(r *v1.DiscardReceptionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: r.GetId()}
	}),

	// QuarantineService: reader to read the records; operator for a file's
	// header, a search by UID, and a release or a rejection.
	magnetarv1connect.QuarantineServiceListQuarantinedFilesProcedure: byMessage(func(r *v1.ListQuarantinedFilesRequest) (access.Requirement, access.Request) {
		role := master.RoleReader
		if r.GetStudyInstanceUid() != "" || r.GetSopInstanceUid() != "" {
			role = master.RoleOperator
		}
		return onTenantNamed(r.GetTenant(), role), access.Request{Details: quarantineFilterDetails(r)}
	}),
	magnetarv1connect.QuarantineServiceGetQuarantinedFileProcedure: byMessage(func(r *v1.GetQuarantinedFileRequest) (access.Requirement, access.Request) {
		role := master.RoleReader
		if r.GetIncludeHeader() {
			role = master.RoleOperator
		}
		return onTenantNamed(r.GetTenant(), role), access.Request{Object: r.GetId(), Details: headerDetails(r.GetIncludeHeader())}
	}),
	magnetarv1connect.QuarantineServiceReleaseQuarantinedFileProcedure: byMessage(func(r *v1.ReleaseQuarantinedFileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: r.GetId(), Details: releaseDetails(r, "")}
	}),
	magnetarv1connect.QuarantineServiceRejectQuarantinedFileProcedure: byMessage(func(r *v1.RejectQuarantinedFileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: r.GetId()}
	}),
}

// builtinSettings is the list of settings the binary ships, which the
// table reads a setting's kind from: the table has no other list, and the
// handlers check the kind again against theirs (settingsService).
var builtinSettings = settings.MustBuiltin()

// settingChange returns the requirement of a change of the setting named
// name on scope: admin on the scope, except for a setting a tenant's value
// of which the installation's admin alone sets, which needs admin on the
// installation whatever the scope (settings.proto, SetSetting).
func settingChange(name string, scope *v1.Scope) access.Requirement {
	if def, ok := builtinSettings.Lookup(name); ok && def.TenantSetByInstallation {
		return installationAdmin
	}
	return onScope(scope, master.RoleAdmin)
}

// settingFacts returns the audit facts of a request about one setting.
func settingFacts(name string, scope *v1.Scope) access.Request {
	return access.Request{Object: name, Details: map[string]string{detailScope: scopeDetail(scope)}}
}

// auditFilterDetails returns the filters of a listing of the audit log, as
// its entry's details record them (audit.proto, ListAuditEntries).
func auditFilterDetails(r *v1.ListAuditEntriesRequest) map[string]string {
	d := map[string]string{detailScope: scopeDetail(r.GetScope())}
	if r.GetKeyId() != "" {
		d[detailKeyID] = r.GetKeyId()
	}
	if o, ok := masterOutcome(r.GetOutcome()); ok && o != "" {
		d[detailOutcome] = string(o)
	}
	if r.GetStartTime() != nil {
		d[detailStart] = r.GetStartTime().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if r.GetEndTime() != nil {
		d[detailEnd] = r.GetEndTime().AsTime().UTC().Format(time.RFC3339Nano)
	}
	return d
}
