package settings

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// KeyDefaultLifetime is how long a new API key lives when its maker names
// no expiry. Only the installation sets it: a key belongs to the
// installation, not to a tenant.
const KeyDefaultLifetime = "key.default-lifetime"

// The bounds of a key's default lifetime: at least an hour, so a mistyped
// value cannot make keys that expire before they are handed over, and at most
// the longest expiry a key may be given (access.proto, CreateKeyRequest).
const (
	MinKeyLifetime = time.Hour
	MaxKeyLifetime = 3650 * 24 * time.Hour
)

// ReservedPrefix starts the names the system keeps for itself in the
// master's settings store, such as the installation's id. No setting of a
// table may use it, so the settings service, which knows only a table's
// names, answers such a name as one that does not exist: it neither lists,
// reads, sets nor unsets it.
const ReservedPrefix = "system."

// The bounds every setting's name and value are held to, the same as the
// databases hold them (master.ValidateSettingName, master.MaxSettingValueLength),
// restated here because the core imports nothing of Magnetar's.
const (
	MaxNameLength  = 128
	MaxValueLength = 64 << 10
)

// Definition is one setting the server knows.
type Definition struct {
	// Name is 1 to 128 of a-z, 0-9, ".", "_" and "-".
	Name string
	// Default is the built-in default: the value that applies when neither
	// the installation nor a tenant has set one. Validate accepts it.
	Default string
	// TenantOverride reports whether a tenant may set its own value.
	TenantOverride bool
	// Validate returns an error, whose text is for the person who wrote the
	// value, unless the value is one this setting accepts. Whether an empty
	// value is accepted is its own business.
	Validate func(value string) error
}

// Table is a list of definitions, looked up by name. It is not changed once
// made, so it is safe for concurrent use.
type Table struct {
	byName map[string]Definition
	names  []string
}

// Builtin returns the definitions of the settings this version of the server
// knows, a new slice each call.
func Builtin() []Definition {
	return []Definition{{
		Name:           KeyDefaultLifetime,
		Default:        "90d",
		TenantOverride: false,
		Validate: func(v string) error {
			d, err := ParseDuration(v)
			if err != nil {
				return err
			}
			if d < MinKeyLifetime || d > MaxKeyLifetime {
				return errors.New("is at least 1h and at most 3650d")
			}
			return nil
		},
	}}
}

// New returns a table of defs. It refuses a name that is not valid or given
// twice, a definition without a Validate function, and a built-in default
// that its own Validate refuses: a table that cannot answer every name it
// holds is a mistake of the code, found when the server starts.
func New(defs ...Definition) (*Table, error) {
	t := &Table{byName: make(map[string]Definition, len(defs))}
	for _, d := range defs {
		switch {
		case !validName(d.Name):
			return nil, errors.New("settings: the name " + strconv.Quote(d.Name) + " is not 1 to 128 of a-z, 0-9, ., _ and -")
		case strings.HasPrefix(d.Name, ReservedPrefix):
			return nil, errors.New("settings: the name " + d.Name + " is under " + ReservedPrefix + ", which the system keeps for itself")
		case d.Validate == nil:
			return nil, errors.New("settings: " + d.Name + " has no Validate function")
		case len(d.Default) > MaxValueLength:
			return nil, errors.New("settings: the default of " + d.Name + " is longer than 64 KiB")
		}
		if _, dup := t.byName[d.Name]; dup {
			return nil, errors.New("settings: " + d.Name + " is defined twice")
		}
		if err := d.Validate(d.Default); err != nil {
			return nil, errors.New("settings: the default of " + d.Name + " is refused by its own rule: " + err.Error())
		}
		t.byName[d.Name] = d
		t.names = append(t.names, d.Name)
	}
	slices.Sort(t.names)
	return t, nil
}

// MustBuiltin returns the table of Builtin. It panics if Builtin is not a
// valid table, which its test rules out.
func MustBuiltin() *Table {
	t, err := New(Builtin()...)
	if err != nil {
		panic(err)
	}
	return t
}

// Lookup returns the definition named name, and whether the table holds it.
func (t *Table) Lookup(name string) (Definition, bool) {
	d, ok := t.byName[name]
	return d, ok
}

// Names returns every name of the table in byte order, which is the order
// the databases list settings in. The caller must not change the slice.
func (t *Table) Names() []string { return t.names }

// ParseDuration reads a duration written as a positive whole number followed
// by one unit, "h" for hours or "d" for days of 24 hours: "90d", "2160h".
// It is the one form a duration setting takes, so a value reads the same in
// every place it is shown, and it has no fractions, no signs and no mixed
// units to misread.
func ParseDuration(s string) (time.Duration, error) {
	bad := errors.New("is a whole number of hours or days, such as 90d or 2160h")
	if len(s) < 2 {
		return 0, bad
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	default:
		return 0, bad
	}
	digits := s[:len(s)-1]
	if strings.Trim(digits, "0123456789") != "" || digits[0] == '0' {
		return 0, bad
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	// Past a hundred thousand years, which no bound here allows, a count
	// would overflow the duration; it is refused rather than wrapped.
	if err != nil || n > int64(time.Duration(1<<62)/unit) {
		return 0, bad
	}
	return time.Duration(n) * unit, nil
}

// validName reports whether name is 1 to 128 of a-z, 0-9, ".", "_" and "-".
func validName(name string) bool {
	if name == "" || len(name) > MaxNameLength {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '.' && b != '_' && b != '-' {
			return false
		}
	}
	return true
}
