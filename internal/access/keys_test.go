package access_test

import (
	"strings"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/secret"
)

// TestCreateKeyExpiry guards the contract's expiry rules: 90 days unless
// given, "never" only when asked for, a positive period of at most 10 years,
// and a key that works with the secret returned once.
func TestCreateKeyExpiry(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name string
		exp  access.Expiry
		want time.Time
		ok   bool
	}{
		{"default", access.Expiry{}, t0.Add(90 * 24 * time.Hour), true},
		{"never", access.NeverExpire(), time.Time{}, true},
		{"a day", access.ExpireAfter(24 * time.Hour), t0.Add(24 * time.Hour), true},
		{"ten years", access.ExpireAfter(access.MaxKeyLifetime), t0.Add(3650 * 24 * time.Hour), true},
		{"past ten years", access.ExpireAfter(access.MaxKeyLifetime + time.Microsecond), time.Time{}, false},
		{"zero", access.ExpireAfter(0), time.Time{}, false},
		{"negative", access.ExpireAfter(-time.Hour), time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.reset()
			made, err := e.access.CreateKey(e.ctx, local, facts("CreateKey"), "lab robot", tc.exp)
			_, entries := e.recorded()
			if len(entries) != 1 {
				t.Fatalf("%d audit entries, want 1", len(entries))
			}
			if !tc.ok {
				wantErr(t, "create", err, fault.Invalid, "")
				if entries[0].Outcome != master.OutcomeFailed {
					t.Fatalf("entry %+v", entries[0])
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !made.Key.Expires.Equal(tc.want) || made.Key.Holder != "lab robot" || made.Key.Hash != nil {
				t.Fatalf("key %+v", made.Key)
			}
			if entries[0].Object != made.Key.ID || entries[0].Details["holder"] != "lab robot" || entries[0].Outcome != master.OutcomeDone {
				t.Fatalf("entry %+v", entries[0])
			}
			if _, err := e.access.Check(e.ctx, access.APIKey(made.Secret.Key()), access.Authenticated(), facts("m")); err != nil {
				t.Fatalf("the new key does not work: %v", err)
			}
		})
	}
}

// TestCreateKeyHolder guards the holder's rules: required, at most 256
// bytes of UTF-8.
func TestCreateKeyHolder(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		holder string
		ok     bool
	}{
		{"", false},
		{strings.Repeat("a", 256), true},
		{strings.Repeat("a", 257), false},
		{"Zoë 山田", true},
		{"bad\xff", false},
	} {
		_, err := e.access.CreateKey(e.ctx, local, facts("CreateKey"), tc.holder, access.Expiry{})
		if tc.ok && err != nil {
			t.Errorf("holder of %d bytes: %v", len(tc.holder), err)
		}
		if !tc.ok && fault.KindOf(err) != fault.Invalid {
			t.Errorf("holder of %d bytes: %v, want Invalid", len(tc.holder), err)
		}
	}
}

// TestManagementOrder guards the order of checks on the operations this
// package implements: unauthenticated first, then the role, then the
// request's own validity, so a caller who may not call a method learns
// nothing about what a valid request to it looks like.
func TestManagementOrder(t *testing.T) {
	e := newEnv(t)
	reader := e.key(time.Time{}, on(master.Installation, master.RoleOperator))
	_, err := e.access.CreateKey(e.ctx, access.APIKey("nonsense"), facts("CreateKey"), "", access.ExpireAfter(-1))
	wantErr(t, "no key, bad request", err, fault.Unauthenticated, access.MessageUnauthenticated)
	_, err = e.access.CreateKey(e.ctx, access.APIKey(reader.full), facts("CreateKey"), "", access.ExpireAfter(-1))
	wantErr(t, "operator, bad request", err, fault.Denied, access.MessageDenied)
	_, err = e.access.CreateKey(e.ctx, local, facts("CreateKey"), "", access.ExpireAfter(-1))
	wantErr(t, "admin, bad request", err, fault.Invalid, "")

	for name, call := range map[string]func(access.Credential) error{
		"GetKey": func(c access.Credential) error {
			_, err := e.access.GetKey(e.ctx, c, facts("GetKey"), reader.id)
			return err
		},
		"ListKeys": func(c access.Credential) error {
			_, _, err := e.access.ListKeys(e.ctx, c, facts("ListKeys"), master.Page{})
			return err
		},
		"RevokeKey": func(c access.Credential) error {
			_, err := e.access.RevokeKey(e.ctx, c, facts("RevokeKey"), reader.id)
			return err
		},
		"SetGrant": func(c access.Credential) error {
			_, _, err := e.access.SetGrant(e.ctx, c, facts("SetGrant"), reader.id, access.InstallationScope, master.RoleAdmin)
			return err
		},
		"RemoveGrant": func(c access.Credential) error {
			_, err := e.access.RemoveGrant(e.ctx, c, facts("RemoveGrant"), reader.id, access.InstallationScope)
			return err
		},
		"ListGrants": func(c access.Credential) error {
			_, _, err := e.access.ListGrants(e.ctx, c, facts("ListGrants"), access.GrantsOfKey(reader.id), master.Page{})
			return err
		},
	} {
		wantErr(t, name+" by an installation operator", call(access.APIKey(reader.full)), fault.Denied, access.MessageDenied)
	}
}

// TestRevokeTwice guards that revoking a revoked key succeeds and keeps its
// first revoke time, and that an unknown key is NotFound.
func TestRevokeTwice(t *testing.T) {
	e := newEnv(t)
	k := e.key(time.Time{})
	first, err := e.access.RevokeKey(e.ctx, local, facts("RevokeKey"), k.id)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(t0.Add(time.Hour))
	second, err := e.access.RevokeKey(e.ctx, local, facts("RevokeKey"), k.id)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Revoked.Equal(t0) || !second.Revoked.Equal(t0) || second.Hash != nil {
		t.Fatalf("first %v, second %v", first.Revoked, second.Revoked)
	}
	_, err = e.access.RevokeKey(e.ctx, local, facts("RevokeKey"), "NoSuchKey123")
	wantErr(t, "unknown", err, fault.NotFound, "")
}

// TestGrants guards setting, listing and removing grants: the previous role
// returned, writer refused on the installation, unknown key and tenant
// NotFound, a revoked key Conflict, removing what is not held a success, and
// scopes named by tenant name.
func TestGrants(t *testing.T) {
	e := newEnv(t)
	k := e.key(time.Time{})
	set := func(scope access.Scope, role master.Role) (master.Role, error) {
		_, prev, err := e.access.SetGrant(e.ctx, local, facts("SetGrant"), k.id, scope, role)
		return prev, err
	}
	if prev, err := set(access.TenantNamed("lab"), master.RoleReader); err != nil || prev != "" {
		t.Fatalf("first set: %q %v", prev, err)
	}
	if prev, err := set(access.TenantNamed("lab"), master.RoleAdmin); err != nil || prev != master.RoleReader {
		t.Fatalf("second set: %q %v", prev, err)
	}
	if prev, err := set(access.InstallationScope, master.RoleReader); err != nil || prev != "" {
		t.Fatalf("installation: %q %v", prev, err)
	}
	_, err := set(access.InstallationScope, master.RoleWriter)
	wantErr(t, "writer on the installation", err, fault.Invalid, "")
	_, err = set(access.TenantNamed("nosuch"), master.RoleReader)
	wantErr(t, "unknown tenant", err, fault.NotFound, access.MessageNoTenant)
	_, err = set(access.TenantNamed("lab"), "boss")
	wantErr(t, "unknown role", err, fault.Invalid, "")
	_, _, err = e.access.SetGrant(e.ctx, local, facts("SetGrant"), "NoSuchKey123", access.InstallationScope, master.RoleReader)
	wantErr(t, "unknown key", err, fault.NotFound, "")

	got, next, err := e.access.ListGrants(e.ctx, local, facts("ListGrants"), access.GrantsOfKey(k.id), master.Page{})
	if err != nil || next != "" {
		t.Fatal(err)
	}
	want := []access.Grant{
		{KeyID: k.id, Scope: access.InstallationScope, Role: master.RoleReader},
		{KeyID: k.id, Scope: access.TenantNamed("lab"), Role: master.RoleAdmin},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("grants %+v, want %+v", got, want)
	}
	onLab, _, err := e.access.ListGrants(e.ctx, local, facts("ListGrants"), access.GrantsOnScope(access.TenantNamed("lab")), master.Page{})
	if err != nil || len(onLab) != 1 || onLab[0] != want[1] {
		t.Fatalf("on lab: %+v %v", onLab, err)
	}
	_, _, err = e.access.ListGrants(e.ctx, local, facts("ListGrants"), access.GrantFilter{}, master.Page{})
	wantErr(t, "no filter", err, fault.Invalid, "")
	_, _, err = e.access.ListGrants(e.ctx, local, facts("ListGrants"), access.GrantsOfKey(k.id), master.Page{Limit: 1001})
	wantErr(t, "page too large", err, fault.Invalid, "")

	prev, err := e.access.RemoveGrant(e.ctx, local, facts("RemoveGrant"), k.id, access.TenantNamed("lab"))
	if err != nil || prev != master.RoleAdmin {
		t.Fatalf("remove: %q %v", prev, err)
	}
	prev, err = e.access.RemoveGrant(e.ctx, local, facts("RemoveGrant"), k.id, access.TenantNamed("lab"))
	if err != nil || prev != "" {
		t.Fatalf("remove again: %q %v", prev, err)
	}

	if _, err := e.access.RevokeKey(e.ctx, local, facts("RevokeKey"), k.id); err != nil {
		t.Fatal(err)
	}
	e.reset()
	_, err = set(access.TenantNamed("lab"), master.RoleReader)
	wantErr(t, "revoked key", err, fault.Conflict, "")
	if _, entries := e.recorded(); len(entries) != 1 || entries[0].Outcome != master.OutcomeFailed || entries[0].Details["scope"] != "tenant/lab" {
		t.Fatalf("entries %+v", entries)
	}
}

// TestKeysListedWithoutHash guards that GetKey, ListKeys and WhoAmI return
// keys without their hashes, and that WhoAmI names its scopes by tenant.
func TestKeysListedWithoutHash(t *testing.T) {
	e := newEnv(t)
	k := e.key(time.Time{}, on(master.TenantScope(e.clinic.ID), master.RoleReader))
	keys, _, err := e.access.ListKeys(e.ctx, local, facts("ListKeys"), master.Page{})
	if err != nil || len(keys) != 1 {
		t.Fatalf("%d keys, %v", len(keys), err)
	}
	got, err := e.access.GetKey(e.ctx, local, facts("GetKey"), k.id)
	if err != nil {
		t.Fatal(err)
	}
	if keys[0].Hash != nil || got.Hash != nil {
		t.Fatal("a key came back with its hash")
	}
	_, err = e.access.GetKey(e.ctx, local, facts("GetKey"), "NoSuchKey123")
	wantErr(t, "unknown", err, fault.NotFound, "")

	p, grants, err := e.access.WhoAmI(e.ctx, access.APIKey(k.full), facts("WhoAmI"))
	if err != nil {
		t.Fatal(err)
	}
	pk, isKey := p.Key()
	if !isKey || pk.ID != k.id || pk.Hash != nil || len(grants) != 1 || grants[0].Scope != access.TenantNamed("clinic") {
		t.Fatalf("whoami: %+v %+v", pk, grants)
	}
	lp, lg, err := e.access.WhoAmI(e.ctx, local, facts("WhoAmI"))
	if err != nil || !lp.IsLocalOperator() || lp.LocalUser() != "501" || len(lg) != 0 {
		t.Fatalf("local whoami: %+v %v %v", lp, lg, err)
	}
}

// TestCreatedKeyPrintsNoSecret guards that the one value carrying a
// secret, the result of CreateKey, prints only the key's id in every form.
func TestCreatedKeyPrintsNoSecret(t *testing.T) {
	e := newEnv(t)
	made, err := e.access.CreateKey(e.ctx, local, facts("CreateKey"), "h", access.Expiry{})
	if err != nil {
		t.Fatal(err)
	}
	full := made.Secret.Key()
	_, sec, err := secret.ParseAPIKey(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := sprintf(form, made); strings.Contains(out, sec) {
			t.Fatalf("%s prints the secret", form)
		}
	}
}
