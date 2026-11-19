package postgres

import (
	"testing"

	"github.com/amrshadid/magnetar/internal/tenant"
	"github.com/amrshadid/magnetar/internal/tenant/tenanttest"
)

// TestTenantPipelineContract runs the contract suite's pipeline groups on
// Postgres, each case on a schema of its own, in parallel.
func TestTenantPipelineContract(t *testing.T) {
	tenanttest.RunPipeline(t, func(tb testing.TB, id string) tenant.Pipeline {
		return openTenantTest(tb, id).(*Tenant)
	})
}

// TestTenantPipelineContractShared runs the pipeline's cases that need two
// instances on one tenant database, each with a pool of its own, as two
// processes would: here their transactions are truly at once.
func TestTenantPipelineContractShared(t *testing.T) {
	tenanttest.RunPipelineShared(t, func(tb testing.TB, id string) (tenant.Pipeline, tenant.Pipeline) {
		a, b := openTenantPair(tb, id)
		return a.(*Tenant), b.(*Tenant)
	})
}
