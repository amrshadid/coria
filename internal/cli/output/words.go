package output

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/fault"
)

// The words below are how a person reads the API's enums, scopes and keys. The
// commands and the interactive program share them, so one value is never
// worded two ways. None of them cleans its result: a caller that draws text
// the server sent passes it through Clean.

// enumWord writes an enum value as a word for a person: its name without the
// prefix, in lowercase, so TENANT_STATE_READY is "ready".
func enumWord(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

// Role is a role as a word; no role is "none".
func Role(r v1.Role) string {
	if r == v1.Role_ROLE_UNSPECIFIED {
		return "none"
	}
	return enumWord(r.String(), "ROLE_")
}

// Database is a database type as a word.
func Database(t v1.DatabaseType) string {
	if t == v1.DatabaseType_DATABASE_TYPE_UNSPECIFIED {
		return "unknown"
	}
	return enumWord(t.String(), "DATABASE_TYPE_")
}

// State is a tenant state as a word.
func State(s v1.TenantState) string {
	if s == v1.TenantState_TENANT_STATE_UNSPECIFIED {
		return "unknown"
	}
	return enumWord(s.String(), "TENANT_STATE_")
}

// Outcome is an audit outcome as a word.
func Outcome(o v1.AuditOutcome) string {
	if o == v1.AuditOutcome_AUDIT_OUTCOME_UNSPECIFIED {
		return "unknown"
	}
	return enumWord(o.String(), "AUDIT_OUTCOME_")
}

// Scope is a scope as words: "installation" or "tenant <name>".
func Scope(sc *v1.Scope) string {
	if t, ok := sc.GetKind().(*v1.Scope_Tenant); ok {
		return "tenant " + t.Tenant
	}
	if _, ok := sc.GetKind().(*v1.Scope_Installation); ok {
		return "installation"
	}
	return "unknown"
}

// SettingFrom says where a setting's value comes from, in words: "built-in
// default", "installation", or "tenant <name> (override)". A server that
// does not say (an older one) is read from the setting's scope.
func SettingFrom(st *v1.Setting) string {
	switch st.GetSource() {
	case v1.SettingSource_SETTING_SOURCE_BUILT_IN:
		return "built-in default"
	case v1.SettingSource_SETTING_SOURCE_INSTALLATION:
		return "installation"
	case v1.SettingSource_SETTING_SOURCE_TENANT:
		return Scope(st.GetScope()) + " (override)"
	}
	if st.GetScope().GetTenant() != "" {
		return Scope(st.GetScope()) + " (override)"
	}
	return "installation"
}

// KeyLabel is a key's id with its holder, for a sentence.
func KeyLabel(k *v1.Key) string {
	if k.GetHolder() == "" {
		return k.GetId()
	}
	return k.GetId() + " (" + k.GetHolder() + ")"
}

// KeyStatus says whether a key works at now: revoked, expired, or active. It
// is worked out from the times the server sent, which are all a client has;
// the server decides for itself on each request.
func KeyStatus(k *v1.Key, now time.Time) string {
	switch {
	case k.GetRevokeTime() != nil:
		return "revoked"
	case k.GetExpireTime() != nil && !k.GetExpireTime().AsTime().After(now):
		return "expired"
	}
	return "active"
}

// Method shortens a method's full name, "/magnetar.v1.TenantService/CreateTenant",
// to "TenantService/CreateTenant".
func Method(m string) string {
	return strings.TrimPrefix(m, "/magnetar.v1.")
}

// Who says who made a request: the key's id, the local operator's user, or
// nothing when the request carried neither.
func Who(e *v1.AuditEntry) string {
	switch {
	case e.GetKeyId() != "":
		return e.GetKeyId()
	case e.GetLocalUserId() != "":
		return "local:" + e.GetLocalUserId()
	}
	return "-"
}

// ParseSpan reads a length of time written as a number and a unit: s, m, h, d
// (24 hours) or w (7 days), such as 90d or 36h.
func ParseSpan(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("not a length of time")
	}
	unit := map[byte]time.Duration{
		's': time.Second, 'm': time.Minute, 'h': time.Hour,
		'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour,
	}[s[len(s)-1]]
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if unit == 0 || err != nil || n <= 0 || n > int64(time.Duration(1<<62)/unit) {
		return 0, fmt.Errorf("not a length of time")
	}
	return time.Duration(n) * unit, nil
}

// CheckKeyID refuses a value that is a whole key. A key id is public; a whole
// key is a secret, and one typed as an argument would be in the process list
// and in shell history, and one typed into a field would be drawn on the
// screen. The message never repeats the value.
func CheckKeyID(id string) error {
	if id == "" {
		return fault.New(fault.Invalid, "the key id is empty; \"magnetar key list\" shows the ids")
	}
	if strings.HasPrefix(id, "mgt_") {
		return fault.New(fault.Invalid, "that is a whole key, which is a secret and is never an argument; give the key's id, which \"magnetar key list\" shows")
	}
	return nil
}
