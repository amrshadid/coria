package access

import (
	"context"
	"crypto/sha256"
	"maps"
	"strconv"
	"unicode/utf8"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/secret"
)

// The messages a refused caller is shown. Each is one fixed text for its
// kind, whatever the real cause, so the answer cannot be used to tell an
// unknown key from a revoked one, or a tenant that does not exist from one
// the key has no grant on (access.proto, "What a caller may do"). The real
// cause is in the audit entry's reason.
const (
	// MessageUnauthenticated answers every credential that is no good.
	MessageUnauthenticated = "the request carries no valid key"
	// MessageDenied answers a good key with no sufficient grant, and a good
	// key naming a tenant that does not exist when it may not list every
	// tenant.
	MessageDenied = "the key may not do this"
	// MessageNoTenant answers a caller who may list every tenant and names
	// one that does not exist (D37).
	MessageNoTenant = "no tenant has this name"
)

// The reasons a refusal's audit entry records. They are for the admin who
// reads the log and never reach the refused caller.
const (
	reasonNoKey        = "no key"
	reasonMalformed    = "malformed key"
	reasonUnknownKey   = "unknown key"
	reasonWrongSecret  = "wrong secret"
	reasonRevoked      = "key revoked"
	reasonExpired      = "key expired"
	reasonNoLocalUser  = "local caller without a user id"
	reasonNoGrant      = "no sufficient grant"
	reasonNoTenant     = "no such tenant"
	reasonBadCheck     = "server error: the check was called without a valid requirement or method"
	reasonStoreFailure = "the master database failed during the check"
)

// maxGrantPages bounds how many pages of a key's grants WhoAmI reads: a
// thousand grants a page, so a hundred thousand grants, beyond any
// installation's tenants. A store that keeps returning a cursor is a broken
// store, and the call fails rather than loop.
const maxGrantPages = 100

// absentTenantID is the tenant id a check asks RolesFor about when the
// tenant the request named does not exist. Asking about some tenant, rather
// than the installation only, makes the store run the same statement as for
// a tenant that exists, so a missing tenant and a missing grant cost the
// same. The role it returns is never used: the tenant is already known to
// be missing. "-" is a valid id that the server, which makes tenant ids, does
// not give.
const absentTenantID = "-"

// unknownKeyHash is what the secret of a key with an unknown id is compared
// with, so that answering an unknown id costs the same hash and comparison
// as answering a wrong secret, and the time of the answer does not tell the
// two apart.
var unknownKeyHash [sha256.Size]byte

// Credential is what a request presents: a key, the local operator, or
// nothing. The zero Credential is a request with no key.
type Credential struct {
	key       string
	localUser string
	local     bool
}

// APIKey is a credential of a presented key, mgt_<id>_<secret>, as it came
// in the request. The empty string is a request with no key.
func APIKey(presented string) Credential {
	return Credential{key: presented}
}

// LocalOperator is the credential of a request on the installation's local
// socket, made by the operating system user with this id (a uid, or a SID on
// Windows). The server calls it only after the socket has shown the caller
// is the user that runs the server (M0 design note, part 5); this package
// trusts that and holds the local operator to be admin everywhere. An empty
// user id is refused as unauthenticated.
func LocalOperator(userID string) Credential {
	return Credential{localUser: userID, local: true}
}

// String prints which sort of credential it is and never the key, so a
// credential that reaches a log by mistake carries no secret.
func (c Credential) String() string {
	switch {
	case c.local:
		return "access.LocalOperator(" + strconv.Quote(c.localUser) + ")"
	case c.key == "":
		return "access.Credential(none)"
	}
	return "access.Credential(key)"
}

// GoString is String, for the %#v verb.
func (c Credential) GoString() string { return c.String() }

// requirementKind is what a Requirement asks for.
type requirementKind int

const (
	// needInvalid is the zero Requirement, which no caller meets: a
	// handler that forgot to set one fails closed.
	needInvalid requirementKind = iota
	needAuthenticated
	needInstallation
	needTenant
	needTenantRegistration
)

// Requirement is what a request needs of its caller. The zero Requirement is
// met by nobody.
type Requirement struct {
	kind   requirementKind
	role   master.Role
	tenant string
}

// Authenticated requires only a good credential: WhoAmI and ListTenants,
// whose answers the caller's grants then shape.
func Authenticated() Requirement {
	return Requirement{kind: needAuthenticated}
}

// OnInstallation requires at least role on the installation. Only a grant
// on the installation meets it, or the local operator.
func OnInstallation(role master.Role) Requirement {
	return Requirement{kind: needInstallation, role: role}
}

// OnTenant requires at least role on the tenant named tenantName, as the
// request named it. Only a grant on that tenant meets it, or the local
// operator: a role on the installation gives nothing inside a tenant
// (access.proto, Role).
func OnTenant(tenantName string, role master.Role) Requirement {
	return Requirement{kind: needTenant, role: role, tenant: tenantName}
}

// TenantRegistration requires the right to read one tenant's registration
// and state, as GetTenant does: reader or more on that tenant, or reader or
// more on the installation, or the local operator (tenant.proto,
// GetTenant).
func TenantRegistration(tenantName string) Requirement {
	return Requirement{kind: needTenantRegistration, role: master.RoleReader, tenant: tenantName}
}

// namesTenant reports whether r names a tenant the check must look up.
func (r Requirement) namesTenant() bool {
	return r.kind == needTenant || r.kind == needTenantRegistration
}

// valid reports whether r is one a constructor above made.
func (r Requirement) valid() bool {
	switch r.kind {
	case needAuthenticated:
		return true
	case needInstallation, needTenant, needTenantRegistration:
		return r.role.Valid()
	}
	return false
}

// Request is what the check needs to know of the request for its audit
// entry. The server fills it from the request; none of it is trusted, so each
// text is cut to what an audit entry holds (see clip). Method is required.
type Request struct {
	// Method is the API method or command, such as
	// "/magnetar.v1.AccessService/CreateKey".
	Method string
	// RemoteAddress is where the request came from; empty on the local
	// socket.
	RemoteAddress string
	// RequestID is the id the server made for the request.
	RequestID string
	// Object is what the request is about, such as a key's id.
	Object string
	// Details are what else the entry needs, never a secret. At most
	// MaxRequestDetails pairs, so the master can still add a grant's old and
	// new role.
	Details map[string]string
}

// MaxRequestDetails bounds Request.Details: the master's bound less the two
// details a change of grant adds (master.AuditEntry.WithRoleChange).
const MaxRequestDetails = master.MaxAuditDetails - 2

// Principal is who made a request, with the roles the check read. Only
// Access.Check makes one; the zero Principal is nobody and holds nothing. A
// key's principal knows its role on the installation and, when the
// requirement named an existing tenant, its role there; not its other
// grants, which the check does not read (WhoAmI does).
type Principal struct {
	key        master.Key
	local      string
	isKey      bool
	isLoc      bool
	instRole   master.Role
	tenantID   string
	tenantRole master.Role
}

// IsLocalOperator reports whether the caller is the local operator.
func (p Principal) IsLocalOperator() bool { return p.isLoc }

// LocalUser returns the local operator's operating system user id, or "".
func (p Principal) LocalUser() string { return p.local }

// Key returns the caller's key, without its hash, and whether the caller is
// a key.
func (p Principal) Key() (master.Key, bool) {
	k := p.key
	k.Hash = nil
	return k, p.isKey
}

// KeyID returns the caller's key id, or "" for the local operator.
func (p Principal) KeyID() string { return p.key.ID }

// Role returns the role the caller holds on scope, or "" for none or for a
// tenant the check did not read. The local operator holds admin on every
// scope. A grant on the installation is not a role on a tenant.
func (p Principal) Role(scope master.Scope) master.Role {
	switch {
	case p.isLoc:
		return master.RoleAdmin
	case !p.isKey:
		return ""
	case scope.IsInstallation():
		return p.instRole
	case p.tenantID != "" && scope.TenantID == p.tenantID:
		return p.tenantRole
	}
	return ""
}

// CanListTenants reports whether the caller may list every tenant: the
// local operator, or a key with reader or more on the installation (D37).
// Such a caller is told NotFound for a tenant that does not exist.
func (p Principal) CanListTenants() bool {
	return atLeast(p.Role(master.Installation), master.RoleReader)
}

// rank orders the roles: each includes the ones before it.
func rank(r master.Role) int {
	switch r {
	case master.RoleReader:
		return 1
	case master.RoleWriter:
		return 2
	case master.RoleOperator:
		return 3
	case master.RoleAdmin:
		return 4
	}
	return 0
}

// atLeast reports whether have includes need. No role includes an unknown
// one, and the empty role includes nothing.
func atLeast(have, need master.Role) bool {
	n := rank(need)
	return n > 0 && rank(have) >= n
}

// Decision is what Check allowed: the caller, the tenant the requirement
// named, and the audit entry of the request for the caller to write. Only
// Check makes one; the zero Decision carries no principal and an entry the
// master refuses, so a handler that skipped the check cannot record a change.
type Decision struct {
	principal Principal
	tenant    master.Tenant
	hasTenant bool
	entry     master.AuditEntry
}

// Principal returns who made the request.
func (d Decision) Principal() Principal { return d.principal }

// Tenant returns the tenant the requirement named, as the master holds it,
// and whether there was one. Its state is not checked here: whether a tenant
// that is not ready can serve a request is the handler's to decide.
func (d Decision) Tenant() (master.Tenant, bool) { return d.tenant, d.hasTenant }

// Entry returns the request's audit entry with its outcome and reason, for
// the caller to write with its change (a change and its entry are one
// transaction) or to append after a read. Each call returns a copy with its
// own details map, so adding details to it changes nothing else. The entry
// has one id, so write it once.
func (d Decision) Entry(outcome master.Outcome, reason string) master.AuditEntry {
	e := d.entry
	e.Details = maps.Clone(d.entry.Details)
	e.Outcome = outcome
	e.Reason = clip(reason, master.MaxAuditReasonLength)
	return e
}

// Check decides whether the caller cred may make a request that needs req,
// and is the only way to get a Principal. Its steps, in the order of the API
// contract:
//
//  1. The credential: the key is parsed, read by its id, its secret compared
//     in constant time, and refused when revoked or expired; or it is the
//     local operator. Any failure is Unauthenticated with
//     MessageUnauthenticated.
//  2. When req names a tenant, it is read by name.
//  3. The key's roles on the installation and on that tenant are read with
//     one RolesFor, every time: there is no cache, so a grant removed is
//     gone on the next request.
//  4. A missing tenant is NotFound with MessageNoTenant for a caller who may
//     list every tenant, and otherwise Denied with MessageDenied, exactly as
//     for an existing tenant without a grant: both make the calls Key,
//     TenantByName, RolesFor, in that order, and queue one audit entry.
//  5. The role is compared with req; too little is Denied.
//
// A refusal's audit entry is handed to the audit writer with its real
// cause; it is not written on the request's path. When the request is
// allowed, the entry is in the Decision: the caller writes it with its
// change, or hands it to Record after a read. A key that passes step 1 and
// was not marked used within the minute has its last use marked, off the
// request's path. A store that fails is Unavailable or Internal, never a
// refusal.
func (a *Access) Check(ctx context.Context, cred Credential, req Requirement, facts Request) (Decision, error) {
	entry := a.newEntry(facts)

	if entry.ID == "" || entry.Method == "" || !req.valid() || len(facts.Details) > MaxRequestDetails {
		// A server bug, not the caller's doing: it fails, and is recorded if
		// the entry can be. The audit id may itself be the bug, so an entry
		// without one is not attempted.
		err := fault.New(fault.Internal, "internal error")
		if entry.ID != "" {
			if entry.Method == "" {
				entry.Method = "unknown"
			}
			entry.Details = nil
			a.record(entry, master.OutcomeFailed, reasonBadCheck)
		}
		return Decision{}, err
	}
	if req.namesTenant() {
		entry.TenantName = clipTenantName(req.tenant)
	}

	p, reason, err := a.authenticate(ctx, cred, &entry)
	if err != nil {
		return Decision{}, a.fail(entry, err)
	}
	if reason != "" {
		a.record(entry, master.OutcomeRefused, reason)
		return Decision{}, fault.New(fault.Unauthenticated, MessageUnauthenticated)
	}

	d := Decision{principal: p}
	var tenantMissing bool
	if req.namesTenant() {
		t, err := a.store.TenantByName(ctx, req.tenant)
		switch kind := fault.KindOf(err); {
		case err == nil:
			d.tenant, d.hasTenant = t, true
		case kind == fault.NotFound || kind == fault.Invalid:
			// A name the master refuses as invalid names no tenant either:
			// it is answered as missing, after the same one call, so a
			// caller cannot use the shape of names to learn anything.
			tenantMissing = true
		default:
			return Decision{}, a.fail(entry, err)
		}
	}
	if p.isKey {
		tenantID := ""
		switch {
		case d.hasTenant:
			tenantID = d.tenant.ID
		case tenantMissing:
			tenantID = absentTenantID
		}
		inst, ten, err := a.store.RolesFor(ctx, p.key.ID, tenantID)
		if err != nil {
			return Decision{}, a.fail(entry, err)
		}
		p.instRole = inst
		if d.hasTenant {
			p.tenantID, p.tenantRole = d.tenant.ID, ten
		}
		d.principal = p
	}

	switch {
	case tenantMissing && p.CanListTenants():
		a.record(entry, master.OutcomeFailed, reasonNoTenant)
		return Decision{}, fault.New(fault.NotFound, MessageNoTenant)
	case tenantMissing:
		a.record(entry, master.OutcomeRefused, reasonNoTenant)
		return Decision{}, fault.New(fault.Denied, MessageDenied)
	case !allows(p, req, d.tenant):
		a.record(entry, master.OutcomeRefused, reasonNoGrant)
		return Decision{}, fault.New(fault.Denied, MessageDenied)
	}
	d.entry = entry
	return d, nil
}

// allows reports whether p meets req, given the tenant req named (read by
// Check, and existing).
func allows(p Principal, req Requirement, t master.Tenant) bool {
	switch req.kind {
	case needAuthenticated:
		return p.isKey || p.isLoc
	case needInstallation:
		return atLeast(p.Role(master.Installation), req.role)
	case needTenant:
		return atLeast(p.Role(master.TenantScope(t.ID)), req.role)
	case needTenantRegistration:
		return atLeast(p.Role(master.TenantScope(t.ID)), master.RoleReader) || p.CanListTenants()
	}
	return false
}

// authenticate turns cred into a principal. It returns a non-empty reason
// when the credential is refused, and an error only when the store failed.
// It sets the entry's key id or local user as soon as either is known, so a
// refusal of a key with a known shape records which key it was
// (audit.proto, AuditEntry.key_id).
func (a *Access) authenticate(ctx context.Context, cred Credential, entry *master.AuditEntry) (Principal, string, error) {
	if cred.local {
		entry.LocalUser = clip(cred.localUser, master.MaxAuditFieldLength)
		if cred.localUser == "" || entry.LocalUser != cred.localUser {
			return Principal{}, reasonNoLocalUser, nil
		}
		return Principal{local: cred.localUser, isLoc: true}, "", nil
	}
	if cred.key == "" {
		return Principal{}, reasonNoKey, nil
	}
	id, sec, err := secret.ParseAPIKey(cred.key)
	if err != nil {
		return Principal{}, reasonMalformed, nil
	}
	entry.KeyID = id
	k, err := a.store.Key(ctx, id)
	if fault.KindOf(err) == fault.NotFound {
		secret.VerifyAPISecret(sec, unknownKeyHash[:])
		return Principal{}, reasonUnknownKey, nil
	}
	if err != nil {
		return Principal{}, "", err
	}
	// The secret first: a caller who does not hold it learns nothing more,
	// and the log does not record "revoked" for a guess.
	if !secret.VerifyAPISecret(sec, k.Hash) {
		return Principal{}, reasonWrongSecret, nil
	}
	if !k.Revoked.IsZero() {
		return Principal{}, reasonRevoked, nil
	}
	if !k.Expires.IsZero() && !entry.Time.Before(k.Expires) {
		return Principal{}, reasonExpired, nil
	}
	a.queueMark(k, entry.Time)
	k.Hash = nil
	return Principal{key: k, isKey: true}, "", nil
}

// allGrants reads every grant of a key, page by page.
func (a *Access) allGrants(ctx context.Context, keyID string) ([]master.Grant, error) {
	var out []master.Grant
	cursor := ""
	for range maxGrantPages {
		page, next, err := a.store.GrantsByKey(ctx, keyID, master.Page{Cursor: cursor, Limit: master.MaxPageLimit})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
	return nil, fault.New(fault.Internal, "access: a key's grants did not end after "+strconv.Itoa(maxGrantPages)+" pages")
}

// newEntry starts a request's audit entry from its facts, with a fresh id
// and the time of the check. Every text is clipped to the master's bounds:
// the facts come from the request, and an entry the master refuses would be
// a refusal without a record. An id the id source gets wrong is left empty,
// which Check fails as Internal.
func (a *Access) newEntry(facts Request) master.AuditEntry {
	id := a.newID()
	if master.ValidateID("audit entry id", id) != nil {
		id = ""
	}
	e := master.AuditEntry{
		ID:            id,
		Time:          a.clock(),
		RemoteAddress: clip(facts.RemoteAddress, master.MaxAuditFieldLength),
		Method:        clip(facts.Method, master.MaxAuditFieldLength),
		Object:        clip(facts.Object, master.MaxAuditFieldLength),
		RequestID:     clip(facts.RequestID, master.MaxAuditFieldLength),
	}
	if len(facts.Details) > 0 {
		e.Details = make(map[string]string, len(facts.Details))
		for k, v := range facts.Details {
			k = clip(k, master.MaxAuditDetailKeyLength)
			if k == "" {
				continue
			}
			e.Details[k] = clip(v, master.MaxAuditDetailValueLength)
		}
	}
	return e
}

// record hands a refusal's or a failure's audit entry to the audit writer.
// The request already has its answer, and the answer to a refused caller
// must not depend on whether the log took it, nor wait for a synced commit:
// a flood of bad keys would otherwise hold the master's one writer for
// everyone.
func (a *Access) record(e master.AuditEntry, outcome master.Outcome, reason string) {
	e.Outcome = outcome
	e.Reason = clip(reason, master.MaxAuditReasonLength)
	a.enqueueAudit(e)
}

// fail turns a store's failure during a check into the caller's error, and
// records it. A master that does not answer is Unavailable, as the contract
// says ("a database that does not answer is a failure, not a refusal"); any
// other failure is Internal, whose message says nothing of the cause.
func (a *Access) fail(e master.AuditEntry, err error) error {
	a.record(e, master.OutcomeFailed, reasonStoreFailure+": "+fault.KindOf(err).String())
	if fault.KindOf(err) == fault.Unavailable {
		return fault.Wrap(fault.Unavailable, err, "the master database does not answer")
	}
	return fault.Wrap(fault.Internal, err, "internal error")
}

// clip returns s as an audit entry can hold it: valid UTF-8 with no NUL
// byte, of at most max bytes, cut at a character's boundary. A field of an
// audit entry that broke the master's bounds would make the whole entry
// refused, and the request unrecorded.
func clip(s string, max int) string {
	ok := len(s) <= max && utf8.ValidString(s)
	for i := 0; ok && i < len(s); i++ {
		ok = s[i] != 0
	}
	if ok {
		return s
	}
	b := make([]byte, 0, min(len(s), max))
	for _, r := range s {
		if r == 0 {
			continue
		}
		// An invalid byte reads as utf8.RuneError and is written as its
		// three-byte replacement character.
		if len(b)+utf8.RuneLen(r) > max {
			break
		}
		b = utf8.AppendRune(b, r)
	}
	return string(b)
}

// clipTenantName returns a tenant's name as the request gave it, for the
// audit entry (audit.proto: kept as given, also when no tenant has it). A
// name that could not be a tenant's is quoted in ASCII, so it cannot pass
// for one in the log, and clipped.
func clipTenantName(name string) string {
	if master.ValidateTenantName(name) == nil {
		return name
	}
	return clip(strconv.QuoteToASCII(name), master.MaxAuditFieldLength)
}
