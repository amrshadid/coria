// Package api_test checks rules of the API contract that buf lint cannot
// express, by reading the descriptors compiled into the generated Go code.
package api_test

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// Imported for its side effect: it registers the coria.v1 files in
	// protoregistry.GlobalFiles.
	_ "github.com/amrshadid/coria/gen/coria/v1"
)

// The package whose messages are checked.
const contractPackage = "coria.v1"

// secretFields are the only fields through which a secret crosses the API: a
// secret never appears in an API answer, except a key's secret once, when it
// is made, so that a log or a client's history cannot leak one. Each must be
// marked debug_redact, which the server's redactor reads, and no other field
// may be.
var secretFields = []string{
	"coria.v1.CreateKeyResponse.secret",
	"coria.v1.CreateTenantRequest.database_address",
	"coria.v1.RotateKeyResponse.secret",
	"coria.v1.S3Storage.secret_access_key",
	"coria.v1.StowSender.credential",
	"coria.v1.TenantExportSummary.transfer_key",
}

// patientDataFields are the fields through which a file's values cross the
// API in a trial run of a profile: the caller's own data (a file it sent,
// or a held file it may read), answered only to it. Each must be marked
// debug_redact, so the server's redactor removes them before anything is
// logged, as it removes a secret.
var patientDataFields = []string{
	"coria.v1.TrialElement.after",
	"coria.v1.TrialElement.before",
	"coria.v1.TrialFile.content",
	"coria.v1.TrialFile.name",
	"coria.v1.CodeRequest.value",
	"coria.v1.LookupEntry.value",
	"coria.v1.TrialResult.name",
}

// textFields lists every field of the contract that carries free text: each
// string or bytes field, and each map with string or bytes keys or values.
// A secret can only travel in such a field. A field added to the contract
// that is not listed here fails the test, so adding one is a deliberate act:
// whoever lists it has decided it carries no secret, or that it is one of
// secretFields.
var textFields = []string{
	// scope.proto
	"coria.v1.Scope.tenant",

	// access.proto
	"coria.v1.Key.id",
	"coria.v1.Key.holder",
	"coria.v1.Grant.key_id",
	"coria.v1.LocalOperator.user_id",
	"coria.v1.CreateKeyRequest.holder",
	"coria.v1.CreateKeyResponse.secret",
	"coria.v1.GetKeyRequest.id",
	"coria.v1.ListKeysRequest.page_token",
	"coria.v1.ListKeysResponse.next_page_token",
	"coria.v1.RevokeKeyRequest.id",
	"coria.v1.SetGrantRequest.key_id",
	"coria.v1.ListGrantsRequest.key_id",
	"coria.v1.ListGrantsRequest.page_token",
	"coria.v1.ListGrantsResponse.next_page_token",
	"coria.v1.RemoveGrantRequest.key_id",
	"coria.v1.Key.networks",
	"coria.v1.Key.client_cert_sha256",
	"coria.v1.Key.last_use_address",
	"coria.v1.Key.successor_id",
	"coria.v1.CreateKeyRequest.networks",
	"coria.v1.CreateKeyRequest.client_cert_sha256",
	"coria.v1.RotateKeyRequest.id",
	"coria.v1.RotateKeyResponse.secret",
	"coria.v1.SetKeyRulesRequest.id",
	"coria.v1.SetKeyRulesRequest.networks",
	"coria.v1.SetKeyRulesRequest.client_cert_sha256",

	// audit.proto
	"coria.v1.AuditEntry.id",
	"coria.v1.AuditEntry.key_id",
	"coria.v1.AuditEntry.local_user_id",
	"coria.v1.AuditEntry.client_address",
	"coria.v1.AuditEntry.method",
	"coria.v1.AuditEntry.tenant",
	"coria.v1.AuditEntry.object",
	"coria.v1.AuditEntry.request_id",
	"coria.v1.AuditEntry.details",
	"coria.v1.AuditEntry.reason",
	"coria.v1.ListAuditEntriesRequest.key_id",
	"coria.v1.ListAuditEntriesRequest.page_token",
	"coria.v1.ListAuditEntriesResponse.next_page_token",

	// settings.proto
	"coria.v1.Setting.name",
	"coria.v1.Setting.value",
	"coria.v1.GetSettingRequest.name",
	"coria.v1.UnsetSettingRequest.name",
	"coria.v1.ListSettingsRequest.page_token",
	"coria.v1.ListSettingsResponse.next_page_token",

	// tenant.proto
	"coria.v1.Tenant.name",
	"coria.v1.Tenant.state_reason",
	"coria.v1.CreateTenantRequest.name",
	"coria.v1.CreateTenantRequest.database_address",
	"coria.v1.GetTenantRequest.name",
	"coria.v1.ListTenantsRequest.page_token",
	"coria.v1.ListTenantsResponse.next_page_token",
	"coria.v1.RemoveTenantRequest.name",
	"coria.v1.RemoveTenantRequest.confirm_name",
	"coria.v1.RemoveTenantResponse.data_location",

	// quarantine.proto
	"coria.v1.Caller.calling_ae_title",
	"coria.v1.Caller.called_ae_title",
	"coria.v1.Caller.address",
	"coria.v1.QuarantinedFile.id",
	"coria.v1.QuarantinedFile.details",
	"coria.v1.QuarantinedFile.receiver",
	"coria.v1.QuarantinedFile.reception",
	"coria.v1.QuarantinedFile.sha256",
	"coria.v1.QuarantinedFile.kept_sha256",
	"coria.v1.QuarantinedFile.sop_class_uid",
	"coria.v1.QuarantinedFile.sop_instance_uid",
	"coria.v1.QuarantinedFile.study_instance_uid",
	"coria.v1.QuarantinedFile.series_instance_uid",
	"coria.v1.QuarantinedFile.transfer_syntax_uid",
	"coria.v1.QuarantinedFile.release_reception",
	"coria.v1.QuarantinedFile.reject_reason",
	"coria.v1.QuarantinedFile.release_reason",
	"coria.v1.QuarantinedFile.released_from",
	"coria.v1.QuarantinedFile.release_receiver",
	"coria.v1.ListQuarantinedFilesRequest.tenant",
	"coria.v1.ListQuarantinedFilesRequest.receiver",
	"coria.v1.ListQuarantinedFilesRequest.reception",
	"coria.v1.ListQuarantinedFilesRequest.page_token",
	"coria.v1.ListQuarantinedFilesRequest.study_instance_uid",
	"coria.v1.ListQuarantinedFilesRequest.sop_instance_uid",
	"coria.v1.ListQuarantinedFilesResponse.next_page_token",
	"coria.v1.GetQuarantinedFileRequest.tenant",
	"coria.v1.GetQuarantinedFileRequest.id",
	"coria.v1.GetQuarantinedFileResponse.header_json",
	"coria.v1.ReleaseQuarantinedFileRequest.tenant",
	"coria.v1.ReleaseQuarantinedFileRequest.id",
	"coria.v1.ReleaseQuarantinedFileRequest.receiver",
	"coria.v1.RejectQuarantinedFileRequest.tenant",
	"coria.v1.RejectQuarantinedFileRequest.id",
	"coria.v1.RejectQuarantinedFileRequest.reason",
	"coria.v1.ReleaseQuarantinedFileRequest.reason",

	// reception.proto
	"coria.v1.Reception.id",
	"coria.v1.Reception.receiver",
	"coria.v1.Reception.study_instance_uid",
	"coria.v1.Reception.subject_label",
	"coria.v1.Reception.session_label",
	"coria.v1.Reception.storage",
	"coria.v1.Reception.failure",
	"coria.v1.Reception.calling_ae_titles",
	"coria.v1.Reception.retry_of",
	"coria.v1.Reception.retry_reception",
	"coria.v1.Reception.discard_reason",
	"coria.v1.ReceptionScan.label",
	"coria.v1.ReceptionScan.series_instance_uid",
	"coria.v1.ReceptionArrival.id",
	"coria.v1.ListReceptionsRequest.tenant",
	"coria.v1.ListReceptionsRequest.receiver",
	"coria.v1.ListReceptionsRequest.study_instance_uid",
	"coria.v1.ListReceptionsRequest.page_token",
	"coria.v1.ListReceptionsResponse.next_page_token",
	"coria.v1.GetReceptionRequest.tenant",
	"coria.v1.GetReceptionRequest.id",
	"coria.v1.RetryReceptionRequest.tenant",
	"coria.v1.RetryReceptionRequest.id",
	"coria.v1.RetryReceptionRequest.receiver",
	"coria.v1.DiscardReceptionRequest.tenant",
	"coria.v1.DiscardReceptionRequest.id",
	"coria.v1.DiscardReceptionRequest.reason",

	// receiver.proto
	"coria.v1.Receiver.name",
	"coria.v1.Receiver.storage",
	"coria.v1.DimseReceiver.bind_address",
	"coria.v1.DimseReceiver.ae_title",
	"coria.v1.DimseReceiver.sop_classes",
	"coria.v1.DimseReceiver.transfer_syntaxes",
	"coria.v1.StowReceiver.key_ids",
	"coria.v1.CallerRule.calling_ae_title",
	"coria.v1.CallerRule.address_range",
	"coria.v1.ReceiverTls.certificate_file",
	"coria.v1.ReceiverTls.private_key_file",
	"coria.v1.NamingRule.subject",
	"coria.v1.NamingRule.session",
	"coria.v1.NamingRule.scan",
	"coria.v1.NamingPattern.attribute",
	"coria.v1.NamingPattern.expression",
	"coria.v1.ReceiverHealth.detail",
	"coria.v1.AddReceiverRequest.tenant",
	"coria.v1.GetReceiverRequest.tenant",
	"coria.v1.GetReceiverRequest.name",
	"coria.v1.ListReceiversRequest.tenant",
	"coria.v1.ListReceiversRequest.page_token",
	"coria.v1.ListReceiversResponse.next_page_token",
	"coria.v1.SetReceiverRequest.tenant",
	"coria.v1.RemoveReceiverRequest.tenant",
	"coria.v1.RemoveReceiverRequest.name",

	// monitor.proto
	"coria.v1.StorageStatus.storage",
	"coria.v1.ReceiverStatus.receiver",
	"coria.v1.TenantStatus.tenant",
	"coria.v1.TenantStatus.event_cursor",
	"coria.v1.GetStatusRequest.page_token",
	"coria.v1.GetStatusResponse.next_page_token",
	"coria.v1.AssociationFailed.receiver",
	"coria.v1.ReceptionStepReached.reception",
	"coria.v1.ReceptionStepReached.receiver",
	"coria.v1.ReceptionStepReached.subject_label",
	"coria.v1.ReceptionStepReached.session_label",
	"coria.v1.ReceptionStepReached.retry_of",
	"coria.v1.FileQuarantined.file",
	"coria.v1.FileQuarantined.reception",
	"coria.v1.FileQuarantined.receiver",
	"coria.v1.QuarantineDecided.file",
	"coria.v1.QuarantineDecided.release_reception",
	"coria.v1.ReceiverHealthChanged.receiver",
	"coria.v1.StorageSpaceChanged.storage",
	"coria.v1.FramesAdded.subject_label",
	"coria.v1.FramesAdded.session_label",
	"coria.v1.FramesAdded.scan_label",
	"coria.v1.Event.cursor",
	"coria.v1.Event.tenant",
	"coria.v1.WatchKeepAlive.cursor",
	"coria.v1.WatchKeepAlive.tenant_cursors",
	"coria.v1.WatchKeepAlive.unanswered_tenants",
	"coria.v1.WatchRequest.cursor",
	"coria.v1.WatchRequest.tenant_cursors",

	// storage.proto
	"coria.v1.Storage.name",
	"coria.v1.DirectoryStorage.root",
	"coria.v1.DirectoryStorage.path",
	"coria.v1.S3Storage.endpoint",
	"coria.v1.S3Storage.region",
	"coria.v1.S3Storage.bucket",
	"coria.v1.S3Storage.prefix",
	"coria.v1.S3Storage.access_key_id",
	"coria.v1.S3Storage.secret_access_key",
	"coria.v1.AddStorageRequest.tenant",
	"coria.v1.GetStorageRequest.tenant",
	"coria.v1.GetStorageRequest.name",
	"coria.v1.ListStoragesRequest.tenant",
	"coria.v1.ListStoragesRequest.page_token",
	"coria.v1.ListStoragesResponse.next_page_token",
	"coria.v1.SetStorageRequest.tenant",
	"coria.v1.RemoveStorageRequest.tenant",
	"coria.v1.RemoveStorageRequest.name",
	"coria.v1.TestStorageRequest.tenant",
	"coria.v1.TestStorageRequest.name",

	// catalog.proto
	"coria.v1.Subject.label",
	"coria.v1.Session.subject_label",
	"coria.v1.Session.label",
	"coria.v1.Session.study_instance_uid",
	"coria.v1.Session.study_date",
	"coria.v1.Session.modalities",
	"coria.v1.Session.storage",
	"coria.v1.Session.receiver",
	"coria.v1.Scan.subject_label",
	"coria.v1.Scan.session_label",
	"coria.v1.Scan.label",
	"coria.v1.Scan.series_instance_uid",
	"coria.v1.Scan.modality",
	"coria.v1.Scan.series_description",
	"coria.v1.Scan.storage",
	"coria.v1.Scan.manifest_sha256",
	"coria.v1.Scan.receiver",
	"coria.v1.ListSubjectsRequest.tenant",
	"coria.v1.ListSubjectsRequest.label_prefix",
	"coria.v1.ListSubjectsRequest.page_token",
	"coria.v1.ListSubjectsResponse.next_page_token",
	"coria.v1.GetSubjectRequest.tenant",
	"coria.v1.GetSubjectRequest.subject_label",
	"coria.v1.ListSessionsRequest.tenant",
	"coria.v1.ListSessionsRequest.subject_label",
	"coria.v1.ListSessionsRequest.label_prefix",
	"coria.v1.ListSessionsRequest.study_date_start",
	"coria.v1.ListSessionsRequest.study_date_end",
	"coria.v1.ListSessionsRequest.modality",
	"coria.v1.ListSessionsRequest.receiver",
	"coria.v1.ListSessionsRequest.page_token",
	"coria.v1.ListSessionsResponse.next_page_token",
	"coria.v1.GetSessionRequest.tenant",
	"coria.v1.GetSessionRequest.subject_label",
	"coria.v1.GetSessionRequest.session_label",
	"coria.v1.ListScansRequest.tenant",
	"coria.v1.ListScansRequest.subject_label",
	"coria.v1.ListScansRequest.session_label",
	"coria.v1.ListScansRequest.label_prefix",
	"coria.v1.ListScansRequest.modality",
	"coria.v1.ListScansRequest.series_description_prefix",
	"coria.v1.ListScansRequest.receiver",
	"coria.v1.ListScansRequest.page_token",
	"coria.v1.ListScansResponse.next_page_token",
	"coria.v1.GetScanRequest.tenant",
	"coria.v1.GetScanRequest.subject_label",
	"coria.v1.GetScanRequest.session_label",
	"coria.v1.GetScanRequest.scan_label",

	// metadata.proto
	"coria.v1.InstanceMetadata.instance_id",
	"coria.v1.InstanceMetadata.sop_instance_uid",
	"coria.v1.InstanceMetadata.dicom_json",
	"coria.v1.InstanceMetadata.transfer_syntax_uid",
	"coria.v1.GetScanMetadataRequest.tenant",
	"coria.v1.GetScanMetadataRequest.subject_label",
	"coria.v1.GetScanMetadataRequest.session_label",
	"coria.v1.GetScanMetadataRequest.scan_label",
	"coria.v1.GetScanMetadataRequest.page_token",
	"coria.v1.GetScanMetadataResponse.next_page_token",
	"coria.v1.GetScanMetadataResponse.manifest_sha256",
	"coria.v1.GetInstanceMetadataRequest.tenant",
	"coria.v1.GetInstanceMetadataRequest.subject_label",
	"coria.v1.GetInstanceMetadataRequest.session_label",
	"coria.v1.GetInstanceMetadataRequest.scan_label",
	"coria.v1.GetInstanceMetadataRequest.instance_id",

	// data.proto
	"coria.v1.ScanMarker.subject_label",
	"coria.v1.ScanMarker.session_label",
	"coria.v1.ScanMarker.scan_label",
	"coria.v1.ScanMarker.manifest_sha256",
	"coria.v1.ArchivedFile.instance_id",
	"coria.v1.ReadPosition.session_label",
	"coria.v1.ReadPosition.scan_label",
	"coria.v1.ReadPosition.instance_id",
	"coria.v1.ReadFilesRequest.instance_id",
	"coria.v1.ArchivedFile.key",
	"coria.v1.ArchivedFile.sha256",
	"coria.v1.ArchivedFile.sop_instance_uid",
	"coria.v1.ArchivedFile.subject_label",
	"coria.v1.ArchivedFile.session_label",
	"coria.v1.ArchivedFile.scan_label",
	"coria.v1.FileChunk.data",
	"coria.v1.ReadFilesRequest.tenant",
	"coria.v1.ReadFilesRequest.subject_label",
	"coria.v1.ReadFilesRequest.session_label",
	"coria.v1.ReadFilesRequest.scan_label",

	// profile.proto: a profile is configuration a person writes; no field
	// carries a secret or a value read from a file.
	"coria.v1.ProfileVersionRef.name",
	"coria.v1.ProfileDocument.bases",
	"coria.v1.ProfileDocument.description",
	"coria.v1.ProfileRule.name",
	"coria.v1.ProfileRule.reason",
	"coria.v1.ProfileSelector.tag",
	"coria.v1.ProfileSelector.keyword",
	"coria.v1.ProfileSelector.group",
	"coria.v1.ProfileSelector.vr",
	"coria.v1.ProfileSelector.path",
	"coria.v1.CodeRequest.value",
	"coria.v1.LookupEntry.value",
	"coria.v1.CodeRequest.tenant",
	"coria.v1.CodeRequest.attribute",
	"coria.v1.CodeResponse.attribute",
	"coria.v1.CodeResponse.code",
	"coria.v1.SubjectLabelCode.receiver",
	"coria.v1.SubjectLabelCode.subject_label",
	"coria.v1.LookupTableCode.table",
	"coria.v1.LookupTableCode.code",
	"coria.v1.ImportLookupCodesRequest.tenant",
	"coria.v1.ImportLookupCodesRequest.table",
	"coria.v1.LookupEntry.code",
	"coria.v1.ImportLookupCodesResponse.table",
	"coria.v1.ListLookupTablesRequest.tenant",
	"coria.v1.ListLookupTablesRequest.page_token",
	"coria.v1.ListLookupTablesResponse.next_page_token",
	"coria.v1.LookupTable.name",
	"coria.v1.DescribeProfileRequest.tenant",
	"coria.v1.DescribeProfileResponse.checksum",
	"coria.v1.DescribeProfileResponse.notes",
	"coria.v1.ProfileClassEffect.action",
	"coria.v1.ProfileClassEffect.elements",
	"coria.v1.ProfileMethod.withheld",
	"coria.v1.ProfileMethod.method_values",
	"coria.v1.ProfileMethod.identity_removed",
	"coria.v1.ProfileMethod.temporal_modified",
	"coria.v1.MethodCode.value",
	"coria.v1.MethodCode.scheme",
	"coria.v1.MethodCode.meaning",
	"coria.v1.DeidJob.profile",
	"coria.v1.DeidJob.subject_label",
	"coria.v1.DeidJob.session_label",
	"coria.v1.DeidJob.scan_label",
	"coria.v1.JobTally.key",
	"coria.v1.ValuePatternSelector.pattern",
	"coria.v1.ValuePatternSelector.vr",
	"coria.v1.RedactAction.pattern",
	"coria.v1.RedactAction.replacement",
	"coria.v1.PrivateElement.creator",
	"coria.v1.PrivateElement.group",
	"coria.v1.PrivateElement.element",
	"coria.v1.ContentItemSelector.scheme",
	"coria.v1.ContentItemSelector.value",
	"coria.v1.ContentItemSelector.value_type",
	"coria.v1.ProfileCondition.equals",
	"coria.v1.ProfileCondition.prefix",
	"coria.v1.ProfileCondition.pattern",
	"coria.v1.ReplaceAction.constant",
	"coria.v1.ReplaceAction.template",
	"coria.v1.SetAction.constant",
	"coria.v1.SetAction.template",
	"coria.v1.LookupAction.table",
	"coria.v1.PixelRule.name",
	"coria.v1.PixelRule.reason",
	"coria.v1.ExpandedProfile.bases",
	"coria.v1.ExpandedRule.origin",
	"coria.v1.ExpandedRule.vrs",
	"coria.v1.ExpandedPixelRule.origin",
	"coria.v1.ProfileVersion.name",
	"coria.v1.ProfileVersion.checksum",
	"coria.v1.ProfileVersion.created_by_key_id",
	"coria.v1.ProfileVersion.note",
	"coria.v1.ProfileSummary.name",
	"coria.v1.ProfileProblem.rule_name",
	"coria.v1.ProfileProblem.field",
	"coria.v1.ProfileProblem.code",
	"coria.v1.ProfileProblem.message",
	"coria.v1.ProfileBase.name",
	"coria.v1.ProfileBase.unavailable_reason",
	"coria.v1.ProfileBase.description",
	"coria.v1.CreateProfileVersionRequest.tenant",
	"coria.v1.CreateProfileVersionRequest.name",
	"coria.v1.CreateProfileVersionRequest.note",
	"coria.v1.GetProfileVersionRequest.tenant",
	"coria.v1.GetProfileVersionRequest.name",
	"coria.v1.ListProfilesRequest.tenant",
	"coria.v1.ListProfilesRequest.page_token",
	"coria.v1.ListProfilesResponse.next_page_token",
	"coria.v1.ListProfileVersionsRequest.tenant",
	"coria.v1.ListProfileVersionsRequest.name",
	"coria.v1.ListProfileVersionsRequest.page_token",
	"coria.v1.ListProfileVersionsResponse.next_page_token",
	"coria.v1.ValidateProfileRequest.tenant",
	"coria.v1.ValidateProfileResponse.checksum",

	// profile.proto, trial runs and explanations: a file a caller sends,
	// its name, and the values before and after are the caller's own data,
	// listed in patientDataFields too; the rest is a profile's words and
	// the server's.
	"coria.v1.ProfileVersionName.name",
	"coria.v1.TrialFile.name",
	"coria.v1.TrialFile.content",
	"coria.v1.TrialReception.receiver",
	"coria.v1.TrialReception.calling_ae_title",
	"coria.v1.TrialReception.called_ae_title",
	"coria.v1.TryProfileRequest.tenant",
	"coria.v1.TryProfileRequest.quarantine_records",
	"coria.v1.TryProfileResponse.checksum",
	"coria.v1.TryProfileResponse.reception_refusal",
	"coria.v1.TrialResult.name",
	"coria.v1.TrialResult.quarantine_record",
	"coria.v1.TrialResult.reason",
	"coria.v1.TrialResult.cause",
	"coria.v1.TrialResult.path",
	"coria.v1.TrialResult.rule",
	"coria.v1.TrialResult.receiver",
	"coria.v1.TrialElement.path",
	"coria.v1.TrialElement.keyword",
	"coria.v1.TrialElement.vr",
	"coria.v1.TrialElement.action",
	"coria.v1.TrialElement.rule_name",
	"coria.v1.TrialElement.origin",
	"coria.v1.TrialElement.reason",
	"coria.v1.TrialElement.before",
	"coria.v1.TrialElement.after",
	// A file's pixel work: fixed words (risk cause, action, JPEG marker
	// names) and a profile's rule and base names, never a file's value.
	"coria.v1.TrialPixels.risk_cause",
	"coria.v1.TrialPixels.rule",
	"coria.v1.TrialPixels.origin",
	"coria.v1.TrialPixels.action",
	"coria.v1.TrialPixels.segments_removed",
	"coria.v1.TrialCount.key",
	"coria.v1.ExplainProfileRequest.tenant",
	"coria.v1.ExplainProfileRequest.quarantine_record",
	"coria.v1.ExplainProfileResponse.checksum",
	"coria.v1.ExplainProfileResponse.refusal",
	"coria.v1.ExplainProfileResponse.refusal_path",
	"coria.v1.ExplainedElement.path",
	"coria.v1.ExplainedElement.keyword",
	"coria.v1.ExplainedElement.vr",
	"coria.v1.ExplainedElement.action",
	"coria.v1.ExplainedElement.rule_name",
	"coria.v1.ExplainedElement.origin",
	"coria.v1.ExplainedElement.reason",
	"coria.v1.ExplainedElement.selected_by",
	"coria.v1.ExplainedCandidate.rule_name",
	"coria.v1.ExplainedCandidate.origin",
	"coria.v1.ExplainedCandidate.selected_by",
	"coria.v1.ExplainedCandidate.why_not",
	"coria.v1.DiffProfileVersionsRequest.tenant",
	"coria.v1.DiffProfileVersionsRequest.name",
	"coria.v1.DiffProfileVersionsRequest.to_name",
	"coria.v1.DiffProfileVersionsResponse.bases_added",
	"coria.v1.DiffProfileVersionsResponse.bases_removed",
	"coria.v1.ProfileRuleChange.origin",

	// sender.proto: a sender's configuration is what a person writes; the
	// credential is the one secret, listed in secretFields too.
	"coria.v1.Sender.name",
	"coria.v1.DimseSender.host",
	"coria.v1.DimseSender.called_ae_title",
	"coria.v1.DimseSender.calling_ae_title",
	"coria.v1.StowSender.url",
	"coria.v1.StowSender.username",
	"coria.v1.StowSender.header_name",
	"coria.v1.StowSender.credential",
	"coria.v1.SenderTls.ca_file",
	"coria.v1.SenderTls.certificate_file",
	"coria.v1.SenderTls.private_key_file",
	"coria.v1.SenderTls.server_name",
	"coria.v1.SenderHealth.detail",
	"coria.v1.SenderTestResult.detail",
	"coria.v1.AddSenderRequest.tenant",
	"coria.v1.GetSenderRequest.tenant",
	"coria.v1.GetSenderRequest.name",
	"coria.v1.ListSendersRequest.tenant",
	"coria.v1.ListSendersRequest.page_token",
	"coria.v1.ListSendersResponse.next_page_token",
	"coria.v1.SetSenderRequest.tenant",
	"coria.v1.RemoveSenderRequest.tenant",
	"coria.v1.RemoveSenderRequest.name",
	"coria.v1.TestSenderRequest.tenant",
	"coria.v1.TestSenderRequest.name",

	// job.proto: ids, labels, fixed phrases and the UIDs of a SOP class and
	// a transfer syntax, which name kinds of object, not a person.
	"coria.v1.SendJob.sender",
	"coria.v1.SendJob.subject_label",
	"coria.v1.SendJob.session_label",
	"coria.v1.SendJob.scan_label",
	"coria.v1.SendJob.reception",
	"coria.v1.SendJob.receiver",
	"coria.v1.Job.id",
	"coria.v1.Job.reason",
	"coria.v1.Job.created_by_key_id",
	"coria.v1.Job.retry_of",
	"coria.v1.Job.retry_job",
	"coria.v1.JobFailure.instance_id",
	"coria.v1.JobFailure.subject_label",
	"coria.v1.JobFailure.session_label",
	"coria.v1.JobFailure.scan_label",
	"coria.v1.JobFailure.sop_class_uid",
	"coria.v1.JobFailure.transfer_syntax_uid",
	"coria.v1.JobFailure.detail",
	"coria.v1.CreateJobRequest.tenant",
	"coria.v1.GetJobRequest.tenant",
	"coria.v1.GetJobRequest.id",
	"coria.v1.ListJobsRequest.tenant",
	"coria.v1.ListJobsRequest.sender",
	"coria.v1.ListJobsRequest.page_token",
	"coria.v1.ListJobsResponse.next_page_token",
	"coria.v1.CancelJobRequest.tenant",
	"coria.v1.CancelJobRequest.id",
	"coria.v1.RetryJobRequest.tenant",
	"coria.v1.RetryJobRequest.id",
	"coria.v1.ListJobFailuresRequest.tenant",
	"coria.v1.ListJobFailuresRequest.id",
	"coria.v1.ListJobFailuresRequest.page_token",
	"coria.v1.ListJobFailuresResponse.next_page_token",

	// receiver.proto: the senders a receiver forwards to, by name.
	"coria.v1.Receiver.forward_to",

	// receiver.proto and quarantine.proto: a receiver's routes name a
	// route, a storage and senders; a release names a storage. The
	// conditions' texts are ProfileCondition's, listed with profile.proto.
	"coria.v1.Route.name",
	"coria.v1.Route.storage",
	"coria.v1.RouteForwardTo.senders",
	"coria.v1.ReleaseQuarantinedFileRequest.storage",
	"coria.v1.QuarantinedFile.release_storage",
	"coria.v1.TrialResult.route",

	// monitor.proto: the events and status of jobs and senders carry ids,
	// names and fixed phrases.
	"coria.v1.SenderStatus.sender",
	"coria.v1.JobStepReached.job",
	"coria.v1.JobStepReached.sender",
	"coria.v1.JobStepReached.receiver",
	"coria.v1.JobStepReached.reception",
	"coria.v1.JobStepReached.retry_of",
	"coria.v1.JobStepReached.reason",
	"coria.v1.JobProgressed.job",
	"coria.v1.JobProgressed.sender",
	"coria.v1.SenderHealthChanged.sender",

	// job.proto, scrub and rebuild jobs: a storage's name and a schema's
	// name, written by a person.
	"coria.v1.ScrubJob.storage",
	"coria.v1.RebuildJob.scratch_schema",

	// correction.proto: labels, ids, keys, lowercase hexadecimal checksums,
	// names, fixed phrases and a person's reason, which the contract says
	// never holds a value read from a file. An instance id and a key hold
	// a SOP Instance UID as received, as they do in data.proto.
	"coria.v1.CorrectionTarget.subject_label",
	"coria.v1.CorrectionTarget.session_label",
	"coria.v1.CorrectionTarget.scan_label",
	"coria.v1.CorrectionTarget.instance_id",
	"coria.v1.PeerCopy.sender",
	"coria.v1.DeletePreview.storages",
	"coria.v1.DeletePreview.jobs",
	"coria.v1.DeletePreview.confirm",
	"coria.v1.Correction.id",
	"coria.v1.Correction.to_label",
	"coria.v1.Correction.quarantined_file",
	"coria.v1.Correction.reason",
	"coria.v1.Correction.created_by_key_id",
	"coria.v1.Correction.state_reason",
	"coria.v1.Correction.old_sha256",
	"coria.v1.Correction.new_sha256",
	"coria.v1.Correction.undo_reason",
	"coria.v1.DeleteRequest.tenant",
	"coria.v1.DeleteRequest.reason",
	"coria.v1.DeleteRequest.confirm",
	"coria.v1.RelabelRequest.tenant",
	"coria.v1.RelabelRequest.to_label",
	"coria.v1.RelabelRequest.reason",
	"coria.v1.ReplaceRequest.tenant",
	"coria.v1.ReplaceRequest.quarantined_file",
	"coria.v1.ReplaceRequest.reason",
	"coria.v1.ListCorrectionsRequest.tenant",
	"coria.v1.ListCorrectionsRequest.page_token",
	"coria.v1.ListCorrectionsResponse.next_page_token",
	"coria.v1.GetCorrectionRequest.tenant",
	"coria.v1.GetCorrectionRequest.id",
	"coria.v1.UndoCorrectionRequest.tenant",
	"coria.v1.UndoCorrectionRequest.id",
	"coria.v1.UndoCorrectionRequest.reason",
	"coria.v1.PurgeCorrectionRequest.tenant",
	"coria.v1.PurgeCorrectionRequest.id",
	"coria.v1.AllowArrivalsRequest.tenant",
	"coria.v1.AllowArrivalsRequest.id",
	"coria.v1.AllowArrivalsRequest.reason",
	"coria.v1.ScrubFinding.id",
	"coria.v1.ScrubFinding.storage",
	"coria.v1.ScrubFinding.key",
	"coria.v1.ScrubFinding.subject_label",
	"coria.v1.ScrubFinding.session_label",
	"coria.v1.ScrubFinding.scan_label",
	"coria.v1.ScrubFinding.instance_id",
	"coria.v1.ScrubFinding.expected_sha256",
	"coria.v1.ScrubFinding.found_sha256",
	"coria.v1.ScrubFinding.job",
	"coria.v1.ScrubFinding.clear_job",
	"coria.v1.ListScrubFindingsRequest.tenant",
	"coria.v1.ListScrubFindingsRequest.storage",
	"coria.v1.ListScrubFindingsRequest.page_token",
	"coria.v1.ListScrubFindingsResponse.next_page_token",
	"coria.v1.StorageDisposition.storage",
	"coria.v1.StoragePurge.storage",
	"coria.v1.StoragePurge.why",
	"coria.v1.InstanceRef.instance_id",
	"coria.v1.ResolvedInstance.sha256",
	"coria.v1.ResolvedInstance.correction",
	"coria.v1.ResolvedInstance.subject_label",
	"coria.v1.ResolvedInstance.session_label",
	"coria.v1.ResolvedInstance.scan_label",
	"coria.v1.ResolveInstancesRequest.tenant",

	// audit_seal.proto: digests, signatures, public keys and the encoded
	// form of entries that AuditService already answers; nothing here is a
	// secret, since a public key checks a signature and cannot make one.
	"coria.v1.AuditCheckpoint.tenant",
	"coria.v1.AuditCheckpoint.head",
	"coria.v1.AuditCheckpoint.previous_digest",
	"coria.v1.AuditCheckpoint.digest",
	"coria.v1.AuditCheckpoint.signature",
	"coria.v1.AuditCheckpoint.signed_text",
	"coria.v1.AuditPublicKey.public_key",
	"coria.v1.AuditPublicKey.public_key_pem",
	"coria.v1.AuditVerifyProblem.detail",
	"coria.v1.ListAuditCheckpointsRequest.page_token",
	"coria.v1.ListAuditCheckpointsResponse.next_page_token",
	"coria.v1.AuditExportHeader.installation_id",
	"coria.v1.AuditExportHeader.tenant",
	"coria.v1.AuditExportHeader.chain_before",
	"coria.v1.SealedAuditEntry.encoded",
	"coria.v1.SealedAuditEntry.chain",
	"coria.v1.GetAuditPublicKeyResponse.installation_id",
	"coria.v1.AuditSealStatus.last_digest",
	"coria.v1.VerifyAuditRequest.expected_checkpoint_digest",
	"coria.v1.AuditCheckpoint.installation_id",
	"coria.v1.AuditCheckpoint.log",
	"coria.v1.AuditPublicKey.installation_id",
	"coria.v1.AuditAnchor.log",
	"coria.v1.AuditAnchor.digest",
	"coria.v1.AuditExportHeader.log",

	// backup.proto: the encrypted bytes of a backup or an export (useless
	// without the installation key file or the transfer key), names,
	// formats and checksums. The transfer key is the one secret, listed in
	// secretFields too.
	"coria.v1.BackupSection.tenant",
	"coria.v1.BackupSection.sha256",
	"coria.v1.BackupSummary.format",
	"coria.v1.BackupSummary.installation_id",
	"coria.v1.BackupSummary.sha256",
	"coria.v1.CreateBackupResponse.data",
	"coria.v1.ExportTenantRequest.tenant",
	"coria.v1.TenantExportSummary.transfer_key",
	"coria.v1.ExportTenantResponse.data",
	"coria.v1.StorageOwner.storage",
	"coria.v1.BackupSection.event_history",
	"coria.v1.BackupSection.last_correction",
	"coria.v1.BackupSection.last_reception",
	"coria.v1.ReclaimTenantRequest.name",
	"coria.v1.ReinstateKeysRequest.key_ids",

	// monitor.proto: a tenant's scrub and seal status and the events of
	// scrubs and corrections carry ids, names, labels and digests.
	"coria.v1.ScrubSummary.job",
	"coria.v1.ScrubFindingChanged.finding",
	"coria.v1.ScrubFindingChanged.storage",
	"coria.v1.ScrubFindingChanged.subject_label",
	"coria.v1.ScrubFindingChanged.session_label",
	"coria.v1.ScrubFindingChanged.scan_label",
	"coria.v1.ScrubFindingChanged.job",
	"coria.v1.CatalogChanged.correction",
	"coria.v1.CatalogChanged.subject_label",
	"coria.v1.CatalogChanged.session_label",
	"coria.v1.CatalogChanged.scan_label",
	"coria.v1.CatalogChanged.to_label",
	"coria.v1.CatalogRebuilt.job",
	"coria.v1.TenantStatus.event_history",
	"coria.v1.WatchKeepAlive.event_history",
	"coria.v1.WatchKeepAlive.tenant_event_histories",
}

// TestOnlyTheNamedFieldsCarrySecrets guards where secrets may cross the
// contract: exactly the named fields (secrets, and a trial run's values) are
// marked debug_redact, and every
// field that can carry text is on the reviewed list. A text field added
// without being listed, a secret field that loses its mark, or another field
// that gains one fails here rather than in a review.
func TestOnlyTheNamedFieldsCarrySecrets(t *testing.T) {
	fields := contractFields(t)

	// A walk that found nothing would pass the checks below for no reason.
	if len(fields) < 50 {
		t.Fatalf("found %d fields in %s, want at least 50: is the generated code registered?", len(fields), contractPackage)
	}

	var redacted, text []string
	for _, field := range fields {
		name := string(field.FullName())
		opts, _ := field.Options().(*descriptorpb.FieldOptions)
		if opts.GetDebugRedact() {
			redacted = append(redacted, name)
		}
		if carriesText(field) {
			text = append(text, name)
			if !slices.Contains(textFields, name) {
				t.Errorf("%s carries text and is not in textFields: list it once it is known to carry no secret", name)
			}
		}
	}

	slices.Sort(redacted)
	marked := slices.Concat(secretFields, patientDataFields)
	slices.Sort(marked)
	if !slices.Equal(redacted, marked) {
		t.Errorf("fields marked debug_redact = %q, want exactly %q", redacted, marked)
	}

	// An entry for a field that no longer exists or no longer carries text
	// would let the list drift from the contract it reviews.
	for _, name := range textFields {
		if !slices.Contains(text, name) {
			t.Errorf("textFields lists %s, which is not a text field of the contract", name)
		}
	}
}

// carriesText reports whether a field can hold free text: a string or bytes
// field, repeated or not, or a map whose keys or values are strings or bytes.
func carriesText(field protoreflect.FieldDescriptor) bool {
	if field.IsMap() {
		return isText(field.MapKey()) || isText(field.MapValue())
	}
	return isText(field)
}

// isText reports whether a field's values are strings or bytes.
func isText(field protoreflect.FieldDescriptor) bool {
	return field.Kind() == protoreflect.StringKind || field.Kind() == protoreflect.BytesKind
}

// contractFields answers every field of every message in the files of
// contractPackage, nested messages included. The entries of a map are
// reached through the map field itself, so their synthetic messages are
// skipped.
func contractFields(t *testing.T) []protoreflect.FieldDescriptor {
	t.Helper()
	var fields []protoreflect.FieldDescriptor
	var walk func(protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := range ms.Len() {
			m := ms.Get(i)
			if m.IsMapEntry() {
				continue
			}
			for j := range m.Fields().Len() {
				fields = append(fields, m.Fields().Get(j))
			}
			walk(m.Messages())
		}
	}
	files := 0
	protoregistry.GlobalFiles.RangeFilesByPackage(contractPackage, func(f protoreflect.FileDescriptor) bool {
		files++
		walk(f.Messages())
		return true
	})
	if files == 0 {
		t.Fatalf("no files of package %s are registered", contractPackage)
	}
	return fields
}
