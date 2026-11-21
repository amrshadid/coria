package api

import (
	"context"

	connect "connectrpc.com/connect/v2"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// errNotServed is what a method the contract defines and no handler serves
// yet answers, should a call ever reach it. The server does not mount such
// a method (Methods.Pending), so it answers the call itself, as it answers
// any method it does not serve; these bodies exist only because a served
// service's generated interface names every one of its methods.
var errNotServed = connect.NewError(connect.CodeUnimplemented, "no such method")

// ReclaimTenant is not served yet: TenantService's other methods are.
func (tenantService) ReclaimTenant(context.Context, *v1.ReclaimTenantRequest) (*v1.ReclaimTenantResponse, error) {
	return nil, errNotServed
}

// ReinstateKeys is not served yet: AccessService's other methods are.
func (accessService) ReinstateKeys(context.Context, *v1.ReinstateKeysRequest) (*v1.ReinstateKeysResponse, error) {
	return nil, errNotServed
}

// ListImportFiles is not served yet: JobService's other methods are.
func (jobService) ListImportFiles(context.Context, *v1.ListImportFilesRequest) (*v1.ListImportFilesResponse, error) {
	return nil, errNotServed
}

// ListImportUnits is not served yet: JobService's other methods are.
func (jobService) ListImportUnits(context.Context, *v1.ListImportUnitsRequest) (*v1.ListImportUnitsResponse, error) {
	return nil, errNotServed
}
