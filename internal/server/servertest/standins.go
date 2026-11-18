package servertest

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	magnetarv1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/server"
)

// HugeSize is the size of the state reason ListTenants answers for the
// token "huge": over the 4 MiB limit by a byte, before framing.
const HugeSize = server.MaxMessageBytes + 1

// Standins are the stand-in handlers of the four services.
type Standins struct {
	// Access is the check the delegating methods call.
	Access *access.Access
	// Delay is how long ListTenants holds a request for the token "wait".
	Delay time.Duration
	// Finished counts the requests of the token "wait" that ran to the end,
	// so a test can show that every request in flight finished.
	Finished atomic.Int64
	// Ran counts every handler call, so a test can show a refused request
	// never ran one.
	Ran atomic.Int64
}

// Register registers the four stand-in services on s.
func (h *Standins) Register(s *connect.Server) {
	magnetarv1connect.RegisterAccessServiceHandler(s, &accessService{h: h})
	magnetarv1connect.RegisterTenantServiceHandler(s, &tenantService{h: h})
	magnetarv1connect.RegisterSettingsServiceHandler(s, &settingsService{h: h})
	magnetarv1connect.RegisterAuditServiceHandler(s, &auditService{h: h})
}

// call returns the request's Call and counts the handler's run.
func (h *Standins) call(ctx context.Context) (*server.Call, error) {
	h.Ran.Add(1)
	c, ok := server.CallFrom(ctx)
	if !ok {
		return nil, fault.New(fault.Internal, "internal error")
	}
	return c, nil
}

// accessService is the stand-in AccessService.
type accessService struct {
	magnetarv1connect.UnimplementedAccessServiceHandler
	h *Standins
}

// CreateKey makes a key through the access package.
func (s *accessService) CreateKey(ctx context.Context, req *magnetarv1.CreateKeyRequest) (*magnetarv1.CreateKeyResponse, error) {
	c, err := s.h.call(ctx)
	if err != nil {
		return nil, err
	}
	exp := access.Expiry{}
	switch e := req.GetExpiry().(type) {
	case *magnetarv1.CreateKeyRequest_NeverExpire:
		if !e.NeverExpire {
			return nil, fault.New(fault.Invalid, "never_expire must be true when set")
		}
		exp = access.NeverExpire()
	case *magnetarv1.CreateKeyRequest_ExpireAfter:
		exp = access.ExpireAfter(e.ExpireAfter.AsDuration())
	}
	made, err := s.h.Access.CreateKey(ctx, c.Credential, c.Facts, req.GetHolder(), exp)
	if err != nil {
		return nil, err
	}
	return &magnetarv1.CreateKeyResponse{Key: keyOf(made.Key), Secret: made.Secret.Key()}, nil
}

// ListKeys lists keys through the access package.
func (s *accessService) ListKeys(ctx context.Context, req *magnetarv1.ListKeysRequest) (*magnetarv1.ListKeysResponse, error) {
	c, err := s.h.call(ctx)
	if err != nil {
		return nil, err
	}
	keys, next, err := s.h.Access.ListKeys(ctx, c.Credential, c.Facts, master.Page{Cursor: req.GetPageToken(), Limit: int(req.GetPageSize())})
	if err != nil {
		return nil, err
	}
	out := &magnetarv1.ListKeysResponse{NextPageToken: next}
	for _, k := range keys {
		out.Keys = append(out.Keys, keyOf(k))
	}
	return out, nil
}

// SetGrant sets a grant through the access package.
func (s *accessService) SetGrant(ctx context.Context, req *magnetarv1.SetGrantRequest) (*magnetarv1.SetGrantResponse, error) {
	c, err := s.h.call(ctx)
	if err != nil {
		return nil, err
	}
	scope := access.Scope{}
	if t, ok := req.GetScope().GetKind().(*magnetarv1.Scope_Tenant); ok {
		scope = access.TenantNamed(t.Tenant)
	}
	role := roleOf(req.GetRole())
	g, prev, err := s.h.Access.SetGrant(ctx, c.Credential, c.Facts, req.GetKeyId(), scope, role)
	if err != nil {
		return nil, err
	}
	return &magnetarv1.SetGrantResponse{
		Grant:        &magnetarv1.Grant{KeyId: g.KeyID, Scope: req.GetScope(), Role: req.GetRole()},
		PreviousRole: roleToProto(prev),
	}, nil
}

// WhoAmI answers the caller through the access package.
func (s *accessService) WhoAmI(ctx context.Context, _ *magnetarv1.WhoAmIRequest) (*magnetarv1.WhoAmIResponse, error) {
	c, err := s.h.call(ctx)
	if err != nil {
		return nil, err
	}
	p, _, err := s.h.Access.WhoAmI(ctx, c.Credential, c.Facts)
	if err != nil {
		return nil, err
	}
	out := &magnetarv1.Principal{}
	if p.IsLocalOperator() {
		out.Caller = &magnetarv1.Principal_LocalOperator{LocalOperator: &magnetarv1.LocalOperator{UserId: p.LocalUser()}}
	} else if k, ok := p.Key(); ok {
		out.Caller = &magnetarv1.Principal_Key{Key: keyOf(k)}
	}
	return &magnetarv1.WhoAmIResponse{Principal: out}, nil
}

// tenantService is the stand-in TenantService.
type tenantService struct {
	magnetarv1connect.UnimplementedTenantServiceHandler
	h *Standins
}

// ListTenants answers an empty page, or does what its page token asks (see
// the package documentation).
func (s *tenantService) ListTenants(ctx context.Context, req *magnetarv1.ListTenantsRequest) (*magnetarv1.ListTenantsResponse, error) {
	if _, err := s.h.call(ctx); err != nil {
		return nil, err
	}
	switch req.GetPageToken() {
	case "panic":
		panic("a stand-in handler panics on purpose")
	case "wait":
		t := time.NewTimer(s.h.Delay)
		defer t.Stop()
		select {
		case <-t.C:
			s.h.Finished.Add(1)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "block":
		<-ctx.Done()
		return nil, ctx.Err()
	case "huge":
		return &magnetarv1.ListTenantsResponse{Tenants: []*magnetarv1.Tenant{{Name: "big", StateReason: strings.Repeat("x", HugeSize)}}}, nil
	}
	return &magnetarv1.ListTenantsResponse{}, nil
}

// CreateTenant echoes the tenant it is given, without its address.
func (s *tenantService) CreateTenant(ctx context.Context, req *magnetarv1.CreateTenantRequest) (*magnetarv1.CreateTenantResponse, error) {
	if _, err := s.h.call(ctx); err != nil {
		return nil, err
	}
	return &magnetarv1.CreateTenantResponse{Tenant: &magnetarv1.Tenant{
		Name: req.GetName(), DatabaseType: req.GetDatabaseType(), State: magnetarv1.TenantState_TENANT_STATE_READY, CreateTime: timestamppb.Now(),
	}}, nil
}

// settingsService is the stand-in SettingsService.
type settingsService struct {
	magnetarv1connect.UnimplementedSettingsServiceHandler
	h *Standins
}

// GetSetting answers UNIMPLEMENTED once the check has let it run, and
// counts the run.
func (s *settingsService) GetSetting(ctx context.Context, _ *magnetarv1.GetSettingRequest) (*magnetarv1.GetSettingResponse, error) {
	if _, err := s.h.call(ctx); err != nil {
		return nil, err
	}
	return nil, connect.NewError(connect.CodeUnimplemented, "a stand-in")
}

// auditService is the stand-in AuditService.
type auditService struct {
	magnetarv1connect.UnimplementedAuditServiceHandler
	h *Standins
}

// keyOf is a key in the contract's form.
func keyOf(k master.Key) *magnetarv1.Key {
	out := &magnetarv1.Key{Id: k.ID, Holder: k.Holder, CreateTime: timestamppb.New(k.Created)}
	if !k.Expires.IsZero() {
		out.ExpireTime = timestamppb.New(k.Expires)
	}
	if !k.LastUsed.IsZero() {
		out.LastUseTime = timestamppb.New(k.LastUsed)
	}
	if !k.Revoked.IsZero() {
		out.RevokeTime = timestamppb.New(k.Revoked)
	}
	return out
}

// roleOf is the master's role of a contract role.
func roleOf(r magnetarv1.Role) master.Role {
	switch r {
	case magnetarv1.Role_ROLE_READER:
		return master.RoleReader
	case magnetarv1.Role_ROLE_WRITER:
		return master.RoleWriter
	case magnetarv1.Role_ROLE_OPERATOR:
		return master.RoleOperator
	case magnetarv1.Role_ROLE_ADMIN:
		return master.RoleAdmin
	}
	return ""
}

// roleToProto is the contract's role of a master role.
func roleToProto(r master.Role) magnetarv1.Role {
	switch r {
	case master.RoleReader:
		return magnetarv1.Role_ROLE_READER
	case master.RoleWriter:
		return magnetarv1.Role_ROLE_WRITER
	case master.RoleOperator:
		return magnetarv1.Role_ROLE_OPERATOR
	case master.RoleAdmin:
		return magnetarv1.Role_ROLE_ADMIN
	}
	return magnetarv1.Role_ROLE_UNSPECIFIED
}
