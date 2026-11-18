package receive

import (
	"net/netip"
	"net/url"
	"os"
	"testing"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/blob/s3"
)

// s3Env is the address of a scratch S3 server, as the S3 driver's own tests
// take it: http://KEY:SECRET@host:port/bucket, with region=NAME and
// pathstyle=0 in the query when needed. requireS3 turns a missing address
// from a skip into a failure, as in CI.
const (
	s3Env     = "MAGNETAR_TEST_S3"
	requireS3 = "MAGNETAR_REQUIRE_S3"
)

// TestStudyIsArchivedOnS3 guards the whole pipeline with an S3 storage: the
// study is archived byte for byte with its manifest and sidecar, under a
// prefix of its own in the scratch bucket.
func TestStudyIsArchivedOnS3(t *testing.T) {
	address := os.Getenv(s3Env)
	if address == "" {
		if os.Getenv(requireS3) != "" {
			t.Fatalf("%s is not set, and %s is", s3Env, requireS3)
		}
		t.Skipf("%s is not set; the S3 end-to-end test is skipped", s3Env)
	}
	u, err := url.Parse(address)
	if err != nil || u.User == nil || len(u.Path) < 2 {
		t.Fatalf("%s is not an http URL with a key, a secret and a bucket", s3Env)
	}
	secret, _ := u.User.Password()
	region := u.Query().Get("region")
	if region == "" {
		region = "us-east-1"
	}
	store, err := s3.Open(t.Context(), s3.Config{
		Endpoint: u.Scheme + "://" + u.Host, Region: region, Bucket: u.Path[1:], Prefix: "receive-" + newID()[:12] + "/",
		PathStyle: u.Query().Get("pathstyle") != "0", AccessKeyID: s3.NewSecret(u.User.Username()),
		SecretAccessKey: s3.NewSecret(secret), AllowHTTP: u.Scheme == "http", AllowAddress: func(netip.Addr) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e := newEnv(t, envOptions{storage: func(blob.Store) blob.Store { return store }})
	e.storage = store
	e.send(studyFiles...)
	rs := e.settle(1)
	if !done(rs[0]) || rs[0].Counts.FilesArchived != 7 {
		t.Fatalf("reception %+v", rs[0])
	}
	checkArchive(t, e, rs[0], sentMap(t, studyFiles...))
}
