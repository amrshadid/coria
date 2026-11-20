package send

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amrshadid/coria/internal/tenant"
)

// stepsOf writes a test's steps as step=outcome, with the fault of a
// failed one.
func stepsOf(steps []TestStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Step + "=" + s.Outcome)
		if s.Fault != "" {
			b.WriteString("(" + string(s.Fault) + ")")
		}
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

// TestTestSender guards TestSender's steps (sender.proto): a DIMSE peer
// passes resolve, connect, the association, the C-ECHO and the release,
// with TLS skipped; one that listens nowhere fails at connect and skips the
// rest; a stow peer passes with the OPTIONS request's HTTP status in its
// detail, and one that refuses the credential fails it as rejected. The
// health of the sender is not changed.
func TestTestSender(t *testing.T) {
	f := newFixture(t)
	f.inst.plain = true
	peer := &dimsePeer{}
	up := f.addDIMSE("up", startPeer(t, peer))
	down := f.addDIMSE("down", freePort(t))
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions || r.URL.Path != "/dicom-web/studies" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-xyz" {
			http.Error(w, "who", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(web.Close)
	stow := f.addStow("stow", web.URL+"/dicom-web")
	wrong := f.addStow("wrong", web.URL+"/dicom-web", func(s *tenant.Sender) { s.STOW.SealedCredential = []byte("other") })
	s := f.start(f.config())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, tc := range []struct {
		row    tenant.Sender
		want   string
		detail string
	}{
		{up, "resolve=passed connect=passed tls=skipped associate=passed request=passed release=passed", ""},
		{down, "resolve=passed connect=failed(unreachable) tls=skipped associate=skipped request=skipped release=skipped", ""},
		{stow, "resolve=passed connect=passed tls=skipped associate=skipped request=passed release=skipped", "HTTP 204"},
		{wrong, "resolve=passed connect=passed tls=skipped associate=skipped request=failed(rejected) release=skipped", "HTTP 401"},
	} {
		steps, err := s.TestSender(ctx, "lab", tc.row)
		if err != nil {
			t.Fatalf("%s: %v", tc.row.Name, err)
		}
		if got := stepsOf(steps); got != tc.want {
			t.Errorf("%s: steps %s, want %s", tc.row.Name, got, tc.want)
		}
		if tc.detail != "" && steps[4].Detail != tc.detail {
			t.Errorf("%s: the request's detail %q, want %q", tc.row.Name, steps[4].Detail, tc.detail)
		}
	}
	if peer.arrivals() != 0 {
		t.Errorf("a test sent %d instances", peer.arrivals())
	}
	for _, name := range []string{"up", "down"} {
		if h, _ := s.Health("lab", name); h.State != tenant.SenderUnknown {
			t.Errorf("%s: health %+v after a test, want unknown", name, h)
		}
	}
}
