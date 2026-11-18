// Package api_test checks rules of the API contract that buf lint cannot
// express, by reading the descriptors compiled into the generated Go code.
package api_test

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// Imported for its side effect: it registers the magnetar.v1 files in
	// protoregistry.GlobalFiles.
	_ "github.com/amrshadid/magnetar/gen/magnetar/v1"
)

// The package whose messages are checked.
const contractPackage = "magnetar.v1"

// secretFields are the only fields through which a secret crosses the API: a
// secret never appears in an API answer, except a key's secret once, when it
// is made, so that a log or a client's history cannot leak one. Each must be
// marked debug_redact, which the server's redactor reads, and no other field
// may be.
var secretFields = []string{
	"magnetar.v1.CreateKeyResponse.secret",
	"magnetar.v1.CreateTenantRequest.database_address",
}

// textFields lists every field of the contract that carries free text: each
// string or bytes field, and each map with string or bytes keys or values.
// A secret can only travel in such a field. A field added to the contract
// that is not listed here fails the test, so adding one is a deliberate act:
// whoever lists it has decided it carries no secret, or that it is one of
// secretFields.
var textFields = []string{
	// scope.proto
	"magnetar.v1.Scope.tenant",

	// access.proto
	"magnetar.v1.Key.id",
	"magnetar.v1.Key.holder",
	"magnetar.v1.Grant.key_id",
	"magnetar.v1.LocalOperator.user_id",
	"magnetar.v1.CreateKeyRequest.holder",
	"magnetar.v1.CreateKeyResponse.secret",
	"magnetar.v1.GetKeyRequest.id",
	"magnetar.v1.ListKeysRequest.page_token",
	"magnetar.v1.ListKeysResponse.next_page_token",
	"magnetar.v1.RevokeKeyRequest.id",
	"magnetar.v1.SetGrantRequest.key_id",
	"magnetar.v1.ListGrantsRequest.key_id",
	"magnetar.v1.ListGrantsRequest.page_token",
	"magnetar.v1.ListGrantsResponse.next_page_token",
	"magnetar.v1.RemoveGrantRequest.key_id",

	// audit.proto
	"magnetar.v1.AuditEntry.id",
	"magnetar.v1.AuditEntry.key_id",
	"magnetar.v1.AuditEntry.local_user_id",
	"magnetar.v1.AuditEntry.client_address",
	"magnetar.v1.AuditEntry.method",
	"magnetar.v1.AuditEntry.tenant",
	"magnetar.v1.AuditEntry.object",
	"magnetar.v1.AuditEntry.request_id",
	"magnetar.v1.AuditEntry.details",
	"magnetar.v1.AuditEntry.reason",
	"magnetar.v1.ListAuditEntriesRequest.key_id",
	"magnetar.v1.ListAuditEntriesRequest.page_token",
	"magnetar.v1.ListAuditEntriesResponse.next_page_token",

	// settings.proto
	"magnetar.v1.Setting.name",
	"magnetar.v1.Setting.value",
	"magnetar.v1.GetSettingRequest.name",
	"magnetar.v1.UnsetSettingRequest.name",
	"magnetar.v1.ListSettingsRequest.page_token",
	"magnetar.v1.ListSettingsResponse.next_page_token",

	// tenant.proto
	"magnetar.v1.Tenant.name",
	"magnetar.v1.Tenant.state_reason",
	"magnetar.v1.CreateTenantRequest.name",
	"magnetar.v1.CreateTenantRequest.database_address",
	"magnetar.v1.GetTenantRequest.name",
	"magnetar.v1.ListTenantsRequest.page_token",
	"magnetar.v1.ListTenantsResponse.next_page_token",
	"magnetar.v1.RemoveTenantRequest.name",
	"magnetar.v1.RemoveTenantRequest.confirm_name",
	"magnetar.v1.RemoveTenantResponse.data_location",
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
