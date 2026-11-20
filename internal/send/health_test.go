package send

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/amrshadid/go-dicom/network"

	"github.com/amrshadid/coria/internal/tenant"
)

// TestPeerDownUsesNoAttempts guards a sender whose peer is down: after
// three connections in a row fail the sender is down for peer_failing, its
// unit is given back without spending an attempt and is not claimed while
// it is, however long that lasts; the peer is tried again on the backoff,
// and once it answers the sender is up and the job ends done. Each change
// of health is a sender_health event.
func TestPeerDownUsesNoAttempts(t *testing.T) {
	f := newFixture(t)
	addr := freePort(t)
	f.archive("ses-1", scanFiles{"mr", generated(t, 2, 256)})
	f.addDIMSE("peer", addr)
	rec := f.record()
	job := f.createJob("peer", "ses-1")
	s := f.start(f.config())
	f.stepOnEach(s, "the sender's going down", time.Minute, 2*time.Minute, failedConnections(s, "peer"), func() bool {
		h, _ := s.Health("lab", "peer")
		return h.State == tenant.SenderDown
	})
	h, _ := s.Health("lab", "peer")
	if h.DownReason != tenant.SenderDownPeerFailing || h.Fault != tenant.SenderFaultUnreachable || h.FailedConnections < DownAfter || h.NextTry.IsZero() {
		t.Errorf("health %+v, want down for peer_failing, unreachable, with a next try", h)
	}
	// An hour of the clock passes while the peer stays down: the probes
	// fail and no unit is worked.
	for range 20 {
		f.clock.add(5 * time.Minute)
		s.Poke("lab")
		time.Sleep(20 * time.Millisecond)
	}
	// Two workers may each make a connection while the sender goes down;
	// only those before it spend an attempt.
	released, failed := rec.counts()
	if failed > DownAfter-1 || released < 1 {
		t.Errorf("given back %d times with an attempt and %d without, want at most %d and at least 1", failed, released, DownAfter-1)
	}
	if j, _ := f.p.Job(f.ctx, job.ID); j.State.Ended() {
		t.Fatalf("job ended %s while its peer was down", j.State)
	}
	// The peer comes up on its address.
	ap := netip.MustParseAddrPort(addr)
	peer := &dimsePeer{}
	peer.data, peer.sums, peer.syntax, peer.count = map[string][]byte{}, map[string][32]byte{}, map[string]string{}, map[string]int{}
	srv, err := network.StartServer(context.Background(), network.SCPConfig{AETitle: "PEER", Port: int(ap.Port()), BindAddress: "127.0.0.1"}, peer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.SetSupportedTransferSyntaxes(network.AllTransferSyntaxes())
	// Past the next try, which is at most an hour away; then no more, so
	// no lease runs out under a unit that sends.
	f.clock.add(2 * time.Hour)
	j := f.waitJob(s, job.ID, 30*time.Second, ended)
	if j.State != tenant.JobDone || peer.arrivals() != 2 {
		t.Errorf("job %s with the peer holding %d, want done with 2", j.State, peer.arrivals())
	}
	if h, _ := s.Health("lab", "peer"); h.State != tenant.SenderUp || h.FailedConnections != 0 {
		t.Errorf("health %+v after the peer answered, want up", h)
	}
	page, err := f.p.(tenant.Events).ReadEvents(f.ctx, "", []tenant.EventType{tenant.EventSenderHealth}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, e := range page.Events {
		if p, ok := e.Payload.(*tenant.SenderHealthChanged); ok {
			states = append(states, string(p.State)+"/"+string(p.DownReason))
		}
	}
	if !containsInOrder(states, "down/peer_failing", "up/") {
		t.Errorf("sender_health events %v, want down for peer_failing, then up", states)
	}
}

// containsInOrder reports whether want appear in got in that order.
func containsInOrder(got []string, want ...string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// TestDamagedBytesNeverReachThePeerWhole guards the checked read: an
// archived file changed on the storage near its end is aborted before its
// last fragment (the peer sees the data set cut and never answers it), a
// file gone from the storage is not sent at all, both are refused as
// damaged_in_storage, and the others are sent.
func TestDamagedBytesNeverReachThePeerWhole(t *testing.T) {
	f := newFixture(t)
	peer := &dimsePeer{}
	addr := startPeer(t, peer)
	files := generated(t, 4, 1<<20)
	f.archive("ses-1", scanFiles{"mr", files})
	uids := make([]string, len(files))
	for i, b := range files {
		uids[i] = uidOf(t, b)
	}
	damaged, missing := uids[1], uids[2]
	bad := bytes.Clone(f.files[damaged])
	bad[len(bad)-100] ^= 0xFF
	if _, err := f.store.Put(f.ctx, f.keys[damaged], bytes.NewReader(bad)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Delete(f.ctx, f.keys[missing]); err != nil {
		t.Fatal(err)
	}
	f.addDIMSE("peer", addr)
	job := f.createJob("peer", "ses-1")
	s := f.start(f.config())
	j := f.waitJob(s, job.ID, 30*time.Second, ended)
	if j.State != tenant.JobPartial || j.Counts.InstancesSent != 2 || j.Counts.InstancesRefused != 2 {
		t.Fatalf("job %s %+v, want partial with 2 sent and 2 refused", j.State, j.Counts)
	}
	for _, fl := range f.failures(job.ID) {
		if fl.Code != tenant.FailureDamagedInStorage || (fl.SOPInstanceUID != damaged && fl.SOPInstanceUID != missing) {
			t.Errorf("failure %+v, want damaged_in_storage of the changed and the missing file", fl)
		}
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.count[damaged] != 0 || peer.count[missing] != 0 {
		t.Errorf("the peer answered the changed file %d times and the missing one %d times, want never", peer.count[damaged], peer.count[missing])
	}
	if peer.cut != 1 {
		t.Errorf("the peer saw %d data sets cut short, want the changed one", peer.cut)
	}
}
