package fakeserver

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/api/codes"
	"github.com/amrshadid/magnetar/internal/fault"
)

// RequestIDHeader is the response header the contract has the server set on
// every answer.
const RequestIDHeader = "Magnetar-Request-Id"

// tenantName is the contract's rule for a tenant name: 1 to 63 lowercase
// ASCII letters, digits and hyphens, starting with a letter.
var tenantName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// settingName is the contract's rule for a setting name.
var settingName = regexp.MustCompile(`^[a-z0-9._-]{1,128}$`)

// KnownSettings is the fixed list of settings the fake knows, with their
// installation defaults: a setting not here is "not found".
var KnownSettings = map[string]string{
	"audit.retention":      "365d",
	"receive.quiet-period": "5s",
}

// Server is the fake. Its exported fields are set before it is served; the
// methods that read and change its state are safe to call from the test while
// the server runs.
type Server struct {
	// Now is the clock; the real time when nil.
	Now func() time.Time
	// RequireKey, when not empty, is the one key the server accepts, as
	// "Authorization: Bearer <RequireKey>"; any other request is answered
	// UNAUTHENTICATED. When empty the caller is the local operator, as on the
	// local socket.
	RequireKey string
	// CallerKeyID is the id of the key RequireKey stands for, which WhoAmI
	// reports.
	CallerKeyID string
	// Delay is how long each call waits before it is answered, for a test of
	// the client's deadline.
	Delay time.Duration

	mu        sync.Mutex
	tenants   map[string]*v1.Tenant
	addresses map[string]string
	keys      []*keyRecord
	grants    map[string]v1.Role
	settings  map[string]map[string]string
	audit     []*v1.AuditEntry
	fail      map[string]*connect.Error
	auth      []string
	requests  int
	nextKey   int
}

// keyRecord is a key and the secret it was made with.
type keyRecord struct {
	key    *v1.Key
	secret string
}

// New returns an empty fake with the installation defaults of KnownSettings.
func New() *Server {
	s := &Server{
		tenants:   map[string]*v1.Tenant{},
		addresses: map[string]string{},
		grants:    map[string]v1.Role{},
		settings:  map[string]map[string]string{"": {}},
		fail:      map[string]*connect.Error{},
	}
	for k, v := range KnownSettings {
		s.settings[""][k] = v
	}
	return s
}

// now returns the fake's time.
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// FailWith makes every call of a procedure (such as
// magnetarv1connect.TenantServiceCreateTenantProcedure) answer with an API
// error of the given code and message, after the request id is set.
func (s *Server) FailWith(procedure string, code connect.Code, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[procedure] = connect.NewError(code, message)
}

// AddTenant puts a tenant in the fake directly, for a test that needs many.
func (s *Server) AddTenant(t *v1.Tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[t.GetName()] = t
}

// AddKey puts a key in the fake directly.
func (s *Server) AddKey(k *v1.Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, &keyRecord{key: k})
}

// AddAudit puts an entry in the audit log, newest first when called in that
// order.
func (s *Server) AddAudit(e *v1.AuditEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
}

// DatabaseAddress returns the address CreateTenant received for a tenant, so a
// test can show that it reached the server and nowhere else.
func (s *Server) DatabaseAddress(tenant string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addresses[tenant]
}

// AuthorizationHeaders returns the Authorization header of every request seen,
// in order, so a test can show what the client sent.
func (s *Server) AuthorizationHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

// Requests returns how many API calls the server has answered.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Register adds the four services to a Connect server, with the interceptor
// that makes the request id, checks the key and turns kinds into codes.
func (s *Server) Register(srv *connect.Server) {
	magnetarv1connect.RegisterTenantServiceHandler(srv, tenantHandler{s})
	magnetarv1connect.RegisterAccessServiceHandler(srv, accessHandler{s})
	magnetarv1connect.RegisterSettingsServiceHandler(srv, settingsHandler{s})
	magnetarv1connect.RegisterAuditServiceHandler(srv, auditHandler{s})
}

// interceptor is the one place every call passes: the request id first, so
// every answer has it; then the key; then the forced failure of a test; and
// last the handler, whose error is turned into a code by the codes package,
// the way the real server will.
func (s *Server) interceptor(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		info, _ := connect.CallInfoForServerContext(ctx)
		s.mu.Lock()
		s.requests++
		id := fmt.Sprintf("req-%06d", s.requests)
		header := info.RequestHeader().Get("Authorization")
		s.auth = append(s.auth, header)
		forced := s.fail[spec.Procedure]
		s.mu.Unlock()
		info.ResponseHeader().Set(RequestIDHeader, id)

		if s.Delay > 0 {
			select {
			case <-time.After(s.Delay):
			case <-ctx.Done():
				return codes.ToConnect(fault.New(fault.Unavailable, "the call was abandoned"))
			}
		}

		if s.RequireKey != "" && header != "Bearer "+s.RequireKey {
			// One message for every cause, as the contract requires.
			return codes.ToConnect(fault.New(fault.Unauthenticated, "the key is missing, unknown, expired or revoked"))
		}
		if forced != nil {
			return forced
		}
		if err := next(ctx, spec, stream); err != nil {
			return codes.ToConnect(err)
		}
		return nil
	}
}

// pageOf applies the contract's paging to n items: a size of zero is 100, more
// than 1000 or less than zero is invalid, and a token is opaque (here, the
// offset behind a prefix). It returns the range to answer and the next token.
func pageOf(n int, size int32, token string) (from, to int, next string, err error) {
	switch {
	case size < 0 || size > 1000:
		return 0, 0, "", fault.New(fault.Invalid, "page_size must be between 0 and 1000")
	case size == 0:
		size = 100
	}
	if len(token) > 1024 {
		return 0, 0, "", fault.New(fault.Invalid, "page_token is too long")
	}
	if token != "" {
		off, ok := strings.CutPrefix(token, "p")
		v, convErr := strconv.Atoi(off)
		if !ok || convErr != nil || v < 0 || v > n {
			return 0, 0, "", fault.New(fault.Invalid, "page_token is not one this server made")
		}
		from = v
	}
	to = min(from+int(size), n)
	if to < n {
		next = "p" + strconv.Itoa(to)
	}
	return from, to, next, nil
}

// scopeOf reads a Scope into its tenant name, which is empty for the
// installation, or an Invalid error for a scope with neither or an empty
// tenant name.
func scopeOf(sc *v1.Scope) (tenant string, installation bool, err error) {
	switch k := sc.GetKind().(type) {
	case *v1.Scope_Tenant:
		if k.Tenant == "" {
			return "", false, fault.New(fault.Invalid, "the scope names no tenant")
		}
		return k.Tenant, false, nil
	case *v1.Scope_Installation:
		return "", true, nil
	}
	return "", false, fault.New(fault.Invalid, "the scope is neither a tenant nor the installation")
}

// scopeText is a scope as a key in the grant table.
func scopeText(sc *v1.Scope) string {
	if t := sc.GetTenant(); t != "" {
		return "tenant:" + t
	}
	return "installation"
}

// checkTenant is the contract's answer for a tenant that must exist; the
// caller here may list every tenant, so a missing one is NOT_FOUND.
func (s *Server) checkTenant(name string) error {
	if _, ok := s.tenants[name]; !ok {
		return fault.New(fault.NotFound, "no tenant has that name")
	}
	return nil
}

// tenantHandler implements TenantService.
type tenantHandler struct{ s *Server }

// CreateTenant makes a tenant, refusing a bad name, a database address that
// does not fit the type, and a name already taken.
func (h tenantHandler) CreateTenant(_ context.Context, req *v1.CreateTenantRequest) (*v1.CreateTenantResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if !tenantName.MatchString(req.GetName()) {
		return nil, fault.New(fault.Invalid, "the tenant name must be 1 to 63 lowercase letters, digits and hyphens, starting with a letter")
	}
	switch req.GetDatabaseType() {
	case v1.DatabaseType_DATABASE_TYPE_SQLITE:
		if req.GetDatabaseAddress() != "" {
			return nil, fault.New(fault.Invalid, "a SQLite tenant takes no database address")
		}
	case v1.DatabaseType_DATABASE_TYPE_POSTGRES:
		if req.GetDatabaseAddress() == "" || len(req.GetDatabaseAddress()) > 4096 {
			return nil, fault.New(fault.Invalid, "a Postgres tenant needs a database address of at most 4 KiB")
		}
	default:
		return nil, fault.New(fault.Invalid, "the database type is required")
	}
	if _, dup := s.tenants[req.GetName()]; dup {
		return nil, fault.New(fault.Conflict, "a tenant with that name exists")
	}
	t := &v1.Tenant{
		Name:         req.GetName(),
		DatabaseType: req.GetDatabaseType(),
		State:        v1.TenantState_TENANT_STATE_READY,
		CreateTime:   timestamppb.New(s.now()),
	}
	s.tenants[t.GetName()] = t
	s.addresses[t.GetName()] = req.GetDatabaseAddress()
	return &v1.CreateTenantResponse{Tenant: t}, nil
}

// GetTenant answers one tenant.
func (h tenantHandler) GetTenant(_ context.Context, req *v1.GetTenantRequest) (*v1.GetTenantResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if req.GetName() == "" {
		return nil, fault.New(fault.Invalid, "the tenant name is required")
	}
	if err := h.s.checkTenant(req.GetName()); err != nil {
		return nil, err
	}
	return &v1.GetTenantResponse{Tenant: h.s.tenants[req.GetName()]}, nil
}

// ListTenants answers a page of tenants ordered by name.
func (h tenantHandler) ListTenants(_ context.Context, req *v1.ListTenantsRequest) (*v1.ListTenantsResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	names := make([]string, 0, len(h.s.tenants))
	for n := range h.s.tenants {
		names = append(names, n)
	}
	sort.Strings(names)
	from, to, next, err := pageOf(len(names), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	res := &v1.ListTenantsResponse{NextPageToken: next}
	for _, n := range names[from:to] {
		res.Tenants = append(res.Tenants, h.s.tenants[n])
	}
	return res, nil
}

// RemoveTenant unregisters a tenant and says where its data was left. The name
// is sent twice and the two must agree.
func (h tenantHandler) RemoveTenant(_ context.Context, req *v1.RemoveTenantRequest) (*v1.RemoveTenantResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if req.GetName() == "" || req.GetName() != req.GetConfirmName() {
		return nil, fault.New(fault.Invalid, "name and confirm_name must both be given and be equal")
	}
	t, ok := h.s.tenants[req.GetName()]
	if !ok {
		return nil, fault.New(fault.NotFound, "no tenant has that name")
	}
	delete(h.s.tenants, req.GetName())
	loc := "/var/lib/magnetar/tenants/" + t.GetName() + ".db"
	if t.GetDatabaseType() == v1.DatabaseType_DATABASE_TYPE_POSTGRES {
		loc = "db.example.org:5432/" + t.GetName()
	}
	return &v1.RemoveTenantResponse{DatabaseType: t.GetDatabaseType(), DataLocation: loc}, nil
}

// accessHandler implements AccessService.
type accessHandler struct{ s *Server }

// findKey returns the record of a key id.
func (s *Server) findKey(id string) (*keyRecord, error) {
	if id == "" {
		return nil, fault.New(fault.Invalid, "the key id is required")
	}
	for _, k := range s.keys {
		if k.key.GetId() == id {
			return k, nil
		}
	}
	return nil, fault.New(fault.NotFound, "no key has that id")
}

// CreateKey makes a key whose secret is answered once.
func (h accessHandler) CreateKey(_ context.Context, req *v1.CreateKeyRequest) (*v1.CreateKeyResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.GetHolder() == "" || len(req.GetHolder()) > 256 {
		return nil, fault.New(fault.Invalid, "the holder is required and at most 256 bytes")
	}
	created := s.now()
	expire := created.Add(90 * 24 * time.Hour)
	switch e := req.GetExpiry().(type) {
	case *v1.CreateKeyRequest_ExpireAfter:
		d := e.ExpireAfter.AsDuration()
		if d <= 0 || d > 3650*24*time.Hour {
			return nil, fault.New(fault.Invalid, "expire_after must be positive and at most 3650 days")
		}
		expire = created.Add(d)
	case *v1.CreateKeyRequest_NeverExpire:
		if !e.NeverExpire {
			return nil, fault.New(fault.Invalid, "never_expire must be true when set")
		}
	}
	s.nextKey++
	id := fmt.Sprintf("k%011d", s.nextKey)
	key := &v1.Key{Id: id, Holder: req.GetHolder(), CreateTime: timestamppb.New(created)}
	if _, never := req.GetExpiry().(*v1.CreateKeyRequest_NeverExpire); !never {
		key.ExpireTime = timestamppb.New(expire)
	}
	secret := "mgt_" + id + "_" + strings.Repeat("S", 44)
	s.keys = append(s.keys, &keyRecord{key: key, secret: secret})
	return &v1.CreateKeyResponse{Key: key, Secret: secret}, nil
}

// GetKey answers one key.
func (h accessHandler) GetKey(_ context.Context, req *v1.GetKeyRequest) (*v1.GetKeyResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	k, err := h.s.findKey(req.GetId())
	if err != nil {
		return nil, err
	}
	return &v1.GetKeyResponse{Key: k.key}, nil
}

// ListKeys answers a page of keys, oldest first.
func (h accessHandler) ListKeys(_ context.Context, req *v1.ListKeysRequest) (*v1.ListKeysResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	from, to, next, err := pageOf(len(h.s.keys), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	res := &v1.ListKeysResponse{NextPageToken: next}
	for _, k := range h.s.keys[from:to] {
		res.Keys = append(res.Keys, k.key)
	}
	return res, nil
}

// RevokeKey ends a key; revoking a revoked key answers it with its first
// revoke time.
func (h accessHandler) RevokeKey(_ context.Context, req *v1.RevokeKeyRequest) (*v1.RevokeKeyResponse, error) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	k, err := h.s.findKey(req.GetId())
	if err != nil {
		return nil, err
	}
	if k.key.GetRevokeTime() == nil {
		k.key.RevokeTime = timestamppb.New(h.s.now())
	}
	return &v1.RevokeKeyResponse{Key: k.key}, nil
}

// SetGrant sets a key's role on a scope, replacing the one it had.
func (h accessHandler) SetGrant(_ context.Context, req *v1.SetGrantRequest) (*v1.SetGrantResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	k, err := s.findKey(req.GetKeyId())
	if err != nil {
		return nil, err
	}
	tenant, installation, err := scopeOf(req.GetScope())
	if err != nil {
		return nil, err
	}
	if req.GetRole() == v1.Role_ROLE_UNSPECIFIED {
		return nil, fault.New(fault.Invalid, "the role is required")
	}
	if installation && req.GetRole() == v1.Role_ROLE_WRITER {
		return nil, fault.New(fault.Invalid, "the writer role is not given on the installation")
	}
	if !installation {
		if err := s.checkTenant(tenant); err != nil {
			return nil, err
		}
	}
	if k.key.GetRevokeTime() != nil {
		return nil, fault.New(fault.Conflict, "the key is revoked")
	}
	gk := req.GetKeyId() + "|" + scopeText(req.GetScope())
	prev := s.grants[gk]
	s.grants[gk] = req.GetRole()
	return &v1.SetGrantResponse{
		Grant:        &v1.Grant{KeyId: req.GetKeyId(), Scope: req.GetScope(), Role: req.GetRole()},
		PreviousRole: prev,
	}, nil
}

// allGrants returns every grant ordered by key id and then scope.
func (s *Server) allGrants() []*v1.Grant {
	keys := make([]string, 0, len(s.grants))
	for k := range s.grants {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*v1.Grant, 0, len(keys))
	for _, gk := range keys {
		keyID, scope, _ := strings.Cut(gk, "|")
		g := &v1.Grant{KeyId: keyID, Role: s.grants[gk], Scope: &v1.Scope{Kind: &v1.Scope_Installation{Installation: &v1.Installation{}}}}
		if t, ok := strings.CutPrefix(scope, "tenant:"); ok {
			g.Scope = &v1.Scope{Kind: &v1.Scope_Tenant{Tenant: t}}
		}
		out = append(out, g)
	}
	return out
}

// ListGrants answers the grants of one key or on one scope; exactly one filter
// is required.
func (h accessHandler) ListGrants(_ context.Context, req *v1.ListGrantsRequest) (*v1.ListGrantsResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var match func(*v1.Grant) bool
	switch f := req.GetFilter().(type) {
	case *v1.ListGrantsRequest_KeyId:
		if _, err := s.findKey(f.KeyId); err != nil {
			return nil, err
		}
		match = func(g *v1.Grant) bool { return g.GetKeyId() == f.KeyId }
	case *v1.ListGrantsRequest_Scope:
		tenant, installation, err := scopeOf(f.Scope)
		if err != nil {
			return nil, err
		}
		if !installation {
			if err := s.checkTenant(tenant); err != nil {
				return nil, err
			}
		}
		want := scopeText(f.Scope)
		match = func(g *v1.Grant) bool { return scopeText(g.GetScope()) == want }
	default:
		return nil, fault.New(fault.Invalid, "exactly one of key_id and scope is required")
	}
	var grants []*v1.Grant
	for _, g := range s.allGrants() {
		if match(g) {
			grants = append(grants, g)
		}
	}
	from, to, next, err := pageOf(len(grants), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	return &v1.ListGrantsResponse{Grants: grants[from:to], NextPageToken: next}, nil
}

// RemoveGrant takes back a grant; removing one that is not held succeeds.
func (h accessHandler) RemoveGrant(_ context.Context, req *v1.RemoveGrantRequest) (*v1.RemoveGrantResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.findKey(req.GetKeyId()); err != nil {
		return nil, err
	}
	tenant, installation, err := scopeOf(req.GetScope())
	if err != nil {
		return nil, err
	}
	if !installation {
		if err := s.checkTenant(tenant); err != nil {
			return nil, err
		}
	}
	gk := req.GetKeyId() + "|" + scopeText(req.GetScope())
	prev := s.grants[gk]
	delete(s.grants, gk)
	return &v1.RemoveGrantResponse{PreviousRole: prev}, nil
}

// WhoAmI answers the key the request carried with its grants, or the local
// operator when the server needs no key.
func (h accessHandler) WhoAmI(context.Context, *v1.WhoAmIRequest) (*v1.WhoAmIResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RequireKey == "" {
		return &v1.WhoAmIResponse{Principal: &v1.Principal{
			Caller: &v1.Principal_LocalOperator{LocalOperator: &v1.LocalOperator{UserId: "501"}},
		}}, nil
	}
	p := &v1.Principal{}
	if k, err := s.findKey(s.CallerKeyID); err == nil {
		p.Caller = &v1.Principal_Key{Key: k.key}
	} else {
		p.Caller = &v1.Principal_Key{Key: &v1.Key{Id: s.CallerKeyID}}
	}
	for _, g := range s.allGrants() {
		if g.GetKeyId() == s.CallerKeyID {
			p.Grants = append(p.Grants, g)
		}
	}
	return &v1.WhoAmIResponse{Principal: p}, nil
}

// settingsHandler implements SettingsService.
type settingsHandler struct{ s *Server }

// settingScope reads a settings scope into the key of the settings map: ""
// for the installation, the tenant name otherwise, after checking the tenant
// exists.
func (s *Server) settingScope(sc *v1.Scope) (string, error) {
	tenant, _, err := scopeOf(sc)
	if err != nil {
		return "", err
	}
	if tenant != "" {
		if err := s.checkTenant(tenant); err != nil {
			return "", err
		}
	}
	return tenant, nil
}

// checkSettingName applies the contract's rule for a name and the fixed list.
func checkSettingName(name string) error {
	if !settingName.MatchString(name) {
		return fault.New(fault.Invalid, "the setting name must be 1 to 128 of lowercase letters, digits, '.', '_' and '-'")
	}
	if _, ok := KnownSettings[name]; !ok {
		return fault.New(fault.NotFound, "no setting has that name")
	}
	return nil
}

// settingOf builds a Setting for a scope key.
func settingOf(name, scopeKey, value string) *v1.Setting {
	sc := &v1.Scope{Kind: &v1.Scope_Installation{Installation: &v1.Installation{}}}
	if scopeKey != "" {
		sc = &v1.Scope{Kind: &v1.Scope_Tenant{Tenant: scopeKey}}
	}
	return &v1.Setting{Name: name, Scope: sc, Value: value}
}

// GetSetting answers the value that applies to a scope and where it came from.
func (h settingsHandler) GetSetting(_ context.Context, req *v1.GetSettingRequest) (*v1.GetSettingResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, err := s.settingScope(req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := checkSettingName(req.GetName()); err != nil {
		return nil, err
	}
	if v, ok := s.settings[sk][req.GetName()]; ok {
		return &v1.GetSettingResponse{Setting: settingOf(req.GetName(), sk, v)}, nil
	}
	return &v1.GetSettingResponse{Setting: settingOf(req.GetName(), "", s.settings[""][req.GetName()])}, nil
}

// SetSetting sets a default or an override.
func (h settingsHandler) SetSetting(_ context.Context, req *v1.SetSettingRequest) (*v1.SetSettingResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	st := req.GetSetting()
	sk, err := s.settingScope(st.GetScope())
	if err != nil {
		return nil, err
	}
	if err := checkSettingName(st.GetName()); err != nil {
		return nil, err
	}
	if st.GetValue() == "" || len(st.GetValue()) > 64<<10 {
		return nil, fault.New(fault.Invalid, "the value is required and at most 64 KiB")
	}
	if s.settings[sk] == nil {
		s.settings[sk] = map[string]string{}
	}
	s.settings[sk][st.GetName()] = st.GetValue()
	return &v1.SetSettingResponse{Setting: settingOf(st.GetName(), sk, st.GetValue())}, nil
}

// UnsetSetting removes a tenant's override; the installation's cannot be.
func (h settingsHandler) UnsetSetting(_ context.Context, req *v1.UnsetSettingRequest) (*v1.UnsetSettingResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, installation, _ := scopeOf(req.GetScope()); installation {
		return nil, fault.New(fault.Invalid, "the installation's default cannot be removed")
	}
	sk, err := s.settingScope(req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := checkSettingName(req.GetName()); err != nil {
		return nil, err
	}
	delete(s.settings[sk], req.GetName())
	return &v1.UnsetSettingResponse{Setting: settingOf(req.GetName(), "", s.settings[""][req.GetName()])}, nil
}

// ListSettings answers the settings set on a scope, ordered by name.
func (h settingsHandler) ListSettings(_ context.Context, req *v1.ListSettingsRequest) (*v1.ListSettingsResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, err := s.settingScope(req.GetScope())
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(s.settings[sk]))
	for n := range s.settings[sk] {
		names = append(names, n)
	}
	sort.Strings(names)
	from, to, next, err := pageOf(len(names), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	res := &v1.ListSettingsResponse{NextPageToken: next}
	for _, n := range names[from:to] {
		res.Settings = append(res.Settings, settingOf(n, sk, s.settings[sk][n]))
	}
	return res, nil
}

// auditHandler implements AuditService.
type auditHandler struct{ s *Server }

// ListAuditEntries answers a page of the entries added with AddAudit that
// match the scope and the filters, newest first as they were added.
func (h auditHandler) ListAuditEntries(_ context.Context, req *v1.ListAuditEntriesRequest) (*v1.ListAuditEntriesResponse, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	tenant, installation, err := scopeOf(req.GetScope())
	if err != nil {
		return nil, err
	}
	if !installation {
		if err := s.checkTenant(tenant); err != nil {
			return nil, err
		}
	}
	if req.GetStartTime() != nil && req.GetEndTime() != nil && !req.GetEndTime().AsTime().After(req.GetStartTime().AsTime()) {
		return nil, fault.New(fault.Invalid, "end_time must be after start_time")
	}
	var entries []*v1.AuditEntry
	for _, e := range s.audit {
		switch {
		case e.GetTenant() != tenant:
		case req.GetKeyId() != "" && e.GetKeyId() != req.GetKeyId():
		case req.GetOutcome() != v1.AuditOutcome_AUDIT_OUTCOME_UNSPECIFIED && e.GetOutcome() != req.GetOutcome():
		case req.GetStartTime() != nil && e.GetTime().AsTime().Before(req.GetStartTime().AsTime()):
		case req.GetEndTime() != nil && !e.GetTime().AsTime().Before(req.GetEndTime().AsTime()):
		default:
			entries = append(entries, e)
		}
	}
	from, to, next, err := pageOf(len(entries), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	return &v1.ListAuditEntriesResponse{Entries: entries[from:to], NextPageToken: next}, nil
}
