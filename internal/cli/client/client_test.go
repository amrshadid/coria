package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect/v2"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/cli/client"
	"github.com/amrshadid/magnetar/internal/cli/fakeserver"
	"github.com/amrshadid/magnetar/internal/fault"
)

// testKey is a made-up key for the fake; it is not a real credential.
const testKey = "mgt_testtesttest_" + "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqr"

// env returns an environment reader over a map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// listTenants is the call the tests make: the simplest one that needs no setup.
func listTenants(c *client.Client) (*v1.ListTenantsResponse, error) {
	return client.Call(context.Background(), c, "tenant list", func(ctx context.Context) (*v1.ListTenantsResponse, error) {
		return c.Tenants.ListTenants(ctx, &v1.ListTenantsRequest{})
	})
}

// TestLocalSocketCall guards the default transport: a call over the unix
// socket with HTTP/2 without TLS reaches the server, sends no key, and an
// error answer carries its kind, the server's message and the request id.
func TestLocalSocketCall(t *testing.T) {
	fake := fakeserver.New()
	fake.AddTenant(&v1.Tenant{Name: "lab1"})
	dir := fake.ServeUnix(t)
	c, err := client.New(client.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := listTenants(c)
	if err != nil || len(res.GetTenants()) != 1 {
		t.Fatalf("ListTenants = %v, %v", res, err)
	}
	if h := fake.AuthorizationHeaders(); len(h) != 1 || h[0] != "" {
		t.Errorf("Authorization headers on the socket = %q, want one empty", h)
	}

	_, err = client.Call(context.Background(), c, "tenant show", func(ctx context.Context) (*v1.GetTenantResponse, error) {
		return c.Tenants.GetTenant(ctx, &v1.GetTenantRequest{Name: "nope"})
	})
	var ce *client.CallError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a CallError", err)
	}
	if fault.KindOf(err) != fault.NotFound || ce.RequestID != "req-000002" {
		t.Errorf("kind %v, request id %q", fault.KindOf(err), ce.RequestID)
	}
	if want := "tenant show: no tenant has that name (request id req-000002)"; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
}

// TestNetworkSendsTheKeyOverTLS guards the network transport: TLS on loopback
// with a certificate the client trusts, the key from the environment sent as a
// bearer token, and a wrong key answered as unauthenticated with a request id.
func TestNetworkSendsTheKeyOverTLS(t *testing.T) {
	fake := fakeserver.New()
	fake.RequireKey = testKey
	addr, roots := fake.ServeTLS(t)
	c, err := client.New(client.Config{Server: addr, RootCAs: roots, Getenv: env(map[string]string{client.EnvKey: testKey})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := listTenants(c); err != nil {
		t.Fatal(err)
	}
	if h := fake.AuthorizationHeaders(); len(h) != 1 || h[0] != "Bearer "+testKey {
		t.Errorf("Authorization headers = %q", h)
	}

	bad, err := client.New(client.Config{Server: addr, RootCAs: roots, Getenv: env(map[string]string{client.EnvKey: "wrong"})})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	_, err = listTenants(bad)
	if fault.KindOf(err) != fault.Unauthenticated || !strings.Contains(err.Error(), "request id req-") {
		t.Errorf("wrong key: kind %v, error %v", fault.KindOf(err), err)
	}
	if strings.Contains(err.Error(), "wrong") && strings.Contains(err.Error(), "Bearer") {
		t.Errorf("the error shows the key: %v", err)
	}
}

// TestUntrustedCertificateIsRefused guards the TLS check: a server whose
// certificate the system does not trust is refused with a line that says so,
// and the key is not sent to it.
func TestUntrustedCertificateIsRefused(t *testing.T) {
	fake := fakeserver.New()
	addr, _ := fake.ServeTLS(t)
	c, err := client.New(client.Config{Server: addr, Getenv: env(map[string]string{client.EnvKey: testKey})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = listTenants(c)
	if err == nil || !strings.Contains(err.Error(), "its certificate is not trusted by this system (signed by an unknown authority)") {
		t.Fatalf("error = %v", err)
	}
	if fake.Requests() != 0 {
		t.Errorf("the server saw %d requests, want none", fake.Requests())
	}
}

// TestLoopbackHTTPWorks guards the one place plain http is allowed: a loopback
// address.
func TestLoopbackHTTPWorks(t *testing.T) {
	fake := fakeserver.New()
	fake.RequireKey = testKey
	addr := fake.ServeHTTP(t)
	c, err := client.New(client.Config{Server: addr, Getenv: env(map[string]string{client.EnvKey: testKey})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := listTenants(c); err != nil {
		t.Fatal(err)
	}
}

// TestServerAddressRules guards ParseServer: https anywhere, http only for
// loopback, nothing else, and no credentials in the address; and that no
// message quotes the credentials it refused.
func TestServerAddressRules(t *testing.T) {
	ok := []string{"https://example.org", "https://example.org:8443/", "http://localhost:8080", "http://127.0.0.1:1", "http://[::1]:9", "http://127.9.9.9"}
	for _, a := range ok {
		if _, err := client.ParseServer(a); err != nil {
			t.Errorf("ParseServer(%q) = %v, want accepted", a, err)
		}
	}
	bad := map[string]string{
		"http://example.org":                  "http is allowed only for a loopback host",
		"http://10.0.0.5":                     "http is allowed only for a loopback host",
		"http://localhost.example.org":        "http is allowed only for a loopback host",
		"ftp://example.org":                   "must start with https://",
		"example.org:8443":                    "not a URL",
		"example://host":                      "must start with https://",
		"":                                    "not a URL",
		"https://":                            "not a URL",
		"https://user:hunter2@example.org":    "must not hold a user name or password",
		"https://example.org?token=hunter2":   "must not have a query",
		"https://example.org/#hunter2":        "must not have a query or a fragment",
		"http://exa mple.org/hunter2":         "not a URL",
		"https://%zz:hunter2@example.org/bad": "not a URL",
	}
	for a, want := range bad {
		_, err := client.ParseServer(a)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseServer(%q) = %v, want an error containing %q", a, err, want)
			continue
		}
		if fault.KindOf(err) != fault.Invalid {
			t.Errorf("ParseServer(%q): kind %v, want invalid", a, fault.KindOf(err))
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("ParseServer(%q) quotes the credentials: %v", a, err)
		}
	}
}

// TestReadKey guards where a key comes from and that no message shows it: the
// file wins over the environment, whitespace around the key is dropped, and an
// empty, huge or non-printable key is refused with a message that names the
// source and not the content.
func TestReadKey(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if k, err := client.ReadKey("", env(map[string]string{client.EnvKey: " " + testKey + "\n"})); err != nil || k != testKey {
		t.Errorf("from the environment: %q, %v", k, err)
	}
	if k, err := client.ReadKey(write("k", testKey+"\n"), env(map[string]string{client.EnvKey: "other"})); err != nil || k != testKey {
		t.Errorf("from the file: %q, %v", k, err)
	}
	cases := map[string]struct {
		file string
		env  string
		want string
	}{
		"unset":      {"", "", "no key: set MAGNETAR_KEY or pass --key-file PATH"},
		"empty file": {write("empty", "\n"), "", "no key"},
		"missing":    {filepath.Join(dir, "missing"), "", "cannot read the key file"},
		"huge":       {write("huge", strings.Repeat("secretsecret", 200)), "", "larger than a key can be"},
		"has space":  {"", "secret value", "holds characters a key does not have"},
		"control":    {"", "secret\x07value", "holds characters a key does not have"},
	}
	for name, c := range cases {
		_, err := client.ReadKey(c.file, env(map[string]string{client.EnvKey: c.env}))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "secret") && name != "unset" {
			t.Errorf("%s: the error shows key content: %v", name, err)
		}
	}
}

// TestNewNeedsOneWayToReach guards the choice of transport: both a data
// directory and a server, or neither, is a usage error.
func TestNewNeedsOneWayToReach(t *testing.T) {
	for name, cfg := range map[string]client.Config{
		"neither": {},
		"both":    {DataDir: "/x", Server: "https://example.org"},
	} {
		_, err := client.New(cfg)
		if fault.KindOf(err) != fault.Invalid {
			t.Errorf("%s: error %v, want invalid", name, err)
		}
	}
	if _, err := client.New(client.Config{Server: "https://example.org", Getenv: env(nil)}); fault.KindOf(err) != fault.Invalid {
		t.Errorf("a server with no key: %v, want invalid", err)
	}
	if _, err := client.New(client.Config{DataDir: "/x", Timeout: -time.Second}); fault.KindOf(err) != fault.Invalid {
		t.Errorf("a negative timeout: %v, want invalid", err)
	}
}

// TestUnreachableSocket guards the message when nothing listens: it names the
// socket and is a failure, not a usage error.
func TestUnreachableSocket(t *testing.T) {
	dir := t.TempDir()
	c, err := client.New(client.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = listTenants(c)
	want := "cannot reach the server at " + client.SocketPath(dir)
	if err == nil || !strings.Contains(err.Error(), want) || fault.KindOf(err) != fault.Unavailable {
		t.Errorf("error %v (kind %v), want %q", err, fault.KindOf(err), want)
	}
}

// TestDeadline guards the per-call deadline: a server that does not answer in
// time is reported with the timeout and the flag that changes it.
func TestDeadline(t *testing.T) {
	fake := fakeserver.New()
	fake.Delay = 2 * time.Second
	dir := fake.ServeUnix(t)
	c, err := client.New(client.Config{DataDir: dir, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = listTenants(c)
	if err == nil || !strings.Contains(err.Error(), "no answer from") || !strings.Contains(err.Error(), "--timeout") {
		t.Errorf("error = %v", err)
	}
}

// TestForcedCodes guards the mapping of every code the contract names, through
// the real transport: the kind each gets, and the request id in the message.
func TestForcedCodes(t *testing.T) {
	cases := map[connect.Code]fault.Kind{
		connect.CodeInvalidArgument:   fault.Invalid,
		connect.CodeUnauthenticated:   fault.Unauthenticated,
		connect.CodePermissionDenied:  fault.Denied,
		connect.CodeNotFound:          fault.NotFound,
		connect.CodeAlreadyExists:     fault.Conflict,
		connect.CodeUnavailable:       fault.Unavailable,
		connect.CodeInternal:          fault.Internal,
		connect.CodeResourceExhausted: fault.Invalid,
	}
	for code, kind := range cases {
		fake := fakeserver.New()
		fake.FailWith(magnetarv1connect.TenantServiceListTenantsProcedure, code, "forced")
		dir := fake.ServeUnix(t)
		c, err := client.New(client.Config{DataDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		_, err = listTenants(c)
		c.Close()
		if fault.KindOf(err) != kind || !strings.Contains(err.Error(), "forced (request id req-000001)") {
			t.Errorf("%v: kind %v, error %v; want kind %v with the request id", code, fault.KindOf(err), err, kind)
		}
	}
}
