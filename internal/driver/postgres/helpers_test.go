package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/magnetar/internal/master"
)

// addressEnv names the variable that holds the address of a Postgres
// database the tests may make schemas in. requireEnv, when set to anything,
// turns a missing address from a skip into a failure: CI sets it on Linux,
// where a Postgres is always there, so a service that failed to start cannot
// make the tests pass by not running.
const (
	addressEnv = "MAGNETAR_TEST_POSTGRES"
	requireEnv = "MAGNETAR_REQUIRE_POSTGRES"
)

// testAddress returns the address of the test database. Without one it skips
// the test and says why, or fails it when requireEnv is set.
func testAddress(tb testing.TB) string {
	tb.Helper()
	address := os.Getenv(addressEnv)
	if address != "" {
		return address
	}
	if os.Getenv(requireEnv) != "" {
		tb.Fatalf("%s is not set, and %s is: the Postgres tests must run here, not be skipped", addressEnv, requireEnv)
	}
	tb.Skipf("%s is not set; the Postgres tests are skipped (point it at a postgres:// URL of a scratch database to run them)", addressEnv)
	return ""
}

// admin opens a connection to the database at address for a test's own
// setup and checks, closed when tb ends. A failure is reported without the
// address.
func admin(tb testing.TB, address string) *pgx.Conn {
	tb.Helper()
	// The pool's own parameters mean nothing to a single connection, and
	// pgx would send them to the server as settings, which refuses them.
	u, err := url.Parse(address)
	if err != nil {
		tb.Fatal("the test address is not a URL")
	}
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(k, "pool_") {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	address = u.String()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		cfg, _ := pgx.ParseConfig(address)
		tb.Fatalf("cannot connect to the test database: %s", newRedactor(address, cfg).text(err.Error()))
	}
	tb.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer ccancel()
		_ = conn.Close(cctx) // A failed close of a test's connection hides nothing.
	})
	return conn
}

// randomName returns prefix and 12 hex digits, a name no other run uses.
func randomName(tb testing.TB, prefix string) string {
	tb.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return prefix + hex.EncodeToString(b)
}

// withParams returns address with the query parameters set.
func withParams(tb testing.TB, address string, params map[string]string) string {
	tb.Helper()
	u, err := url.Parse(address)
	if err != nil {
		tb.Fatal("the test address is not a URL")
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// mustExec runs statements on conn, failing the test on an error.
func mustExec(tb testing.TB, conn *pgx.Conn, statements ...string) {
	tb.Helper()
	for _, s := range statements {
		if _, err := conn.Exec(context.Background(), s); err != nil {
			tb.Fatalf("%s: %v", s, err)
		}
	}
}

// newSchema makes a schema no other test uses in the test database, dropped
// when tb ends, and returns an address whose search_path is that schema.
// Each test therefore has its own tables, in the one database the CI service
// or the local container has, and runs never collide. The pool is kept to
// three connections: the suite opens a store per test and runs them in
// parallel, and Postgres allows 100 connections by default.
func newSchema(tb testing.TB) string {
	tb.Helper()
	return newSchemaAt(tb, testAddress(tb))
}

// newSchemaAt is newSchema in the database that base names.
func newSchemaAt(tb testing.TB, base string) string {
	tb.Helper()
	conn := admin(tb, base)
	schema := randomName(tb, "mt_")
	mustExec(tb, conn, `CREATE SCHEMA `+schema)
	tb.Cleanup(func() {
		c := admin(tb, base)
		mustExec(tb, c, `DROP SCHEMA `+schema+` CASCADE`)
	})
	return withParams(tb, base, map[string]string{"search_path": schema, "pool_max_conns": "3"})
}

// openUnmigrated opens a store in a new schema without migrating it, closed
// when tb ends.
func openUnmigrated(tb testing.TB) *Master {
	tb.Helper()
	return openAt(tb, newSchema(tb))
}

// openAt opens a store at address, closed when tb ends.
func openAt(tb testing.TB, address string) *Master {
	tb.Helper()
	m, err := OpenMaster(context.Background(), address)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = m.Close() })
	return m
}

// settledAcquired returns how many connections are out of m's pool once the
// count has stopped changing. After a call whose context ended, pgx closes the
// connection the canceled statement used and the pool drops it in the
// background, so the count is read again, for up to two seconds, until it is
// zero.
func settledAcquired(m *base) int32 {
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := m.pool.Stat().AcquiredConns()
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// openTest opens a fresh, migrated master in a new schema, closed when tb
// ends.
func openTest(tb testing.TB) master.Store {
	tb.Helper()
	m := openUnmigrated(tb)
	if err := m.Migrate(context.Background()); err != nil {
		tb.Fatal(err)
	}
	return m
}
