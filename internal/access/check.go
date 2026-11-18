package access

import (
	"context"
	"crypto/sha256"
	"maps"
	"strconv"
	"time"

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
	// one that does not exist: such a caller learns nothing new from it, and
	// an operator who mistypes a name is told so.
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
	reasonNoLocalUser  = "local caller without a usable user id"
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

// APIKey is a credential of a presented key, mgt_<id>_<secret>. The server
// takes it from the Authorization header, strips the "Bearer" scheme and the
// space after it (the scheme compared case-insensitively, RFC 6750 2.1), and
// passes only the key: anything else, such as the scheme left in, is a
// malformed key. The empty string is a request with no key.
func APIKey(presented string) Credential {
	return Credential{key: presented}
}

// LocalOperator is the credential of a request on the installation's local
// socket, made by the operating system user with this id (a uid, or a SID on
// Windows). It makes its holder admin on the installation and on every
// tenant, so only the server may call it, in package internal/server, and
// only after the socket has shown the caller is the user that runs the
// server: whoever that user is can read the installation's files anyway. A
// test of this package fails if any other package calls it. An empty user id,
// or one holding a control character or longer than an audit field, is
// refused as unauthenticated.
func LocalOperator(userID string) Credential {
	return Credential{localUser: userID, local: true}
}

// String prints which sort of credential it is and never the key, so a
// credential that reaches a log by mistake carries no secret.
func (c Credential) String() string {
	switch {
	case c.local:
		return "access.LocalOperator(" + strconv.QuoteToASCII(c.localUser) + ")"
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
// text is escaped and cut to what an audit entry holds (see auditText).
// Method is required.
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
	// new role, and the audit writer a note that a tenant's log did not
	// answer.
	Details map[string]string
}

// MaxRequestDetails bounds Request.Details: the master's bound less the two
// details a change of grant adds (master.AuditEntry.WithRoleChange) and the
// one the audit writer adds to an entry it moves to the master's log.
const MaxRequestDetails = master.MaxAuditDetails - 3

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

// Key returns the caller's key, without its hash, as the check read it, and
// whether the caller is a key.
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

// CanListTenants reports whether the caller may list every tenant: the local
// operator, or a key with reader or more on the installation. Such a caller
// is told NotFound for a tenant that does not exist: it could list the
// tenants anyway, so the answer reveals nothing (access.proto, "A tenant that
// does not exist").
func (p Principal) CanListTenants() bool {
	return atLeast(p.Role(master.Installation), master.RoleReader)
}

// atLeast reports whether have includes need, by the one order of roles
// the master states (master.Role.Includes), the order every change's own
// check of its actor uses too. No role includes an unknown one, and the
// empty role includes nothing.
func atLeast(have, need master.Role) bool {
	return have.Includes(need)
}

// TenantRef names the tenant a requirement named: its id and its name, as
// the check read them, and nothing else. A handler that needs the tenant's
// state, database or any other field reads the tenant itself.
type TenantRef struct {
	// ID is the tenant's id at the time of the check.
	ID string
	// Name is the tenant's name, as the request gave it.
	Name string
}

// Decision is what Check allowed: the caller, the tenant the requirement
// named, and the facts of the request for its audit entries. Only Check
// makes one, and no other package can make or alter one: the fields are
// unexported and the zero Decision carries no caller, so a handler that
// skipped the check can neither call a function of this package that takes
// a Decision nor make an audit entry the master takes.
//
// A Decision is for one request. It is not a session: every change made
// for a key carries the key as its Actor, and the master checks it again in
// the change's transaction; every read this package makes for a key checks
// it again first. So a key revoked since the check is refused even with a
// Decision in hand.
type Decision struct {
	principal Principal
	tenant    TenantRef
	hasTenant bool
	entry     master.AuditEntry
	newID     func() string
	clock     func() time.Time
}

// Actor returns who makes a change for d, as the master's changes take it
// (master.Actor), needing role on scope: the caller's key, with the time of
// this call as the time its expiry is judged at, or the local operator, whom
// no store refuses. The zero Decision gives the zero Actor, which every
// change refuses as Invalid, so a Decision that did not come from Check
// opens nothing here either. A grant on the installation is not a role on a
// tenant: scope is the scope the change needs, as the requirement Check
// allowed named it.
func (d Decision) Actor(scope master.Scope, role master.Role) master.Actor {
	switch {
	case !d.checked() || d.clock == nil:
		return master.Actor{}
	case d.principal.isLoc:
		return master.LocalOperator()
	}
	return master.ActingKey(d.principal.key.ID, scope, role, d.clock())
}

// Principal returns who made the request.
func (d Decision) Principal() Principal { return d.principal }

// checked reports whether d is a Decision Check made: one with a caller and
// an id source. The zero Decision, all another package can make, is not.
func (d Decision) checked() bool {
	return d.newID != nil && (d.principal.isKey || d.principal.isLoc)
}

// Tenant returns the tenant the requirement named, its id and name only, and
// whether there was one. Its state is not checked here: whether a tenant
// that is not ready can serve a request is the handler's to decide, from the
// tenant it reads.
func (d Decision) Tenant() (TenantRef, bool) { return d.tenant, d.hasTenant }

// Entry returns an audit entry of the request with its outcome and reason,
// for the caller to write with its change (a change and its entry are one
// transaction), in the log the change concerns: the tenant's for a change
// inside a tenant, the master's for the installation. facts adds what the
// handler learned after the check (Facts); the identity of the request is the
// check's. Each call makes a new entry with its own fresh id and its own
// details map, so a handler that reads and then changes writes two entries
// and loses neither; all of a request's entries share its request id. A
// read's entry goes to Record or RecordTo instead.
func (d Decision) Entry(outcome master.Outcome, reason string, facts Facts) master.AuditEntry {
	e := d.entry
	if d.newID != nil {
		e.ID = d.newID()
	}
	e.Details = maps.Clone(d.entry.Details)
	e.Outcome = outcome
	e.Reason = auditText(reason, master.MaxAuditReasonLength)
	return facts.apply(e)
}

// Check decides whether the caller cred may make a request that needs req,
// and is the only way to get a Decision. The server calls it once for every
// request. Its steps, in the order of the API contract:
//
//  1. The credential: the key is parsed, read by its id, its secret compared
//     in constant time, and refused when revoked or expired; or it is the
//     local operator. Any failure is Unauthenticated with
//     MessageUnauthenticated.
//  2. For a key, one RolesFor reads its role on the installation and, when
//     req names a tenant, whether that tenant exists, its id, and the key's
//     role there, by the name as the request gave it, every time: there is
//     no cache, so a grant removed is gone on the next request. The local
//     operator, who has no grants, reads the tenant by name instead.
//  3. A missing tenant is NotFound with MessageNoTenant for a caller who may
//     list every tenant, and otherwise Denied with MessageDenied, exactly as
//     for an existing tenant without a grant: both make the same two calls,
//     Key then RolesFor, and queue one audit entry.
//  4. The role is compared with req; too little is Denied.
//
// A refusal's audit entry is handed to the audit writer for the master's log,
// so a refused caller causes no write in a tenant's database, with its real
// cause and the tenant's name as given; it is not written on the request's
// path. When the request is allowed, the Decision makes its entries. A key
// that passes step 1 and was not marked used within the minute has its last
// use marked, off the request's path. A store that fails is Unavailable or
// Internal, never a refusal.
func (a *Access) Check(ctx context.Context, cred Credential, req Requirement, facts Request) (Decision, error) {
	entry := a.newEntry(facts)
	entry.ID = a.newID()
	if master.ValidateID("audit entry id", entry.ID) != nil {
		// The id source is broken: no entry could be recorded, so the
		// request fails rather than go unrecorded.
		return Decision{}, fault.New(fault.Internal, "internal error")
	}
	if entry.Method == "" || !req.valid() || len(facts.Details) > MaxRequestDetails {
		// A server bug, not the caller's doing: it fails, and is recorded.
		if entry.Method == "" {
			entry.Method = "unknown"
		}
		entry.Details = nil
		a.record(entry, master.OutcomeFailed, reasonBadCheck)
		return Decision{}, fault.New(fault.Internal, "internal error")
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

	d := Decision{newID: a.newID, clock: a.clock}
	var tenantMissing bool
	switch {
	case p.isKey:
		name := ""
		if req.namesTenant() {
			// The name as the request gave it, whether or not a tenant has
			// it: a missing tenant and a missing grant make the same call
			// with the same kind of argument, and the store runs the same
			// statement for both, a name that is not valid included.
			name = req.tenant
		}
		r, err := a.store.RolesFor(ctx, p.key.ID, name)
		if err != nil {
			return Decision{}, a.fail(entry, err)
		}
		p.instRole = r.Installation
		if req.namesTenant() {
			if r.TenantFound {
				p.tenantID, p.tenantRole = r.TenantID, r.Tenant
				d.tenant, d.hasTenant = TenantRef{ID: r.TenantID, Name: req.tenant}, true
			} else {
				tenantMissing = true
			}
		}
	case req.namesTenant():
		// The local operator holds no grant; only the tenant is read.
		t, err := a.store.TenantByName(ctx, req.tenant)
		switch kind := fault.KindOf(err); {
		case err == nil:
			d.tenant, d.hasTenant = TenantRef{ID: t.ID, Name: req.tenant}, true
		case kind == fault.NotFound || kind == fault.Invalid:
			tenantMissing = true
		default:
			return Decision{}, a.fail(entry, err)
		}
	}
	d.principal = p

	switch {
	case tenantMissing && p.CanListTenants():
		a.record(entry, master.OutcomeFailed, reasonNoTenant)
		return Decision{}, fault.New(fault.NotFound, MessageNoTenant)
	case tenantMissing:
		a.record(entry, master.OutcomeRefused, reasonNoTenant)
		return Decision{}, fault.New(fault.Denied, MessageDenied)
	case !allows(p, req, d.tenant.ID):
		a.record(entry, master.OutcomeRefused, reasonNoGrant)
		return Decision{}, fault.New(fault.Denied, MessageDenied)
	}
	entry.ID = ""
	d.entry = entry
	return d, nil
}

// allows reports whether p meets req, given the id of the tenant req named
// (found by Check).
func allows(p Principal, req Requirement, tenantID string) bool {
	switch req.kind {
	case needAuthenticated:
		return p.isKey || p.isLoc
	case needInstallation:
		return atLeast(p.Role(master.Installation), req.role)
	case needTenant:
		return atLeast(p.Role(master.TenantScope(tenantID)), req.role)
	case needTenantRegistration:
		return atLeast(p.Role(master.TenantScope(tenantID)), master.RoleReader) || p.CanListTenants()
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
		entry.LocalUser = auditText(cred.localUser, master.MaxAuditFieldLength)
		// A user id the entry could not hold as it is (too long, or with a
		// character that had to be escaped) is not one the socket reported.
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
		a.verify(sec, unknownKeyHash[:])
		return Principal{}, reasonUnknownKey, nil
	}
	if err != nil {
		return Principal{}, "", err
	}
	if reason := a.keyRefusal(k, sec, entry.Time); reason != "" {
		return Principal{}, reason, nil
	}
	a.queueMark(k, entry.Time)
	k.Hash = nil
	return Principal{key: k, isKey: true}, "", nil
}

// keyRefusal returns why a key read from the master is refused at now, or
// "". The secret is compared first: a caller who does not hold it learns
// nothing more, and the log does not record "revoked" for a guess.
func (a *Access) keyRefusal(k master.Key, sec string, now time.Time) string {
	if !a.verify(sec, k.Hash) {
		return reasonWrongSecret
	}
	return keyState(k, now)
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

// newEntry starts a request's audit entry from its facts, at the time of the
// check, without an id. Every text is escaped and cut to the master's
// bounds: the facts come from the request, and an entry the master refuses
// would be a refusal without a record, while a control character in the log
// could forge a line of it.
func (a *Access) newEntry(facts Request) master.AuditEntry {
	e := master.AuditEntry{
		Time:          a.clock(),
		RemoteAddress: auditText(facts.RemoteAddress, master.MaxAuditFieldLength),
		Method:        auditText(facts.Method, master.MaxAuditFieldLength),
		Object:        auditText(facts.Object, master.MaxAuditFieldLength),
		RequestID:     auditText(facts.RequestID, master.MaxAuditFieldLength),
	}
	if len(facts.Details) > 0 {
		e.Details = make(map[string]string, len(facts.Details))
		for k, v := range facts.Details {
			k = auditText(k, master.MaxAuditDetailKeyLength)
			if k == "" {
				continue
			}
			e.Details[k] = auditText(v, master.MaxAuditDetailValueLength)
		}
	}
	return e
}

// record hands a refusal's, or a failed check's, audit entry to the audit
// writer, for the master's log: a refused caller must cause no write in a
// tenant's database. The request already has its answer, and the answer to a
// refused caller must not depend on whether the log took it, nor wait for a
// synced commit.
func (a *Access) record(e master.AuditEntry, outcome master.Outcome, reason string) {
	e.Outcome = outcome
	e.Reason = auditText(reason, master.MaxAuditReasonLength)
	a.enqueueRefusal(e)
}

// fail turns a store's failure during a check into the caller's error, and
// records it. A master that does not answer is Unavailable, as the contract
// says ("a database that does not answer is a failure, not a refusal"); any
// other failure is Internal, whose message says nothing of the cause.
func (a *Access) fail(e master.AuditEntry, err error) error {
	a.record(e, master.OutcomeFailed, reasonStoreFailure+": "+fault.KindOf(err).String())
	return storeError(err)
}

// storeError returns the caller's error for a store's failure.
func storeError(err error) error {
	if fault.KindOf(err) == fault.Unavailable {
		return fault.Wrap(fault.Unavailable, err, "the master database does not answer")
	}
	return fault.Wrap(fault.Internal, err, "internal error")
}

// clipTenantName returns a tenant's name as the request gave it, for the
// audit entry (audit.proto: kept as given, also when no tenant has it). A
// name that could not be a tenant's is quoted in ASCII (strconv.QuoteToASCII,
// which escapes every control and non-ASCII character as auditText would),
// so it cannot pass for one in the log, and cut to an audit field. It is not
// passed through auditText as well, which would escape its escapes again.
func clipTenantName(name string) string {
	if master.ValidateTenantName(name) == nil {
		return name
	}
	q := strconv.QuoteToASCII(name)
	if len(q) > master.MaxAuditFieldLength {
		q = q[:master.MaxAuditFieldLength]
	}
	return q
}
