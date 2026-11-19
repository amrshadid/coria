package receive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/receive/incoming"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// TestRecoverReconcilesEachState guards Recover against the incoming area's
// every state, set up by hand as a crash leaves them: a file being written
// is removed; a landed file the database never heard of is checked and
// recorded; a landed file whose row exists gets its recorded name; a
// recorded file no row keeps is removed; and a row whose file is gone is
// counted missing, not dropped. The pipeline then archives what it found,
// and holds the file whose copy is gone as incoming_lost.
func TestRecoverReconcilesEachState(t *testing.T) {
	e := newEnv(t, envOptions{manual: true})
	land := func(name string) incoming.File {
		f, err := e.p.area.Land(e.ctx, "dimse", bytesReader(corpus(t, name)), 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// A landed file with no row: its sender was answered, its row never
	// written.
	orphan := land(studyFiles[0])
	// A landed file with its row: the rename after the batch was lost.
	renamed := land(studyFiles[1])
	d, err := e.p.check(e.ctx, landed{file: renamed, receiver: "dimse"})
	if err != nil {
		t.Fatal(err)
	}
	// A row whose file is gone.
	gone := land(studyFiles[2])
	d2, err := e.p.check(e.ctx, landed{file: gone, receiver: "dimse"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.AddReceivedFiles(e.ctx, []tenant.ReceivedFile{*d.received, *d2.received}, e.p.now()); err != nil {
		t.Fatal(err)
	}
	if err := e.p.area.Remove(gone.Path); err != nil {
		t.Fatal(err)
	}
	// A recorded file no row keeps: its removal was cut short.
	leftover := land(studyFiles[3])
	if err := e.p.area.Commit(leftover.Path); err != nil {
		t.Fatal(err)
	}
	// A file being written.
	id := incoming.NewID()
	tmp := filepath.Join(e.cfg.IncomingRoot, "lab", "dimse", id[:2])
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, id+".tmp"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}

	e.manual = false
	_ = e.p.area.Close()
	e.start()
	// Start ran Recover; what it found is in the stats and the area.
	if got := e.p.Stats().MissingFiles.Load(); got != 1 {
		t.Errorf("missing files %d, want 1", got)
	}
	if ok, _ := e.p.area.Exists(leftover.Path); ok {
		t.Error("a recorded file no row keeps was not removed")
	}
	if _, err := os.Stat(filepath.Join(tmp, id+".tmp")); !os.IsNotExist(err) {
		t.Error("a file being written was not removed")
	}
	// The reception holds the orphan, now recorded, the renamed file, and
	// the file whose bytes are gone. That one is held as lost, with no
	// bytes kept and what is known of it on its record, and the other two
	// are archived: one lost copy does not fail the study.
	e.waitFor("the reception to end", 10*time.Minute, e.allEnded(1))
	r := e.receptions()[0]
	if r.Counts.FilesReceived != 3 || r.State != reception.Done || r.Counts.FilesArchived != 2 || r.Counts.FilesQuarantined != 1 {
		t.Fatalf("reception %+v", r)
	}
	qs, _, err := e.store.ListQuarantined(e.ctx, tenant.QuarantineFilter{ReceptionID: r.ID}, master.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].Reason != tenant.QuarantineIncomingLost || qs[0].ContentKept || qs[0].SHA256 != gone.SHA256 ||
		qs[0].SOPInstanceUID != d2.received.SOPInstanceUID {
		t.Fatalf("quarantine %+v, want the gone file held as incoming_lost with nothing kept", qs)
	}
	for _, f := range []incoming.File{orphan, renamed} {
		if ok, _ := e.p.area.Exists(f.Path); ok {
			t.Errorf("file %s was archived and verified, and its incoming copy is still there", f.ID)
		}
	}
}

// TestRecoverReportCountsAtStart guards the report Start returns.
func TestRecoverReportCountsAtStart(t *testing.T) {
	e := newEnv(t, envOptions{manual: true})
	f, err := e.p.area.Land(e.ctx, "dimse", bytesReader(corpus(t, studyFiles[0])), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_ = f
	_ = e.p.area.Close()
	p, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := p.Start(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(e.ctx) })
	if rep.Rechecked != 1 || rep.Temp != 0 || rep.Missing != 0 {
		t.Fatalf("report %+v", rep)
	}
}
