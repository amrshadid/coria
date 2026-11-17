package cli_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/cli"
	"github.com/amrshadid/magnetar/internal/cli/client"
	"github.com/amrshadid/magnetar/internal/cli/fakeserver"
)

// testKey is a made-up key for the network tests; it is not a real credential
// and the fake accepts it only because the test says so.
const testKey = "mgt_k00000000001_" + "SSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSSS"

// fixedNow is the clock of every test, so times in the output are the same on
// every run.
var fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// zone is the zone the tests show times in: not UTC, so a test that forgot to
// convert would show a different hour, with an offset that is easy to read.
var zone = time.FixedZone("EAT", 3*60*60)

// result is what one run of the command line wrote and returned.
type result struct {
	stdout string
	stderr string
	code   int
}

// rig is a fake server and the environment to reach it.
type rig struct {
	t     *testing.T
	fake  *fakeserver.Server
	env   map[string]string
	roots *x509.CertPool
}

// newRig serves a fake on a unix socket and points the commands at it through
// MAGNETAR_DATA_DIR, the default way a person reaches a local server.
func newRig(t *testing.T) *rig {
	t.Helper()
	fake := fakeserver.New()
	fake.Now = func() time.Time { return fixedNow }
	dir := fake.ServeUnix(t)
	return &rig{t: t, fake: fake, env: map[string]string{client.EnvDataDir: dir}}
}

// newNetworkRig serves a fake over TLS that accepts only testKey, and points
// the commands at it through MAGNETAR_SERVER and MAGNETAR_KEY.
func newNetworkRig(t *testing.T) *rig {
	t.Helper()
	fake := fakeserver.New()
	fake.Now = func() time.Time { return fixedNow }
	fake.RequireKey = testKey
	fake.CallerKeyID = "k00000000001"
	addr, roots := fake.ServeTLS(t)
	return &rig{t: t, fake: fake, roots: roots, env: map[string]string{client.EnvServer: addr, client.EnvKey: testKey}}
}

// run runs the command line with the arguments and the rig's environment and
// returns what it wrote and its exit code.
func (r *rig) run(args ...string) result {
	r.t.Helper()
	return runWith(r.env, r.roots, args...)
}

// runWith is run with an environment of the test's own.
func runWith(env map[string]string, roots *x509.CertPool, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, cli.Env{
		Stdout:   &stdout,
		Stderr:   &stderr,
		Getenv:   func(k string) string { return env[k] },
		Now:      func() time.Time { return fixedNow },
		Location: zone,
		RootCAs:  roots,
	})
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}
