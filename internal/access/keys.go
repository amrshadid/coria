package access

import (
	"context"
	"strconv"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/secret"
)

// The limits of the keys this package makes (access.proto,
// CreateKeyRequest; D38).
const (
	// DefaultKeyLifetime is how long a key lives when its maker names no
	// expiry: a key nobody remembers still stops working on its own.
	DefaultKeyLifetime = 90 * 24 * time.Hour
	// MaxKeyLifetime is the longest expiry a key may be given, ten years. A
	// key meant to outlive that is made with NeverExpire, in so many words.
	MaxKeyLifetime = 3650 * 24 * time.Hour
	// DefaultPageSize is the size of a page when the request asks for none.
	DefaultPageSize = 100
	// MaxPageTokenLength bounds a page token, as the contract states.
	MaxPageTokenLength = 1 << 10
)

// The keys of the details this package writes in audit entries.
const (
	detailHolder  = "holder"
	detailExpires = "expires"
	detailScope   = "scope"
	detailFilter  = "filter"
)

// Expiry is when a new key stops working. The zero Expiry is the default,
// DefaultKeyLifetime after the key is made.
type Expiry struct {
	after time.Duration
	never bool
	set   bool
}

// ExpireAfter is an expiry d after the key is made. d must be positive and
// at most MaxKeyLifetime, or CreateKey refuses it as Invalid.
func ExpireAfter(d time.Duration) Expiry { return Expiry{after: d, set: true} }

// NeverExpire is a key that never expires. It is its own constructor, so a
// key without an expiry is always asked for in so many words.
func NeverExpire() Expiry { return Expiry{never: true, set: true} }

// expires returns the expiry time of a key made at now, the zero time for
// never, or an Invalid error.
func (e Expiry) expires(now time.Time) (time.Time, error) {
	switch {
	case !e.set:
		return now.Add(DefaultKeyLifetime), nil
	case e.never:
		return time.Time{}, nil
	case e.after <= 0 || e.after > MaxKeyLifetime:
		return time.Time{}, fault.New(fault.Invalid, "a key's expiry is positive and at most 10 years (3650 days)")
	}
	return now.Add(e.after), nil
}

// Scope is a scope as a request names it: one tenant by its name, or the
// installation. The zero Scope is the installation.
type Scope struct {
	// Tenant is the tenant's name, or empty for the installation.
	Tenant string
}

// InstallationScope is the installation.
var InstallationScope = Scope{}

// TenantNamed returns the scope of the tenant with this name.
func TenantNamed(name string) Scope { return Scope{Tenant: name} }

// IsInstallation reports whether s is the installation.
func (s Scope) IsInstallation() bool { return s.Tenant == "" }

// String returns "installation" or "tenant/<name>", as the scope is written
// in an audit entry's details. A name that could not be a tenant's is quoted.
func (s Scope) String() string {
	if s.IsInstallation() {
		return "installation"
	}
	return "tenant/" + clipTenantName(s.Tenant)
}

// Grant is a grant as a request reads it: its scope by tenant name.
type Grant struct {
	KeyID string
	Scope Scope
	Role  master.Role
}

// CreatedKey is a key just made. Secret holds the whole key, to be shown to
// its holder once; it prints as its id only, and Secret.Key() is the one
// call that reads it. Key has no hash.
type CreatedKey struct {
	Key    master.Key
	Secret secret.NewAPIKey
}

// GrantFilter says whose grants ListGrants lists: one key's, or those on one
// scope. The zero GrantFilter is refused as Invalid: there is no listing of
// every grant at once.
type GrantFilter struct {
	keyID   string
	scope   Scope
	byKey   bool
	byScope bool
}

// GrantsOfKey lists the grants of one key, on every scope.
func GrantsOfKey(keyID string) GrantFilter { return GrantFilter{keyID: keyID, byKey: true} }

// GrantsOnScope lists the grants on one scope, of every key.
func GrantsOnScope(s Scope) GrantFilter { return GrantFilter{scope: s, byScope: true} }

// admin checks that cred is the installation's admin, as every operation on
// keys and grants requires in this version (access.proto, AccessService),
// and returns the request's entry with object and details set. The details
// are this package's own, replacing those of facts.
func (a *Access) admin(ctx context.Context, cred Credential, facts Request, object string, details map[string]string) (master.AuditEntry, error) {
	facts.Object = object
	facts.Details = details
	d, err := a.Check(ctx, cred, OnInstallation(master.RoleAdmin), facts)
	if err != nil {
		return master.AuditEntry{}, err
	}
	return d.Entry(master.OutcomeDone, ""), nil
}

// failAfter records why an allowed request did not complete, and returns the
// error its caller is shown. What the caller may know (Invalid, NotFound,
// Conflict) keeps its kind and message. A store's failure is Unavailable or
// Internal, with a message that says nothing of the cause. The entry is the
// one the failed change did not write (a change and its entry are both
// written or neither), so its id is still free.
func (a *Access) failAfter(e master.AuditEntry, err error) error {
	switch fault.KindOf(err) {
	case fault.Invalid, fault.NotFound, fault.Conflict:
		a.record(e, master.OutcomeFailed, fault.Message(err))
		return err
	}
	return a.fail(e, err)
}

// appendRead hands a read's audit entry to the audit writer. A read changes
// nothing, so its entry does not need its own synced commit on the request's
// path; it is written with the next batch (see the package documentation
// for what that means when the server is overwhelmed or killed).
func (a *Access) appendRead(e master.AuditEntry) {
	a.enqueueAudit(e)
}

// page returns a request's page with the contract's default and bounds
// applied: zero is DefaultPageSize; a negative size, one above
// master.MaxPageLimit, or a token over 1 KiB is Invalid.
func page(p master.Page) (master.Page, error) {
	if p.Limit == 0 {
		p.Limit = DefaultPageSize
	}
	if p.Limit < 0 || p.Limit > master.MaxPageLimit {
		return p, fault.New(fault.Invalid, "a page's size is 0 to "+strconv.Itoa(master.MaxPageLimit))
	}
	if len(p.Cursor) > MaxPageTokenLength {
		return p, fault.New(fault.Invalid, "a page token is at most 1 KiB")
	}
	return p, nil
}

// publicKey returns k without its hash: nothing this package returns
// carries one.
func publicKey(k master.Key) master.Key {
	k.Hash = nil
	return k
}

// CreateKey makes a key for holder, which expires as exp says. It needs the
// installation's admin. The holder is required, at most 256 bytes of UTF-8.
// The key and its audit entry, which names the key and its holder and never
// its secret, are written in one transaction. The whole key is in the
// result's Secret, once: only its hash is stored.
func (a *Access) CreateKey(ctx context.Context, cred Credential, facts Request, holder string, exp Expiry) (CreatedKey, error) {
	entry, err := a.admin(ctx, cred, facts, "", map[string]string{detailHolder: clip(holder, master.MaxAuditDetailValueLength)})
	if err != nil {
		return CreatedKey{}, err
	}
	now := entry.Time
	expires, err := exp.expires(now)
	if err != nil {
		return CreatedKey{}, a.failAfter(entry, err)
	}
	made, err := secret.MakeAPIKey()
	if err != nil {
		return CreatedKey{}, a.fail(entry, err)
	}
	k := master.Key{ID: made.ID, Hash: made.Hash[:], Holder: holder, Created: now, Expires: expires}
	entry.Object = k.ID
	if expires.IsZero() {
		entry.Details[detailExpires] = "never"
	} else {
		entry.Details[detailExpires] = expires.UTC().Format(time.RFC3339)
	}
	if err := k.Validate(); err != nil {
		return CreatedKey{}, a.failAfter(entry, err)
	}
	if err := a.store.CreateKey(ctx, k, entry); err != nil {
		return CreatedKey{}, a.failAfter(entry, err)
	}
	return CreatedKey{Key: publicKey(k), Secret: made}, nil
}

// GetKey returns one key, revoked and expired keys included, without its
// hash. It needs the installation's admin. An unknown id is NotFound.
func (a *Access) GetKey(ctx context.Context, cred Credential, facts Request, id string) (master.Key, error) {
	entry, err := a.admin(ctx, cred, facts, clip(id, master.MaxAuditFieldLength), nil)
	if err != nil {
		return master.Key{}, err
	}
	if err := master.ValidateID("key id", id); err != nil {
		return master.Key{}, a.failAfter(entry, err)
	}
	k, err := a.store.Key(ctx, id)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			err = fault.New(fault.NotFound, "no key has this id")
		}
		return master.Key{}, a.failAfter(entry, err)
	}
	a.appendRead(entry)
	return publicKey(k), nil
}

// ListKeys returns a page of every key, revoked and expired keys included,
// oldest first, without their hashes, and the token of the next page. It
// needs the installation's admin.
func (a *Access) ListKeys(ctx context.Context, cred Credential, facts Request, p master.Page) ([]master.Key, string, error) {
	entry, err := a.admin(ctx, cred, facts, "", nil)
	if err != nil {
		return nil, "", err
	}
	p, err = page(p)
	if err != nil {
		return nil, "", a.failAfter(entry, err)
	}
	keys, next, err := a.store.ListKeys(ctx, p)
	if err != nil {
		return nil, "", a.failAfter(entry, err)
	}
	a.appendRead(entry)
	for i := range keys {
		keys[i] = publicKey(keys[i])
	}
	return keys, next, nil
}

// RevokeKey ends a key for good and returns it with its revoke time. It
// needs the installation's admin. Revoking a revoked key changes nothing and
// returns its first revoke time; the request is still recorded. An unknown
// id is NotFound. Because every check reads the key, the key's next request
// is refused.
func (a *Access) RevokeKey(ctx context.Context, cred Credential, facts Request, id string) (master.Key, error) {
	entry, err := a.admin(ctx, cred, facts, clip(id, master.MaxAuditFieldLength), nil)
	if err != nil {
		return master.Key{}, err
	}
	if err := master.ValidateID("key id", id); err != nil {
		return master.Key{}, a.failAfter(entry, err)
	}
	if _, err := a.store.RevokeKey(ctx, id, entry.Time, entry); err != nil {
		if fault.KindOf(err) == fault.NotFound {
			err = fault.New(fault.NotFound, "no key has this id")
		}
		return master.Key{}, a.failAfter(entry, err)
	}
	// The revocation and its entry are written; the key is read back for
	// the answer. If that read fails, the caller is told so and may repeat
	// the call, which is idempotent.
	k, err := a.store.Key(ctx, id)
	if err != nil {
		if fault.KindOf(err) == fault.Unavailable {
			return master.Key{}, fault.Wrap(fault.Unavailable, err, "the master database does not answer")
		}
		return master.Key{}, fault.Wrap(fault.Internal, err, "internal error")
	}
	return publicKey(k), nil
}

// resolve returns the master's scope for s, reading the tenant by name. A
// tenant that does not exist is NotFound: the caller has passed the
// installation admin's check, so it may list every tenant (D37).
func (a *Access) resolve(ctx context.Context, s Scope) (master.Scope, error) {
	if s.IsInstallation() {
		return master.Installation, nil
	}
	if err := master.ValidateTenantName(s.Tenant); err != nil {
		return master.Scope{}, err
	}
	t, err := a.store.TenantByName(ctx, s.Tenant)
	if fault.KindOf(err) == fault.NotFound {
		return master.Scope{}, fault.New(fault.NotFound, MessageNoTenant)
	}
	if err != nil {
		return master.Scope{}, err
	}
	return master.TenantScope(t.ID), nil
}

// SetGrant gives the key keyID role on scope, replacing in one step the
// role it held there, and returns the grant and the role it replaced (the
// empty Role for none). It needs the installation's admin. Writer is not a
// role on the installation and is Invalid there; an unknown key or tenant is
// NotFound; a revoked key is Conflict. The audit entry has the scope and,
// from the master, the old and the new role.
func (a *Access) SetGrant(ctx context.Context, cred Credential, facts Request, keyID string, scope Scope, role master.Role) (Grant, master.Role, error) {
	entry, err := a.admin(ctx, cred, facts, clip(keyID, master.MaxAuditFieldLength), map[string]string{detailScope: scope.String()})
	if err != nil {
		return Grant{}, "", err
	}
	if err := master.ValidateID("key id", keyID); err != nil {
		return Grant{}, "", a.failAfter(entry, err)
	}
	if !role.Valid() {
		return Grant{}, "", a.failAfter(entry, fault.New(fault.Invalid, "a role is reader, writer, operator or admin"))
	}
	if role == master.RoleWriter && scope.IsInstallation() {
		return Grant{}, "", a.failAfter(entry, fault.New(fault.Invalid, "writer is not a role on the installation"))
	}
	ms, err := a.resolve(ctx, scope)
	if err != nil {
		return Grant{}, "", a.failAfter(entry, err)
	}
	// The port's SetGrant does not refuse a revoked key, so it is read
	// here first. A revocation between this read and the write below lets
	// the grant be set on a key that can no longer use it, which gives
	// nothing, since every check refuses a revoked key.
	k, err := a.store.Key(ctx, keyID)
	if fault.KindOf(err) == fault.NotFound {
		err = fault.New(fault.NotFound, "no key has this id")
	}
	if err == nil && !k.Revoked.IsZero() {
		err = fault.New(fault.Conflict, "the key is revoked")
	}
	if err != nil {
		return Grant{}, "", a.failAfter(entry, err)
	}
	previous, err := a.store.SetGrant(ctx, master.Grant{KeyID: keyID, Scope: ms, Role: role}, entry)
	if err != nil {
		return Grant{}, "", a.failAfter(entry, err)
	}
	return Grant{KeyID: keyID, Scope: scope, Role: role}, previous, nil
}

// RemoveGrant takes back the key keyID's grant on scope and returns the
// role it had, or the empty Role when it held none, which changes nothing
// and succeeds. It needs the installation's admin. An unknown key or tenant
// is NotFound.
func (a *Access) RemoveGrant(ctx context.Context, cred Credential, facts Request, keyID string, scope Scope) (master.Role, error) {
	entry, err := a.admin(ctx, cred, facts, clip(keyID, master.MaxAuditFieldLength), map[string]string{detailScope: scope.String()})
	if err != nil {
		return "", err
	}
	if err := master.ValidateID("key id", keyID); err != nil {
		return "", a.failAfter(entry, err)
	}
	ms, err := a.resolve(ctx, scope)
	if err != nil {
		return "", a.failAfter(entry, err)
	}
	previous, err := a.store.RemoveGrant(ctx, keyID, ms, entry)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			err = fault.New(fault.NotFound, "no key has this id")
		}
		return "", a.failAfter(entry, err)
	}
	return previous, nil
}

// ListGrants returns a page of the grants filter keeps, with each scope by
// its tenant's name, and the token of the next page. It needs the
// installation's admin. A scope's tenant that does not exist is NotFound; a
// key that does not exist has no grants.
func (a *Access) ListGrants(ctx context.Context, cred Credential, facts Request, filter GrantFilter, p master.Page) ([]Grant, string, error) {
	desc := "none"
	switch {
	case filter.byKey:
		desc = "key/" + clip(filter.keyID, master.MaxIDLength)
	case filter.byScope:
		desc = filter.scope.String()
	}
	entry, err := a.admin(ctx, cred, facts, "", map[string]string{detailFilter: desc})
	if err != nil {
		return nil, "", err
	}
	p, err = page(p)
	if err != nil {
		return nil, "", a.failAfter(entry, err)
	}
	var grants []master.Grant
	var next string
	switch {
	case filter.byKey:
		if err := master.ValidateID("key id", filter.keyID); err != nil {
			return nil, "", a.failAfter(entry, err)
		}
		grants, next, err = a.store.GrantsByKey(ctx, filter.keyID, p)
	case filter.byScope:
		var ms master.Scope
		ms, err = a.resolve(ctx, filter.scope)
		if err == nil {
			grants, next, err = a.store.GrantsByScope(ctx, ms, p)
		}
	default:
		err = fault.New(fault.Invalid, "grants are listed by key or by scope")
	}
	if err != nil {
		return nil, "", a.failAfter(entry, err)
	}
	out, err := a.named(ctx, grants)
	if err != nil {
		return nil, "", a.failAfter(entry, err)
	}
	a.appendRead(entry)
	return out, next, nil
}

// named returns grants with each tenant's id replaced by its name, reading
// each tenant once per call. Nothing is kept between calls.
func (a *Access) named(ctx context.Context, grants []master.Grant) ([]Grant, error) {
	names := map[string]string{}
	out := make([]Grant, 0, len(grants))
	for _, g := range grants {
		s := InstallationScope
		if !g.Scope.IsInstallation() {
			name, ok := names[g.Scope.TenantID]
			if !ok {
				t, err := a.store.Tenant(ctx, g.Scope.TenantID)
				if err != nil {
					return nil, err
				}
				name = t.Name
				names[g.Scope.TenantID] = name
			}
			s = TenantNamed(name)
		}
		out = append(out, Grant{KeyID: g.KeyID, Scope: s, Role: g.Role})
	}
	return out, nil
}

// WhoAmI returns the caller and every grant of its key, each scope by its
// tenant's name, and records the request. Any authenticated caller may ask.
// The local operator has no grants: it is admin by being the user that runs
// the server.
func (a *Access) WhoAmI(ctx context.Context, cred Credential, facts Request) (Principal, []Grant, error) {
	d, err := a.Check(ctx, cred, Authenticated(), facts)
	if err != nil {
		return Principal{}, nil, err
	}
	entry := d.Entry(master.OutcomeDone, "")
	var all []master.Grant
	if d.principal.isKey {
		all, err = a.allGrants(ctx, d.principal.key.ID)
		if err != nil {
			return Principal{}, nil, a.failAfter(entry, err)
		}
	}
	grants, err := a.named(ctx, all)
	if err != nil {
		return Principal{}, nil, a.failAfter(entry, err)
	}
	a.appendRead(entry)
	return d.principal, grants, nil
}
