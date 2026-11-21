package record

import (
	"encoding/json"
	"io"
	"time"

	"github.com/amrshadid/coria/internal/core/layout"
)

// MaxGeneration is the greatest generation an owner record holds: below
// 2^53, so a JSON number reads back exactly in every tool.
const MaxGeneration = 1<<53 - 1

// maxIDLength is the longest installation or tenant id.
const maxIDLength = 64

// Owner is the owner record of a tenant on one storage: which installation,
// at which generation, may write the tenant's objects there.
type Owner struct {
	// Format is Format.
	Format int `json:"format"`
	// Installation is the id of the installation that owns the tenant's
	// objects.
	Installation string `json:"installation"`
	// Tenant is the tenant's id.
	Tenant string `json:"tenant"`
	// Generation counts the hand-offs and restores of the tenant, from 1;
	// the greater generation wins.
	Generation int64 `json:"generation"`
	// Written is when the record was written.
	Written time.Time `json:"written"`
}

// Key is the key of the owner record on a storage.
func (Owner) Key() string { return layout.OwnerKey }

// validID reports whether s is 1 to 64 of A-Z, a-z, 0-9, "_" and "-".
func validID(s string) bool {
	if s == "" || len(s) > maxIDLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// Validate returns an error wrapping ErrFormat unless o is a record this
// package writes.
func (o Owner) Validate() error {
	switch {
	case o.Format != Format:
		return bad("format %d is not %d", o.Format, Format)
	case !validID(o.Installation):
		return bad("installation is 1 to %d of A-Z, a-z, 0-9, _ and -", maxIDLength)
	case !validID(o.Tenant):
		return bad("tenant is 1 to %d of A-Z, a-z, 0-9, _ and -", maxIDLength)
	case o.Generation < 1 || o.Generation > MaxGeneration:
		return bad("generation is 1 to %d", int64(MaxGeneration))
	case !validTime(o.Written):
		return bad("written is a time in UTC")
	}
	return nil
}

// Supersedes reports whether o is a later owner than other: a higher
// generation. Equal generations of different installations are neither
// later; a caller treats them as a conflict.
func (o Owner) Supersedes(other Owner) bool { return o.Generation > other.Generation }

// EncodeOwner returns the record's bytes, or an error wrapping ErrFormat if
// it is not valid. The time is written in UTC.
func EncodeOwner(o Owner) ([]byte, error) {
	o.Written = o.Written.UTC()
	if err := o.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(o)
	if err != nil {
		return nil, bad("it cannot be encoded")
	}
	return append(b, '\n'), nil
}

// DecodeOwner reads one owner record from r, which must hold nothing after
// it, within MaxBytes.
func DecodeOwner(r io.Reader) (Owner, error) {
	var o Owner
	if err := decode(r, &o); err != nil {
		return Owner{}, err
	}
	if err := o.Validate(); err != nil {
		return Owner{}, err
	}
	return o, nil
}
