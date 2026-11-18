package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amrshadid/magnetar/internal/core/label"
	"github.com/amrshadid/magnetar/internal/core/layout"
	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// The tests in this file need several writers at once, which only a
// database such as Postgres has: many connections, through several stores
// on one schema as several processes would be, calling at the same moment.
// Each runs its race many rounds and then checks the rows themselves, read
// with a connection of its own.

// cT0 is the time the concurrency tests start at.
var cT0 = time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)

// cenv is one concurrency test: its stores on one schema, a connection that
// reads the rows, and a source of ids.
type cenv struct {
	t      *testing.T
	stores []*Tenant
	db     *pgx.Conn
	ctx    context.Context
	seq    atomic.Int64
}

// newCenv opens n migrated stores of one tenant on a new schema, each with a
// pool of conns connections.
func newCenv(t *testing.T, n, conns int) *cenv {
	t.Helper()
	address := withParams(t, newSchema(t), map[string]string{"pool_max_conns": fmt.Sprint(conns)})
	e := &cenv{t: t, ctx: context.Background(), db: admin(t, address)}
	for range n {
		e.stores = append(e.stores, openTenantAt(t, address, "conc"))
	}
	return e
}

// id returns a new id with the prefix.
func (e *cenv) id(p string) string { return fmt.Sprintf("%s%07d", p, e.seq.Add(1)) }

// audit returns a new audit entry.
func (e *cenv) audit() master.AuditEntry {
	return master.AuditEntry{ID: e.id("a"), Time: cT0, Method: "Test", Outcome: master.OutcomeDone}
}

// ok fails the test on an error.
func (e *cenv) ok(what string, err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatalf("%s: %v", what, err)
	}
}

// p is the first store.
func (e *cenv) p() *Tenant { return e.stores[0] }

// storage adds a directory storage.
func (e *cenv) storage(name string) {
	e.t.Helper()
	_, err := e.p().AddStorage(e.ctx, tenant.Storage{Name: name, Type: tenant.StorageDirectory,
		Directory: tenant.DirectoryStorage{Root: "fast", Path: name}}, cT0, e.audit(), nil)
	e.ok("AddStorage", err)
}

// receiver adds a receiver on the storage.
func (e *cenv) receiver(name, storage string, port int) {
	e.t.Helper()
	_, err := e.p().AddReceiver(e.ctx, tenant.Receiver{Name: name, Type: tenant.ReceiverDIMSE, Storage: storage,
		DIMSE:       tenant.DIMSEReceiver{Port: port, AETitle: "ARCHIVE"},
		QuietPeriod: tenant.DefaultQuietPeriod, MaxOpenTime: tenant.DefaultMaxOpenTime, MaxFileBytes: tenant.DefaultMaxFileBytes},
		cT0, e.audit(), nil)
	e.ok("AddReceiver", err)
}

// cUID returns a valid UID unique to kind and n.
func cUID(kind, n int) string { return fmt.Sprintf("2.25.%d.%d", kind, n+1) }

// cSum returns a checksum made from n, never zero.
func cSum(n int) tenant.SHA256 {
	var s tenant.SHA256
	for i := range s {
		s[i] = byte(n >> (8 * (i % 4)) & 0xff)
	}
	s[31] = 1
	return s
}

// cFile is received file n of reception r, in series se.
func cFile(r tenant.Reception, se, n int) tenant.ReceivedFile {
	return tenant.ReceivedFile{ReceptionID: r.ID, SOPInstanceUID: cUID(3, n), SeriesUID: cUID(2, se), StudyUID: r.StudyUID,
		SOPClassUID: "1.2.840.10008.5.1.4.1.1.4", TransferSyntaxUID: "1.2.840.10008.1.2.1", Size: int64(1000 + n%1000), SHA256: cSum(n),
		Incoming: fmt.Sprintf("%s/f%07d.dcm", r.ID, n), Caller: tenant.Caller{CallingAETitle: "SCANNER1", Address: "192.0.2.7:51234"},
		Association: "assoc-" + r.ID}
}

// claimOfFile is the claim of a received file.
func claimOfFile(f tenant.ReceivedFile) tenant.InstanceClaim {
	return tenant.InstanceClaim{SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SHA256: f.SHA256}
}

// identified opens a reception of study at receiver, records files, and
// identifies it to storage with the labels given.
func (e *cenv) identified(receiver, studyUID, storage, subject, session string, files func(tenant.Reception) []tenant.ReceivedFile) (tenant.Reception, []tenant.ReceivedFile) {
	e.t.Helper()
	r, _, err := e.p().OpenReception(e.ctx, receiver, studyUID, e.id("rc"), cT0)
	e.ok("OpenReception", err)
	fs := files(r)
	for i := 0; i < len(fs); i += tenant.MaxReceivedBatch {
		_, err = e.p().AddReceivedFiles(e.ctx, fs[i:min(i+tenant.MaxReceivedBatch, len(fs))], cT0)
		e.ok("AddReceivedFiles", err)
	}
	r, err = e.p().StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet, At: cT0.Add(time.Minute)}, nil)
	e.ok("quiet", err)
	r, err = e.p().StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Complete, Event: reception.Identify, At: cT0.Add(2 * time.Minute),
		SubjectLabel: subject, SessionLabel: session, Storage: storage}, nil)
	e.ok("identify", err)
	return r, fs
}

// scanCommit is the commit of files (of one series) of r into scan.
func scanCommit(r tenant.Reception, subject, session, scan, storage string, files []tenant.ReceivedFile) tenant.ScanCommit {
	p := label.Path{Subject: subject, Session: session, Scan: scan}
	c := tenant.ScanCommit{ReceptionID: r.ID, StudyUID: r.StudyUID, SeriesUID: files[0].SeriesUID, SubjectLabel: subject,
		SessionLabel: session, ScanLabel: scan, Storage: storage, At: cT0.Add(3 * time.Minute)}
	for _, f := range files {
		key, _ := layout.InstanceKey(p, f.SOPInstanceUID)
		c.Instances = append(c.Instances, tenant.ConfirmedInstance{SOPInstanceUID: f.SOPInstanceUID, Key: key, Length: f.Size,
			Size: f.Size, ArchivedSHA256: f.SHA256})
	}
	return c
}

// atOnce runs fn(i) for i in [0, n) on n goroutines released together, and
// waits for all of them.
func atOnce(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			fn(i)
		})
	}
	close(start)
	wg.Wait()
}

// deadlocks is how many changes the stores ran again after a deadlock.
func (e *cenv) deadlocks() int64 {
	var n int64
	for _, s := range e.stores {
		n += s.retried.deadlocks.Load()
	}
	return n
}

// scalar reads one integer with the test's own connection.
func (e *cenv) scalar(query string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.db.QueryRow(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// checkTotals fails unless the catalog's and the quarantine's running
// totals equal counts over their rows, each scan's counts equal its
// instances, and no claim is held by a reception that ended, is not
// identified, or is not there.
func (e *cenv) checkTotals(when string) {
	e.t.Helper()
	for _, c := range []struct{ what, query string }{
		{"the catalog's totals disagree with its rows", `SELECT count(*) FROM catalog_totals WHERE
			subjects <> (SELECT count(*) FROM subjects) OR sessions <> (SELECT count(*) FROM sessions) OR
			scans <> (SELECT count(*) FROM scans) OR instances <> (SELECT count(*) FROM instances WHERE state = 'held')`},
		{"scans' counts disagree with their instances", `SELECT count(*) FROM scans s WHERE
			instances <> (SELECT count(*) FROM instances i WHERE i.scan_id = s.id) OR
			bytes <> (SELECT coalesce(sum(size), 0) FROM instances i WHERE i.scan_id = s.id)`},
		{"the quarantine's totals disagree with its records", `SELECT count(*) FROM quarantine_totals WHERE
			files <> (SELECT count(*) FROM quarantine WHERE state = 'held') OR
			bytes <> (SELECT coalesce(sum(size), 0) FROM quarantine WHERE state = 'held' AND content_kept)`},
		{"claims are held by receptions that ended or are not archiving", `SELECT count(*) FROM instances i
			LEFT JOIN receptions r ON r.id = i.reception_id WHERE i.state = 'claimed' AND (r.id IS NULL OR r.ended OR r.storage = '')`},
		{"receptions' archived counts disagree with what they confirmed", `SELECT count(*) FROM receptions r WHERE
			files_archived <> (SELECT count(*) FROM instances i WHERE i.reception_id = r.id AND i.state = 'held') OR
			bytes_archived <> (SELECT coalesce(sum(size), 0) FROM instances i WHERE i.reception_id = r.id AND i.state = 'held')`},
		{"a study's arrivals are not numbered 1 to n", `SELECT count(*) FROM (SELECT study_uid FROM receptions GROUP BY study_uid
			HAVING max(arrival) <> count(*) OR min(arrival) <> 1 OR count(DISTINCT arrival) <> count(*)) AS bad`},
		{"a reception done breaks received = archived + duplicate + quarantined", `SELECT count(*) FROM receptions WHERE state = 'done' AND
			(files_received <> files_archived + files_duplicate + files_quarantined OR
			 bytes_received <> bytes_archived + bytes_duplicate + bytes_quarantined)`},
	} {
		if n := e.scalar(c.query); n != 0 {
			e.t.Fatalf("%s: %s (%d)", when, c.what, n)
		}
	}
}

// portAnswer reports whether err is one of the port's own answers to a
// change that met another (a Conflict: a precondition, a step from a state
// left, a refusal of the core, a label conflict, a lease lost), never an
// unclassified failure.
func portAnswer(err error) bool {
	return fault.KindOf(err) == fault.Conflict
}

// TestOpenOneReceptionAtOnce guards OpenReception with many writers: 24
// calls at once, through three stores, for one study at three receivers
// open exactly one reception per receiver, every call at a receiver
// answers that one, and the study's arrivals are 1, 2 and 3, each once;
// twenty rounds, each a new study.
func TestOpenOneReceptionAtOnce(t *testing.T) {
	e := newCenv(t, 3, 8)
	e.storage("store")
	receivers := []string{"a", "b", "c"}
	for i, r := range receivers {
		e.receiver(r, "store", 11112+i)
	}
	for round := range 20 {
		study := cUID(1, round)
		const n = 24
		got := make([]tenant.Reception, n)
		errs := make([]error, n)
		atOnce(n, func(i int) {
			got[i], _, errs[i] = e.stores[i%3].OpenReception(e.ctx, receivers[i%3], study, fmt.Sprintf("o%02d-%02d", round, i), cT0)
		})
		byReceiver := map[string]string{}
		arrivals := map[int]string{}
		for i := range n {
			e.ok("OpenReception", errs[i])
			r := got[i]
			if id, seen := byReceiver[r.Receiver]; seen && id != r.ID {
				t.Fatalf("round %d: opens at %s made two receptions, %s and %s", round, r.Receiver, id, r.ID)
			}
			byReceiver[r.Receiver] = r.ID
			if id, seen := arrivals[r.Arrival]; seen && id != r.ID {
				t.Fatalf("round %d: arrival %d given to %s and %s", round, r.Arrival, id, r.ID)
			}
			arrivals[r.Arrival] = r.ID
		}
		if len(byReceiver) != 3 || len(arrivals) != 3 || arrivals[1] == "" || arrivals[2] == "" || arrivals[3] == "" {
			t.Fatalf("round %d: receptions %v, arrivals %v; want one at each receiver, arrivals 1 to 3", round, byReceiver, arrivals)
		}
		if rows := e.scalar(`SELECT count(*) FROM receptions WHERE study_uid = $1`, study); rows != 3 {
			t.Fatalf("round %d: %d rows for the study, want 3", round, rows)
		}
	}
	if n := e.uniqueRetries(); n != 0 {
		t.Errorf("%d opens were run again after a unique violation: an open did not meet the reception that won", n)
	}
	e.checkTotals("after the opens")
}

// TestClaimsInOppositeOrders guards ClaimInstances with two writers: two
// receptions claim the same 200 instances at once, one given them in
// ascending order and the other in descending, through two stores; forty
// rounds. Every UID is claimed now by exactly one and answered Claimed, by
// that one, to the other; no error reaches either caller; and since every
// claim takes its rows in sorted order, no deadlock happened at all, not
// even one the driver hid by running the claim again.
func TestClaimsInOppositeOrders(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	e.receiver("a", "store", 11112)
	e.receiver("b", "store", 11113)
	const files = 200
	for round := range 40 {
		study := cUID(1, round)
		mkFiles := func(r tenant.Reception) []tenant.ReceivedFile {
			out := make([]tenant.ReceivedFile, files)
			for i := range out {
				out[i] = cFile(r, round, round*1000+i)
			}
			return out
		}
		r1, f1 := e.identified("a", study, "store", "sub-01", "ses-"+study, mkFiles)
		r2, f2 := e.identified("b", study, "store", "sub-01", "ses-"+study, mkFiles)
		c1, c2 := make2Claims(f1, false), make2Claims(f2, true)
		recs := []tenant.Reception{r1, r2}
		claims := [][]tenant.InstanceClaim{c1, c2}
		answers := make([][]tenant.Duplicate, 2)
		errs := make([]error, 2)
		atOnce(2, func(i int) {
			answers[i], errs[i] = e.stores[i].ClaimInstances(e.ctx, recs[i].ID, claims[i], cT0, nil)
		})
		e.ok("ClaimInstances", errs[0])
		e.ok("ClaimInstances", errs[1])
		kinds := map[string][2]tenant.Duplicate{}
		for i := range 2 {
			for k, c := range claims[i] {
				v := kinds[c.SOPInstanceUID]
				v[i] = answers[i][k]
				kinds[c.SOPInstanceUID] = v
			}
		}
		for uid, v := range kinds {
			a, b := v[0], v[1]
			switch {
			case a.Kind == tenant.ClaimedNow && b.Kind == tenant.Claimed && b.HeldBy == r1.ID:
			case b.Kind == tenant.ClaimedNow && a.Kind == tenant.Claimed && a.HeldBy == r2.ID:
			default:
				t.Fatalf("round %d: %s answered %v and %v, want one claimed now and the other told so", round, uid, a, b)
			}
		}
	}
	if n := e.deadlocks(); n != 0 {
		t.Errorf("%d claims were run again after a deadlock: the claims did not take their rows in one order", n)
	}
	if n := e.uniqueRetries(); n != 0 {
		t.Errorf("%d claims were run again after a unique violation", n)
	}
	e.checkTotals("after the claims")
}

// make2Claims returns the claims of files in ascending order of UID, or
// descending.
func make2Claims(files []tenant.ReceivedFile, descending bool) []tenant.InstanceClaim {
	out := make([]tenant.InstanceClaim, len(files))
	for i, f := range files {
		out[i] = claimOfFile(f)
	}
	slices.SortFunc(out, func(a, b tenant.InstanceClaim) int {
		if descending {
			a, b = b, a
		}
		switch {
		case a.SOPInstanceUID < b.SOPInstanceUID:
			return -1
		case a.SOPInstanceUID > b.SOPInstanceUID:
			return 1
		}
		return 0
	})
	return out
}

// TestCommitsShareCatalogRows guards CommitScan with many writers, in ten
// rounds each with a new subject:
//
//   - six receptions of one new study, at six receivers, commit their
//     scans at once (one series all share, and one of each's own): one
//     subject, one session, seven scans;
//   - six receptions of six new studies of that subject commit at once:
//     still one subject, and six more sessions;
//   - two new studies of the subject with one session label commit at
//     once: one is made, the other is a label conflict held by another
//     session, never an unclassified failure.
//
// After each round the running totals equal the rows.
func TestCommitsShareCatalogRows(t *testing.T) {
	e := newCenv(t, 3, 8)
	e.storage("store")
	const k = 6
	for i := range k {
		e.receiver(fmt.Sprintf("r%d", i), "store", 11112+i)
	}
	n := 0
	files := func(r tenant.Reception, series ...int) []tenant.ReceivedFile {
		var out []tenant.ReceivedFile
		for _, se := range series {
			for range 20 {
				n++
				out = append(out, cFile(r, se, n))
			}
		}
		return out
	}
	commitAll := func(round int, commits []tenant.ScanCommit) []error {
		errs := make([]error, len(commits))
		atOnce(len(commits), func(i int) { _, errs[i] = e.stores[i%len(e.stores)].CommitScan(e.ctx, commits[i]) })
		return errs
	}
	for round := range 10 {
		subject := fmt.Sprintf("sub-%02d", round)
		before, err := e.p().CatalogTotals(e.ctx)
		e.ok("CatalogTotals", err)

		// One new study at six receivers.
		study := cUID(1, 100+round)
		session := "ses-" + study
		shared := 10000 + round*100
		var commits []tenant.ScanCommit
		for i := range k {
			own := shared + 1 + i
			r, fs := e.identified(fmt.Sprintf("r%d", i), study, "store", subject, session, func(r tenant.Reception) []tenant.ReceivedFile {
				return files(r, shared, own)
			})
			claims := make([]tenant.InstanceClaim, len(fs))
			for j, f := range fs {
				claims[j] = claimOfFile(f)
			}
			_, err := e.p().ClaimInstances(e.ctx, r.ID, claims, cT0, nil)
			e.ok("ClaimInstances", err)
			commits = append(commits, scanCommit(r, subject, session, "scan-shared", "store", fs[:20]),
				scanCommit(r, subject, session, fmt.Sprintf("scan-%d", i), "store", fs[20:]))
		}
		for _, err := range commitAll(round, commits) {
			e.ok("CommitScan of one study at once", err)
		}
		after, err := e.p().CatalogTotals(e.ctx)
		e.ok("CatalogTotals", err)
		if d := (tenant.CatalogCounts{Subjects: after.Subjects - before.Subjects, Sessions: after.Sessions - before.Sessions,
			Scans: after.Scans - before.Scans, Instances: after.Instances - before.Instances}); d != (tenant.CatalogCounts{Subjects: 1, Sessions: 1, Scans: 1 + k, Instances: 40 * k}) {
			t.Fatalf("round %d: one study at once made %+v, want 1 subject, 1 session, %d scans, %d instances", round, d, 1+k, 40*k)
		}

		// Six new studies of the subject.
		commits = commits[:0]
		for i := range k {
			st := cUID(1, 1000+round*10+i)
			r, fs := e.identified(fmt.Sprintf("r%d", i), st, "store", subject, "ses-"+st, func(r tenant.Reception) []tenant.ReceivedFile {
				return files(r, 20000+round*10+i)
			})
			claims := make([]tenant.InstanceClaim, len(fs))
			for j, f := range fs {
				claims[j] = claimOfFile(f)
			}
			_, err := e.p().ClaimInstances(e.ctx, r.ID, claims, cT0, nil)
			e.ok("ClaimInstances", err)
			commits = append(commits, scanCommit(r, subject, "ses-"+st, "scan-1", "store", fs))
		}
		for _, err := range commitAll(round, commits) {
			e.ok("CommitScan of six studies of one subject at once", err)
		}
		final, err := e.p().CatalogTotals(e.ctx)
		e.ok("CatalogTotals", err)
		if final.Subjects != after.Subjects || final.Sessions != after.Sessions+k {
			t.Fatalf("round %d: six studies of one subject made %d subjects and %d sessions, want 0 and %d",
				round, final.Subjects-after.Subjects, final.Sessions-after.Sessions, k)
		}

		// Two studies, one session label.
		commits = commits[:0]
		for i := range 2 {
			st := cUID(1, 5000+round*10+i)
			r, fs := e.identified(fmt.Sprintf("r%d", i), st, "store", subject, "ses-twice", func(r tenant.Reception) []tenant.ReceivedFile {
				return files(r, 30000+round*10+i)
			})
			claims := make([]tenant.InstanceClaim, len(fs))
			for j, f := range fs {
				claims[j] = claimOfFile(f)
			}
			_, err := e.p().ClaimInstances(e.ctx, r.ID, claims, cT0, nil)
			e.ok("ClaimInstances", err)
			commits = append(commits, scanCommit(r, subject, "ses-twice", "scan-1", "store", fs))
		}
		errs := commitAll(round, commits)
		made, refused := 0, 0
		for _, err := range errs {
			var lc *tenant.LabelConflict
			switch {
			case err == nil:
				made++
			case errors.As(err, &lc) && lc.Label == "session" && lc.Fault == tenant.FaultHeldByAnotherSession && fault.KindOf(err) == fault.Conflict:
				refused++
			default:
				t.Fatalf("round %d: a commit of a session label taken at once: %v", round, err)
			}
		}
		if made != 1 || refused != 1 {
			t.Fatalf("round %d: two sessions with one label at once: %d made, %d refused; want 1 and 1", round, made, refused)
		}
		e.checkTotals(fmt.Sprintf("round %d", round))
	}
	if n := e.uniqueRetries(); n != 0 {
		t.Errorf("%d commits were run again after a unique violation: a catalog row was not found or made as its insert met one", n)
	}
	if n := e.deadlocks(); n != 0 {
		t.Errorf("%d commits were run again after a deadlock", n)
	}
}

// TestWorkersTakeEachUnitOnce guards leased work with many writers. Sixteen
// workers through two stores claim 400 units, each until none is due: every
// unit is claimed exactly once. Then 200 more units are leased for a
// second, and eight workers finish them (completing or failing each) while
// eight others, at once, claim them two seconds later, taking over every
// lease that ended: a unit whose takeover succeeded was never completed or
// failed by its first worker, whose write was refused as a lease lost and
// changed nothing.
func TestWorkersTakeEachUnitOnce(t *testing.T) {
	e := newCenv(t, 2, 10)
	for i := range 400 {
		_, _, err := e.p().EnqueueWork(e.ctx, tenant.Work{ID: fmt.Sprintf("w%04d", i), Kind: "archive", Due: cT0.Add(time.Duration(i%7) * time.Millisecond)}, cT0)
		e.ok("EnqueueWork", err)
	}
	const workers = 16
	claimed := make([][]string, workers)
	errs := make([]error, workers)
	atOnce(workers, func(k int) {
		for {
			w, err := e.stores[k%2].ClaimWork(e.ctx, "archive", fmt.Sprintf("worker-%d", k), cT0.Add(time.Second), time.Minute)
			if fault.KindOf(err) == fault.NotFound {
				return
			}
			if err != nil {
				errs[k] = err
				return
			}
			claimed[k] = append(claimed[k], w.ID)
		}
	})
	seen := map[string]int{}
	for k := range workers {
		e.ok("ClaimWork", errs[k])
		for _, id := range claimed[k] {
			seen[id]++
		}
	}
	if len(seen) != 400 {
		t.Errorf("%d units claimed, want 400", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s claimed %d times", id, n)
		}
	}

	for i := range 200 {
		_, _, err := e.p().EnqueueWork(e.ctx, tenant.Work{ID: fmt.Sprintf("x%04d", i), Kind: "lease", Due: cT0}, cT0)
		e.ok("EnqueueWork", err)
	}
	// The first workers lease every unit for a second.
	var first []tenant.Work
	for {
		w, err := e.stores[len(first)%2].ClaimWork(e.ctx, "lease", fmt.Sprintf("first-%d", len(first)%8), cT0, time.Second)
		if fault.KindOf(err) == fault.NotFound {
			break
		}
		e.ok("ClaimWork", err)
		first = append(first, w)
	}
	// Then, at once, each first worker finishes its units (completing some,
	// failing others) while the second workers take over every lease that
	// ended, two seconds later.
	var mu sync.Mutex
	finished := map[string]bool{} // units a first worker's write was accepted for
	takenOver := map[string]bool{}
	var failures []string
	atOnce(workers, func(k int) {
		store := e.stores[k%2]
		if k < workers/2 {
			for i := k; i < len(first); i += workers / 2 {
				w := first[i]
				var err error
				if i%2 == 0 {
					err = store.CompleteWork(e.ctx, w.Fence(), cT0.Add(500*time.Millisecond), nil)
				} else {
					_, err = store.FailWork(e.ctx, w.Fence(), "storage unreachable", cT0.Add(500*time.Millisecond))
				}
				mu.Lock()
				switch {
				case err == nil:
					finished[w.ID] = true
				case !errors.Is(err, tenant.ErrLeaseLost):
					failures = append(failures, "first write: "+err.Error())
				}
				mu.Unlock()
			}
			return
		}
		for {
			w, err := store.ClaimWork(e.ctx, "lease", fmt.Sprintf("second-%d", k), cT0.Add(2*time.Second), time.Hour)
			if fault.KindOf(err) == fault.NotFound {
				return
			}
			mu.Lock()
			if err != nil {
				failures = append(failures, "takeover: "+err.Error())
			} else {
				takenOver[w.ID] = true
			}
			mu.Unlock()
			if err != nil {
				return
			}
		}
	})
	for _, f := range failures {
		t.Error(f)
	}
	for id := range finished {
		if takenOver[id] {
			w, _ := e.p().Work(e.ctx, id)
			t.Errorf("%s: its first worker's write was accepted and it was taken over too (%+v)", id, w)
		}
	}
	// How the race fell is the machine's; both outcomes are logged.
	t.Logf("%d units finished by their first worker, %d taken over", len(finished), len(takenOver))
	// Every unit taken over is leased by its second worker, unchanged by the
	// first.
	if n := e.scalar(`SELECT count(*) FROM work WHERE kind = 'lease' AND attempt >= 2 AND (state <> 'leased' OR owner NOT LIKE 'second-%')`); n != 0 {
		t.Errorf("%d units taken over were changed by a write under a lost lease", n)
	}
}

// TestStorageRemovalRacesIdentifyAndCommit guards a storage's row as the
// lock of its use, eighty rounds: a removal of a storage and an identify to
// it at once never both succeed (one is a precondition failure), and a
// change of where its files are and an identify at once fail with nothing
// but a precondition; and a
// commit into a storage and its removal at once leave the commit made and
// the removal refused, with no other error.
func TestStorageRemovalRacesIdentifyAndCommit(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	e.receiver("a", "store", 11112)
	for round := range 80 {
		name := fmt.Sprintf("s%03d", round)
		e.storage(name)
		r, _, err := e.p().OpenReception(e.ctx, "a", cUID(1, round), e.id("rc"), cT0)
		e.ok("OpenReception", err)
		r, err = e.p().StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet, At: cT0}, nil)
		e.ok("quiet", err)
		var identify, other error
		move := round%2 == 1
		atOnce(2, func(i int) {
			if i == 0 {
				_, identify = e.stores[0].StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Complete, Event: reception.Identify,
					At: cT0, SubjectLabel: "sub-01", SessionLabel: "ses-" + r.ID, Storage: name}, nil)
				return
			}
			if move {
				_, other = e.stores[1].SetStorage(e.ctx, tenant.Storage{Name: name, Directory: tenant.DirectoryStorage{Path: name + "-moved"}},
					[]tenant.StorageField{tenant.FieldDirectoryPath}, cT0, e.audit(), nil)
			} else {
				_, other = e.stores[1].RemoveStorage(e.ctx, name, e.audit(), nil)
			}
		})
		for _, err := range []error{identify, other} {
			if err != nil && !errors.Is(err, tenant.ErrPrecondition) {
				t.Fatalf("round %d: %v, want success or a precondition failure", round, err)
			}
		}
		// A move and an identify may both succeed: the move first, then the
		// identify, as one writer would order them, before any file is on
		// the storage. A removal and an identify may not: the reception
		// would name a storage that is gone.
		if !move && (identify == nil) == (other == nil) {
			t.Fatalf("round %d: identify %v and the removal %v; want exactly one to succeed", round, identify, other)
		}
	}
	// A commit and a removal at once.
	for round := range 20 {
		name := fmt.Sprintf("c%03d", round)
		e.storage(name)
		study := cUID(1, 1000+round)
		r, fs := e.identified("a", study, name, "sub-01", "ses-"+study, func(r tenant.Reception) []tenant.ReceivedFile {
			return []tenant.ReceivedFile{cFile(r, 1000+round, 100000+round)}
		})
		_, err := e.p().ClaimInstances(e.ctx, r.ID, []tenant.InstanceClaim{claimOfFile(fs[0])}, cT0, nil)
		e.ok("ClaimInstances", err)
		var commit, remove error
		atOnce(2, func(i int) {
			if i == 0 {
				_, commit = e.stores[0].CommitScan(e.ctx, scanCommit(r, "sub-01", "ses-"+study, "scan-1", name, fs))
			} else {
				_, remove = e.stores[1].RemoveStorage(e.ctx, name, e.audit(), nil)
			}
		})
		if commit != nil || !errors.Is(remove, tenant.ErrPrecondition) {
			t.Fatalf("round %d: commit %v, removal %v; want the commit made and the removal refused", round, commit, remove)
		}
	}
	if n := e.scalar(`SELECT count(*) FROM receptions r WHERE NOT ended AND storage <> '' AND NOT EXISTS (SELECT 1 FROM storages s WHERE s.name = r.storage)`); n != 0 {
		t.Errorf("%d receptions in progress name a storage that was removed", n)
	}
	e.checkTotals("after the races")
}

// TestHoldRacesReceivedBatch guards a hold and a received batch of the same
// file at once, a hundred rounds: the file is one received file and one
// record, quarantined once, and received once or twice: once when the batch
// came first and the hold saw its file, twice when the hold came first, as
// the two would in either order on one writer.
func TestHoldRacesReceivedBatch(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	e.receiver("a", "store", 11112)
	for round := range 100 {
		r, _, err := e.p().OpenReception(e.ctx, "a", cUID(1, round), e.id("rc"), cT0)
		e.ok("OpenReception", err)
		f := cFile(r, round, round)
		q := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineBadLabel,
			Details: map[string]string{"label": "subject", "fault": tenant.FaultEmpty}, Receiver: "a", ReceptionID: r.ID,
			SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SOPClassUID: f.SOPClassUID,
			TransferSyntaxUID: f.TransferSyntaxUID, SHA256: f.SHA256, Size: f.Size, Incoming: f.Incoming, ContentKept: true, Quarantined: cT0}
		var errs [2]error
		atOnce(2, func(i int) {
			if i == 0 {
				_, errs[0] = e.stores[0].AddReceivedFiles(e.ctx, []tenant.ReceivedFile{f}, cT0)
			} else {
				_, errs[1] = e.stores[1].HoldFiles(e.ctx, []tenant.QuarantinedFile{q})
			}
		})
		e.ok("AddReceivedFiles", errs[0])
		e.ok("HoldFiles", errs[1])
		got, _, err := e.p().Reception(e.ctx, r.ID)
		e.ok("Reception", err)
		c := got.Counts
		if c.FilesQuarantined != 1 || (c.FilesReceived != 1 && c.FilesReceived != 2) || c.BytesReceived != c.FilesReceived*f.Size {
			t.Fatalf("round %d: %+v, want quarantined once and received once or twice", round, c)
		}
	}
	e.checkTotals("after the holds")
}

// uniqueRetries is how many changes the stores ran again after a unique
// violation: every insert of the pipeline meets a row made meanwhile with ON
// CONFLICT DO NOTHING and a read, so none should be.
func (e *cenv) uniqueRetries() int64 {
	var n int64
	for _, s := range e.stores {
		n += s.retried.unique.Load()
	}
	return n
}

// TestBatchRacesQuietStep guards the order of a received batch and the quiet
// step of its reception, a hundred rounds: a batch with a later file and a
// quiet step decided from the last file before it, at once, never both
// succeed. Either the step comes first, and the batch is refused as the
// reception closed (its files belong to the next arrival), or the batch
// comes first, and the step is refused as the state changed.
func TestBatchRacesQuietStep(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	e.receiver("a", "store", 11112)
	for round := range 100 {
		r, _, err := e.p().OpenReception(e.ctx, "a", cUID(1, round), e.id("rc"), cT0)
		e.ok("OpenReception", err)
		var batch, step error
		atOnce(2, func(i int) {
			if i == 0 {
				_, batch = e.stores[0].AddReceivedFiles(e.ctx, []tenant.ReceivedFile{cFile(r, round, round)}, cT0.Add(time.Second))
			} else {
				_, step = e.stores[1].StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet,
					At: cT0.Add(time.Minute), LastFile: r.LastFile}, nil)
			}
		})
		switch {
		case batch == nil && errors.Is(step, tenant.ErrStateChanged):
		case step == nil && errors.Is(batch, tenant.ErrReceptionClosed):
		default:
			t.Fatalf("round %d: batch %v, step %v; want exactly one, the other refused by the port's rule", round, batch, step)
		}
	}
}

// TestClaimRacesEnd guards the order of a claim and the step that ends its
// reception, a hundred rounds: a claim of fifty instances and a Fail step
// at once leave no claim held by the ended reception, whichever comes
// first: the claim waits for the step and is refused, or the step waits
// for the claim and releases what it claimed.
func TestClaimRacesEnd(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	e.receiver("a", "store", 11112)
	for round := range 100 {
		study := cUID(1, round)
		r, fs := e.identified("a", study, "store", "sub-01", "ses-"+study, func(r tenant.Reception) []tenant.ReceivedFile {
			out := make([]tenant.ReceivedFile, 50)
			for i := range out {
				out[i] = cFile(r, round, round*100+i)
			}
			return out
		})
		claims := make([]tenant.InstanceClaim, len(fs))
		for i, f := range fs {
			claims[i] = claimOfFile(f)
		}
		var claim, end error
		atOnce(2, func(i int) {
			if i == 0 {
				_, claim = e.stores[0].ClaimInstances(e.ctx, r.ID, claims, cT0, nil)
			} else {
				_, end = e.stores[1].StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Identified, Event: reception.Fail,
					At: cT0, Failure: "storage unreachable"}, nil)
			}
		})
		e.ok("the Fail step", end)
		if claim != nil && !errors.Is(claim, tenant.ErrPrecondition) {
			t.Fatalf("round %d: the claim: %v, want success or a precondition failure", round, claim)
		}
		if n := e.scalar(`SELECT count(*) FROM instances WHERE reception_id = $1 AND state = 'claimed'`, r.ID); n != 0 {
			t.Fatalf("round %d: %d claims held by a reception that ended", round, n)
		}
	}
}

// TestConfigChangesAtOnce guards the rules across rows of configuration with
// changes made at once through two stores, sixty rounds: two storages in one
// directory, two receivers on one port, and a storage's removal with a
// receiver added to archive to it, never both succeed.
func TestConfigChangesAtOnce(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	for round := range 60 {
		var errs [2]error
		var what string
		switch round % 3 {
		case 0:
			what = "two storages in one directory"
			atOnce(2, func(i int) {
				_, errs[i] = e.stores[i].AddStorage(e.ctx, tenant.Storage{Name: fmt.Sprintf("d%03d-%d", round, i), Type: tenant.StorageDirectory,
					Directory: tenant.DirectoryStorage{Root: "fast", Path: fmt.Sprintf("same%03d", round)}}, cT0, e.audit(), nil)
			})
		case 1:
			what = "two receivers on one port"
			atOnce(2, func(i int) {
				_, errs[i] = e.stores[i].AddReceiver(e.ctx, tenant.Receiver{Name: fmt.Sprintf("p%03d-%d", round, i), Type: tenant.ReceiverDIMSE,
					Storage: "store", DIMSE: tenant.DIMSEReceiver{Port: 20000 + round, AETitle: "ARCHIVE"},
					QuietPeriod: tenant.DefaultQuietPeriod, MaxOpenTime: tenant.DefaultMaxOpenTime, MaxFileBytes: tenant.DefaultMaxFileBytes}, cT0, e.audit(), nil)
			})
		case 2:
			what = "a storage removed and a receiver added on it"
			name := fmt.Sprintf("gone%03d", round)
			e.storage(name)
			atOnce(2, func(i int) {
				if i == 0 {
					_, errs[0] = e.stores[0].RemoveStorage(e.ctx, name, e.audit(), nil)
					return
				}
				_, errs[1] = e.stores[1].AddReceiver(e.ctx, tenant.Receiver{Name: fmt.Sprintf("on%03d", round), Type: tenant.ReceiverDIMSE,
					Storage: name, DIMSE: tenant.DIMSEReceiver{Port: 30000 + round, AETitle: "ARCHIVE"},
					QuietPeriod: tenant.DefaultQuietPeriod, MaxOpenTime: tenant.DefaultMaxOpenTime, MaxFileBytes: tenant.DefaultMaxFileBytes}, cT0, e.audit(), nil)
			})
		}
		for _, err := range errs {
			if err != nil && !errors.Is(err, tenant.ErrPrecondition) {
				t.Fatalf("round %d: %s: %v, want success or a precondition failure", round, what, err)
			}
		}
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("round %d: %s: %v and %v; want exactly one to succeed", round, what, errs[0], errs[1])
		}
	}
}

// TestClaimWorkSkipsHeldUnit guards that a claim never waits for a unit
// another transaction holds: with the earliest due unit's row locked by a
// transaction that stays open, a claim returns the next unit at once.
func TestClaimWorkSkipsHeldUnit(t *testing.T) {
	e := newCenv(t, 1, 4)
	for _, id := range []string{"early", "late"} {
		due := cT0
		if id == "late" {
			due = cT0.Add(time.Second)
		}
		_, _, err := e.p().EnqueueWork(e.ctx, tenant.Work{ID: id, Kind: "archive", Due: due}, cT0)
		e.ok("EnqueueWork", err)
	}
	tx, err := e.db.Begin(e.ctx)
	e.ok("Begin", err)
	defer func() { _ = tx.Rollback(e.ctx) }()
	if _, err := tx.Exec(e.ctx, `SELECT 1 FROM work WHERE id = 'early' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()
	w, err := e.p().ClaimWork(ctx, "archive", "worker-1", cT0.Add(time.Minute), time.Minute)
	if err != nil || w.ID != "late" {
		t.Fatalf("a claim with the earliest unit held = %s, %v; want the next unit at once", w.ID, err)
	}
}

// TestReleaseRacesQuietStep guards a release into a reception that is
// receiving against the reception's quiet step, a hundred rounds: a release
// of a held file of the study and the quiet step at once never leave the
// file in the reception the step closed. Either the release comes first,
// and the step decided from the last file before it is refused, or the
// step comes first, and the file joins the study's next arrival.
func TestReleaseRacesQuietStep(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	e.receiver("a", "store", 11112)
	for round := range 100 {
		r, _, err := e.p().OpenReception(e.ctx, "a", cUID(1, round), e.id("rc"), cT0)
		e.ok("OpenReception", err)
		f := cFile(r, round, round)
		q := tenant.QuarantinedFile{ID: e.id("q"), Reason: tenant.QuarantineBadLabel,
			Details: map[string]string{"label": "subject", "fault": tenant.FaultEmpty}, Receiver: "a",
			SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SOPClassUID: f.SOPClassUID,
			TransferSyntaxUID: f.TransferSyntaxUID, SHA256: f.SHA256, Size: f.Size, Incoming: "quarantine/" + f.SOPInstanceUID,
			ContentKept: true, Quarantined: cT0}
		_, err = e.p().HoldFiles(e.ctx, []tenant.QuarantinedFile{q})
		e.ok("HoldFiles", err)
		var released tenant.QuarantinedFile
		var release, step error
		atOnce(2, func(i int) {
			if i == 0 {
				released, release = e.stores[0].ReleaseQuarantined(e.ctx, q.ID, tenant.ReleaseAsIs, "", e.id("rc"), cT0.Add(time.Second), e.audit(), nil)
			} else {
				_, step = e.stores[1].StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet,
					At: cT0.Add(time.Minute), LastFile: r.LastFile}, nil)
			}
		})
		e.ok("ReleaseQuarantined", release)
		if step != nil && !errors.Is(step, tenant.ErrStateChanged) {
			t.Fatalf("round %d: the quiet step: %v, want success or the state changed", round, step)
		}
		if step == nil && released.ReleaseReception == r.ID {
			t.Fatalf("round %d: the released file joined the reception the quiet step closed", round)
		}
		if step != nil && released.ReleaseReception != r.ID {
			t.Fatalf("round %d: the quiet step was refused, yet the file joined %s, not %s", round, released.ReleaseReception, r.ID)
		}
	}
}

// TestCommitRacesEnd guards the order of a scan's commit and the step that
// ends its reception, a hundred rounds: a commit of a hundred claimed
// instances and a Fail step at once leave either the instances held (the
// commit came first) or released (the step came first, and the commit is
// refused), no claim of the ended reception, the counts equal to the rows,
// and no deadlock at all: a commit takes the reception's row first, as the
// step does, so neither holds a row the other waits for.
func TestCommitRacesEnd(t *testing.T) {
	e := newCenv(t, 2, 6)
	e.storage("store")
	e.receiver("a", "store", 11112)
	for round := range 100 {
		study := cUID(1, round)
		r, fs := e.identified("a", study, "store", "sub-01", "ses-"+study, func(r tenant.Reception) []tenant.ReceivedFile {
			out := make([]tenant.ReceivedFile, 100)
			for i := range out {
				out[i] = cFile(r, round, round*1000+i)
			}
			return out
		})
		claims := make([]tenant.InstanceClaim, len(fs))
		for i, f := range fs {
			claims[i] = claimOfFile(f)
		}
		_, err := e.p().ClaimInstances(e.ctx, r.ID, claims, cT0, nil)
		e.ok("ClaimInstances", err)
		var commit, end error
		atOnce(2, func(i int) {
			if i == 0 {
				_, commit = e.stores[0].CommitScan(e.ctx, scanCommit(r, "sub-01", "ses-"+study, "scan-1", "store", fs))
			} else {
				_, end = e.stores[1].StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Identified, Event: reception.Fail,
					At: cT0, Failure: "storage unreachable"}, nil)
			}
		})
		e.ok("the Fail step", end)
		if commit != nil && !errors.Is(commit, tenant.ErrPrecondition) {
			t.Fatalf("round %d: the commit: %v, want success or a precondition failure", round, commit)
		}
		held := e.scalar(`SELECT count(*) FROM instances WHERE reception_id = $1 AND state = 'held'`, r.ID)
		if (commit == nil && held != 100) || (commit != nil && held != 0) {
			t.Fatalf("round %d: commit %v, and %d instances held", round, commit, held)
		}
	}
	if n := e.deadlocks(); n != 0 {
		t.Errorf("%d changes were run again after a deadlock", n)
	}
	e.checkTotals("after the commits")
}
