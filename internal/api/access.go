package api

import (
	"context"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/gen/coria/v1/coriav1connect"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/master"
)

// accessService is AccessService. Every change and read goes through package
// access, which has the master check the Decision's key again (in the
// change's transaction, or before a read), writes a change with its audit
// entry in one transaction, records a read through its audit writer,
// and records every failure itself; so an error from it is answered as it
// is, never recorded twice. What this file records is a request it refuses
// as invalid before calling access.
type accessService struct{ c *core }

var _ coriav1connect.AccessServiceHandler = accessService{}

// CreateKey makes a key and answers its secret, once.
func (s accessService) CreateKey(ctx context.Context, req *v1.CreateKeyRequest) (*v1.CreateKeyResponse, error) {
	c := s.c
	d, err := c.changing(ctx, coriav1connect.AccessServiceCreateKeyProcedure)
	if err != nil {
		return nil, err
	}
	// The requirement is fixed, so the check's entry has no object; a
	// request refused here names it in its entry.
	facts := access.Facts{Details: map[string]string{"holder": req.GetHolder()}}
	if err := text("holder", req.GetHolder(), 1, MaxHolderLength); err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	var exp access.Expiry
	switch e := req.GetExpiry().(type) {
	case *v1.CreateKeyRequest_ExpireAfter:
		if e.ExpireAfter.CheckValid() != nil {
			return nil, c.failWith(d, nil, facts, invalid("expire_after", "is not a valid duration"))
		}
		after := e.ExpireAfter.AsDuration()
		if after <= 0 || after > MaxKeyLifetime {
			return nil, c.failWith(d, nil, facts, invalid("expire_after", "is positive and at most 10 years (3650 days)"))
		}
		exp = access.ExpireAfter(after)
	case *v1.CreateKeyRequest_NeverExpire:
		if !e.NeverExpire {
			return nil, c.failWith(d, nil, facts, invalid("never_expire", "is true when set; leave expiry unset for the installation's key.default-lifetime"))
		}
		exp = access.NeverExpire()
	default:
		// No expiry asked for: the installation's key.default-lifetime
		// applies, else its built-in default (access.proto,
		// CreateKeyRequest.expiry).
		lifetime, known, err := c.keyLifetime(ctx)
		if err != nil {
			return nil, c.failWith(d, nil, facts, err)
		}
		if known {
			exp = access.ExpireAfter(lifetime)
		}
	}
	made, err := c.access.CreateKey(ctx, d, req.GetHolder(), exp)
	if err != nil {
		return nil, c.toAPI(d, err)
	}
	return &v1.CreateKeyResponse{Key: keyMessage(made.Key), Secret: made.Secret.Key()}, nil
}

// GetKey answers one key, revoked and expired keys included.
func (s accessService) GetKey(ctx context.Context, req *v1.GetKeyRequest) (*v1.GetKeyResponse, error) {
	c := s.c
	d, err := c.decision(ctx, coriav1connect.AccessServiceGetKeyProcedure)
	if err != nil {
		return nil, err
	}
	// The requirement is fixed, so the check's entry has no object; a
	// request refused here names it in its entry.
	facts := access.Facts{Object: req.GetId()}
	if err := keyID("id", req.GetId()); err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	k, err := c.access.GetKey(ctx, d, req.GetId())
	if err != nil {
		return nil, c.toAPI(d, err)
	}
	return &v1.GetKeyResponse{Key: keyMessage(k)}, nil
}

// ListKeys answers a page of every key, oldest first.
func (s accessService) ListKeys(ctx context.Context, req *v1.ListKeysRequest) (*v1.ListKeysResponse, error) {
	c := s.c
	d, err := c.decision(ctx, coriav1connect.AccessServiceListKeysProcedure)
	if err != nil {
		return nil, err
	}
	page, err := pageOf(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, c.fail(d, err)
	}
	keys, next, err := c.access.ListKeys(ctx, d, page)
	if err != nil {
		return nil, c.toAPI(d, listError(err, page.Cursor))
	}
	out := &v1.ListKeysResponse{Keys: make([]*v1.Key, 0, len(keys)), NextPageToken: next}
	for _, k := range keys {
		out.Keys = append(out.Keys, keyMessage(k))
	}
	return out, nil
}

// RevokeKey ends a key for good; revoking it again answers its first revoke
// time.
func (s accessService) RevokeKey(ctx context.Context, req *v1.RevokeKeyRequest) (*v1.RevokeKeyResponse, error) {
	c := s.c
	d, err := c.changing(ctx, coriav1connect.AccessServiceRevokeKeyProcedure)
	if err != nil {
		return nil, err
	}
	// The requirement is fixed, so the check's entry has no object; a
	// request refused here names it in its entry.
	facts := access.Facts{Object: req.GetId()}
	if err := keyID("id", req.GetId()); err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	k, err := c.access.RevokeKey(ctx, d, req.GetId())
	if err != nil {
		return nil, c.toAPI(d, err)
	}
	return &v1.RevokeKeyResponse{Key: keyMessage(k)}, nil
}

// SetGrant sets a key's role on a scope, replacing the role it held there
// in one step.
func (s accessService) SetGrant(ctx context.Context, req *v1.SetGrantRequest) (*v1.SetGrantResponse, error) {
	c := s.c
	d, err := c.changing(ctx, coriav1connect.AccessServiceSetGrantProcedure)
	if err != nil {
		return nil, err
	}
	// The requirement is fixed, so the check's entry has no object; a
	// request refused here names it in its entry.
	facts := access.Facts{Object: req.GetKeyId(), Details: map[string]string{detailScope: scopeDetail(req.GetScope())}}
	if err := keyID("key_id", req.GetKeyId()); err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	scope, err := scopeOf("scope", req.GetScope())
	if err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	role, ok := masterRole(req.GetRole())
	if !ok {
		return nil, c.failWith(d, nil, facts, invalid("role", "is required: reader, writer, operator or admin"))
	}
	if role == master.RoleWriter && scope.IsInstallation() {
		return nil, c.failWith(d, nil, facts, invalid("role", "writer is not a role on the installation"))
	}
	g, previous, err := c.access.SetGrant(ctx, d, req.GetKeyId(), scope, role)
	if err != nil {
		return nil, c.toAPI(d, err)
	}
	return &v1.SetGrantResponse{Grant: grantMessage(g), PreviousRole: roleOf(previous)}, nil
}

// ListGrants answers a page of the grants of one key or on one scope.
func (s accessService) ListGrants(ctx context.Context, req *v1.ListGrantsRequest) (*v1.ListGrantsResponse, error) {
	c := s.c
	d, err := c.decision(ctx, coriav1connect.AccessServiceListGrantsProcedure)
	if err != nil {
		return nil, err
	}
	// The requirement is fixed, so the check's entry has no object; a
	// request refused here names it in its entry.
	facts := access.Facts{Details: map[string]string{"filter": grantFilterDetail(req)}}
	var filter access.GrantFilter
	switch f := req.GetFilter().(type) {
	case *v1.ListGrantsRequest_KeyId:
		if err := keyID("key_id", f.KeyId); err != nil {
			return nil, c.failWith(d, nil, facts, err)
		}
		filter = access.GrantsOfKey(f.KeyId)
	case *v1.ListGrantsRequest_Scope:
		scope, err := scopeOf("scope", f.Scope)
		if err != nil {
			return nil, c.failWith(d, nil, facts, err)
		}
		filter = access.GrantsOnScope(scope)
	default:
		return nil, c.failWith(d, nil, facts, invalid("filter", "exactly one of key_id and scope is required"))
	}
	page, err := pageOf(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	grants, next, err := c.access.ListGrants(ctx, d, filter, page)
	if err != nil {
		return nil, c.toAPI(d, listError(err, page.Cursor))
	}
	out := &v1.ListGrantsResponse{Grants: make([]*v1.Grant, 0, len(grants)), NextPageToken: next}
	for _, g := range grants {
		out.Grants = append(out.Grants, grantMessage(g))
	}
	return out, nil
}

// RemoveGrant takes back a key's grant on a scope; removing one it does not
// hold succeeds.
func (s accessService) RemoveGrant(ctx context.Context, req *v1.RemoveGrantRequest) (*v1.RemoveGrantResponse, error) {
	c := s.c
	d, err := c.changing(ctx, coriav1connect.AccessServiceRemoveGrantProcedure)
	if err != nil {
		return nil, err
	}
	// The requirement is fixed, so the check's entry has no object; a
	// request refused here names it in its entry.
	facts := access.Facts{Object: req.GetKeyId(), Details: map[string]string{detailScope: scopeDetail(req.GetScope())}}
	if err := keyID("key_id", req.GetKeyId()); err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	scope, err := scopeOf("scope", req.GetScope())
	if err != nil {
		return nil, c.failWith(d, nil, facts, err)
	}
	previous, err := c.access.RemoveGrant(ctx, d, req.GetKeyId(), scope)
	if err != nil {
		return nil, c.toAPI(d, err)
	}
	return &v1.RemoveGrantResponse{PreviousRole: roleOf(previous)}, nil
}

// WhoAmI answers the caller: its key with the key's grants, or the local
// operator.
func (s accessService) WhoAmI(ctx context.Context, _ *v1.WhoAmIRequest) (*v1.WhoAmIResponse, error) {
	c := s.c
	d, err := c.decision(ctx, coriav1connect.AccessServiceWhoAmIProcedure)
	if err != nil {
		return nil, err
	}
	p, grants, err := c.access.WhoAmI(ctx, d)
	if err != nil {
		return nil, c.toAPI(d, err)
	}
	out := &v1.Principal{Grants: make([]*v1.Grant, 0, len(grants))}
	if k, ok := p.Key(); ok {
		out.Caller = &v1.Principal_Key{Key: keyMessage(k)}
	} else {
		out.Caller = &v1.Principal_LocalOperator{LocalOperator: &v1.LocalOperator{UserId: p.LocalUser()}}
	}
	for _, g := range grants {
		out.Grants = append(out.Grants, grantMessage(g))
	}
	return &v1.WhoAmIResponse{Principal: out}, nil
}

// grantFilterDetail returns the filter of a ListGrants request as its audit
// entry's details write it.
func grantFilterDetail(r *v1.ListGrantsRequest) string {
	switch f := r.GetFilter().(type) {
	case *v1.ListGrantsRequest_KeyId:
		return "key/" + f.KeyId
	case *v1.ListGrantsRequest_Scope:
		return scopeDetail(f.Scope)
	}
	return "none"
}
