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
	"coria.v1.ProfileDocument.bases",
	"coria.v1.ProfileDocument.description",
	"coria.v1.ProfileRule.name",
	"coria.v1.ProfileRule.reason",
	"coria.v1.ProfileSelector.tag",
	"coria.v1.ProfileSelector.keyword",
	"coria.v1.ProfileSelector.group",
	"coria.v1.ProfileSelector.vr",
	"coria.v1.ProfileSelector.path",
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
}

// TestOnlyTheNamedFieldsCarrySecrets guards where secrets may cross the
// contract: exactly the two named fields are marked debug_redact, and every
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
	if !slices.Equal(redacted, secretFields) {
		t.Errorf("fields marked debug_redact = %q, want exactly %q", redacted, secretFields)
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
