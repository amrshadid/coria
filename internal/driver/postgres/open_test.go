package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amrshadid/magnetar/internal/fault"
)

// secretsIn are the pieces of a made-up address that must never be in an
// error. They are chosen to be found by a plain substring search.
var secretsIn = []string{"hunter2needle", "needleuser", "needlehost.invalid", "needledb"}

// noSecrets fails the test when err's text holds any of the pieces of the
// address.
func noSecrets(t *testing.T, what string, err error, extra ...string) {
	t.Helper()
	if err == nil {
		return
	}
	text := err.Error()
	for _, s := range append(append([]string(nil), secretsIn...), extra...) {
		if s != "" && strings.Contains(text, s) {
			t.Errorf("%s: the error repeats %q: %v", what, s, err)
		}
	}
}

// TestOpenRefusesAddresses guards the addresses the driver refuses without a
// server: a scheme that is not postgres:// and a URL that does not parse are
// Invalid, with no cause kept (Go's URL errors quote the whole address, with
// its password); a port nothing listens on is Unavailable. In none does the
// error repeat the address or any part of it. It needs no Postgres.
func TestOpenRefusesAddresses(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		what, address string
		want          fault.Kind
	}{
		{"a keyword/value string", "host=needlehost.invalid user=needleuser password=hunter2needle dbname=needledb", fault.Invalid},
		{"another scheme", "mysql://needleuser:hunter2needle@needlehost.invalid/needledb", fault.Invalid},
		{"a bad port", "postgres://needleuser:hunter2needle@needlehost.invalid:notaport/needledb", fault.Invalid},
		{"a bad escape", "postgres://needleuser:hunter2needle@needlehost.invalid/needledb?sslmode=%zz", fault.Invalid},
		{"an unknown sslmode", "postgres://needleuser:hunter2needle@needlehost.invalid/needledb?sslmode=sometimes", fault.Invalid},
		{"a port nothing listens on", "postgres://needleuser:hunter2needle@127.0.0.1:1/needledb?sslmode=disable&connect_timeout=2", fault.Unavailable},
		{"a host that does not resolve", "postgres://needleuser:hunter2needle@needlehost.invalid/needledb?sslmode=disable&connect_timeout=2", fault.Unavailable},
	} {
		_, err := OpenMaster(ctx, tt.address)
		if got := fault.KindOf(err); got != tt.want {
			t.Errorf("%s: kind %v (%v), want %v", tt.what, got, err, tt.want)
		}
		noSecrets(t, tt.what, err, "127.0.0.1")
	}
}

// TestClassifyUsesCodes guards that a Postgres error becomes a kind from its
// SQLSTATE code and never from its message: a message that talks of a
// duplicate key under another code is not a conflict, and a message that
// talks of nothing under the code of a lost connection is Unavailable.
func TestClassifyUsesCodes(t *testing.T) {
	for _, tt := range []struct {
		code, message string
		want          fault.Kind
	}{
		{"08006", "no words about it", fault.Unavailable},
		{"57P01", "terminating connection due to administrator command", fault.Unavailable},
		{"53300", "too many connections", fault.Unavailable},
		{"40P01", "deadlock detected", fault.Unavailable},
		{"40001", "could not serialize access", fault.Unavailable},
		{"55P03", "could not obtain lock", fault.Unavailable},
		{"28P01", "password authentication failed", fault.Unavailable},
		{"3D000", "database does not exist", fault.Unavailable},
		{"42P01", "relation does not exist", fault.Unavailable},
		{"23505", "duplicate key value violates unique constraint", fault.Internal}, // The caller, not classify, turns this into Conflict.
		{"XX000", "duplicate key value violates unique constraint", fault.Internal},
		{"22P05", "connection refused, timeout, unavailable", fault.Internal},
	} {
		err := classify(nil, &pgconn.PgError{Code: tt.code, Message: tt.message}, "failed")
		if got := fault.KindOf(err); got != tt.want {
			t.Errorf("code %s (%q): kind %v, want %v", tt.code, tt.message, got, tt.want)
		}
	}
	if !isUniqueViolation(&pgconn.PgError{Code: "23505", Message: "words"}) || isUniqueViolation(&pgconn.PgError{Code: "XX000", Message: "duplicate key"}) {
		t.Error("isUniqueViolation does not follow the code")
	}
	if !constraintViolation(&pgconn.PgError{Code: "23505", ConstraintName: "tenants_name_key"}, "tenants_name_key") ||
		constraintViolation(&pgconn.PgError{Code: "23505", ConstraintName: "tenants_pkey"}, "tenants_name_key") {
		t.Error("constraintViolation does not follow the constraint's name")
	}
	if got := fault.KindOf(classify(nil, context.DeadlineExceeded, "failed")); got != fault.Unavailable {
		t.Errorf("a deadline: kind %v, want Unavailable", got)
	}
	// A kind already given is kept.
	given := fault.New(fault.Conflict, "taken")
	if !errors.Is(classify(nil, given, "failed"), given) {
		t.Error("an error that has a kind was changed")
	}
	// The chain still answers errors.Is through the scrubbing.
	if err := classify(nil, context.Canceled, "failed"); !errors.Is(err, context.Canceled) {
		t.Error("a canceled context is not in the chain")
	}
}

// TestRedactor guards the scrubbing: the address, its password, user, host and
// database are replaced in text, each also in its URL-escaped form, the
// longest first.
func TestRedactor(t *testing.T) {
	address := "postgres://us%40er:p%2Fw%3Aord@db.example:6543/the-db?sslmode=disable"
	r := newRedactor(address, nil)
	text := r.text("cannot parse `" + address + "`")
	if strings.Contains(text, "db.example") || strings.Contains(text, "p%2Fw") {
		t.Errorf("the address is in %q", text)
	}
	if r.text("nothing here") != "nothing here" {
		t.Error("text without secrets was changed")
	}
}
