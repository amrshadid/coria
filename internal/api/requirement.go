package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/gen/coria/v1/coriav1connect"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/core/settings"
	"github.com/amrshadid/coria/internal/deid/profile"
	"github.com/amrshadid/coria/internal/master"
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
// "/coria.v1.TenantService/GetTenant", is a method of the table, and
// returns a new empty request message of its input type when its
// requirement depends on the request, or nil when the requirement is fixed,
// so the server need not read the body of a request it may refuse.
func (Methods) RequestFor(procedure string) (msg proto.Message, known bool) {
	return requestFor(rules, procedure)
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
	return requirementIn(rules, procedure, msg)
}

// requestFor is RequestFor over table.
func requestFor(table map[string]rule, procedure string) (proto.Message, bool) {
	r, ok := table[procedure]
	if !ok {
		return nil, false
	}
	if r.newMsg == nil {
		return nil, true
	}
	return r.newMsg(), true
}

// requirementIn is Requirement over table.
func requirementIn(table map[string]rule, procedure string, msg proto.Message) (access.Requirement, access.Request) {
	r, ok := table[procedure]
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
	coriav1connect.AccessServiceCreateKeyProcedure:   fixed(installationAdmin),
	coriav1connect.AccessServiceGetKeyProcedure:      fixed(installationAdmin),
	coriav1connect.AccessServiceListKeysProcedure:    fixed(installationAdmin),
	coriav1connect.AccessServiceRevokeKeyProcedure:   fixed(installationAdmin),
	coriav1connect.AccessServiceSetGrantProcedure:    fixed(installationAdmin),
	coriav1connect.AccessServiceListGrantsProcedure:  fixed(installationAdmin),
	coriav1connect.AccessServiceRemoveGrantProcedure: fixed(installationAdmin),
	coriav1connect.AccessServiceSetKeyRulesProcedure: fixed(installationAdmin),
	// RotateKey: any authenticated caller reaches the handler, and package
	// access lets through only the installation's admin or the key being
	// rotated, deciding before it reads or validates anything else.
	coriav1connect.AccessServiceRotateKeyProcedure: fixed(access.Authenticated()),
	// "Role: none; any caller the server authenticates."
	coriav1connect.AccessServiceWhoAmIProcedure: fixed(access.Authenticated()),

	// TenantService.
	coriav1connect.TenantServiceCreateTenantProcedure: fixed(installationAdmin),
	// "Role: reader on that tenant, or reader on the installation."
	coriav1connect.TenantServiceGetTenantProcedure: byMessage(func(r *v1.GetTenantRequest) (access.Requirement, access.Request) {
		if master.ValidateTenantName(r.GetName()) != nil {
			return access.OnInstallation(master.RoleReader), access.Request{}
		}
		return access.TenantRegistration(r.GetName()), access.Request{}
	}),
	// "Role: none beyond an authenticated caller."
	coriav1connect.TenantServiceListTenantsProcedure:  fixed(access.Authenticated()),
	coriav1connect.TenantServiceRemoveTenantProcedure: fixed(installationAdmin),

	// SettingsService: reader to read, admin to change, on the scope asked
	// for. The object is the setting's name, never its value.
	coriav1connect.SettingsServiceGetSettingProcedure: byMessage(func(r *v1.GetSettingRequest) (access.Requirement, access.Request) {
		return onScope(r.GetScope(), master.RoleReader), settingFacts(r.GetName(), r.GetScope())
	}),
	// "Role: admin on the scope set; for a setting set on a tenant by the
	// installation's admin only, admin on the installation, whatever the
	// scope."
	coriav1connect.SettingsServiceSetSettingProcedure: byMessage(func(r *v1.SetSettingRequest) (access.Requirement, access.Request) {
		s := r.GetSetting()
		return settingChange(s.GetName(), s.GetScope()), settingFacts(s.GetName(), s.GetScope())
	}),
	coriav1connect.SettingsServiceUnsetSettingProcedure: byMessage(func(r *v1.UnsetSettingRequest) (access.Requirement, access.Request) {
		return settingChange(r.GetName(), r.GetScope()), settingFacts(r.GetName(), r.GetScope())
	}),
	coriav1connect.SettingsServiceListSettingsProcedure: byMessage(func(r *v1.ListSettingsRequest) (access.Requirement, access.Request) {
		return onScope(r.GetScope(), master.RoleReader), access.Request{Details: map[string]string{detailScope: scopeDetail(r.GetScope())}}
	}),

	// AuditService: admin on the scope asked for.
	coriav1connect.AuditServiceListAuditEntriesProcedure: byMessage(func(r *v1.ListAuditEntriesRequest) (access.Requirement, access.Request) {
		return onScope(r.GetScope(), master.RoleAdmin), access.Request{Details: auditFilterDetails(r)}
	}),

	// MonitorService: reader on the tenant asked for, operator on the
	// installation for every tenant.
	coriav1connect.MonitorServiceGetStatusProcedure: byMessage(func(r *v1.GetStatusRequest) (access.Requirement, access.Request) {
		return onMonitorScope(r.GetScope()), access.Request{Details: map[string]string{detailScope: scopeDetail(r.GetScope())}}
	}),
	coriav1connect.MonitorServiceWatchProcedure: byMessage(func(r *v1.WatchRequest) (access.Requirement, access.Request) {
		return onMonitorScope(r.GetScope()), access.Request{Details: watchDetails(r)}
	}),

	// StorageService: admin on the tenant to change, reader to read. The
	// object is the storage's name.
	coriav1connect.StorageServiceAddStorageProcedure: byMessage(func(r *v1.AddStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetStorage().GetName(), Details: map[string]string{detailType: storageType(r.GetStorage())}}
	}),
	coriav1connect.StorageServiceGetStorageProcedure: byMessage(func(r *v1.GetStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: r.GetName()}
	}),
	coriav1connect.StorageServiceListStoragesProcedure: byMessage(func(r *v1.ListStoragesRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{}
	}),
	coriav1connect.StorageServiceSetStorageProcedure: byMessage(func(r *v1.SetStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetStorage().GetName(), Details: maskDetails(r.GetUpdateMask().GetPaths())}
	}),
	coriav1connect.StorageServiceRemoveStorageProcedure: byMessage(func(r *v1.RemoveStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetName()}
	}),
	coriav1connect.StorageServiceTestStorageProcedure: byMessage(func(r *v1.TestStorageRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetName()}
	}),

	// ReceiverService: admin on the tenant to change, reader to read. The
	// object is the receiver's name.
	coriav1connect.ReceiverServiceAddReceiverProcedure: byMessage(func(r *v1.AddReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetReceiver().GetName(), Details: requestReceiverDetails(r.GetReceiver())}
	}),
	coriav1connect.ReceiverServiceGetReceiverProcedure: byMessage(func(r *v1.GetReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: r.GetName()}
	}),
	coriav1connect.ReceiverServiceListReceiversProcedure: byMessage(func(r *v1.ListReceiversRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{}
	}),
	coriav1connect.ReceiverServiceSetReceiverProcedure: byMessage(func(r *v1.SetReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetReceiver().GetName(), Details: maskDetails(r.GetUpdateMask().GetPaths())}
	}),
	coriav1connect.ReceiverServiceRemoveReceiverProcedure: byMessage(func(r *v1.RemoveReceiverRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: r.GetName()}
	}),

	// ReceptionService: reader to read, operator to settle a failed
	// reception, and operator to look a study up by its UID.
	coriav1connect.ReceptionServiceListReceptionsProcedure: byMessage(func(r *v1.ListReceptionsRequest) (access.Requirement, access.Request) {
		role := master.RoleReader
		if r.GetStudyInstanceUid() != "" {
			role = master.RoleOperator
		}
		return onTenantNamed(r.GetTenant(), role), access.Request{Details: receptionFilterDetails(r)}
	}),
	coriav1connect.ReceptionServiceGetReceptionProcedure: byMessage(func(r *v1.GetReceptionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: r.GetId()}
	}),
	coriav1connect.ReceptionServiceRetryReceptionProcedure: byMessage(func(r *v1.RetryReceptionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: r.GetId()}
	}),
	coriav1connect.ReceptionServiceDiscardReceptionProcedure: byMessage(func(r *v1.DiscardReceptionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: r.GetId()}
	}),

	// QuarantineService: reader to read the records; operator for a file's
	// header, a search by UID, and a release or a rejection.
	coriav1connect.QuarantineServiceListQuarantinedFilesProcedure: byMessage(func(r *v1.ListQuarantinedFilesRequest) (access.Requirement, access.Request) {
		role := master.RoleReader
		if r.GetStudyInstanceUid() != "" || r.GetSopInstanceUid() != "" {
			role = master.RoleOperator
		}
		return onTenantNamed(r.GetTenant(), role), access.Request{Details: quarantineFilterDetails(r)}
	}),
	coriav1connect.QuarantineServiceGetQuarantinedFileProcedure: byMessage(func(r *v1.GetQuarantinedFileRequest) (access.Requirement, access.Request) {
		role := master.RoleReader
		if r.GetIncludeHeader() {
			role = master.RoleOperator
		}
		return onTenantNamed(r.GetTenant(), role), access.Request{Object: r.GetId(), Details: headerDetails(r.GetIncludeHeader())}
	}),
	coriav1connect.QuarantineServiceReleaseQuarantinedFileProcedure: byMessage(func(r *v1.ReleaseQuarantinedFileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), releaseRole(r)), access.Request{Object: r.GetId(), Details: releaseDetails(r, "")}
	}),
	coriav1connect.QuarantineServiceRejectQuarantinedFileProcedure: byMessage(func(r *v1.RejectQuarantinedFileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: r.GetId()}
	}),

	// MetadataService: reader on the tenant. The object is the scan's path
	// of labels, and an instance id is recorded only as present, since it
	// holds a SOP Instance UID as received in this version.
	coriav1connect.MetadataServiceGetScanMetadataProcedure: byMessage(func(r *v1.GetScanMetadataRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel(), r.GetSessionLabel(), r.GetScanLabel())}
	}),
	coriav1connect.MetadataServiceGetInstanceMetadataProcedure: byMessage(func(r *v1.GetInstanceMetadataRequest) (access.Requirement, access.Request) {
		d := map[string]string{}
		if r.GetInstanceId() != "" {
			d[detailInstance] = presentValue
		}
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel(), r.GetSessionLabel(), r.GetScanLabel()), Details: d}
	}),

	// CatalogService: reader on the tenant. The object is the path of
	// labels the request names, and the filters are in the details.
	coriav1connect.CatalogServiceListSubjectsProcedure: byMessage(func(r *v1.ListSubjectsRequest) (access.Requirement, access.Request) {
		d := catalogFilterDetails(r.GetLabelPrefix(), r.GetOrder())
		timeDetails(d, r.GetStartTime().AsTime(), r.GetStartTime() != nil, r.GetEndTime().AsTime(), r.GetEndTime() != nil)
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Details: d}
	}),
	coriav1connect.CatalogServiceGetSubjectProcedure: byMessage(func(r *v1.GetSubjectRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel())}
	}),
	coriav1connect.CatalogServiceListSessionsProcedure: byMessage(func(r *v1.ListSessionsRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel()), Details: sessionFilterDetails(r)}
	}),
	coriav1connect.CatalogServiceGetSessionProcedure: byMessage(func(r *v1.GetSessionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel(), r.GetSessionLabel())}
	}),
	coriav1connect.CatalogServiceListScansProcedure: byMessage(func(r *v1.ListScansRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel(), r.GetSessionLabel()), Details: scanFilterDetails(r)}
	}),
	coriav1connect.CatalogServiceGetScanProcedure: byMessage(func(r *v1.GetScanRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel(), r.GetSessionLabel(), r.GetScanLabel())}
	}),

	// DataService: reader on the tenant. The object is the path of labels
	// the request names; an instance id and a position are recorded only
	// as present.
	coriav1connect.DataServiceReadFilesProcedure: byMessage(func(r *v1.ReadFilesRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: labelPath(r.GetSubjectLabel(), r.GetSessionLabel(), r.GetScanLabel()), Details: readFilesDetails(r)}
	}),

	// SenderService: admin on the tenant to change or test a sender, reader
	// to read one; the object is the sender's name, recorded only when it
	// passes the rule of a sender's name. JobService: operator on the tenant
	// to make, cancel and retry a job, reader to read jobs and their
	// failures; the object is the job's id, recorded only when it has the
	// form of one.
	coriav1connect.SenderServiceAddSenderProcedure: byMessage(func(r *v1.AddSenderRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: senderObject(r.GetSender().GetName()), Details: map[string]string{detailType: senderType(r.GetSender())}}
	}),
	coriav1connect.SenderServiceGetSenderProcedure: byMessage(func(r *v1.GetSenderRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: senderObject(r.GetName())}
	}),
	coriav1connect.SenderServiceListSendersProcedure: byMessage(func(r *v1.ListSendersRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{}
	}),
	coriav1connect.SenderServiceSetSenderProcedure: byMessage(func(r *v1.SetSenderRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: senderObject(r.GetSender().GetName()), Details: maskDetails(r.GetUpdateMask().GetPaths())}
	}),
	coriav1connect.SenderServiceRemoveSenderProcedure: byMessage(func(r *v1.RemoveSenderRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: senderObject(r.GetName()), Details: removeSenderDetails(r)}
	}),
	coriav1connect.SenderServiceTestSenderProcedure: byMessage(func(r *v1.TestSenderRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: senderObject(r.GetName())}
	}),

	coriav1connect.JobServiceCreateJobProcedure: byMessage(func(r *v1.CreateJobRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Details: createJobDetails(r)}
	}),
	coriav1connect.JobServiceGetJobProcedure: byMessage(func(r *v1.GetJobRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: jobObject(r.GetId())}
	}),
	coriav1connect.JobServiceListJobsProcedure: byMessage(func(r *v1.ListJobsRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Details: jobFilterDetails(r)}
	}),
	coriav1connect.JobServiceCancelJobProcedure: byMessage(func(r *v1.CancelJobRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: jobObject(r.GetId())}
	}),
	coriav1connect.JobServiceRetryJobProcedure: byMessage(func(r *v1.RetryJobRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: jobObject(r.GetId())}
	}),
	coriav1connect.JobServiceListJobFailuresProcedure: byMessage(func(r *v1.ListJobFailuresRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleReader), access.Request{Object: jobObject(r.GetId())}
	}),

	// ProfileService: operator on the tenant to read profiles, to check a
	// document and to try, explain and compare versions (trying reads held
	// files, as reading a held header does); admin to make a version. The
	// list of bases is the server's and needs only an authenticated caller.
	// The object is the profile's name, recorded only when it passes the
	// name rule, since what a caller sends in its place can be anything.
	coriav1connect.ProfileServiceCreateProfileVersionProcedure: byMessage(func(r *v1.CreateProfileVersionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: profileObject(r.GetName())}
	}),
	coriav1connect.ProfileServiceGetProfileVersionProcedure: byMessage(func(r *v1.GetProfileVersionRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: profileObject(r.GetName()), Details: map[string]string{detailVersion: strconv.Itoa(int(r.GetVersion()))}}
	}),
	coriav1connect.ProfileServiceListProfilesProcedure: byMessage(func(r *v1.ListProfilesRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{}
	}),
	coriav1connect.ProfileServiceListProfileVersionsProcedure: byMessage(func(r *v1.ListProfileVersionsRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: profileObject(r.GetName())}
	}),
	coriav1connect.ProfileServiceValidateProfileProcedure: byMessage(func(r *v1.ValidateProfileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{}
	}),
	// "Role: none; any caller the server authenticates."
	coriav1connect.ProfileServiceListProfileBasesProcedure: fixed(access.Authenticated()),
	coriav1connect.ProfileServiceTryProfileProcedure: byMessage(func(r *v1.TryProfileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: trialObject(r.GetProfile())}
	}),
	coriav1connect.ProfileServiceExplainProfileProcedure: byMessage(func(r *v1.ExplainProfileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: trialObject(r.GetProfile())}
	}),
	coriav1connect.ProfileServiceDiffProfileVersionsProcedure: byMessage(func(r *v1.DiffProfileVersionsRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: profileObject(r.GetName()), Details: map[string]string{
			detailFromVersion: strconv.Itoa(int(r.GetFromVersion())), detailToVersion: strconv.Itoa(int(r.GetToVersion())), detailToName: profileOrEmpty(r.GetToName())}}
	}),

	// DeidService: admin on the tenant for the forward lookup and for an
	// import, each joining a value to its codes; operator to list the
	// tables' sizes. The object is the attribute's tag, or the table's
	// name, never a value.
	coriav1connect.DeidServiceCodeProcedure: byMessage(func(r *v1.CodeRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: attributeObject(r.GetAttribute())}
	}),
	coriav1connect.DeidServiceImportLookupCodesProcedure: byMessage(func(r *v1.ImportLookupCodesRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleAdmin), access.Request{Object: profileObject(r.GetTable())}
	}),
	coriav1connect.DeidServiceListLookupTablesProcedure: byMessage(func(r *v1.ListLookupTablesRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{}
	}),
	coriav1connect.ProfileServiceDescribeProfileProcedure: byMessage(func(r *v1.DescribeProfileRequest) (access.Requirement, access.Request) {
		return onTenantNamed(r.GetTenant(), master.RoleOperator), access.Request{Object: trialObject(r.GetProfile())}
	}),
}

// pendingRules is the table of the methods whose service the contract
// defines and no handler serves yet: the services api/not-served.txt
// names. Methods does not consult it, so the server answers each of these
// methods as one it does not serve, after the caller is authenticated, and
// a listed service can never be served by mistake with no handler behind
// it. Each line is written with its method's contract and checked against
// its Role line now; the change that serves a service moves its lines
// into rules and its name off the list, and the tests fail until both are
// done.
var pendingRules = map[string]rule{}

// The keys of the details of the profile methods' entries.
const (
	// detailVersion is the version a request names.
	detailVersion = "version"
	// detailFromVersion and detailToVersion are the versions a comparison
	// names; detailToName the other profile it names, when it names one.
	detailFromVersion = "from_version"
	detailToVersion   = "to_version"
	detailToName      = "to_name"
)

// profileOrEmpty is profileObject that leaves an empty name empty.
func profileOrEmpty(name string) string {
	if name == "" {
		return ""
	}
	return profileObject(name)
}

// trialObject returns the profile a trial run, an explanation or a check
// names, as an audit entry's object: "name@version" ("name@latest" for
// version 0) when the name passes the name rule, "document" for an unsaved
// document (whose checksum the handler adds), "invalid" otherwise.
func trialObject(p *v1.TrialProfile) string {
	switch k := p.GetKind().(type) {
	case *v1.TrialProfile_Version:
		name, version := k.Version.GetName(), k.Version.GetVersion()
		if !profile.ValidName(name) || version < 0 {
			return invalidValue
		}
		if version == 0 {
			return name + "@latest"
		}
		return name + "@" + strconv.Itoa(int(version))
	case *v1.TrialProfile_Document:
		return "document"
	}
	return invalidValue
}

// profileObject returns a profile's name as an audit entry's object: as
// given when it passes the name rule, and "invalid" otherwise.
func profileObject(name string) string {
	if profile.ValidName(name) {
		return name
	}
	return invalidValue
}

// The keys of the details the reading methods put in audit entries.
const (
	detailLabelPrefix       = "label_prefix"
	detailOrder             = "order"
	detailStudyDateStart    = "study_date_start"
	detailStudyDateEnd      = "study_date_end"
	detailModality          = "modality"
	detailDescriptionPrefix = "series_description_prefix"
	detailInstance          = "instance"
	detailStartAfter        = "start_after"
	detailAnnounceOnly      = "announce_only"
	detailRanged            = "range"
	detailWindow            = "window"
)

// invalidValue is what an audit entry records in place of a value the
// request gave that does not pass its rule: the entry still says the
// field was given, and nothing malformed, of any length or content,
// reaches the log.
const invalidValue = "invalid"

// labelPath returns the path of labels a request names, "<subject>",
// "<subject>/<session>" or "<subject>/<session>/<scan>", as an audit
// entry's object writes it: the labels given from the top, up to the first
// empty one, so a level named without the one above it (which the handler
// refuses as invalid) is not recorded as if it were placed. A label that
// does not pass the label rule is written "invalid": what a caller sent
// that is not a label can be anything, a patient's name included. Three
// valid labels fit an entry's field, which holds 256 bytes, only up to
// about 84 bytes each, so the path is clipped.
func labelPath(labels ...string) string {
	var parts []string
	for _, l := range labels {
		if l == "" {
			break
		}
		if label.Check(l) != nil {
			l = invalidValue
		}
		parts = append(parts, l)
	}
	return clip(strings.Join(parts, "/"), master.MaxAuditFieldLength-8)
}

// labelPrefix returns a label prefix as an audit entry records it: as
// given when it is a label or the start of one, and "invalid" otherwise. A
// start of a label may end where a whole label may not (in a "."), or be
// a name reserved on Windows that a longer label is not, so a prefix
// passes when it, or it with one more character that is valid anywhere
// in a label, passes the label rule.
func labelPrefix(p string) string {
	if label.Check(p) == nil || label.Check(p+"_") == nil {
		return p
	}
	return invalidValue
}

// studyDate returns a study date bound as an audit entry records it: as
// given when it has the form of a DICOM date, "YYYYMMDD", and "invalid"
// otherwise.
func studyDate(s string) string {
	if len(s) != 8 || strings.Trim(s, "0123456789") != "" {
		return invalidValue
	}
	return s
}

// modality returns a Modality filter as an audit entry records it: as
// given when it is a value of the CS value representation (PS3.5 6.2), 1
// to 16 of A-Z, 0-9, space and "_", and "invalid" otherwise.
func modality(s string) string {
	if s == "" || len(s) > 16 || strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 _") != "" {
		return invalidValue
	}
	return s
}

// receiverName returns a receiver filter as an audit entry records it: as
// given when it follows the rule of a receiver's name, and "invalid"
// otherwise.
func receiverName(s string) string {
	if master.ValidateTenantName(s) != nil {
		return invalidValue
	}
	return s
}

// catalogFilterDetails returns the details every catalog listing records:
// its label prefix and its order, when given.
func catalogFilterDetails(prefix string, order v1.CatalogOrder) map[string]string {
	d := map[string]string{}
	if prefix != "" {
		d[detailLabelPrefix] = labelPrefix(prefix)
	}
	if order != v1.CatalogOrder_CATALOG_ORDER_UNSPECIFIED {
		d[detailOrder] = clip(order.String(), 64)
	}
	return d
}

// sessionFilterDetails returns the filters of a listing of sessions, as
// its entry's details record them (catalog.proto, ListSessions).
func sessionFilterDetails(r *v1.ListSessionsRequest) map[string]string {
	d := catalogFilterDetails(r.GetLabelPrefix(), r.GetOrder())
	if r.GetStudyDateStart() != "" {
		d[detailStudyDateStart] = studyDate(r.GetStudyDateStart())
	}
	if r.GetStudyDateEnd() != "" {
		d[detailStudyDateEnd] = studyDate(r.GetStudyDateEnd())
	}
	if r.GetModality() != "" {
		d[detailModality] = modality(r.GetModality())
	}
	if r.GetReceiver() != "" {
		d[detailReceiver] = receiverName(r.GetReceiver())
	}
	timeDetails(d, r.GetStartTime().AsTime(), r.GetStartTime() != nil, r.GetEndTime().AsTime(), r.GetEndTime() != nil)
	return d
}

// scanFilterDetails returns the filters of a listing of scans, as its
// entry's details record them (catalog.proto, ListScans). A series
// description is recorded only as present: it is text the sender wrote,
// which can hold anything, a name included.
func scanFilterDetails(r *v1.ListScansRequest) map[string]string {
	d := catalogFilterDetails(r.GetLabelPrefix(), r.GetOrder())
	if r.GetModality() != "" {
		d[detailModality] = modality(r.GetModality())
	}
	if r.GetSeriesDescriptionPrefix() != "" {
		d[detailDescriptionPrefix] = presentValue
	}
	if r.GetReceiver() != "" {
		d[detailReceiver] = receiverName(r.GetReceiver())
	}
	timeDetails(d, r.GetStartTime().AsTime(), r.GetStartTime() != nil, r.GetEndTime().AsTime(), r.GetEndTime() != nil)
	return d
}

// readFilesDetails returns the details of the entry a stream of files
// opens with (data.proto, ReadFiles): whether an instance and a position
// to resume from were given, never which, since an instance id is an
// identifier as received in this version; and whether only the
// announcements were asked for.
func readFilesDetails(r *v1.ReadFilesRequest) map[string]string {
	d := map[string]string{detailAnnounceOnly: strconv.FormatBool(r.GetAnnounceOnly())}
	if r.GetInstanceId() != "" {
		d[detailInstance] = presentValue
	}
	if r.GetStartAfter() != nil {
		d[detailStartAfter] = presentValue
	}
	return d
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

// attributeObject returns the attribute a forward lookup names as an audit
// entry's object: its tag in eight digits when it names a standard
// element, "invalid" otherwise. The value is never an object.
func attributeObject(attr string) string {
	t, _, err := codeAttribute(attr)
	if err != nil {
		return invalidValue
	}
	return fmt.Sprintf("%08X", t)
}
