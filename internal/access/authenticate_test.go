package access_test

import (
	"slices"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// TestAuthenticate guards the judgment of a credential on its own: a
// credential that is no good is refused as Check refuses it, with one
// refused entry naming its real reason and the key id when it has one; a
// good key is read once, and Check given the credential Authenticate
// returned reads its roles without reading the key again, and decides as
// it would have; the local operator is accepted without a read.
func TestAuthenticate(t *testing.T) {
	e := newEnv(t)
	good := e.key(time.Time{}, on(master.Installation, master.RoleReader))
	expired := e.key(t0.Add(-time.Second))
	for _, c := range []struct {
		name   string
		cred   access.Credential
		reason string
		keyID  string
	}{
		{"no key", access.APIKey(""), "no key", ""},
		{"malformed", access.APIKey("mgt_short"), "malformed key", ""},
		{"expired", access.APIKey(expired.full), "key expired", expired.id},
	} {
		e.reset()
		_, err := e.access.Authenticate(e.ctx, c.cred, facts("/m"))
		wantErr(t, c.name, err, fault.Unauthenticated, access.MessageUnauthenticated)
		_, entries := e.recorded()
		if len(entries) != 1 || entries[0].Outcome != master.OutcomeRefused || entries[0].Reason != c.reason ||
			entries[0].KeyID != c.keyID || entries[0].Method != "/m" || entries[0].RequestID != "req-1" {
			t.Errorf("%s: entries %+v", c.name, entries)
		}
	}

	e.reset()
	cred, err := e.access.Authenticate(e.ctx, access.APIKey(good.full), facts("/m"))
	if err != nil {
		t.Fatal(err)
	}
	if calls, entries := e.recorded(); !slices.Equal(calls, []string{"Key"}) || len(entries) != 0 {
		t.Fatalf("authenticating a good key: calls %v, entries %v", calls, entries)
	}
	if _, err := e.access.Check(e.ctx, cred, access.OnInstallation(master.RoleAdmin), facts("/m")); fault.KindOf(err) != fault.Denied {
		t.Fatalf("a reader checked for admin: %v", err)
	}
	d, err := e.access.Check(e.ctx, cred, access.OnInstallation(master.RoleReader), facts("/m"))
	if err != nil || d.Principal().KeyID() != good.id {
		t.Fatalf("a reader checked for reader: %v %v", d.Principal().KeyID(), err)
	}
	calls, entries := e.recorded()
	if !slices.Equal(calls, []string{"Key", "RolesFor", "RolesFor"}) || len(entries) != 1 || entries[0].KeyID != good.id {
		t.Fatalf("checks after Authenticate: calls %v, entries %+v", calls, entries)
	}

	e.reset()
	lc, err := e.access.Authenticate(e.ctx, local, facts("/m"))
	if err != nil {
		t.Fatal(err)
	}
	if d := e.decide(lc, access.OnInstallation(master.RoleAdmin), "/m"); !d.Principal().IsLocalOperator() {
		t.Fatal("the local operator was not accepted as itself")
	}
	if calls, _ := e.recorded(); len(calls) != 0 {
		t.Fatalf("the local operator caused reads: %v", calls)
	}
}

// TestRecordUnchecked guards the entry of a request no check took up: for
// a credential Authenticate accepted, one failed entry with the reason,
// the facts and the caller; for one it did not accept, nothing, so a refused
// caller's request never writes a second entry.
func TestRecordUnchecked(t *testing.T) {
	e := newEnv(t)
	good := e.key(time.Time{})
	cred, err := e.access.Authenticate(e.ctx, access.APIKey(good.full), facts("/m"))
	if err != nil {
		t.Fatal(err)
	}
	e.reset()
	f := facts("/m")
	f.Details = map[string]string{"served": "false"}
	e.access.RecordUnchecked(cred, f, "no handler serves this method")
	e.access.RecordUnchecked(access.APIKey(good.full), f, "not accepted")
	_, entries := e.recorded()
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1: %+v", len(entries), entries)
	}
	en := entries[0]
	if en.Outcome != master.OutcomeFailed || en.Reason != "no handler serves this method" || en.KeyID != good.id ||
		en.Method != "/m" || en.Details["served"] != "false" || en.RemoteAddress != "192.0.2.7:51000" {
		t.Fatalf("the entry is %+v", en)
	}
	if s := e.access.AuditStats(); s.Failed != 1 {
		t.Fatalf("the credential not accepted was not counted as failed: %+v", s)
	}
}
