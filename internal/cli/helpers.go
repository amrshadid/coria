package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/cli/client"
	"github.com/amrshadid/magnetar/internal/cli/output"
	"github.com/amrshadid/magnetar/internal/fault"
)

// Paging limits. The contract's page holds at most 1000 and 100 by default.
const (
	defaultLimit = 100
	maxPageSize  = 1000
)

// maxAddressFile is the contract's limit on a database address, 4 KiB. The
// file is read one byte past it, so a longer one is refused, not cut.
const maxAddressFile = 4096

// enumText writes an enum value as a word for a person: its name without the
// prefix, in lowercase, so TENANT_STATE_READY is "ready".
func enumText(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

// roleText is a role as a word; no role is "none".
func roleText(r v1.Role) string {
	if r == v1.Role_ROLE_UNSPECIFIED {
		return "none"
	}
	return enumText(r.String(), "ROLE_")
}

// databaseText is a database type as a word.
func databaseText(t v1.DatabaseType) string {
	if t == v1.DatabaseType_DATABASE_TYPE_UNSPECIFIED {
		return "unknown"
	}
	return enumText(t.String(), "DATABASE_TYPE_")
}

// stateText is a tenant state as a word.
func stateText(s v1.TenantState) string {
	if s == v1.TenantState_TENANT_STATE_UNSPECIFIED {
		return "unknown"
	}
	return enumText(s.String(), "TENANT_STATE_")
}

// outcomeText is an audit outcome as a word.
func outcomeText(o v1.AuditOutcome) string {
	if o == v1.AuditOutcome_AUDIT_OUTCOME_UNSPECIFIED {
		return "unknown"
	}
	return enumText(o.String(), "AUDIT_OUTCOME_")
}

// scopeText is a scope as words: "installation" or "tenant <name>".
func scopeText(sc *v1.Scope) string {
	if t, ok := sc.GetKind().(*v1.Scope_Tenant); ok {
		return "tenant " + t.Tenant
	}
	if _, ok := sc.GetKind().(*v1.Scope_Installation); ok {
		return "installation"
	}
	return "unknown"
}

// parseRole reads a role name as the person types it.
func parseRole(s string) (v1.Role, error) {
	switch strings.ToLower(s) {
	case "reader":
		return v1.Role_ROLE_READER, nil
	case "writer":
		return v1.Role_ROLE_WRITER, nil
	case "operator":
		return v1.Role_ROLE_OPERATOR, nil
	case "admin":
		return v1.Role_ROLE_ADMIN, nil
	}
	return 0, fault.New(fault.Invalid, fmt.Sprintf("--role %q is not a role; use reader, writer, operator or admin", output.Clean(s)))
}

// parseOutcome reads an audit outcome name.
func parseOutcome(s string) (v1.AuditOutcome, error) {
	switch strings.ToLower(s) {
	case "done":
		return v1.AuditOutcome_AUDIT_OUTCOME_DONE, nil
	case "refused":
		return v1.AuditOutcome_AUDIT_OUTCOME_REFUSED, nil
	case "failed":
		return v1.AuditOutcome_AUDIT_OUTCOME_FAILED, nil
	}
	return 0, fault.New(fault.Invalid, fmt.Sprintf("--outcome %q is not an outcome; use done, refused or failed", output.Clean(s)))
}

// parseSpan reads a length of time written as a number and a unit: s, m, h, d
// (24 hours) or w (7 days), such as 90d or 36h.
func parseSpan(s string) (time.Duration, error) {
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

// parseTime reads the --since and --until forms: an age such as 24h or 7d
// (that long before now), a date (midnight in the zone times are shown in) or
// a full time with its offset (RFC 3339).
func parseTime(flag, s string, now time.Time, loc *time.Location) (time.Time, error) {
	if d, err := parseSpan(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t, nil
	}
	return time.Time{}, fault.New(fault.Invalid, fmt.Sprintf("--%s %q is not a time; use a date (2026-11-17), a time with its offset (2026-11-17T09:30:00+03:00) or an age (24h, 7d)", flag, output.Clean(s)))
}

// checkKeyID refuses a value that is a whole key. A key id is public; a whole
// key is a secret, and one typed as an argument would be in the process list
// and in shell history. The message never repeats the value.
func checkKeyID(id string) error {
	if id == "" {
		return fault.New(fault.Invalid, "the key id is empty; \"magnetar key list\" shows the ids")
	}
	if strings.HasPrefix(id, "mgt_") {
		return fault.New(fault.Invalid, "that is a whole key, which is a secret and is never an argument; give the key's id, which \"magnetar key list\" shows")
	}
	return nil
}

// readAddress reads a tenant's database address from the environment variable
// named by dbEnv or from the file named by dbFile, and from nowhere else. The
// messages name the variable or the file and never show any of the address.
func (a *app) readAddress(dbEnv, dbFile string) (string, error) {
	switch {
	case dbEnv != "":
		v := strings.TrimSpace(a.env.Getenv(dbEnv))
		if v == "" {
			return "", fault.New(fault.Invalid, "--db-env names "+dbEnv+", which is not set or is empty")
		}
		if len(v) > maxAddressFile {
			return "", fault.New(fault.Invalid, dbEnv+" holds an address longer than 4 KiB")
		}
		return v, nil
	case dbFile != "":
		f, err := os.Open(dbFile) // #nosec G304 -- the path is the person's own --db-file
		if err != nil {
			return "", fault.Wrap(fault.Invalid, err, "cannot read --db-file "+dbFile)
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(io.LimitReader(f, maxAddressFile+1))
		if err != nil {
			return "", fault.Wrap(fault.Invalid, err, "cannot read --db-file "+dbFile)
		}
		if len(b) > maxAddressFile {
			return "", fault.New(fault.Invalid, dbFile+" holds an address longer than 4 KiB")
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fault.New(fault.Invalid, "--db-file "+dbFile+" is empty")
		}
		return v, nil
	}
	return "", nil
}

// scopeFlags are the two ways to name a scope on the command line: a tenant,
// or the installation.
type scopeFlags struct {
	tenant       string
	installation bool
}

// add registers --tenant and --installation on a command. what says what the
// scope is for, in the flags' help text.
func (f *scopeFlags) add(cmd *cobra.Command, forWhat string) {
	cmd.Flags().StringVar(&f.tenant, "tenant", "", "the tenant "+forWhat)
	cmd.Flags().BoolVar(&f.installation, "installation", false, "the installation itself "+forWhat+", not a tenant")
}

// scope turns the flags into the contract's Scope. The command marks the two
// flags as exclusive and one required, so exactly one is set.
func (f *scopeFlags) scope() *v1.Scope {
	if f.installation {
		return &v1.Scope{Kind: &v1.Scope_Installation{Installation: &v1.Installation{}}}
	}
	return &v1.Scope{Kind: &v1.Scope_Tenant{Tenant: f.tenant}}
}

// optionalScope is the scope of a command where --tenant is optional: the
// tenant when named, else the installation.
func optionalScope(tenant string) *v1.Scope {
	if tenant != "" {
		return &v1.Scope{Kind: &v1.Scope_Tenant{Tenant: tenant}}
	}
	return &v1.Scope{Kind: &v1.Scope_Installation{Installation: &v1.Installation{}}}
}

// listFlags are the paging flags of every list command.
type listFlags struct {
	limit int
	all   bool
}

// add registers --limit and --all.
func (f *listFlags) add(cmd *cobra.Command) {
	cmd.Flags().IntVar(&f.limit, "limit", defaultLimit, "the most items to fetch; when more exist the command says so")
	cmd.Flags().BoolVar(&f.all, "all", false, "fetch every item, page after page")
	cmd.MarkFlagsMutuallyExclusive("limit", "all")
}

// check refuses a limit that cannot be used.
func (f *listFlags) check() error {
	if f.limit < 1 {
		return fault.New(fault.Invalid, "--limit must be at least 1")
	}
	return nil
}

// fetchPages reads a list a page at a time until it has limit items (or every
// item, with all) and reports whether the server has more. The page size asked
// for is what is still needed, up to the contract's 1000, so --limit 100 is one
// call. A server that answers the token it was just given would make the loop
// run forever, so that is refused.
func fetchPages[T any](ctx context.Context, l listFlags, fetch func(ctx context.Context, size int32, token string) (items []T, next string, err error)) (items []T, more bool, err error) {
	token := ""
	for {
		size := maxPageSize
		if !l.all {
			size = min(l.limit-len(items), maxPageSize)
		}
		page, next, err := fetch(ctx, int32(size), token) // #nosec G115 -- size is at most 1000
		if err != nil {
			return nil, false, err
		}
		items = append(items, page...)
		if next == "" {
			return items, false, nil
		}
		if next == token {
			return nil, false, fault.New(fault.Internal, "the server answered the same page token twice")
		}
		if !l.all && len(items) >= l.limit {
			return items, true, nil
		}
		token = next
	}
}

// moreNote tells the person, on standard error so a pipe stays clean, that the
// list was cut short and how to get the rest.
func (a *app) moreNote(shown int) {
	_, _ = fmt.Fprintf(a.env.Stderr, "magnetar: more exist; showing the first %d; use --limit N or --all to see more\n", shown)
}

// listOut writes a fetched list: as a JSON document with the items under the
// contract's name and a "more" flag, or as a table by the given function. The
// note about more items goes to standard error only for the table, because a
// program reads "more" from the document.
func listOut[T proto.Message](a *app, field string, items []T, more bool, table func() error) error {
	if a.opts.json {
		return a.write(func(w io.Writer) error { return output.JSONList(w, field, items, more) })
	}
	if err := table(); err != nil {
		return err
	}
	if more {
		a.moreNote(len(items))
	}
	return nil
}

// call runs one API call for a command with its name in any error.
func call[T any](ctx context.Context, c *client.Client, cmd *cobra.Command, fn func(context.Context) (*T, error)) (*T, error) {
	return client.Call(ctx, c, what(cmd), fn)
}

// open makes the client for a command and returns it with the function that
// closes it.
func (a *app) open(cmd *cobra.Command) (*client.Client, func(), error) {
	c, err := a.client(cmd)
	if err != nil {
		return nil, nil, err
	}
	return c, c.Close, nil
}

// messageOut writes the answer of a call: the JSON document of the response
// when --json is set, and otherwise what the table function writes.
func messageOut(a *app, m proto.Message, table func(w io.Writer) error) error {
	if a.opts.json {
		return a.write(func(w io.Writer) error { return output.JSONMessage(w, m) })
	}
	return a.write(table)
}
