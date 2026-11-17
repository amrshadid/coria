// Package api_test checks rules of the API contract that buf lint cannot
// express, by reading the descriptors compiled into the generated Go code.
package api_test

import (
	"slices"
	"strings"
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

// secretFields are the only fields through which a secret crosses the API
// (rules/security.md: secrets never appear in an API answer, except a key's
// secret once, when it is made). Each must be marked debug_redact, and no
// other field may be.
var secretFields = []string{
	"magnetar.v1.CreateKeyResponse.secret",
	"magnetar.v1.CreateTenantRequest.database_address",
}

// secretWords are parts of a field name that suggest the field carries a
// secret: a credential, a token, or the address of a database, which holds
// its login and password.
var secretWords = []string{
	"secret",
	"password",
	"passwd",
	"credential",
	"private_key",
	"token",
	"database_address",
	"database_url",
	"db_address",
	"db_url",
	"dsn",
	"connection_string",
}

// harmlessNames are field names that contain a word of secretWords and carry
// no secret: a page token is an opaque cursor into a list.
var harmlessNames = []string{"page_token", "next_page_token"}

// TestOnlyTheNamedFieldsCarrySecrets guards acceptance point 6 of the
// contract: exactly the two named fields are marked debug_redact, and no
// other field of any message has a name that suggests a secret. A field added
// later that carries a secret without being one of the two, or a secret field
// that loses its mark, fails here rather than in a review.
func TestOnlyTheNamedFieldsCarrySecrets(t *testing.T) {
	var redacted, suspect []string
	messages := 0
	for _, field := range contractFields(t, &messages) {
		name := string(field.FullName())
		opts, _ := field.Options().(*descriptorpb.FieldOptions)
		if opts.GetDebugRedact() {
			redacted = append(redacted, name)
		}
		if slices.Contains(secretFields, name) {
			continue
		}
		if word, ok := suggestsSecret(string(field.Name())); ok {
			suspect = append(suspect, name+" (contains "+word+")")
		}
	}

	// A walk that found nothing would pass the checks below for no reason.
	if messages < 20 {
		t.Fatalf("found %d messages in %s, want at least 20: is the generated code registered?", messages, contractPackage)
	}

	slices.Sort(redacted)
	if !slices.Equal(redacted, secretFields) {
		t.Errorf("fields marked debug_redact = %q, want exactly %q", redacted, secretFields)
	}
	for _, name := range suspect {
		t.Errorf("%s: the name suggests a secret, and only %q may carry one", name, secretFields)
	}
}

// TestSuggestsSecret guards the name check itself, so that a change to the
// word lists cannot make TestOnlyTheNamedFieldsCarrySecrets pass everything.
func TestSuggestsSecret(t *testing.T) {
	for _, name := range []string{"secret", "key_secret", "password", "api_token", "database_address", "postgres_dsn", "db_url"} {
		if _, ok := suggestsSecret(name); !ok {
			t.Errorf("suggestsSecret(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"page_token", "next_page_token", "client_address", "name", "key_id", "holder"} {
		if word, ok := suggestsSecret(name); ok {
			t.Errorf("suggestsSecret(%q) = true (%s), want false", name, word)
		}
	}
}

// suggestsSecret reports whether a field name contains a word of secretWords,
// and which, unless the name is one of harmlessNames.
func suggestsSecret(name string) (string, bool) {
	if slices.Contains(harmlessNames, name) {
		return "", false
	}
	lower := strings.ToLower(name)
	for _, word := range secretWords {
		if strings.Contains(lower, word) {
			return word, true
		}
	}
	return "", false
}

// contractFields answers every field of every message, nested messages
// included, in the files of contractPackage, and counts the messages.
func contractFields(t *testing.T, messages *int) []protoreflect.FieldDescriptor {
	t.Helper()
	var fields []protoreflect.FieldDescriptor
	var walk func(protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := range ms.Len() {
			m := ms.Get(i)
			*messages++
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
