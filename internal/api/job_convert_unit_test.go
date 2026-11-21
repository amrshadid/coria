package api

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/tenant"
)

// TestJobFailureCodesMatchTheContract guards the mapping of a job failure's
// code: every value the contract's JobFailureCode defines is a code the
// tenant records, and each recorded code is answered as its own value,
// never as UNSPECIFIED, so a client never reads a refusal the server named
// as one it cannot name.
func TestJobFailureCodesMatchTheContract(t *testing.T) {
	at := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	seen := 0
	for n, name := range v1.JobFailureCode_name {
		if v1.JobFailureCode(n) == v1.JobFailureCode_JOB_FAILURE_CODE_UNSPECIFIED {
			continue
		}
		code := tenant.JobFailureCode(strings.ToLower(strings.TrimPrefix(name, "JOB_FAILURE_CODE_")))
		f := tenant.JobFailure{SOPInstanceUID: "2.25.1", Code: code, Time: at}
		if err := f.Validate(); err != nil {
			t.Errorf("the contract's %s is not a code the tenant records: %v", name, err)
			continue
		}
		if got := jobFailureMessage(f).GetCode(); got != v1.JobFailureCode(n) {
			t.Errorf("the code %s is answered as %s, want %s", code, got, name)
		}
		seen++
	}
	if got := jobFailureMessage(tenant.JobFailure{SOPInstanceUID: "2.25.1", Code: tenant.FailureMetaUnreadable, Time: at}).GetCode(); got != v1.JobFailureCode_JOB_FAILURE_CODE_META_UNREADABLE || seen < 7 {
		t.Errorf("meta_unreadable is answered as %s with %d codes seen, want JOB_FAILURE_CODE_META_UNREADABLE of at least 7", got, seen)
	}
}
