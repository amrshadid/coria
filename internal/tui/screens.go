package tui

import (
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/cli/output"
	"github.com/amrshadid/magnetar/internal/fault"
)

// This file builds the screens: for each, its columns, what a page of it asks
// for, and what its keys open. Every function returns the screen and the
// command that loads it; none of them calls the API.

// installationScope is the scope of the installation itself.
func installationScope() *v1.Scope {
	return &v1.Scope{Kind: &v1.Scope_Installation{Installation: &v1.Installation{}}}
}

// tenantScope is the scope of one tenant.
func tenantScope(name string) *v1.Scope {
	return &v1.Scope{Kind: &v1.Scope_Tenant{Tenant: name}}
}

// scopeFromText reads what a person typed for a scope: the word installation,
// or a tenant's name.
func scopeFromText(s string) *v1.Scope {
	if strings.EqualFold(s, "installation") {
		return installationScope()
	}
	return tenantScope(s)
}

// scopeName words a scope for a heading.
func scopeName(sc *v1.Scope) string {
	if t := sc.GetTenant(); t != "" {
		return "tenant " + clean(t)
	}
	return "the installation"
}

// listDate is a time as a date in the person's zone, for a list, where a cell
// has little room; a detail screen shows the whole time with its offset.
func listDate(ts *timestamppb.Timestamp, loc *time.Location, absent string) string {
	if ts == nil {
		return absent
	}
	return ts.AsTime().In(loc).Format("2006-01-02")
}

// listTime is a time to the second in the person's zone, for the audit log.
func listTime(ts *timestamppb.Timestamp, loc *time.Location) string {
	if ts == nil {
		return "-"
	}
	return ts.AsTime().In(loc).Format("2006-01-02 15:04:05")
}

// newList makes the common part of every list.
func newList(c *ctx, heading string, cols []column, empty string) listScreen {
	return listScreen{base: base{c.newID()}, heading: heading, cols: cols, empty: empty}
}

// ---- tenants ----

// newTenantList makes the list of tenants. It refreshes itself on a timer, so
// a tenant whose database goes down shows its state and reason without a key
// being pressed.
func newTenantList(c *ctx) (screen, tea.Cmd) {
	l := newList(c, "Tenants", []column{{"NAME", 8, 40}, {"DB", 2, 8}, {"STATE", 8, 40}, {"CREATED", 19, 19}},
		"no tenant yet; \"magnetar tenant create\" makes one")
	l.live = true
	l.fetch = func(c *ctx, l listScreen, token string) tea.Cmd { return tenantsPage(c, l.id, l.gen, token) }
	l.open = func(c *ctx, _ listScreen, r row) tea.Cmd {
		t, _ := r.ref.(*v1.Tenant)
		s, cmd := newTenantDetail(c, t, false)
		return push(s, cmd)
	}
	l.note = func(r row) string {
		t, _ := r.ref.(*v1.Tenant)
		if t.GetState() == v1.TenantState_TENANT_STATE_READY {
			return ""
		}
		return t.GetName() + " is " + output.State(t.GetState()) + ": " + t.GetStateReason()
	}
	l.actions = []action{{key: "d", help: "remove", needsRow: true, do: func(c *ctx, _ screen, ref any) tea.Cmd {
		t, _ := ref.(*v1.Tenant)
		s, cmd := newRemoveTenant(c, t.GetName(), 1)
		return push(s, cmd)
	}}}
	nl, cmd := l.start(c)
	return nl, cmd
}

// newTenantDetail makes the screen of one tenant: its registration and state,
// with a key for each of its grants, settings and audit log. fetch is true when
// only the name is known and the rest must be asked for.
func newTenantDetail(c *ctx, t *v1.Tenant, fetch bool) (screen, tea.Cmd) {
	d := detailScreen{base: base{c.newID()}, heading: "Tenant " + clean(t.GetName()), ref: t}
	d.pairs = func(c *ctx, ref any) []pair {
		t, _ := ref.(*v1.Tenant)
		ps := []pair{
			{"Name", t.GetName()},
			{"Database", output.Database(t.GetDatabaseType())},
			{"State", output.State(t.GetState())},
		}
		if t.GetStateReason() != "" {
			ps = append(ps, pair{"Reason", t.GetStateReason()})
		}
		return append(ps, pair{"Created", output.Time(t.GetCreateTime(), c.loc)})
	}
	d.refetch = func(c *ctx, id int, ref any) tea.Cmd {
		t, _ := ref.(*v1.Tenant)
		return getTenant(c, id, t.GetName())
	}
	d.actions = []action{
		{key: "g", help: "grants", do: func(c *ctx, _ screen, ref any) tea.Cmd {
			t, _ := ref.(*v1.Tenant)
			s, cmd := newGrantList(c, grantFilter{scope: tenantScope(t.GetName())})
			return push(s, cmd)
		}},
		{key: "s", help: "settings", do: func(c *ctx, _ screen, ref any) tea.Cmd {
			t, _ := ref.(*v1.Tenant)
			s, cmd := newSettingList(c, tenantScope(t.GetName()))
			return push(s, cmd)
		}},
		{key: "a", help: "audit", do: func(c *ctx, _ screen, ref any) tea.Cmd {
			t, _ := ref.(*v1.Tenant)
			s, cmd := newAuditList(c, tenantScope(t.GetName()))
			return push(s, cmd)
		}},
		{key: "d", help: "remove", do: func(c *ctx, _ screen, ref any) tea.Cmd {
			t, _ := ref.(*v1.Tenant)
			s, cmd := newRemoveTenant(c, t.GetName(), 2)
			return push(s, cmd)
		}},
	}
	d.actions = append(d.actions, tenantActions(t.GetName())...)
	if fetch {
		d.loading = true
		return d, d.refetch(c, d.id, t)
	}
	return d, nil
}

// newRemoveTenant asks for the tenant's name to be typed before it is
// unregistered, and says what that does and does not do.
func newRemoveTenant(c *ctx, name string, pop int) (screen, tea.Cmd) {
	f := formScreen{
		base:    base{c.newID()},
		heading: "Remove tenant " + clean(name),
		intro: []string{
			"This unregisters tenant " + name + " from the installation.",
			"Nothing is deleted: its database and files stay where they are, and the program says where.",
			"Keys that were granted a role on it keep the grant on the record.",
		},
		fields: []formField{{label: "Type the name", hint: name}},
		want:   name,
		verb:   "remove",
		pop:    pop,
	}
	f.submit = func(c *ctx, f formScreen) (tea.Cmd, error) { return removeTenant(c, f.id, name), nil }
	return f, nil
}

// ---- keys ----

// newKeyList makes the list of keys.
func newKeyList(c *ctx) (screen, tea.Cmd) {
	l := newList(c, "Keys", []column{{"ID", 12, 14}, {"HOLDER", 6, 40}, {"STATUS", 6, 8}, {"CREATED", 10, 10}, {"EXPIRES", 7, 10}, {"LAST USED", 7, 10}},
		"no key yet; n makes one")
	l.fetch = func(c *ctx, l listScreen, token string) tea.Cmd { return keysPage(c, l.id, l.gen, token) }
	l.open = func(c *ctx, _ listScreen, r row) tea.Cmd {
		k, _ := r.ref.(*v1.Key)
		s, cmd := newKeyDetail(c, k, false)
		return push(s, cmd)
	}
	l.actions = []action{
		{key: "n", help: "new key", do: func(c *ctx, _ screen, _ any) tea.Cmd {
			s, cmd := newNewKey(c)
			return push(s, cmd)
		}},
		{key: "r", help: "revoke", needsRow: true, do: func(c *ctx, _ screen, ref any) tea.Cmd {
			k, _ := ref.(*v1.Key)
			s, cmd := newRevokeKey(c, k, 1)
			return push(s, cmd)
		}},
	}
	nl, cmd := l.start(c)
	return nl, cmd
}

// newKeyDetail makes the screen of one key, with its grants one key away.
func newKeyDetail(c *ctx, k *v1.Key, fetch bool) (screen, tea.Cmd) {
	d := detailScreen{base: base{c.newID()}, heading: "Key " + clean(k.GetId()), ref: k}
	d.pairs = func(c *ctx, ref any) []pair {
		k, _ := ref.(*v1.Key)
		never := func(ts *timestamppb.Timestamp, absent string) string {
			if ts == nil {
				return absent
			}
			return output.Time(ts, c.loc)
		}
		return []pair{
			{"ID", k.GetId()},
			{"Holder", k.GetHolder()},
			{"Status", output.KeyStatus(k, c.now())},
			{"Created", never(k.GetCreateTime(), "-")},
			{"Expires", never(k.GetExpireTime(), "never")},
			{"Last used", never(k.GetLastUseTime(), "never")},
			{"Revoked", never(k.GetRevokeTime(), "-")},
		}
	}
	d.refetch = func(c *ctx, id int, ref any) tea.Cmd {
		k, _ := ref.(*v1.Key)
		return getKey(c, id, k.GetId())
	}
	d.actions = []action{
		{key: "g", help: "grants", do: func(c *ctx, _ screen, ref any) tea.Cmd {
			k, _ := ref.(*v1.Key)
			s, cmd := newGrantList(c, grantFilter{keyID: k.GetId()})
			return push(s, cmd)
		}},
		{key: "r", help: "revoke", do: func(c *ctx, _ screen, ref any) tea.Cmd {
			k, _ := ref.(*v1.Key)
			s, cmd := newRevokeKey(c, k, 1)
			return push(s, cmd)
		}},
	}
	if fetch {
		d.loading = true
		return d, d.refetch(c, d.id, k)
	}
	return d, nil
}

// newNewKey makes the form that asks for a holder and an expiry. The key
// itself is not here: the answer replaces this form with the screen that shows
// it, so going back never finds a form with a holder typed in and nothing to
// show for it.
func newNewKey(c *ctx) (screen, tea.Cmd) {
	f := formScreen{
		base:    base{c.newID()},
		heading: "New key",
		intro:   []string{"A key is a credential for a person or a system. It has no rights until it is given a grant."},
		fields: []formField{
			{label: "Holder", hint: "who holds the key: a person or a system"},
			{label: "Expires", hint: "after 90d, 12h, 2w ... or never", in: field{}.set("90d")},
		},
		verb: "make the key",
	}
	f.submit = func(c *ctx, f formScreen) (tea.Cmd, error) {
		holder := f.value(0)
		if holder == "" || len(holder) > 256 {
			return nil, fault.New(fault.Invalid, "the holder is required and at most 256 bytes")
		}
		var expire time.Duration
		if e := f.value(1); !strings.EqualFold(e, "never") {
			d, err := output.ParseSpan(e)
			if err != nil {
				return nil, fault.New(fault.Invalid, "the expiry is not a length of time; use a number and s, m, h, d or w, such as 90d, or the word never")
			}
			expire = d
		}
		return createKey(c, f.id, holder, expire), nil
	}
	f.result = func(c *ctx, f formScreen, msg tea.Msg) (screen, tea.Cmd, bool) {
		made, ok := msg.(keyCreatedMsg)
		if !ok {
			return f, nil, false
		}
		s := newSecretScreen(c, made.key, made.secret)
		return f, func() tea.Msg { return replaceMsg{s: s} }, true
	}
	return f, nil
}

// newRevokeKey asks for the key's id to be typed before it is revoked.
func newRevokeKey(c *ctx, k *v1.Key, pop int) (screen, tea.Cmd) {
	id := k.GetId()
	f := formScreen{
		base:    base{c.newID()},
		heading: "Revoke key " + clean(id),
		intro: []string{
			"Revoke key " + output.KeyLabel(k) + ".",
			"From this moment a request with it is refused. There is no way to undo it; make a new key instead.",
		},
		fields: []formField{{label: "Type the key id", hint: id}},
		want:   id,
		verb:   "revoke",
		pop:    pop,
	}
	f.submit = func(c *ctx, f formScreen) (tea.Cmd, error) { return revokeKey(c, f.id, id), nil }
	return f, nil
}

// ---- grants ----

// roleChoices are the four roles a grant can give, in the order of the
// contract.
var roleChoices = []string{"reader", "writer", "operator", "admin"}

// roleFromChoice is the role a choice word stands for.
func roleFromChoice(word string) v1.Role {
	switch word {
	case "reader":
		return v1.Role_ROLE_READER
	case "writer":
		return v1.Role_ROLE_WRITER
	case "operator":
		return v1.Role_ROLE_OPERATOR
	case "admin":
		return v1.Role_ROLE_ADMIN
	}
	return v1.Role_ROLE_UNSPECIFIED
}

// newGrantList makes the list of the grants of a key or on a scope.
func newGrantList(c *ctx, f grantFilter) (screen, tea.Cmd) {
	heading := "Grants of key " + clean(f.keyID)
	if f.scope != nil {
		heading = "Grants on " + scopeName(f.scope)
	}
	l := newList(c, heading, []column{{"KEY", 12, 14}, {"SCOPE", 10, 40}, {"ROLE", 4, 8}}, "no grant")
	l.params = f
	l.fetch = func(c *ctx, l listScreen, token string) tea.Cmd {
		return grantsPage(c, l.id, l.gen, token, l.params.(grantFilter))
	}
	if f.scope != nil {
		// On a tenant's grants, enter opens the key that holds the grant,
		// which is where its holder is shown.
		l.open = func(c *ctx, _ listScreen, r row) tea.Cmd {
			g, _ := r.ref.(*v1.Grant)
			s, cmd := newKeyDetail(c, &v1.Key{Id: g.GetKeyId()}, true)
			return push(s, cmd)
		}
	}
	l.actions = []action{
		{key: "s", help: "set a role", do: func(c *ctx, _ screen, _ any) tea.Cmd {
			s, cmd := newSetGrant(c, f)
			return push(s, cmd)
		}},
		{key: "d", help: "remove", needsRow: true, do: func(c *ctx, _ screen, ref any) tea.Cmd {
			g, _ := ref.(*v1.Grant)
			s, cmd := newRemoveGrant(c, g)
			return push(s, cmd)
		}},
	}
	nl, cmd := l.start(c)
	return nl, cmd
}

// newSetGrant makes the form that gives a key a role: the scope is asked for
// when the list is a key's, and the key when it is a scope's.
func newSetGrant(c *ctx, filter grantFilter) (screen, tea.Cmd) {
	first := formField{label: "Key id", hint: "the id from the key list, not the key itself", keyID: true}
	heading := "Set a role on " + scopeName(filter.scope)
	if filter.scope == nil {
		first = formField{label: "Scope", hint: "the word installation, or a tenant's name", in: field{}.set("installation")}
		heading = "Set a role for key " + clean(filter.keyID)
	}
	f := formScreen{
		base:    base{c.newID()},
		heading: heading,
		fields:  []formField{first, {label: "Role", choices: roleChoices}},
		verb:    "set the role",
	}
	f.submit = func(c *ctx, f formScreen) (tea.Cmd, error) {
		keyID, scope := filter.keyID, filter.scope
		if scope == nil {
			if f.value(0) == "" {
				return nil, fault.New(fault.Invalid, "the scope is required")
			}
			scope = scopeFromText(f.value(0))
		} else {
			keyID = f.value(0)
			if err := output.CheckKeyID(keyID); err != nil {
				return nil, err
			}
		}
		return setGrant(c, f.id, keyID, scope, roleFromChoice(f.value(1))), nil
	}
	return f, nil
}

// newRemoveGrant asks for the key's id to be typed before a grant is taken
// back.
func newRemoveGrant(c *ctx, g *v1.Grant) (screen, tea.Cmd) {
	f := formScreen{
		base:    base{c.newID()},
		heading: "Remove a grant",
		intro: []string{
			"Take away the " + output.Role(g.GetRole()) + " role of key " + g.GetKeyId() + " on " + output.Scope(g.GetScope()) + ".",
			"The key keeps working for whatever its other grants allow.",
		},
		fields: []formField{{label: "Type the key id", hint: g.GetKeyId()}},
		want:   g.GetKeyId(),
		verb:   "remove",
	}
	f.submit = func(c *ctx, f formScreen) (tea.Cmd, error) {
		return removeGrant(c, f.id, g.GetKeyId(), g.GetScope()), nil
	}
	return f, nil
}

// ---- settings ----

// newSettingList makes the list of the settings set on a scope.
func newSettingList(c *ctx, scope *v1.Scope) (screen, tea.Cmd) {
	l := newList(c, "Settings of "+scopeName(scope), []column{{"NAME", 8, 40}, {"VALUE", 5, 60}, {"FROM", 4, 30}},
		"nothing is set here")
	l.params = scope
	l.fetch = func(c *ctx, l listScreen, token string) tea.Cmd {
		return settingsPage(c, l.id, l.gen, token, l.params.(*v1.Scope))
	}
	l.actions = []action{{key: "s", help: "set", do: func(c *ctx, _ screen, _ any) tea.Cmd {
		s, cmd := newSetSetting(c, scope)
		return push(s, cmd)
	}}}
	// A value set on either scope can be unset: a tenant's override, or the
	// installation's value, after which the built-in default applies.
	l.actions = append(l.actions, action{key: "u", help: "unset", needsRow: true, do: func(c *ctx, _ screen, ref any) tea.Cmd {
		st, _ := ref.(*v1.Setting)
		s, cmd := newUnsetSetting(c, st.GetName(), scope)
		return push(s, cmd)
	}})
	nl, cmd := l.start(c)
	return nl, cmd
}

// newSetSetting makes the form that sets a setting on a scope.
func newSetSetting(c *ctx, scope *v1.Scope) (screen, tea.Cmd) {
	f := formScreen{
		base:    base{c.newID()},
		heading: "Set a setting on " + scopeName(scope),
		fields: []formField{
			{label: "Name", hint: "such as key.default-lifetime"},
			{label: "Value"},
		},
		verb: "set",
	}
	f.submit = func(c *ctx, f formScreen) (tea.Cmd, error) {
		// The value may be empty: whether a setting takes an empty value is
		// the setting's own rule, which the server applies.
		if f.value(0) == "" {
			return nil, fault.New(fault.Invalid, "the name is required")
		}
		return setSetting(c, f.id, f.value(0), scope, f.value(1)), nil
	}
	return f, nil
}

// newUnsetSetting asks for the setting's name to be typed before the value
// set on a scope is removed.
func newUnsetSetting(c *ctx, name string, scope *v1.Scope) (screen, tea.Cmd) {
	f := formScreen{
		base:    base{c.newID()},
		heading: "Unset " + clean(name),
		intro:   []string{unsetIntro(scope, name)},
		fields:  []formField{{label: "Type the name", hint: name}},
		want:    name,
		verb:    "unset",
	}
	f.submit = func(c *ctx, f formScreen) (tea.Cmd, error) { return unsetSetting(c, f.id, name, scope), nil }
	return f, nil
}

// unsetIntro says what unsetting name on scope does.
func unsetIntro(scope *v1.Scope, name string) string {
	if scope.GetTenant() != "" {
		return "Remove " + scopeName(scope) + "'s override of " + name + "; what applies on the installation applies again."
	}
	return "Remove the installation's value of " + name + "; the built-in default applies again."
}

// ---- audit ----

// outcomeChoices are the words of the audit filter's outcome; "any" asks for
// every outcome.
var outcomeChoices = []string{"any", "done", "refused", "failed"}

// outcomeFromChoice is the outcome a choice word stands for.
func outcomeFromChoice(word string) v1.AuditOutcome {
	switch word {
	case "done":
		return v1.AuditOutcome_AUDIT_OUTCOME_DONE
	case "refused":
		return v1.AuditOutcome_AUDIT_OUTCOME_REFUSED
	case "failed":
		return v1.AuditOutcome_AUDIT_OUTCOME_FAILED
	}
	return v1.AuditOutcome_AUDIT_OUTCOME_UNSPECIFIED
}

// auditHeading words the heading of an audit list with its filter.
func auditHeading(f auditFilter) string {
	h := "Audit log of " + scopeName(f.scope)
	if f.outcome != v1.AuditOutcome_AUDIT_OUTCOME_UNSPECIFIED {
		h += ", " + output.Outcome(f.outcome) + " only"
	}
	if f.keyID != "" {
		h += ", key " + clean(f.keyID)
	}
	return h
}

// newAuditList makes the audit log of a scope, newest first. f filters it; the
// filter key opens a form that sends the list a filterSetMsg.
func newAuditList(c *ctx, scope *v1.Scope) (screen, tea.Cmd) {
	f := auditFilter{scope: scope}
	l := newList(c, auditHeading(f), []column{{"TIME", 19, 19}, {"OUTCOME", 7, 7}, {"KEY", 6, 14}, {"METHOD", 10, 30}, {"OBJECT", 8, 30}}, "no entry")
	l.params = f
	l.fetch = func(c *ctx, l listScreen, token string) tea.Cmd {
		return auditPage(c, l.id, l.gen, token, l.params.(auditFilter))
	}
	l.open = func(c *ctx, _ listScreen, r row) tea.Cmd {
		e, _ := r.ref.(*v1.AuditEntry)
		return push(newAuditDetail(c, e), nil)
	}
	l.onMsg = func(c *ctx, l listScreen, msg tea.Msg) (listScreen, tea.Cmd, bool) {
		set, ok := msg.(filterSetMsg)
		if !ok {
			return l, nil, false
		}
		f := l.params.(auditFilter)
		f.outcome, f.keyID = set.outcome, set.keyID
		l.params, l.heading = f, auditHeading(f)
		nl, cmd := l.start(c)
		return nl, cmd, true
	}
	l.actions = []action{{key: "f", help: "filter by outcome and key", do: func(c *ctx, self screen, _ any) tea.Cmd {
		cur, _ := self.(listScreen)
		return push(newAuditFilter(c, cur.id, cur.params.(auditFilter)), nil)
	}}}
	nl, cmd := l.start(c)
	return nl, cmd
}

// newAuditFilter makes the form that asks which outcome and which key's entries
// to show, and tells the list with the given id.
func newAuditFilter(c *ctx, listID int, cur auditFilter) screen {
	sel := 0
	for i, w := range outcomeChoices {
		if outcomeFromChoice(w) == cur.outcome {
			sel = i
		}
	}
	f := formScreen{
		base:    base{c.newID()},
		heading: "Filter the audit log",
		fields: []formField{
			{label: "Outcome", choices: outcomeChoices, sel: sel},
			{label: "Key id", hint: "leave empty for every key", in: field{}.set(cur.keyID), keyID: true},
		},
		verb: "filter",
	}
	f.submit = func(_ *ctx, f formScreen) (tea.Cmd, error) {
		keyID := f.value(1)
		if keyID != "" {
			if err := output.CheckKeyID(keyID); err != nil {
				return nil, err
			}
		}
		set := filterSetMsg{outcome: outcomeFromChoice(f.value(0)), keyID: keyID}
		return tea.Batch(back(false, ""), func() tea.Msg { return deliverMsg{to: listID, msg: set} }), nil
	}
	return f
}

// newAuditDetail makes the screen of one entry, whole: its reason, its details
// and the request id, which a person reports.
func newAuditDetail(c *ctx, e *v1.AuditEntry) screen {
	d := detailScreen{base: base{c.newID()}, heading: "Audit entry " + clean(e.GetId()), ref: e}
	d.pairs = func(c *ctx, ref any) []pair {
		e, _ := ref.(*v1.AuditEntry)
		ps := []pair{
			{"ID", e.GetId()},
			{"Time", output.Time(e.GetTime(), c.loc)},
			{"Caller", output.Who(e)},
			{"From", e.GetClientAddress()},
			{"Method", output.Method(e.GetMethod())},
			{"Tenant", e.GetTenant()},
			{"Object", e.GetObject()},
			{"Outcome", output.Outcome(e.GetOutcome())},
			{"Reason", e.GetReason()},
			{"Request id", e.GetRequestId()},
		}
		names := make([]string, 0, len(e.GetDetails()))
		for k := range e.GetDetails() {
			names = append(names, k)
		}
		sort.Strings(names)
		for i, k := range names {
			label := ""
			if i == 0 {
				label = "Details"
			}
			ps = append(ps, pair{label, k + " = " + e.GetDetails()[k]})
		}
		for i := range ps {
			if ps[i].value == "" {
				ps[i].value = "-"
			}
		}
		return ps
	}
	return d
}
