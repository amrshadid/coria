package sqlite

import (
	"testing"

	"github.com/amrshadid/coria/internal/tenant"
	"github.com/amrshadid/coria/internal/tenant/tenanttest"
)

// TestTenantPipelineContract runs the contract suite's pipeline groups on
// SQLite.
func TestTenantPipelineContract(t *testing.T) {
	tenanttest.RunPipeline(t, func(tb testing.TB, id string) tenant.Pipeline {
		return openTenantTest(tb, id).(*Tenant)
	})
}

// TestTenantPipelineContractShared runs the pipeline's tests that need two
// instances on one tenant database.
func TestTenantPipelineContractShared(t *testing.T) {
	tenanttest.RunPipelineShared(t, func(tb testing.TB, id string) (tenant.Pipeline, tenant.Pipeline) {
		a, b := openTenantPair(tb, id)
		return a.(*Tenant), b.(*Tenant)
	})
}
