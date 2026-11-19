package sqlkit

import (
	"encoding/json"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// JSONText writes v as JSON for a column that is stored and returned whole
// and never searched inside, so it is the same text on every database.
func JSONText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fault.Wrap(fault.Internal, err, "a value cannot be written as JSON")
	}
	return string(b), nil
}

// FromJSON reads a column JSONText wrote into v. A column that does not
// read is the server's fault: only JSONText writes one.
func FromJSON(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fault.Wrap(fault.Internal, err, "a stored value cannot be read")
	}
	return nil
}

// ToSHA256 reads a checksum column; NULL or a value of another length is
// none (the zero checksum), which a column's own check never lets in.
func ToSHA256(b []byte) tenant.SHA256 {
	var s tenant.SHA256
	if len(b) == len(s) {
		copy(s[:], b)
	}
	return s
}

// AddDetails returns the audit entry with the details a store adds, checked
// with the rest of the entry: a caller's entry already at its bound of
// details is refused before anything is written, never cut.
func AddDetails(audit master.AuditEntry, pairs ...string) (master.AuditEntry, error) {
	a := audit.WithDetails(pairs...)
	return a, a.Validate()
}
