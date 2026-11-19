package postgres

import (
	"testing"

	"github.com/amrshadid/magnetar/internal/tenant/tenanttest"
)

// TestTenantEventsContract runs the contract suite's event groups on
// Postgres, each case on a schema of its own.
func TestTenantEventsContract(t *testing.T) {
	tenanttest.RunEvents(t, func(tb testing.TB, id string) tenanttest.EventStore {
		return openTenantTest(tb, id).(*Tenant)
	})
}

// TestTenantEventsContractShared runs the event tests that need two
// instances on one tenant database, each with a pool of its own, as two
// processes would: here their transactions are truly at once, which is what
// the feed's lock puts in commit order.
func TestTenantEventsContractShared(t *testing.T) {
	tenanttest.RunEventsShared(t, func(tb testing.TB, id string) (tenanttest.EventStore, tenanttest.EventStore) {
		a, b := openTenantPair(tb, id)
		return a.(*Tenant), b.(*Tenant)
	})
}
