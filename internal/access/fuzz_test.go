package access_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// FuzzCheck guards the check against arbitrary presented keys and tenant
// names (acceptance 5): it never panics, never returns a principal for a
// key that is not the stored one, always answers Unauthenticated with the
// one fixed message, and always writes one refusal entry the master would
// take. The store is in memory, so the fuzzer runs fast; the stored key's
// id is a seed, so well-formed keys with a known id and a wrong secret are
// explored too, but the stored secret is never given.
func FuzzCheck(f *testing.F) {
	m, full := newMem(f)
	a := newMemAccess(f, m, 0)
	id := m.key.ID
	for _, seed := range []string{
		"", "mgt_", "mgt__", "Bearer x", "mgt_" + id + "_",
		"mgt_" + id + "_" + strings.Repeat("1", 44),
		"mgt_" + strings.Repeat("1", 12) + "_" + strings.Repeat("z", 44),
		strings.Repeat("\x00", 61), "mgt_" + id + "_" + strings.Repeat("é", 22),
	} {
		f.Add(seed, "lab")
	}
	f.Add("mgt_"+id+"_"+strings.Repeat("A", 44), strings.Repeat("x", 300))
	f.Fuzz(func(t *testing.T, key, tenant string) {
		if key == full {
			t.Skip("the stored key itself")
		}
		m.mu.Lock()
		before := len(m.audit)
		m.mu.Unlock()
		d, err := a.Check(context.Background(), access.APIKey(key), access.OnTenant(tenant, master.RoleReader), facts("fuzz"))
		if err == nil || !reflect.DeepEqual(d, access.Decision{}) {
			t.Fatalf("a principal for %q: %+v, %v", key, d, err)
		}
		if fault.KindOf(err) != fault.Unauthenticated || err.Error() != access.MessageUnauthenticated {
			t.Fatalf("%q: %v (%s)", key, err, fault.KindOf(err))
		}
		if err := a.FlushAudit(context.Background()); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.audit) != before+1 {
			t.Fatalf("%q: %d entries written, want 1", key, len(m.audit)-before)
		}
		if m.badEntry != nil {
			t.Fatalf("%q: an entry the master would refuse: %v", key, m.badEntry)
		}
		// Keep memory flat over a long run.
		m.audit = m.audit[:0]
	})
}
