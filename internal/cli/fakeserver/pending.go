package fakeserver

import (
	"context"

	connect "connectrpc.com/connect/v2"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// errNotServed answers the methods the contract defines and the real
// server does not serve yet, as the real server does.
var errNotServed = connect.NewError(connect.CodeUnimplemented, "no such method")

// ReclaimTenant is not served yet by the real server, so not here either.
func (tenantHandler) ReclaimTenant(context.Context, *v1.ReclaimTenantRequest) (*v1.ReclaimTenantResponse, error) {
	return nil, errNotServed
}

// ReinstateKeys is not served yet by the real server, so not here either.
func (accessHandler) ReinstateKeys(context.Context, *v1.ReinstateKeysRequest) (*v1.ReinstateKeysResponse, error) {
	return nil, errNotServed
}

// ListImportFiles is not served yet by the real server, so not here either.
func (jobHandler) ListImportFiles(context.Context, *v1.ListImportFilesRequest) (*v1.ListImportFilesResponse, error) {
	return nil, errNotServed
}

// ListImportUnits is not served yet by the real server, so not here either.
func (jobHandler) ListImportUnits(context.Context, *v1.ListImportUnitsRequest) (*v1.ListImportUnitsResponse, error) {
	return nil, errNotServed
}
