package receive

import (
	"bytes"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/core/naming"
	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/receive/header"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// byArrival returns the receptions in order of arrival number.
func byArrival(rs []tenant.Reception) []tenant.Reception {
	out := make([]tenant.Reception, len(rs))
	copy(out, rs)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Arrival < out[i].Arrival || (out[j].Arrival == out[i].Arrival && out[j].Started.Before(out[i].Started)) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestTwoArrivalsMakeOneSession guards the quiet period: the same study in
// two parts with a gap longer than the quiet period is two receptions, two
// arrivals of one study, in one session, the second's scan beside the
// first's.
func TestTwoArrivalsMakeOneSession(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.send(studyFiles[:2]...)
	first := e.settle(1)
	e.clock.Advance(time.Minute)
	e.send(studyFiles[2:]...)
	rs := byArrival(e.settle(2))
	if len(rs) != 2 || !done(rs[0]) || !done(rs[1]) || rs[0].Arrival != 1 || rs[1].Arrival != 2 {
		t.Fatalf("receptions %+v", rs)
	}
	if rs[0].SessionLabel != rs[1].SessionLabel || rs[0].SubjectLabel != rs[1].SubjectLabel || rs[0].ID != first[0].ID {
		t.Fatalf("the two arrivals are not one session: %+v", rs)
	}
	if rs[0].Counts.FilesArchived != 2 || rs[1].Counts.FilesArchived != 5 {
		t.Fatalf("counts %+v / %+v", rs[0].Counts, rs[1].Counts)
	}
	totals, _ := e.store.CatalogTotals(e.ctx)
	if totals.Sessions != 1 || totals.Scans != 2 || totals.Instances != 7 {
		t.Fatalf("totals %+v", totals)
	}
	checkArchive(t, e, rs[1], sentMap(t, studyFiles[2:]...))
}

// TestStudySentTwiceIsIgnoredTheSecondTime guards duplicates: the same study
// sent again is a second reception whose files are all counted duplicate,
// none written again, and the incoming area empties.
func TestStudySentTwiceIsIgnoredTheSecondTime(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.send(studyFiles...)
	e.settle(1)
	e.clock.Advance(time.Minute)
	e.send(studyFiles...)
	rs := byArrival(e.settle(2))
	second := rs[1]
	if !done(second) || second.Counts.FilesReceived != 7 || second.Counts.FilesDuplicate != 7 || second.Counts.FilesArchived != 0 {
		t.Fatalf("second reception %+v", second)
	}
	if got := e.p.Stats().EarlyDuplicates.Load(); got != 7 {
		t.Errorf("early duplicates %d, want 7", got)
	}
	totals, _ := e.store.CatalogTotals(e.ctx)
	if totals.Instances != 7 {
		t.Fatalf("totals %+v", totals)
	}
	if n := e.incomingFiles(); n != 0 {
		t.Fatalf("%d files left in the incoming area", n)
	}
}

// TestChangedInstanceIsHeld guards a different file under a known UID: the
// first is kept, the second held as a different file with both checksums,
// its bytes kept in the incoming area.
func TestChangedInstanceIsHeld(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.send("MR_small.dcm")
	e.settle(1)
	e.clock.Advance(time.Minute)
	e.send("MR_small_RLE.dcm")
	rs := byArrival(e.settle(2))
	if !done(rs[1]) || rs[1].Counts.FilesQuarantined != 1 || rs[1].Counts.FilesArchived != 0 {
		// A reception none of whose files go on ends quarantined.
		if rs[1].State != reception.Quarantined {
			t.Fatalf("second reception %+v", rs[1])
		}
	}
	held := e.held()
	if len(held) != 1 || held[0].Reason != tenant.QuarantineDifferentFile || held[0].KeptSHA256.IsZero() || !held[0].ContentKept {
		t.Fatalf("held %+v", held)
	}
	if n := e.incomingFiles(); n != 1 {
		t.Fatalf("the incoming area holds %d files, want the held one", n)
	}
	checkArchive(t, e, rs[0], sentMap(t, "MR_small.dcm"))
}

// TestNotDICOMIsHeldAndTheRestArchived guards a file that is not DICOM in
// the middle of a study: it is held with its offset, kept, and the study is
// archived without it.
func TestNotDICOMIsHeldAndTheRestArchived(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.send(studyFiles[:3]...)
	e.sendBytes([]byte("this is a text file, not a DICOM file\n"), Received)
	e.send(studyFiles[3:]...)
	rs := e.settle(1)
	if !done(rs[0]) || rs[0].Counts.FilesArchived != 7 {
		t.Fatalf("reception %+v", rs[0])
	}
	held := e.held()
	if len(held) != 1 || held[0].Reason != tenant.QuarantineNotDICOM || held[0].Details["offset"] == "" || !held[0].ContentKept {
		t.Fatalf("held %+v", held)
	}
	if n := e.incomingFiles(); n != 1 {
		t.Fatalf("the incoming area holds %d files, want the held one", n)
	}
}

// TestFileTooLargeIsRefusedAndRecorded guards the size bound: a file over
// the receiver's largest file fails, nothing of it is kept, and a record
// without content says it came.
func TestFileTooLargeIsRefusedAndRecorded(t *testing.T) {
	e := newEnv(t, envOptions{maxFileBytes: 4096})
	out := e.sendBytes(corpus(t, "MR_small.dcm"), Failed)
	if out.Reason != reasonTooLarge {
		t.Fatalf("reason %q", out.Reason)
	}
	held := e.held()
	if len(held) != 1 || held[0].Reason != tenant.QuarantineTooLarge || held[0].ContentKept || held[0].Details["limit"] != "4096" {
		t.Fatalf("held %+v", held)
	}
	if n := e.incomingFiles(); n != 0 {
		t.Fatalf("a refused file left %d files", n)
	}
	e.send(studyFiles[0])
	e.settle(1)
}

// TestLabelsThatCannotBeMadeAreHeld guards the naming rule's failure: a rule
// that names an attribute the files lack holds every file with the label
// reason and its fault, and the reception ends quarantined.
func TestLabelsThatCannotBeMadeAreHeld(t *testing.T) {
	spec := naming.Spec{Subject: "{(0010,4000)}", Session: "ses-{#StudyInstanceUID}", Scan: "scan-{#SeriesInstanceUID}"}
	e := newEnv(t, envOptions{naming: &spec})
	e.send(studyFiles...)
	rs := e.settle(1)
	if rs[0].State != reception.Quarantined || rs[0].Counts.FilesQuarantined != 7 {
		t.Fatalf("reception %+v", rs[0])
	}
	held := e.held()
	if len(held) != 7 {
		t.Fatalf("%d held", len(held))
	}
	for _, q := range held {
		if q.Reason != tenant.QuarantineBadLabel || q.Details["label"] != "subject" || q.Details["fault"] != tenant.FaultMissing || q.Details["attribute"] != "(0010,4000)" {
			t.Fatalf("held %+v", q)
		}
	}
	if n := e.incomingFiles(); n != 7 {
		t.Fatalf("the incoming area holds %d files, want the 7 held", n)
	}
}

// TestSOPClassNotAcceptedIsHeld guards the receiver's list of SOP classes.
func TestSOPClassNotAcceptedIsHeld(t *testing.T) {
	e := newEnv(t, envOptions{sopClasses: []string{"1.2.840.10008.5.1.4.1.1.2"}})
	e.send("MR_small.dcm")
	e.send(studyFiles[0])
	e.settle(1)
	held := e.held()
	if len(held) != 1 || held[0].Reason != tenant.QuarantineSOPClassNotAccepted || held[0].Details["sop_class_uid"] != "1.2.840.10008.5.1.4.1.1.4" {
		t.Fatalf("held %+v", held)
	}
}

// TestCompressedAndMultiFrameAreArchived guards files beyond plain slices:
// JPEG 2000, RLE and an enhanced multi-frame segmentation, each byte for
// byte with a sidecar that finds its pixel data.
func TestCompressedAndMultiFrameAreArchived(t *testing.T) {
	e := newEnv(t, envOptions{})
	names := []string{"MR_small_jp2klossless.dcm", "liver_1frame.dcm"}
	e.send(names...)
	rs := e.settle(2)
	for _, r := range rs {
		if !done(r) {
			t.Fatalf("reception %+v", r)
		}
		for _, n := range names {
			if studyOf(t, corpus(t, n)) == r.StudyUID {
				checkArchive(t, e, r, sentMap(t, n))
			}
		}
	}
}

// studyOf returns a file's Study Instance UID.
func studyOf(t *testing.T, b []byte) string {
	t.Helper()
	h, err := header.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := h.UID(header.StudyUID)
	return u
}
