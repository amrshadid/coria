package tenanttest

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// enqueue queues a unit of kind "reception" due at the time given.
func (e *penv) enqueue(id string, due time.Time) tenant.Work {
	e.Helper()
	w, created, err := e.p.EnqueueWork(e.ctx, tenant.Work{ID: id, Kind: "reception", Subject: "rc-" + id, Due: due}, T0)
	e.ok("EnqueueWork", err)
	if !created {
		e.Fatalf("EnqueueWork of %s queued nothing", id)
	}
	return w
}

// testWorkClaim guards claiming: nothing before it is due; the earliest due
// first; a claim takes only its kind; the claim's lease, owner and attempt
// as stored; a unit leased is not claimed again while its lease holds.
func testWorkClaim(e *penv) {
	queued := e.enqueue("w10", at(10))
	if queued.State != tenant.WorkQueued || queued.Attempt != 0 || queued.MaxAttempts != tenant.DefaultWorkAttempts || !queued.Due.Equal(at(10)) {
		e.Errorf("a queued unit reads %+v", queued)
	}
	e.enqueue("w05", at(5))
	e.enqueue("w20", at(20))
	_, _, err := e.p.EnqueueWork(e.ctx, tenant.Work{ID: "other", Kind: "scrub", Due: at(0)}, T0)
	e.ok("EnqueueWork", err)
	_, err = e.p.ClaimWork(e.ctx, "reception", "worker-1", at(1), time.Minute)
	e.wantKind("ClaimWork before anything is due", err, fault.NotFound)
	first, err := e.p.ClaimWork(e.ctx, "reception", "worker-1", at(10), time.Minute)
	e.ok("ClaimWork", err)
	if first.ID != "w05" || first.State != tenant.WorkLeased || first.Attempt != 1 || first.Owner != "worker-1" || !first.Due.Equal(at(70)) {
		e.Errorf("the first claim = %+v, want w05 leased to worker-1 until 70 s, attempt 1", first)
	}
	second, err := e.p.ClaimWork(e.ctx, "reception", "worker-2", at(10), time.Minute)
	e.ok("ClaimWork", err)
	if second.ID != "w10" {
		e.Errorf("the second claim = %s, want w10", second.ID)
	}
	_, err = e.p.ClaimWork(e.ctx, "reception", "worker-3", at(15), time.Minute)
	e.wantKind("ClaimWork with the units due leased", err, fault.NotFound)
	other, err := e.p.ClaimWork(e.ctx, "scrub", "worker-3", at(15), time.Minute)
	e.ok("ClaimWork of another kind", err)
	if other.ID != "other" {
		e.Errorf("a claim of another kind took %s", other.ID)
	}
	_, _, err = e.p.EnqueueWork(e.ctx, tenant.Work{ID: "w10", Kind: "reception", Due: at(0)}, T0)
	e.wantKind("EnqueueWork with an id taken", err, fault.Conflict)
	_, err = e.p.Work(e.ctx, "missing")
	e.wantKind("Work not there", err, fault.NotFound)
}

// testWorkUniqueKey guards that a unique key queues its work once while it
// is queued or leased, and is free again once the unit is done.
func testWorkUniqueKey(e *penv) {
	w := tenant.Work{ID: "u1", Kind: "reception", Subject: "rc-1", UniqueKey: "reception/rc-1/quiet", Due: at(5)}
	first, created, err := e.p.EnqueueWork(e.ctx, w, T0)
	e.ok("EnqueueWork", err)
	w.ID = "u2"
	again, created2, err := e.p.EnqueueWork(e.ctx, w, T0)
	e.ok("EnqueueWork with the key queued", err)
	if !created || created2 || again.ID != "u1" || first.UniqueKey != w.UniqueKey {
		e.Errorf("a key queued twice: %v %v, answered %s", created, created2, again.ID)
	}
	claimed, err := e.p.ClaimWork(e.ctx, "reception", "worker-1", at(5), time.Minute)
	e.ok("ClaimWork", err)
	w.ID = "u3"
	if leased, created, err := e.p.EnqueueWork(e.ctx, w, T0); err != nil || created || leased.ID != "u1" {
		e.Errorf("EnqueueWork with the key leased = %s, %v, %v", leased.ID, created, err)
	}
	e.ok("CompleteWork", e.p.CompleteWork(e.ctx, claimed.Fence(), at(6), nil))
	w.ID = "u4"
	if fresh, created, err := e.p.EnqueueWork(e.ctx, w, T0); err != nil || !created || fresh.ID != "u4" {
		e.Errorf("EnqueueWork with the key done = %s, %v, %v; want a new unit", fresh.ID, created, err)
	}
}

// testWorkFencing guards the fence: a worker renews while it holds the
// lease; once the lease ends another worker claims the unit with a new
// attempt, and then every write of the first (renew, complete, fail, a
// step) changes nothing and is ErrLeaseLost; the unit is as the second left
// it.
func testWorkFencing(e *penv) {
	e.enqueue("w1", at(0))
	first, err := e.p.ClaimWork(e.ctx, "reception", "worker-1", at(0), time.Minute)
	e.ok("ClaimWork", err)
	renewed, err := e.p.RenewWork(e.ctx, first.Fence(), at(30), time.Minute)
	e.ok("RenewWork", err)
	if !renewed.Due.Equal(at(90)) || renewed.Attempt != 1 {
		e.Errorf("a renewal reads %+v", renewed)
	}
	_, err = e.p.ClaimWork(e.ctx, "reception", "worker-2", at(80), time.Minute)
	e.wantKind("ClaimWork while a renewed lease holds", err, fault.NotFound)
	second, err := e.p.ClaimWork(e.ctx, "reception", "worker-2", at(90), time.Minute)
	e.ok("ClaimWork after the lease ended", err)
	if second.ID != "w1" || second.Attempt != 2 || second.Owner != "worker-2" {
		e.Errorf("the takeover = %+v, want w1, attempt 2, worker-2", second)
	}
	_, err = e.p.RenewWork(e.ctx, first.Fence(), at(95), time.Minute)
	e.wantIs("RenewWork after the lease was taken over", err, tenant.ErrLeaseLost, fault.Conflict)
	e.wantIs("CompleteWork after the lease was taken over", e.p.CompleteWork(e.ctx, first.Fence(), at(95), nil), tenant.ErrLeaseLost, fault.Conflict)
	_, err = e.p.FailWork(e.ctx, first.Fence(), "late", at(95))
	e.wantIs("FailWork after the lease was taken over", err, tenant.ErrLeaseLost, fault.Conflict)
	now, err := e.p.Work(e.ctx, "w1")
	e.ok("Work", err)
	if now.State != tenant.WorkLeased || now.Attempt != 2 || now.Owner != "worker-2" || !now.Due.Equal(at(150)) || now.LastError != "" {
		e.Errorf("writes under a lost lease changed the unit: %+v", now)
	}
	e.ok("CompleteWork by the worker that holds the lease", e.p.CompleteWork(e.ctx, second.Fence(), at(100), nil))
	e.wantIs("CompleteWork of a unit done", e.p.CompleteWork(e.ctx, second.Fence(), at(101), nil), tenant.ErrLeaseLost, fault.Conflict)
	if done, _ := e.p.Work(e.ctx, "w1"); done.State != tenant.WorkDone {
		e.Errorf("the unit is %s, want done", done.State)
	}
}

// testWorkRetries guards failed attempts: a unit is due again after a delay
// that grows with each attempt, and after its last allowed attempt is held
// as failed with its reason; a unit whose last attempt's lease ended is held
// as failed by the next claim, which takes another unit.
func testWorkRetries(e *penv) {
	if d1, d2, d3, dn := tenant.RetryDelay(1), tenant.RetryDelay(2), tenant.RetryDelay(3), tenant.RetryDelay(50); d1 != tenant.RetryBase || d2 != 2*d1 || d3 != 2*d2 || dn != tenant.RetryMax {
		e.Errorf("RetryDelay = %v %v %v ... %v", d1, d2, d3, dn)
	}
	_, _, err := e.p.EnqueueWork(e.ctx, tenant.Work{ID: "r1", Kind: "reception", Due: at(0), MaxAttempts: 3}, T0)
	e.ok("EnqueueWork", err)
	now := at(0)
	for attempt := 1; attempt <= 3; attempt++ {
		w, err := e.p.ClaimWork(e.ctx, "reception", "worker-1", now, time.Minute)
		e.ok(fmt.Sprintf("ClaimWork, attempt %d", attempt), err)
		if w.Attempt != attempt {
			e.Errorf("attempt %d claimed as %d", attempt, w.Attempt)
		}
		failed, err := e.p.FailWork(e.ctx, w.Fence(), "storage unreachable", now.Add(time.Second))
		e.ok("FailWork", err)
		if attempt < 3 {
			wantDue := now.Add(time.Second + tenant.RetryDelay(attempt))
			if failed.State != tenant.WorkQueued || !failed.Due.Equal(wantDue) || failed.LastError != "storage unreachable" || failed.Owner != "" {
				e.Errorf("after attempt %d: %+v, want queued until %v", attempt, failed, wantDue)
			}
			_, err = e.p.ClaimWork(e.ctx, "reception", "worker-1", wantDue.Add(-time.Microsecond), time.Minute)
			e.wantKind("ClaimWork before the retry is due", err, fault.NotFound)
			now = wantDue
		} else if failed.State != tenant.WorkFailed {
			e.Errorf("after the last attempt the unit is %s, want failed", failed.State)
		}
	}
	_, err = e.p.ClaimWork(e.ctx, "reception", "worker-1", at(100000), time.Minute)
	e.wantKind("ClaimWork with only a failed unit", err, fault.NotFound)

	_, _, err = e.p.EnqueueWork(e.ctx, tenant.Work{ID: "once", Kind: "reception", Due: at(0), MaxAttempts: 1}, T0)
	e.ok("EnqueueWork", err)
	_, err = e.p.ClaimWork(e.ctx, "reception", "worker-1", at(0), time.Minute)
	e.ok("ClaimWork", err)
	e.enqueue("next", at(65))
	got, err := e.p.ClaimWork(e.ctx, "reception", "worker-2", at(66), time.Minute)
	e.ok("ClaimWork after the last attempt's lease ended", err)
	if got.ID != "next" {
		e.Errorf("the claim took %s, want next", got.ID)
	}
	if once, _ := e.p.Work(e.ctx, "once"); once.State != tenant.WorkFailed || once.LastError == "" {
		e.Errorf("a unit whose last lease ended reads %+v, want failed with a reason", once)
	}
}

// testWorkConcurrentClaims guards that workers claiming at once get
// different units: every unit is claimed exactly once.
func testWorkConcurrentClaims(e *penv) {
	const units, workers = 40, 8
	for i := range units {
		e.enqueue(fmt.Sprintf("c%02d", i), at(i%5))
	}
	claimed := make([][]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for k := range workers {
		wg.Go(func() {
			<-start
			for {
				w, err := e.p.ClaimWork(e.ctx, "reception", fmt.Sprintf("worker-%d", k), at(10), time.Minute)
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
	}
	close(start)
	wg.Wait()
	seen := map[string]int{}
	for k := range workers {
		e.ok("ClaimWork", errs[k])
		for _, id := range claimed[k] {
			seen[id]++
		}
	}
	if len(seen) != units {
		e.Errorf("%d units claimed, want %d", len(seen), units)
	}
	for id, n := range seen {
		if n != 1 {
			e.Errorf("%s was claimed %d times", id, n)
		}
	}
}

// testWorkChain guards completing a unit with its next one in the same
// transaction: the next is queued, once under its key, and a completion
// refused by its fence queues nothing.
func testWorkChain(e *penv) {
	e.enqueue("step1", at(0))
	w, err := e.p.ClaimWork(e.ctx, "reception", "worker-1", at(0), time.Minute)
	e.ok("ClaimWork", err)
	next := tenant.Work{ID: "step2", Kind: "reception", Subject: "rc-step", UniqueKey: "rc-step/next", Due: at(1)}
	e.wantIs("CompleteWork with a stale fence", e.p.CompleteWork(e.ctx, tenant.WorkFence{ID: "step1", Attempt: 9}, at(1), &next), tenant.ErrLeaseLost, fault.Conflict)
	if _, err := e.p.Work(e.ctx, "step2"); fault.KindOf(err) != fault.NotFound {
		e.Error("a completion refused by its fence queued its next unit")
	}
	e.ok("CompleteWork with next", e.p.CompleteWork(e.ctx, w.Fence(), at(1), &next))
	got, err := e.p.Work(e.ctx, "step2")
	e.ok("Work", err)
	if got.State != tenant.WorkQueued || !got.Due.Equal(at(1)) {
		e.Errorf("the next unit reads %+v", got)
	}
}

// testPipelineBounds guards every bound of the pipeline's operations: a
// batch, a list's limit, a lease, a reason, one past its bound is Invalid,
// and the bound itself is accepted.
func testPipelineBounds(e *penv) {
	e.addReceiver("a", "store", 11112)
	r := e.openReception("a", 1, at(1))
	var batch []tenant.ReceivedFile
	var queries []tenant.DuplicateQuery
	var held []tenant.QuarantinedFile
	for i := range tenant.MaxReceivedBatch {
		f := file(r, 1, i)
		batch = append(batch, f)
		queries = append(queries, tenant.DuplicateQuery{SOPInstanceUID: f.SOPInstanceUID, StudyUID: f.StudyUID, SeriesUID: f.SeriesUID, SHA256: f.SHA256})
		held = append(held, e.heldFile("a", 1, i))
	}
	n, err := e.p.AddReceivedFiles(e.ctx, batch, at(2))
	if err != nil || n != tenant.MaxReceivedBatch {
		e.Errorf("AddReceivedFiles of a batch at its bound = %d, %v", n, err)
	}
	_, err = e.p.AddReceivedFiles(e.ctx, append(batch, file(r, 1, 5000)), at(2))
	e.wantKind("AddReceivedFiles one over", err, fault.Invalid)
	_, err = e.p.CheckDuplicates(e.ctx, queries)
	e.ok("CheckDuplicates at its bound", err)
	_, err = e.p.CheckDuplicates(e.ctx, append(queries, queries[0]))
	e.wantKind("CheckDuplicates one over", err, fault.Invalid)
	_, err = e.p.HoldFiles(e.ctx, held)
	e.ok("HoldFiles at its bound", err)
	_, err = e.p.HoldFiles(e.ctx, append(held, e.heldFile("a", 1, 1)))
	e.wantKind("HoldFiles one over", err, fault.Invalid)
	for _, limit := range []int{0, master.MaxPageLimit + 1} {
		p := master.Page{Limit: limit}
		_, _, err1 := e.p.ListStorages(e.ctx, p)
		_, _, err2 := e.p.ListReceivers(e.ctx, p)
		_, _, err3 := e.p.ListReceptions(e.ctx, tenant.ReceptionFilter{}, p)
		_, _, err4 := e.p.ListQuarantined(e.ctx, tenant.QuarantineFilter{}, p)
		_, _, err5 := e.p.ReceivedFiles(e.ctx, r.ID, p)
		for i, err := range []error{err1, err2, err3, err4, err5} {
			e.wantKind(fmt.Sprintf("list %d with a limit of %d", i+1, limit), err, fault.Invalid)
		}
	}
	e.enqueue("lease", at(0))
	for _, lease := range []time.Duration{tenant.MinLease - 1, tenant.MaxLease + 1} {
		_, err := e.p.ClaimWork(e.ctx, "reception", "worker-1", at(0), lease)
		e.wantKind(fmt.Sprintf("ClaimWork with a lease of %v", lease), err, fault.Invalid)
	}
	_, err = e.p.ClaimWork(e.ctx, "reception", "worker-1", at(0), tenant.MaxLease)
	e.ok("ClaimWork with the longest lease", err)
	_, err = e.p.ClaimWork(e.ctx, "reception", "bad worker", at(0), time.Minute)
	e.wantKind("ClaimWork by a worker with a bad id", err, fault.Invalid)
	_, _, err = e.p.EnqueueWork(e.ctx, tenant.Work{ID: "x", Kind: "reception", Due: at(0), UniqueKey: strings.Repeat("k", tenant.MaxWorkTextLength+1)}, T0)
	e.wantKind("EnqueueWork with a key one byte over", err, fault.Invalid)
	_, err = e.p.FailWork(e.ctx, tenant.WorkFence{ID: "lease", Attempt: 1}, strings.Repeat("r", tenant.MaxWorkTextLength+1), at(1))
	e.wantKind("FailWork with a reason one byte over", err, fault.Invalid)
}

// testPipelineTenantsApart guards that two tenants' pipelines stay apart:
// each reads only its own storages and receptions, and a page token from one
// tenant's list is refused by the other's.
func testPipelineTenantsApart(e *penv) {
	other := e.open(e.T, OtherTenant)
	e.addReceiver("a", "store", 11112)
	e.addStorage("second")
	if _, err := other.Storage(e.ctx, "store"); fault.KindOf(err) != fault.NotFound {
		e.Errorf("another tenant reads this tenant's storage (%v)", err)
	}
	if list, _, err := other.ListReceptions(e.ctx, tenant.ReceptionFilter{}, master.Page{Limit: 10}); err != nil || len(list) != 0 {
		e.Errorf("another tenant lists %d receptions (%v)", len(list), err)
	}
	_, cursor, err := e.p.ListStorages(e.ctx, master.Page{Limit: 1})
	e.ok("ListStorages", err)
	_, _, err = other.ListStorages(e.ctx, master.Page{Limit: 1, Cursor: cursor})
	e.wantKind("another tenant's ListStorages with this tenant's cursor", err, fault.Invalid)
}

// testClaimsShared guards that workers claiming through two instances at
// once, as two processes would, get different units.
func testClaimsShared(e *penv, other tenant.Pipeline) {
	const units = 30
	for i := range units {
		e.enqueue(fmt.Sprintf("s%02d", i), at(0))
	}
	stores := []tenant.Pipeline{e.p, other}
	claimed := make([][]string, 4)
	errs := make([]error, 4)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for k := range 4 {
		wg.Go(func() {
			<-start
			for {
				w, err := stores[k%2].ClaimWork(e.ctx, "reception", fmt.Sprintf("worker-%d", k), at(1), time.Minute)
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
	}
	close(start)
	wg.Wait()
	seen := map[string]int{}
	for k := range claimed {
		e.ok("ClaimWork", errs[k])
		for _, id := range claimed[k] {
			seen[id]++
		}
	}
	if len(seen) != units {
		e.Errorf("%d units claimed across two instances, want %d", len(seen), units)
	}
	for id, n := range seen {
		if n != 1 {
			e.Errorf("%s was claimed %d times across two instances", id, n)
		}
	}
}

// testOpenShared guards that opens of one study at one receiver through two
// instances at once open one reception.
func testOpenShared(e *penv, other tenant.Pipeline) {
	e.addReceiver("a", "store", 11112)
	stores := []tenant.Pipeline{e.p, other}
	ids := make([]string, 8)
	errs := make([]error, 8)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range ids {
		wg.Go(func() {
			<-start
			r, _, err := stores[i%2].OpenReception(e.ctx, "a", uidOf(1, 1), fmt.Sprintf("o%d", i), at(1))
			ids[i], errs[i] = r.ID, err
		})
	}
	close(start)
	wg.Wait()
	for i := range ids {
		e.ok("OpenReception", errs[i])
		if ids[i] != ids[0] {
			e.Errorf("opens through two instances made two receptions: %s and %s", ids[0], ids[i])
		}
	}
}

// testBatchShared guards that a batch written through one instance and
// written again through the other, as after a crash and a restart, changes
// nothing the second time.
func testBatchShared(e *penv, other tenant.Pipeline) {
	e.addReceiver("a", "store", 11112)
	r := e.openReception("a", 1, at(1))
	batch := []tenant.ReceivedFile{file(r, 1, 1), file(r, 1, 2)}
	_, err := e.p.AddReceivedFiles(e.ctx, batch, at(2))
	e.ok("AddReceivedFiles", err)
	n, err := other.AddReceivedFiles(e.ctx, batch, at(3))
	e.ok("AddReceivedFiles on the other instance", err)
	got, _, _ := other.Reception(e.ctx, r.ID)
	if n != 0 || got.Counts.FilesReceived != 2 {
		e.Errorf("a batch written again on the other instance: %d new, %d received", n, got.Counts.FilesReceived)
	}
}

// testInstanceClaimsShared guards that two receptions claiming the same
// instances through two instances of the store at once, as two processes
// would, never both claim one: each UID is claimed now by exactly one, and
// the other is told it is Claimed by that one. On SQLite the two claims are
// ordered by its one writer; a Postgres run shows it with the two
// transactions truly at once, where the second insert waits for the first.
func testInstanceClaimsShared(e *penv, other tenant.Pipeline) {
	e.addReceiver("a", "store", 11112)
	e.addReceiver("b", "store", 11113)
	r1, _ := e.identified("a", 1)
	r2, _ := e.identified("b", 1)
	var claims []tenant.InstanceClaim
	for n := range 50 {
		claims = append(claims, claimOf(file(r1, 1, n)))
	}
	stores := []tenant.Pipeline{e.p, other}
	recs := []tenant.Reception{r1, r2}
	answers := make([][]tenant.Duplicate, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 2 {
		wg.Go(func() {
			<-start
			answers[i], errs[i] = stores[i].ClaimInstances(e.ctx, recs[i].ID, claims, at(20), nil)
		})
	}
	close(start)
	wg.Wait()
	for i := range 2 {
		e.ok("ClaimInstances", errs[i])
	}
	for n := range claims {
		a, b := answers[0][n].Kind, answers[1][n].Kind
		oneEach := (a == tenant.ClaimedNow && b == tenant.Claimed) || (a == tenant.Claimed && b == tenant.ClaimedNow)
		if !oneEach {
			e.Errorf("UID %d claimed at once by two receptions: %v and %v, want one claimed now and the other told claimed", n, a, b)
		}
	}
}
