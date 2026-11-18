package tenanttest

import (
	"errors"
	"fmt"
	"time"

	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/core/layout"
	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// claimOf returns the claim of a received file, with another checksum or
// series when they are not zero.
func claimOf(f tenant.ReceivedFile) tenant.InstanceClaim {
	return tenant.InstanceClaim{SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SHA256: f.SHA256}
}

// claim claims files for r and fails the test on an error.
func (e *penv) claim(r tenant.Reception, claims ...tenant.InstanceClaim) []tenant.Duplicate {
	e.Helper()
	got, err := e.p.ClaimInstances(e.ctx, r.ID, claims, r.StepTime, nil)
	e.ok("ClaimInstances", err)
	return got
}

// check returns CheckDuplicates's answer for one claim.
func (e *penv) check(c tenant.InstanceClaim) tenant.Duplicate {
	e.Helper()
	got, err := e.p.CheckDuplicates(e.ctx, []tenant.DuplicateQuery{c.Query()})
	e.ok("CheckDuplicates", err)
	return got[0]
}

// wantDup fails unless d has the kind, and, when held is not zero, that
// held checksum, and when by is not empty, that reception.
func (e *penv) wantDup(what string, d tenant.Duplicate, kind tenant.DuplicateKind, held tenant.SHA256, by string) {
	e.Helper()
	if d.Kind != kind || (!held.IsZero() && d.HeldSHA256 != held) || (by != "" && d.HeldBy != by) {
		e.Errorf("%s: %+v, want %v held %s by %q", what, d, kind, held, by)
	}
}

// identified opens a reception of study at receiver with files 'ns' and
// moves it to identified, archiving to storage "store".
func (e *penv) identified(receiver string, study int, ns ...int) (tenant.Reception, []tenant.ReceivedFile) {
	e.Helper()
	r := e.openReception(receiver, study, at(10))
	var files []tenant.ReceivedFile
	for _, n := range ns {
		files = append(files, file(r, study, n))
	}
	if len(files) > 0 {
		_, err := e.p.AddReceivedFiles(e.ctx, files, at(11))
		e.ok("AddReceivedFiles", err)
	}
	return e.stepTo(r, reception.Identified, "store"), files
}

// testClaims guards every answer of the claim, each against the rule that
// gives it: a UID no one has is claimed now; claimed again by the same
// reception it is claimed before; another reception's claim answers
// Claimed until it is confirmed, then the held answers (the same file, a
// different one, the UID elsewhere, by study or by series, held or only
// claimed); two checksums under one UID in one reception keep the first;
// a claim to overwrite takes a held UID without unholding it. On SQLite's
// one writer two claims are never at the same moment; a driver with
// several writers is shown by testClaimsShared, which a Postgres run
// exercises with true concurrency.
func testClaims(e *penv) {
	e.addReceiver("a", "store", 11112)
	e.addReceiver("b", "store", 11113)
	r1, f := e.identified("a", 1, 1, 2)
	r2, _ := e.identified("b", 1)
	c1, c2 := claimOf(f[0]), claimOf(f[1])

	got := e.claim(r1, c1, c2)
	e.wantDup("a new UID", got[0], tenant.ClaimedNow, tenant.SHA256{}, "")
	e.wantDup("another new UID", got[1], tenant.ClaimedNow, tenant.SHA256{}, "")
	got = e.claim(r1, c1, c2)
	e.wantDup("a claim written again", got[0], tenant.ClaimedBefore, sum(1), r1.ID)
	e.wantDup("CheckDuplicates of a claimed UID", e.check(c1), tenant.Claimed, sum(1), r1.ID)
	e.wantDup("another reception's claim", e.claim(r2, c1)[0], tenant.Claimed, sum(1), r1.ID)
	elsewhere := c2
	elsewhere.StudyUID = uidOf(1, 9)
	e.wantDup("a UID claimed under another study", e.claim(r2, elsewhere)[0], tenant.UIDElsewhere, sum(2), r1.ID)

	e.confirm(r1, f[:1], []tenant.Duplicate{{Kind: tenant.ClaimedBefore}})
	e.wantDup("a held UID, the same file", e.claim(r2, c1)[0], tenant.SameFile, sum(1), r1.ID)
	other := c1
	other.SHA256 = sum(77)
	e.wantDup("a held UID, another checksum", e.claim(r2, other)[0], tenant.DifferentFile, sum(1), r1.ID)
	e.wantDup("CheckDuplicates of a different file", e.check(other), tenant.DifferentFile, sum(1), r1.ID)
	series := c1
	series.SeriesUID = uidOf(2, 9)
	e.wantDup("a held UID under another series", e.claim(r2, series)[0], tenant.UIDElsewhere, sum(1), r1.ID)
	study := c1
	study.StudyUID = uidOf(1, 9)
	e.wantDup("CheckDuplicates of a held UID under another study", e.check(study), tenant.UIDElsewhere, sum(1), r1.ID)
	e.wantDup("CheckDuplicates of a UID no one has", e.check(claimOf(file(r2, 1, 500))), tenant.NotHeld, tenant.SHA256{}, "")

	twin1, twin2 := claimOf(file(r2, 1, 30)), claimOf(file(r2, 1, 30))
	twin2.SHA256 = sum(31)
	got = e.claim(r2, twin1, twin2)
	e.wantDup("one UID twice in one batch, first", got[0], tenant.ClaimedNow, tenant.SHA256{}, "")
	e.wantDup("one UID twice in one batch, second", got[1], tenant.DifferentFile, sum(30), r2.ID)
	got = e.claim(r2, twin2, twin1)
	e.wantDup("the twins again, in the other order: the second is still the different one", got[0], tenant.DifferentFile, sum(30), r2.ID)
	e.wantDup("the twins again, the first", got[1], tenant.ClaimedBefore, sum(30), r2.ID)

	overwrite := other
	overwrite.Overwrite = true
	e.wantDup("a claim to overwrite", e.claim(r2, overwrite)[0], tenant.ClaimedNow, sum(1), r1.ID)
	e.wantDup("the held instance while a claim to overwrite is pending", e.check(c1), tenant.SameFile, sum(1), r1.ID)
	before, err := e.p.CatalogTotals(e.ctx)
	e.ok("CatalogTotals", err)
	overwritten := f[0]
	overwritten.SHA256, overwritten.Size = sum(77), 4000
	e.confirm(r2, []tenant.ReceivedFile{overwritten}, []tenant.Duplicate{{Kind: tenant.ClaimedNow}})
	e.wantDup("the UID after the overwrite is confirmed", e.check(other), tenant.SameFile, sum(77), r2.ID)
	after, err := e.p.CatalogTotals(e.ctx)
	e.ok("CatalogTotals", err)
	if after.Instances != before.Instances {
		e.Errorf("an overwrite changed the instances held from %d to %d", before.Instances, after.Instances)
	}

	_, err = e.p.ClaimInstances(e.ctx, "missing", []tenant.InstanceClaim{c1}, at(1), nil)
	e.wantKind("ClaimInstances for a reception not there", err, fault.NotFound)
	receiving := e.openReception("a", 3, at(1))
	_, err = e.p.ClaimInstances(e.ctx, receiving.ID, []tenant.InstanceClaim{c1}, at(1), nil)
	e.wantPrecondition("ClaimInstances for a reception not identified", err)
	ended := e.failReception(e.stepTo(e.openReception("a", 4, at(1)), reception.Identified, "store"))
	_, err = e.p.ClaimInstances(e.ctx, ended.ID, []tenant.InstanceClaim{c1}, at(1), nil)
	e.wantPrecondition("ClaimInstances for a reception that ended", err)
	_, err = e.p.ClaimInstances(e.ctx, r1.ID, []tenant.InstanceClaim{c1, c1}, at(1), nil)
	e.wantKind("ClaimInstances naming one file twice", err, fault.Invalid)

	_, _, err = e.p.EnqueueWork(e.ctx, tenant.Work{ID: "claim-work", Kind: "reception", Due: at(0)}, T0)
	e.ok("EnqueueWork", err)
	w, err := e.p.ClaimWork(e.ctx, "reception", "worker-1", at(0), time.Minute)
	e.ok("ClaimWork", err)
	_, err = e.p.ClaimWork(e.ctx, "reception", "worker-2", at(61), time.Minute)
	e.ok("ClaimWork after the lease ended", err)
	fence := w.Fence()
	late := claimOf(file(r1, 1, 600))
	_, err = e.p.ClaimInstances(e.ctx, r1.ID, []tenant.InstanceClaim{late}, at(70), &fence)
	e.wantIs("ClaimInstances under a lease taken over", err, tenant.ErrLeaseLost, fault.Conflict)
	e.wantDup("a claim under a lost lease", e.check(late), tenant.NotHeld, tenant.SHA256{}, "")
}

// testClaimsOutliveTheirReception guards what stays and what goes when a
// reception ends: the instances it confirmed stay held whatever its state
// (failed after archiving, retried, discarded), so its retry finds them the
// same file and archives them no second time; the claims it did not confirm
// are released in the step's transaction, so another reception can claim
// them; a claim to overwrite that is released leaves the held instance as
// it was.
func testClaimsOutliveTheirReception(e *penv) {
	e.addReceiver("a", "store", 11112)
	r, f := e.identified("a", 1, 1, 2)
	e.claim(r, claimOf(f[0]), claimOf(f[1]))
	e.confirm(r, f[:1], []tenant.Duplicate{{Kind: tenant.ClaimedBefore}})
	r = e.failReception(r)
	e.wantDup("a confirmed instance of a reception that failed", e.check(claimOf(f[0])), tenant.SameFile, sum(1), r.ID)
	e.wantDup("a claim of a reception that failed", e.check(claimOf(f[1])), tenant.NotHeld, tenant.SHA256{}, "")
	_, retry, err := e.p.RetryReception(e.ctx, r.ID, "retry-1", "", at(500), e.audit(), nil)
	e.ok("RetryReception", err)
	retry = e.stepTo(retry, reception.Identified, "store")
	got := e.claim(retry, claimOf(f[0]), claimOf(f[1]))
	e.wantDup("the retry's claim of what the failed reception archived", got[0], tenant.SameFile, sum(1), r.ID)
	e.wantDup("the retry's claim of what it did not", got[1], tenant.ClaimedNow, tenant.SHA256{}, "")

	d, df := e.identified("a", 2, 10)
	pending := claimOf(df[0])
	e.claim(d, pending)
	d = e.failReception(d)
	_, err = e.p.DiscardReception(e.ctx, d.ID, "not wanted", at(600), e.audit(), nil)
	e.ok("DiscardReception", err)
	e.wantDup("a claim of a discarded reception", e.check(pending), tenant.NotHeld, tenant.SHA256{}, "")

	o, _ := e.identified("a", 1)
	overwrite := claimOf(f[0])
	overwrite.SHA256, overwrite.Overwrite = sum(88), true
	e.claim(o, overwrite)
	e.failReception(o)
	e.wantDup("a held instance after a claim to overwrite was released", e.check(claimOf(f[0])), tenant.SameFile, sum(1), r.ID)
}

// testCommitScan guards the catalog a commit writes: the subject, session
// and scan found or made, with the labels they were first given and the
// session's storage; a commit written again counting nothing twice; the
// running totals equal to what was made; every label conflict, each with
// its level and fault; labels and storage other than an existing session's
// or scan's own refused; a series of another session refused; an instance
// no one claims, or another reception claims, refused.
func testCommitScan(e *penv) {
	e.addReceiver("a", "store", 11112)
	e.addStorage("other-store")
	r, f := e.identified("a", 1, 1, 2, 3)
	answers := e.commit(r, f...)
	for i, a := range answers {
		if a.Kind != tenant.ClaimedNow {
			e.Errorf("file %d claimed as %v", i, a.Kind)
		}
	}
	totals, err := e.p.CatalogTotals(e.ctx)
	e.ok("CatalogTotals", err)
	if totals != (tenant.CatalogCounts{Subjects: 1, Sessions: 1, Scans: 1, Instances: 3}) {
		e.Errorf("totals after one scan of three = %+v", totals)
	}
	labels, err := e.p.CatalogLabels(e.ctx, r.StudyUID, []string{uidOf(2, 1), uidOf(2, 9)})
	e.ok("CatalogLabels", err)
	if labels.Session == nil || labels.Session.SubjectLabel != "sub-01" || labels.Session.SessionLabel != "ses-"+r.StudyUID ||
		labels.Session.Storage != "store" || labels.Scans[uidOf(2, 1)] != "scan-"+uidOf(2, 1) || len(labels.Scans) != 1 {
		e.Errorf("CatalogLabels = %+v %v", labels.Session, labels.Scans)
	}
	scans, truncated, err := e.p.ReceptionScans(e.ctx, r.ID)
	e.ok("ReceptionScans", err)
	if truncated || len(scans) != 1 || scans[0].Instances != 3 || scans[0].Bytes != 1001+1002+1003 || scans[0].SeriesUID != uidOf(2, 1) {
		e.Errorf("ReceptionScans = %+v, %v", scans, truncated)
	}
	rc, _, _ := e.p.Reception(e.ctx, r.ID)
	if rc.Counts.FilesArchived != 3 || rc.Counts.BytesArchived != 3006 {
		e.Errorf("the reception's archived counts = %d, %d; want 3, 3006", rc.Counts.FilesArchived, rc.Counts.BytesArchived)
	}

	e.confirm(r, f, []tenant.Duplicate{{Kind: tenant.ClaimedBefore}, {Kind: tenant.ClaimedBefore}, {Kind: tenant.ClaimedBefore}})
	again, _ := e.p.CatalogTotals(e.ctx)
	rc2, _, _ := e.p.Reception(e.ctx, r.ID)
	if again != totals || rc2.Counts != rc.Counts {
		e.Errorf("a commit written again changed the totals %+v -> %+v or the counts %+v -> %+v", totals, again, rc.Counts, rc2.Counts)
	}

	base := tenant.ScanCommit{ReceptionID: r.ID, StudyUID: r.StudyUID, SeriesUID: uidOf(2, 1), SubjectLabel: "sub-01",
		SessionLabel: "ses-" + r.StudyUID, ScanLabel: "scan-" + uidOf(2, 1), Storage: "store", At: at(100)}
	commit := func(change func(*tenant.ScanCommit)) error {
		c := base
		change(&c)
		_, err := e.p.CommitScan(e.ctx, c)
		return err
	}
	wantConflict := func(what string, err error, level, faultWord string) {
		e.Helper()
		var lc *tenant.LabelConflict
		if !errors.As(err, &lc) || lc.Label != level || lc.Fault != faultWord || fault.KindOf(err) != fault.Conflict {
			e.Errorf("%s: %v, want a %s label conflict %s", what, err, level, faultWord)
		}
	}
	wantConflict("an existing session under another subject", commit(func(c *tenant.ScanCommit) { c.SubjectLabel = "sub-02" }),
		"subject", tenant.FaultTwoSubjectsForOneStudy)
	e.wantPrecondition("an existing session under another label", commit(func(c *tenant.ScanCommit) { c.SessionLabel = "ses-renamed" }))
	e.wantPrecondition("an existing session on another storage", commit(func(c *tenant.ScanCommit) { c.Storage = "other-store" }))
	e.wantPrecondition("an existing scan under another label", commit(func(c *tenant.ScanCommit) { c.ScanLabel = "scan-renamed" }))
	e.wantPrecondition("a storage the tenant does not have", commit(func(c *tenant.ScanCommit) { c.Storage = "nosuch" }))
	wantConflict("a new scan with a label its session holds", commit(func(c *tenant.ScanCommit) { c.SeriesUID = uidOf(2, 2) }),
		"scan", tenant.FaultHeldByAnotherScan)
	wantConflict("a new scan with a label its session holds in another case", commit(func(c *tenant.ScanCommit) {
		c.SeriesUID, c.ScanLabel = uidOf(2, 2), "SCAN-"+uidOf(2, 1)
	}), "scan", tenant.FaultCaseCollision)

	r2, f2 := e.identified("a", 2, 20)
	other := tenant.ScanCommit{ReceptionID: r2.ID, StudyUID: r2.StudyUID, SeriesUID: uidOf(2, 2), SubjectLabel: "sub-01",
		SessionLabel: "ses-" + r.StudyUID, ScanLabel: "scan-x", Storage: "store", At: at(100)}
	try := func(change func(*tenant.ScanCommit)) error {
		c := other
		change(&c)
		_, err := e.p.CommitScan(e.ctx, c)
		return err
	}
	wantConflict("a new session with a label its subject holds", try(func(*tenant.ScanCommit) {}), "session", tenant.FaultHeldByAnotherSession)
	wantConflict("a new session with a label its subject holds in another case", try(func(c *tenant.ScanCommit) {
		c.SessionLabel = "SES-" + r.StudyUID
	}), "session", tenant.FaultCaseCollision)
	wantConflict("a new subject with a held subject label in another case", try(func(c *tenant.ScanCommit) {
		c.SubjectLabel, c.SessionLabel = "SUB-01", "ses-two"
	}), "subject", tenant.FaultCaseCollision)
	err = try(func(c *tenant.ScanCommit) { c.SessionLabel, c.SeriesUID = "ses-two", uidOf(2, 1) })
	if !errors.Is(err, tenant.ErrSeriesElsewhere) || fault.KindOf(err) != fault.Conflict {
		e.Errorf("a series of another session: %v, want ErrSeriesElsewhere", err)
	}
	key, err := layout.InstanceKey(labelPath("sub-01", "ses-two", "scan-x"), f2[0].SOPInstanceUID)
	e.ok("InstanceKey", err)
	unclaimed := tenant.ConfirmedInstance{SOPInstanceUID: f2[0].SOPInstanceUID, Key: key, Size: 1, ArchivedSHA256: sum(1)}
	e.wantPrecondition("an instance the reception does not claim", try(func(c *tenant.ScanCommit) {
		c.SessionLabel, c.Instances = "ses-two", []tenant.ConfirmedInstance{unclaimed}
	}))
	// An instance another reception claims: r claims it, r2 may not
	// confirm it.
	theirs := file(r, 1, 40)
	theirs.SeriesUID = uidOf(2, 2)
	e.claim(r, claimOf(theirs))
	theirKey, err := layout.InstanceKey(labelPath("sub-01", "ses-two", "scan-x"), theirs.SOPInstanceUID)
	e.ok("InstanceKey", err)
	e.wantPrecondition("an instance another reception claims", try(func(c *tenant.ScanCommit) {
		c.SessionLabel = "ses-two"
		c.Instances = []tenant.ConfirmedInstance{{SOPInstanceUID: theirs.SOPInstanceUID, Key: theirKey, Size: 1, ArchivedSHA256: theirs.SHA256}}
	}))
	e.wantDup("an instance another reception tried to confirm", e.check(claimOf(theirs)), tenant.Claimed, theirs.SHA256, r.ID)
	bad := unclaimed
	bad.Key = "data/sub-01/ses-two/scan-y/" + f2[0].SOPInstanceUID + ".dcm"
	e.wantKind("an instance whose key is not its scan's", try(func(c *tenant.ScanCommit) {
		c.SessionLabel, c.Instances = "ses-two", []tenant.ConfirmedInstance{bad}
	}), fault.Invalid)
	if final, _ := e.p.CatalogTotals(e.ctx); final != totals {
		e.Errorf("refused commits changed the totals %+v -> %+v", totals, final)
	}
	e.ok("a second session of the subject", try(func(c *tenant.ScanCommit) { c.SessionLabel = "ses-two" }))
	if final, _ := e.p.CatalogTotals(e.ctx); final != (tenant.CatalogCounts{Subjects: 1, Sessions: 2, Scans: 2, Instances: 3}) {
		e.Errorf("totals after a second session = %+v", final)
	}
}

// testReleaseIntoSameReception guards a file quarantined while its
// reception is receiving and released back into it: it is counted released
// there and no longer quarantined, received once, and once archived the
// tenant holds it like any file.
func testReleaseIntoSameReception(e *penv) {
	e.addReceiver("a", "store", 11112)
	r := e.openReception("a", 1, at(1))
	f := file(r, 1, 1)
	_, err := e.p.AddReceivedFiles(e.ctx, []tenant.ReceivedFile{f}, at(2))
	e.ok("AddReceivedFiles", err)
	q := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineBadLabel,
		Details: map[string]string{"label": "subject", "attribute": "PatientID", "fault": tenant.FaultEmpty}, Receiver: "a", ReceptionID: r.ID,
		SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SOPClassUID: f.SOPClassUID,
		TransferSyntaxUID: f.TransferSyntaxUID, SHA256: f.SHA256, Size: f.Size, Incoming: f.Incoming, ContentKept: true, Quarantined: at(3)}
	_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{q})
	e.ok("HoldFiles", err)
	rel, err := e.p.ReleaseQuarantined(e.ctx, q.ID, tenant.ReleaseAsIs, "", e.id("rc"), at(4), e.audit(), nil)
	e.ok("ReleaseQuarantined", err)
	if rel.ReleaseReception != r.ID {
		e.Fatalf("the release joined %s, want the reception still receiving, %s", rel.ReleaseReception, r.ID)
	}
	got, _, _ := e.p.Reception(e.ctx, r.ID)
	want := tenant.ReceptionCounts{FilesReceived: 1, BytesReceived: f.Size, FilesReleased: 1}
	if got.Counts != want {
		e.Errorf("after a release into the same reception: %+v, want %+v", got.Counts, want)
	}
	got = e.stepTo(got, reception.Identified, "store")
	e.commit(got, f)
	got = e.stepTo(got, reception.Done, "store")
	e.wantDup("a released file once archived", e.check(claimOf(f)), tenant.SameFile, f.SHA256, r.ID)
	if c := got.Counts; c.FilesReceived != c.FilesArchived+c.FilesDuplicate+c.FilesQuarantined {
		e.Errorf("at the end: %+v", c)
	}
}

// testDiscardKeepsQuarantined guards that a discard's list of files to
// remove, and the count and bytes in its audit entry, leave out every file
// a quarantine record keeps, and agree; the record stays held, with its
// bytes, until it is released or rejected.
func testDiscardKeepsQuarantined(e *penv) {
	e.addReceiver("a", "store", 11112)
	r := e.openReception("a", 1, at(1))
	kept, gone := file(r, 1, 1), file(r, 1, 2)
	_, err := e.p.AddReceivedFiles(e.ctx, []tenant.ReceivedFile{kept, gone}, at(2))
	e.ok("AddReceivedFiles", err)
	r = e.stepTo(r, reception.Complete, "store")
	q := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineBadLabel,
		Details: map[string]string{"label": "subject", "fault": tenant.FaultEmpty}, Receiver: "a", ReceptionID: r.ID,
		SOPInstanceUID: kept.SOPInstanceUID, StudyUID: kept.StudyUID, SeriesUID: kept.SeriesUID, SHA256: kept.SHA256,
		Size: kept.Size, Incoming: kept.Incoming, ContentKept: true, Quarantined: at(3)}
	_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{q})
	e.ok("HoldFiles", err)
	r = e.failReception(r)
	_, _, err = e.p.DiscardedFiles(e.ctx, r.ID, master.Page{Limit: 10})
	e.wantPrecondition("DiscardedFiles of a reception not discarded", err)
	audit := e.audit()
	_, err = e.p.DiscardReception(e.ctx, r.ID, "cleanup", at(900), audit, nil)
	e.ok("DiscardReception", err)
	list := allPages(e, 1, func(p master.Page) ([]tenant.ReceivedFile, string, error) { return e.p.DiscardedFiles(e.ctx, r.ID, p) })
	if len(list) != 1 || list[0].Incoming != gone.Incoming {
		e.Errorf("the files to remove = %+v, want only %s", list, gone.Incoming)
	}
	entry := e.allAudit(master.AuditFilter{})[0]
	if entry.ID != audit.ID || entry.Details[tenant.DetailFiles] != "1" || entry.Details[tenant.DetailBytes] != fmt.Sprint(gone.Size) {
		e.Errorf("the discard's audit entry %+v does not match its list", entry)
	}
	held, err := e.p.QuarantinedFile(e.ctx, q.ID)
	e.ok("QuarantinedFile", err)
	if held.State != tenant.QuarantineHeld || !held.ContentKept {
		e.Errorf("the record of a discarded reception reads %+v, want held with its bytes", held)
	}
}

// testHoldCountsReceived guards the count identity for a file that arrived
// and could not be recorded as a received file (too large, not DICOM): its
// hold counts it received and quarantined, once, so at the end received
// equals archived plus duplicate plus quarantined.
func testHoldCountsReceived(e *penv) {
	e.addReceiver("a", "store", 11112)
	r := e.openReception("a", 1, at(1))
	f := file(r, 1, 1)
	_, err := e.p.AddReceivedFiles(e.ctx, []tenant.ReceivedFile{f}, at(2))
	e.ok("AddReceivedFiles", err)
	large := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineTooLarge, Details: map[string]string{"limit": "4294967296"},
		Receiver: "a", ReceptionID: r.ID, Size: 4096, StudyUID: r.StudyUID, Quarantined: at(3)}
	for range 2 {
		_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{large})
		e.ok("HoldFiles", err)
	}
	r = e.stepTo(r, reception.Identified, "store")
	e.commit(r, f)
	r = e.stepTo(r, reception.Done, "store")
	c := r.Counts
	if c.FilesReceived != 2 || c.FilesReceived != c.FilesArchived+c.FilesDuplicate+c.FilesQuarantined ||
		c.BytesReceived != c.BytesArchived+c.BytesDuplicate+c.BytesQuarantined {
		e.Errorf("done: %+v, want received = archived + duplicate + quarantined, with the large file received once", c)
	}
}

// testCountIdentity guards the contract's identity at the end of every
// reception that did not fail: received = archived + duplicate +
// quarantined, files and bytes. A seeded random run of receptions, by a
// pipeline that holds what it does not archive or ignore, mixes every kind
// of hold (a file with no received file, a received file), releases (each
// mode, into the same reception and into another), rejections, retries and
// discards, and checks the identity whenever a reception ends.
func testCountIdentity(e *penv) {
	e.addReceiver("a", "store", 11112)
	e.addReceiver("b", "store", 11113)
	rng := uint64(42)
	next := func(n int) int {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(n)) //nolint:gosec // G115: n is a small positive count and the result is below it.
	}
	clock := 100
	tick := func() time.Time { clock++; return at(clock) }
	var held []tenant.QuarantinedFile // records still held, to release or reject
	checkEnd := func(r tenant.Reception) {
		e.Helper()
		c := r.Counts
		if r.State != reception.Failed && (c.FilesReceived != c.FilesArchived+c.FilesDuplicate+c.FilesQuarantined ||
			c.BytesReceived != c.BytesArchived+c.BytesDuplicate+c.BytesQuarantined) {
			e.Errorf("reception %s ended %s with %+v: received is not archived + duplicate + quarantined", r.ID, r.State, c)
		}
	}
	// run takes one receiving reception through the pipeline's steps.
	run := func(r tenant.Reception, quarantinedNow map[string]bool) {
		e.Helper()
		r, _, _ = e.p.Reception(e.ctx, r.ID)
		if next(8) == 0 {
			checkEnd(e.failReception(r))
			return
		}
		r = e.stepTo(r, reception.Identified, "store")
		files := allPages(e, 1000, func(p master.Page) ([]tenant.ReceivedFile, string, error) { return e.p.ReceivedFiles(e.ctx, r.ID, p) })
		var todo []tenant.ReceivedFile
		var claims []tenant.InstanceClaim
		for _, f := range files {
			if quarantinedNow[f.SOPInstanceUID+f.SHA256.String()] {
				continue
			}
			c := claimOf(f)
			c.Overwrite = next(3) == 0
			todo, claims = append(todo, f), append(claims, c)
		}
		answers, err := e.p.ClaimInstances(e.ctx, r.ID, claims, tick(), nil)
		e.ok("ClaimInstances", err)
		var dup tenant.StepCounts
		var holds []tenant.QuarantinedFile
		for i, a := range answers {
			f := todo[i]
			switch a.Kind {
			case tenant.SameFile:
				dup.FilesDuplicate, dup.BytesDuplicate = dup.FilesDuplicate+1, dup.BytesDuplicate+f.Size
			case tenant.DifferentFile, tenant.UIDElsewhere, tenant.Claimed:
				q := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineDifferentFile, Receiver: r.Receiver, ReceptionID: r.ID,
					SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SOPClassUID: f.SOPClassUID,
					TransferSyntaxUID: f.TransferSyntaxUID, SHA256: f.SHA256, KeptSHA256: a.HeldSHA256, Size: f.Size,
					Incoming: f.Incoming, ContentKept: true, Quarantined: tick()}
				if a.Kind != tenant.DifferentFile {
					q.Reason, q.KeptSHA256 = tenant.QuarantineUIDElsewhere, tenant.SHA256{}
				}
				holds = append(holds, q)
			}
		}
		e.confirm(r, todo, answers)
		if len(holds) > 0 {
			_, err := e.p.HoldFiles(e.ctx, holds)
			e.ok("HoldFiles", err)
			held = append(held, holds...)
		}
		r, _, _ = e.p.Reception(e.ctx, r.ID)
		r, err = e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: r.State, Event: reception.Archive, At: tick(), Counts: &dup}, nil)
		e.ok("StepReception archive", err)
		checkEnd(e.stepTo(r, reception.Done, "store"))
	}
	for round := range 40 {
		recv := []string{"a", "b"}[next(2)]
		study := next(4)
		r := e.openReception(recv, study, tick())
		quarantinedNow := map[string]bool{}
		var batch []tenant.ReceivedFile
		for range next(6) + 1 {
			f := file(r, study, study*100+next(12))
			f.SeriesUID = uidOf(2, study)
			if next(4) == 0 {
				f.SHA256 = sum(next(3) + 900)
			}
			batch = append(batch, f)
		}
		_, err := e.p.AddReceivedFiles(e.ctx, batch, tick())
		e.ok("AddReceivedFiles", err)
		if next(3) == 0 {
			// A file that could not be recorded as a received file.
			q := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineNotDICOM, Details: map[string]string{"offset": "0"},
				Receiver: recv, ReceptionID: r.ID, Size: int64(next(500) + 1), SHA256: sum(round), Incoming: "quarantine/" + e.id("x"),
				ContentKept: true, Quarantined: tick()}
			_, err := e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{q})
			e.ok("HoldFiles", err)
			held = append(held, q)
		}
		if next(3) == 0 {
			// A received file held at reception (a label it could not make).
			f := batch[0]
			q := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineBadLabel,
				Details: map[string]string{"label": "subject", "fault": tenant.FaultEmpty}, Receiver: recv, ReceptionID: r.ID,
				SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SOPClassUID: f.SOPClassUID,
				TransferSyntaxUID: f.TransferSyntaxUID, SHA256: f.SHA256, Size: f.Size, Incoming: f.Incoming, ContentKept: true, Quarantined: tick()}
			n, err := e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{q})
			e.ok("HoldFiles", err)
			if n == 1 {
				quarantinedNow[f.SOPInstanceUID+f.SHA256.String()] = true
				held = append(held, q)
				if next(2) == 0 {
					// Released back while its reception still receives.
					rel, err := e.p.ReleaseQuarantined(e.ctx, q.ID, tenant.ReleaseAsIs, "", e.id("rc"), tick(), e.audit(), nil)
					e.ok("ReleaseQuarantined into the same reception", err)
					if rel.ReleaseReception == r.ID {
						delete(quarantinedNow, f.SOPInstanceUID+f.SHA256.String())
					}
				}
			}
		}
		run(r, quarantinedNow)
		// Decide some records held: released into another reception (each
		// mode, through either receiver) and run, or rejected.
		for len(held) > 0 && next(2) == 0 {
			i := next(len(held))
			q := held[i]
			held = append(held[:i], held[i+1:]...)
			if next(3) == 0 {
				_, err := e.p.RejectQuarantined(e.ctx, q.ID, "not wanted", tick(), e.audit(), nil)
				if fault.KindOf(err) != fault.Conflict {
					e.ok("RejectQuarantined", err)
				}
				continue
			}
			mode := tenant.ReleaseAsIs
			if q.Reason == tenant.QuarantineDifferentFile && next(2) == 0 {
				mode = tenant.ReleaseOverwrite
			}
			rel, err := e.p.ReleaseQuarantined(e.ctx, q.ID, mode, []string{"a", "b"}[next(2)], e.id("rc"), tick(), e.audit(), nil)
			if fault.KindOf(err) == fault.Conflict {
				continue // released already, or its bytes never kept
			}
			e.ok("ReleaseQuarantined", err)
			if rel.ReleaseReception != "" {
				joined, _, _ := e.p.Reception(e.ctx, rel.ReleaseReception)
				if joined.State == reception.Receiving {
					run(joined, map[string]bool{})
				}
			}
		}
		// Retry or discard a failed reception now and then.
		failed, _, err := e.p.ListReceptions(e.ctx, tenant.ReceptionFilter{States: []reception.State{reception.Failed}}, master.Page{Limit: 5})
		e.ok("ListReceptions", err)
		for _, f := range failed {
			if f.RetryReception != "" || f.DiscardReason != "" {
				continue
			}
			if next(2) == 0 {
				_, err := e.p.DiscardReception(e.ctx, f.ID, "not wanted", tick(), e.audit(), nil)
				e.ok("DiscardReception", err)
				continue
			}
			_, retry, err := e.p.RetryReception(e.ctx, f.ID, e.id("rr"), "", tick(), e.audit(), nil)
			e.ok("RetryReception", err)
			run(retry, map[string]bool{})
		}
	}
	ended, _, err := e.p.ListReceptions(e.ctx, tenant.ReceptionFilter{States: []reception.State{reception.Done}}, master.Page{Limit: 1000})
	e.ok("ListReceptions", err)
	if len(ended) < 20 {
		e.Errorf("only %d receptions ended done, so the test tested little", len(ended))
	}
}

// labelPath returns the three labels as a label.Path.
func labelPath(subject, session, scan string) label.Path {
	return label.Path{Subject: subject, Session: session, Scan: scan}
}
