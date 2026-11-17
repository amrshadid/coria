package master

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/amrshadid/magnetar/internal/fault"
)

// The bounds this package holds every value to. Input is hostile (rules,
// security): a value is bounded before it reaches a database, and a value past
// its bound is Invalid, never cut short.
const (
	// MaxIDLength bounds an id. Ids are opaque to the store; 64 bytes holds a
	// UUID in any of its spellings and a key id with room to spare.
	MaxIDLength = 64
	// MaxTenantNameLength is the longest tenant name (D37): a name appears in
	// URLs and paths, and 63 is the longest DNS label, so a name can also be
	// a host name.
	MaxTenantNameLength = 63
	// MaxDriverLength bounds the name of a database type, such as "sqlite".
	MaxDriverLength = 32
	// MaxSealedAddressLength bounds a tenant's sealed database address. An
	// address is a URL with a password; 16 KiB is far beyond any real one.
	MaxSealedAddressLength = 16 << 10
	// MaxStateReasonLength bounds the reason given with a tenant's state.
	MaxStateReasonLength = 1 << 10
	// KeyHashLength is the length of a key's hash: a SHA-256, made by package
	// secret.
	KeyHashLength = 32
	// MaxHolderLength bounds the name of a key's holder, in bytes (D37).
	MaxHolderLength = 256
	// MaxSettingNameLength bounds a setting's name.
	MaxSettingNameLength = 128
	// MaxSettingValueLength bounds a setting's value, in bytes.
	MaxSettingValueLength = 64 << 10
	// MaxAuditFieldLength bounds the short text fields of an audit entry: who,
	// the remote address, the method, the tenant name, the object and the
	// request id.
	MaxAuditFieldLength = 256
	// MaxAuditReasonLength bounds an audit entry's reason.
	MaxAuditReasonLength = 1 << 10
	// MaxAuditDetails bounds the number of pairs in an audit entry's details.
	MaxAuditDetails = 16
	// MaxAuditDetailKeyLength bounds a key of an audit entry's details.
	MaxAuditDetailKeyLength = 64
	// MaxAuditDetailValueLength bounds a value of an audit entry's details.
	MaxAuditDetailValueLength = 256
	// MaxAuditBatch bounds how many entries one AppendAudit takes, so one
	// call cannot hold the database's write lock for long.
	MaxAuditBatch = 10000
	// MaxPageLimit is the largest page a list returns.
	MaxPageLimit = 1000
)

// The keys AuditEntry.Details gets from SetGrant and RemoveGrant: the role a
// grant had before the change and the role it has after, "none" for no
// grant (D37).
const (
	DetailOldRole = "old_role"
	DetailNewRole = "new_role"
	// NoRole is the value of DetailOldRole and DetailNewRole when there is
	// no grant on that side of the change.
	NoRole = "none"
)

// TenantState says whether a tenant can be served.
type TenantState string

// The states of a tenant. A tenant that is not ready is refused on its own;
// the others keep working (M0 design note, part 2).
const (
	// StateReady is a tenant whose database answers and is at the binary's
	// schema version.
	StateReady TenantState = "ready"
	// StateUnreachable is a tenant whose database does not answer.
	StateUnreachable TenantState = "unreachable"
	// StateUpgrading is a tenant whose database is being migrated.
	StateUpgrading TenantState = "upgrading"
	// StateFailed is a tenant that cannot be served until a person acts; the
	// reason says why.
	StateFailed TenantState = "failed"
)

// Valid reports whether s is one of the four states.
func (s TenantState) Valid() bool {
	switch s {
	case StateReady, StateUnreachable, StateUpgrading, StateFailed:
		return true
	}
	return false
}

// Role is what a grant allows. Each role includes the ones before it (M0
// design note, part 4).
type Role string

// The roles.
const (
	RoleReader   Role = "reader"
	RoleWriter   Role = "writer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

// Valid reports whether r is one of the four roles. The empty role, which
// SetGrant returns when there was no grant before, is not valid in a grant.
func (r Role) Valid() bool {
	switch r {
	case RoleReader, RoleWriter, RoleOperator, RoleAdmin:
		return true
	}
	return false
}

// detail returns the role as it is written in an audit entry's details.
func (r Role) detail() string {
	if r == "" {
		return NoRole
	}
	return string(r)
}

// Outcome is how a request ended, as its audit entry records it.
type Outcome string

// The outcomes.
const (
	// OutcomeDone is a request that did what it asked.
	OutcomeDone Outcome = "done"
	// OutcomeRefused is a request the archive refused: no key, no grant, an
	// invalid argument.
	OutcomeRefused Outcome = "refused"
	// OutcomeFailed is a request that was allowed and did not succeed.
	OutcomeFailed Outcome = "failed"
)

// Valid reports whether o is one of the three outcomes.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeDone, OutcomeRefused, OutcomeFailed:
		return true
	}
	return false
}

// Tenant is one independent archive inside the installation, as the master
// knows it: its name and how to reach its database.
type Tenant struct {
	// ID is the tenant's opaque id, given by the caller.
	ID string
	// Name is 1 to 63 of a-z, 0-9 and "-", starting with a letter (D37).
	Name string
	// Driver names the database type of the tenant's database.
	Driver string
	// SealedAddress is the tenant's database address as package secret
	// sealed it. The store keeps the bytes and never opens them.
	SealedAddress []byte
	// State says whether the tenant can be served.
	State TenantState
	// StateReason says why the tenant is in its state; it may be empty.
	StateReason string
	// Created is when the tenant was made.
	Created time.Time
}

// Validate returns an Invalid error when t cannot be stored.
func (t Tenant) Validate() error {
	if err := ValidateID("tenant id", t.ID); err != nil {
		return err
	}
	if err := ValidateTenantName(t.Name); err != nil {
		return err
	}
	if t.Driver == "" || len(t.Driver) > MaxDriverLength || !allBytes(t.Driver, isLowerAlnum) {
		return invalid("a database type is 1 to " + strconv.Itoa(MaxDriverLength) + " of a-z and 0-9")
	}
	if len(t.SealedAddress) == 0 || len(t.SealedAddress) > MaxSealedAddressLength {
		return invalid("a sealed database address is 1 to " + strconv.Itoa(MaxSealedAddressLength) + " bytes")
	}
	if err := ValidateTenantState(t.State, t.StateReason); err != nil {
		return err
	}
	return ValidateTime("creation time", t.Created)
}

// ValidateTenantState returns an Invalid error unless state is one of the
// four and reason is UTF-8 of at most 1 KiB.
func ValidateTenantState(state TenantState, reason string) error {
	if !state.Valid() {
		return invalid("a tenant's state is ready, unreachable, upgrading or failed")
	}
	return validateText("state reason", reason, 0, MaxStateReasonLength)
}

// ValidateTenantName returns an Invalid error unless name is 1 to 63 of a-z,
// 0-9 and "-", starting with a letter (D37). A name appears in URLs and
// paths, so it is checked here, once, before it reaches either.
func ValidateTenantName(name string) error {
	if name == "" || len(name) > MaxTenantNameLength || name[0] < 'a' || name[0] > 'z' ||
		!allBytes(name, func(b byte) bool { return isLowerAlnum(b) || b == '-' }) {
		return invalid("a tenant's name is 1 to 63 of a-z, 0-9 and -, starting with a letter")
	}
	return nil
}

// Key is an API key as the master keeps it: its id and the hash of its
// secret, never the secret.
type Key struct {
	// ID is the key's public id, the part between "mgt_" and the secret.
	ID string
	// Hash is the SHA-256 of the key's secret, made by package secret.
	Hash []byte
	// Holder names the person or system that holds the key: any UTF-8, at
	// most 256 bytes (D37).
	Holder string
	// Created is when the key was made.
	Created time.Time
	// Expires is when the key stops working; the zero time is never.
	Expires time.Time
	// LastUsed is when the key was last used, to within a minute; the zero
	// time is never.
	LastUsed time.Time
	// Revoked is when the key was revoked; the zero time is not revoked.
	Revoked time.Time
}

// Validate returns an Invalid error when k cannot be created. A new key has
// never been used and is not revoked, so those two times must be zero.
func (k Key) Validate() error {
	if err := ValidateID("key id", k.ID); err != nil {
		return err
	}
	if len(k.Hash) != KeyHashLength {
		return invalid("a key's hash is " + strconv.Itoa(KeyHashLength) + " bytes")
	}
	if err := validateText("holder", k.Holder, 1, MaxHolderLength); err != nil {
		return err
	}
	if err := ValidateTime("creation time", k.Created); err != nil {
		return err
	}
	if !k.Expires.IsZero() {
		if err := ValidateTime("expiry", k.Expires); err != nil {
			return err
		}
		if !k.Expires.After(k.Created) {
			return invalid("a key's expiry is after its creation")
		}
	}
	if !k.LastUsed.IsZero() || !k.Revoked.IsZero() {
		return invalid("a new key has not been used or revoked")
	}
	return nil
}

// Scope is what a grant applies to: one tenant, or the installation. The zero
// Scope is the installation.
type Scope struct {
	// TenantID is the tenant's id, or empty for the installation.
	TenantID string
}

// Installation is the scope of the whole installation.
var Installation = Scope{}

// TenantScope returns the scope of one tenant.
func TenantScope(tenantID string) Scope {
	return Scope{TenantID: tenantID}
}

// IsInstallation reports whether s is the installation.
func (s Scope) IsInstallation() bool {
	return s.TenantID == ""
}

// Validate returns an Invalid error when s names a tenant with an id that is
// not valid.
func (s Scope) Validate() error {
	if s.IsInstallation() {
		return nil
	}
	return ValidateID("tenant id", s.TenantID)
}

// String returns "installation" or "tenant/<id>", as the scope is written in
// an audit entry's details.
func (s Scope) String() string {
	if s.IsInstallation() {
		return "installation"
	}
	return "tenant/" + s.TenantID
}

// Grant gives a key a role on one scope. A key has at most one grant per
// scope (D37).
type Grant struct {
	KeyID string
	Scope Scope
	Role  Role
}

// Validate returns an Invalid error when g cannot be stored.
func (g Grant) Validate() error {
	if err := ValidateID("key id", g.KeyID); err != nil {
		return err
	}
	if err := g.Scope.Validate(); err != nil {
		return err
	}
	if !g.Role.Valid() {
		return invalid("a role is reader, writer, operator or admin")
	}
	return nil
}

// Setting is one installation setting.
type Setting struct {
	Name  string
	Value string
}

// ValidateSettingName returns an Invalid error unless name is 1 to 128 of
// a-z, 0-9, ".", "_" and "-".
func ValidateSettingName(name string) error {
	if name == "" || len(name) > MaxSettingNameLength ||
		!allBytes(name, func(b byte) bool { return isLowerAlnum(b) || b == '.' || b == '_' || b == '-' }) {
		return invalid("a setting's name is 1 to 128 of a-z, 0-9, ., _ and -")
	}
	return nil
}

// ValidateSettingValue returns an Invalid error unless value is UTF-8 of at
// most 64 KiB.
func ValidateSettingValue(value string) error {
	return validateText("setting value", value, 0, MaxSettingValueLength)
}

// AuditEntry records one request, or one change, made to the installation
// (M0 design note, part 6). It never holds a key's secret or a database
// password; the caller that builds it is responsible for that, and the bounds
// below keep an entry small.
type AuditEntry struct {
	// ID is the entry's opaque id, given by the caller, unique among entries.
	ID string
	// Time is when the request was made.
	Time time.Time
	// KeyID is the id of the key that made the request; empty when it was
	// made on the local socket or with no key at all.
	KeyID string
	// LocalUser is the operating system user that made the request on the
	// local socket; empty otherwise. At most one of KeyID and LocalUser is
	// set.
	LocalUser string
	// RemoteAddress is where the request came from.
	RemoteAddress string
	// Method is the API method or command that was called.
	Method string
	// TenantName is the tenant the request named, as it named it: not a
	// tenant id, so the entry still reads right after the tenant is removed,
	// and can record a request for a tenant that never existed.
	TenantName string
	// Object is what inside the tenant or installation the request was about.
	Object string
	// Outcome is how the request ended.
	Outcome Outcome
	// Reason is the real cause of a refusal or a failure, which the caller
	// may not have been told; it may be empty.
	Reason string
	// Details holds what else the entry needs to be read: a database type, a
	// scope, an old and a new role, a setting's name, the filters of a list.
	// Never a secret.
	Details map[string]string
	// RequestID is the request's id, which also appears in the log.
	RequestID string
}

// Validate returns an Invalid error when e cannot be stored.
func (e AuditEntry) Validate() error {
	if err := ValidateID("audit entry id", e.ID); err != nil {
		return err
	}
	if err := ValidateTime("audit time", e.Time); err != nil {
		return err
	}
	if e.KeyID != "" && e.LocalUser != "" {
		return invalid("an audit entry names a key or a local user, not both")
	}
	if e.KeyID != "" {
		if err := ValidateID("key id", e.KeyID); err != nil {
			return err
		}
	}
	for _, f := range []struct{ name, value string }{
		{"local user", e.LocalUser},
		{"remote address", e.RemoteAddress},
		{"tenant name", e.TenantName},
		{"object", e.Object},
		{"request id", e.RequestID},
	} {
		if err := validateText(f.name, f.value, 0, MaxAuditFieldLength); err != nil {
			return err
		}
	}
	if err := validateText("method", e.Method, 1, MaxAuditFieldLength); err != nil {
		return err
	}
	if !e.Outcome.Valid() {
		return invalid("an outcome is done, refused or failed")
	}
	if err := validateText("reason", e.Reason, 0, MaxAuditReasonLength); err != nil {
		return err
	}
	if len(e.Details) > MaxAuditDetails {
		return invalid("an audit entry has at most " + strconv.Itoa(MaxAuditDetails) + " details")
	}
	for k, v := range e.Details {
		if err := validateText("detail key", k, 1, MaxAuditDetailKeyLength); err != nil {
			return err
		}
		if err := validateText("detail value", v, 0, MaxAuditDetailValueLength); err != nil {
			return err
		}
	}
	return nil
}

// WithRoleChange returns a copy of e whose details also say the role a grant
// had before a change and the role it has after, "none" for no grant. A
// driver calls it inside the change, where the old role is known, so the
// entry it writes is exact even when two changes race (D37). The copy has
// its own map, so the caller's entry is not changed.
func (e AuditEntry) WithRoleChange(old, next Role) AuditEntry {
	details := make(map[string]string, len(e.Details)+2)
	for k, v := range e.Details {
		details[k] = v
	}
	details[DetailOldRole] = old.detail()
	details[DetailNewRole] = next.detail()
	e.Details = details
	return e
}

// ValidateRoleChange returns an Invalid error when e would be past its bounds
// once WithRoleChange has added its two details, so a driver can refuse it
// before it changes anything.
func (e AuditEntry) ValidateRoleChange() error {
	return e.WithRoleChange(RoleAdmin, RoleAdmin).Validate()
}

// AuditFilter narrows ListAudit. A zero field matches every entry.
type AuditFilter struct {
	// TenantName keeps entries that named this tenant.
	TenantName string
	// KeyID keeps entries made with this key.
	KeyID string
	// Outcome keeps entries with this outcome.
	Outcome Outcome
	// Since keeps entries at or after this time.
	Since time.Time
	// Until keeps entries before this time.
	Until time.Time
}

// Validate returns an Invalid error when f cannot be applied.
func (f AuditFilter) Validate() error {
	if err := validateText("tenant name", f.TenantName, 0, MaxAuditFieldLength); err != nil {
		return err
	}
	if f.KeyID != "" {
		if err := ValidateID("key id", f.KeyID); err != nil {
			return err
		}
	}
	if f.Outcome != "" && !f.Outcome.Valid() {
		return invalid("an outcome is done, refused or failed")
	}
	for _, t := range []struct {
		name string
		t    time.Time
	}{{"start time", f.Since}, {"end time", f.Until}} {
		if !t.t.IsZero() {
			if err := ValidateTime(t.name, t.t); err != nil {
				return err
			}
		}
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Until.After(f.Since) {
		return invalid("an end time is after the start time")
	}
	return nil
}

// Page asks for one page of a list: up to Limit items after the one Cursor
// points at. The empty cursor is the start of the list.
type Page struct {
	// Cursor is the next cursor a previous page returned, or empty.
	Cursor string
	// Limit is how many items to return, 1 to 1000.
	Limit int
}

// Validate returns an Invalid error unless the limit is 1 to 1000. A cursor
// is checked by the store that made it, since only it can read one.
func (p Page) Validate() error {
	if p.Limit < 1 || p.Limit > MaxPageLimit {
		return invalid("a page's limit is 1 to " + strconv.Itoa(MaxPageLimit))
	}
	return nil
}

// The range of times the store keeps. A time is kept as microseconds since
// 1970 in a 64-bit integer by the SQL drivers; the range is narrower than
// that allows so that every driver can keep it, and wide enough for any date
// an archive records.
var (
	minTime = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	maxTime = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
)

// ValidateTime returns an Invalid error unless t is in the years 1970 to
// 9999. The zero time is outside them, so a time the caller forgot to set is
// refused rather than stored as year 1.
func ValidateTime(name string, t time.Time) error {
	if t.Before(minTime) || !t.Before(maxTime) {
		return invalid("the " + name + " is between the years 1970 and 9999")
	}
	return nil
}

// ValidateID returns an Invalid error unless id is 1 to 64 of A-Z, a-z, 0-9,
// "_" and "-". Ids are opaque to the store, but appear in cursors, logs and
// paths, so they are held to characters that need no escaping anywhere.
func ValidateID(name, id string) error {
	if id == "" || len(id) > MaxIDLength || !allBytes(id, func(b byte) bool {
		return isLowerAlnum(b) || (b >= 'A' && b <= 'Z') || b == '_' || b == '-'
	}) {
		return invalid("a " + name + " is 1 to " + strconv.Itoa(MaxIDLength) + " of A-Z, a-z, 0-9, _ and -")
	}
	return nil
}

// validateText returns an Invalid error unless s is valid UTF-8 of min to max
// bytes with no NUL byte. Invalid UTF-8 and NUL are refused because Postgres
// refuses both in a text column and SQLite would keep them, and the two
// drivers must agree.
func validateText(name, s string, min, max int) error {
	if len(s) < min || len(s) > max {
		if min > 0 {
			return invalid("a " + name + " is " + strconv.Itoa(min) + " to " + strconv.Itoa(max) + " bytes")
		}
		return invalid("a " + name + " is at most " + strconv.Itoa(max) + " bytes")
	}
	if !utf8.ValidString(s) {
		return invalid("a " + name + " is UTF-8")
	}
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return invalid("a " + name + " holds no NUL byte")
		}
	}
	return nil
}

// allBytes reports whether ok holds for every byte of s.
func allBytes(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

// isLowerAlnum reports whether b is a-z or 0-9.
func isLowerAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// invalid returns an Invalid error with a message for the caller.
func invalid(message string) error {
	return fault.New(fault.Invalid, message)
}
