package tenanttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/core/reception"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// EventStore is a store with the pipeline and the event feed.
type EventStore interface {
	tenant.Pipeline
	tenant.Events
}

// OpenEvents is Open for a store that implements tenant.Events.
type OpenEvents func(tb testing.TB, tenantID string) EventStore

// OpenEventsPair is OpenPair for stores that implement tenant.Events: two
// instances on one database.
type OpenEventsPair func(tb testing.TB, tenantID string) (first, second EventStore)

// RunEvents runs the contract suite's event groups (tenant.Events) against
// the stores open makes, each test on its own store, in parallel. A driver
// that implements Events calls it as well as RunPipeline.
func RunEvents(t *testing.T, open OpenEvents) {
	tests := []struct {
		name string
		fn   func(*eenv)
	}{
		{"EventsWithTheirChanges", testEventsWithTheirChanges},
		{"StepEventBothOrNeither", testStepEventBothOrNeither},
		{"EventPayloadsHoldNoUID", testEventPayloadsHoldNoUID},
		{"EventPaging", testEventPaging},
		{"EventFilterAdvances", testEventFilterAdvances},
		{"EventAppendOnce", testEventAppendOnce},
		{"EventCursors", testEventCursors},
		{"EventsExpire", testEventsExpire},
		{"PruneUnderReader", testPruneUnderReader},
		{"StatusSnapshot", testStatusSnapshot},
		{"EventBounds", testEventBounds},
		{"LateCommit", func(e *eenv) { testLateCommit(e, e.ev) }},
		{"PruneFollowsFeedOrder", testPruneFollowsFeedOrder},
		{"SnapshotDividesTheFeed", testSnapshotDividesTheFeed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := open(t, SuiteTenant)
			popen := func(tb testing.TB, id string) tenant.Pipeline { return open(tb, id) }
			tt.fn(&eenv{penv: &penv{env: &env{T: t, s: s, ctx: context.Background()}, p: s, open: popen}, ev: s, openEv: open})
		})
	}
}

// RunEventsShared runs the event tests that need two instances of the store
// on one database.
func RunEventsShared(t *testing.T, open OpenEventsPair) {
	t.Run("LateCommitAcrossInstances", func(t *testing.T) {
		t.Parallel()
		first, second := open(t, SuiteTenant)
		e := &eenv{penv: &penv{env: &env{T: t, s: first, ctx: context.Background()}, p: first}, ev: first}
		testLateCommit(e, second)
	})
}

// eenv is an event test's store and its helpers; it has penv's too.
type eenv struct {
	*penv
	ev     EventStore
	openEv OpenEvents
}

// read reads one page, failing the test on an error.
func (e *eenv) read(after string, limit int, types ...tenant.EventType) tenant.EventPage {
	e.Helper()
	p, err := e.ev.ReadEvents(e.ctx, after, types, limit)
	e.ok("ReadEvents", err)
	if p.Cursor == "" {
		e.Fatal("a page carries no cursor")
	}
	return p
}

// readAll reads pages of limit from after until a short page, and returns
// every event and the cursor after the last page.
func (e *eenv) readAll(after string, limit int, types ...tenant.EventType) ([]tenant.Event, string) {
	e.Helper()
	var out []tenant.Event
	for range 100000 {
		p := e.read(after, limit, types...)
		out = append(out, p.Events...)
		after = p.Cursor
		if len(p.Events) < limit {
			return out, after
		}
	}
	e.Fatal("a read of the feed did not end")
	return nil, ""
}

// now returns the feed's current cursor.
func (e *eenv) now() string {
	e.Helper()
	c, err := e.ev.EventCursor(e.ctx)
	e.ok("EventCursor", err)
	return c
}

// health returns a receiver health event at second s.
func (e *eenv) health(receiver string, s int) tenant.EventRecord {
	e.Helper()
	ev, err := tenant.NewEvent(&tenant.ReceiverHealthChanged{Receiver: receiver, State: tenant.ReceiverDown,
		DownReason: "address_in_use", Detail: "the port is taken by another program", Since: at(s)}, at(s))
	e.ok("NewEvent", err)
	return ev
}

// space returns a storage space event at second s.
func (e *eenv) space(storage string, s int) tenant.EventRecord {
	e.Helper()
	free := int64(s)
	ev, err := tenant.NewEvent(&tenant.StorageSpaceChanged{Storage: storage, State: tenant.StorageNearlyFull,
		UsedBytes: 1 << 30, FreeBytes: &free}, at(s))
	e.ok("NewEvent", err)
	return ev
}

// appendAll appends events with fresh keys, failing the test on an error.
func (e *eenv) appendAll(events ...tenant.EventRecord) {
	e.Helper()
	e.appendTo(e.ev, events...)
}

// appendTo appends events to store s with fresh keys.
func (e *eenv) appendTo(s EventStore, events ...tenant.EventRecord) {
	e.Helper()
	batch := make([]tenant.EventAppend, len(events))
	for i, ev := range events {
		batch[i] = tenant.EventAppend{Key: e.id("ev"), Event: ev}
	}
	n, err := s.AppendEvents(e.ctx, batch)
	e.ok("AppendEvents", err)
	if n != len(events) {
		e.Fatalf("AppendEvents wrote %d of %d new events", n, len(events))
	}
}

// types returns the types of events, in order.
func types(events []tenant.Event) []tenant.EventType {
	out := make([]tenant.EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

// stepOf returns an event's reception step payload, failing the test when
// it carries another.
func (e *eenv) stepOf(ev tenant.Event) *tenant.ReceptionStepReached {
	e.Helper()
	p, ok := ev.Payload.(*tenant.ReceptionStepReached)
	if !ok {
		e.Fatalf("an event of type %s carries %T, want a reception step", ev.Type, ev.Payload)
	}
	return p
}

// testEventsWithTheirChanges guards that each change of the pipeline that
// the feed reports writes its event, with what the contract's message
// holds: an open (receiving), each step with the step it left, whether the
// caller gives a hook or not, held files with the totals after each, a
// rejection and a release with the totals after them, a release that opens
// a reception, a failure and its retry, and a unit of work that gives up.
func testEventsWithTheirChanges(e *eenv) {
	e.addReceiver("a", "store", 11112)
	start := e.now()
	r := e.openReception("a", 1, at(10))
	_, err := e.p.AddReceivedFiles(e.ctx, []tenant.ReceivedFile{file(r, 1, 1), file(r, 1, 2)}, at(11))
	e.ok("AddReceivedFiles", err)
	r, err = e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet, At: at(70)},
		func(context.Context, tenant.Reception, tenant.Reception) error { return nil })
	e.ok("StepReception with a hook", err)
	r, err = e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Complete, Event: reception.Identify, At: at(80),
		SubjectLabel: "sub-01", SessionLabel: "ses-01", Storage: "store"}, nil)
	e.ok("StepReception identify, without a hook", err)
	_, err = e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Identified, Event: reception.Fail, At: at(90),
		Failure: "storage unreachable"}, nil)
	e.ok("StepReception fail", err)

	q1, q2 := e.heldFile("a", 100, 100), e.heldFile("a", 200, 101)
	q2.ReceptionID = r.ID
	_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{q1, q2})
	e.ok("HoldFiles", err)
	_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{q1})
	e.ok("HoldFiles again", err)
	_, err = e.p.RejectQuarantined(e.ctx, q1.ID, "not ours", at(110), e.audit(), nil)
	e.ok("RejectQuarantined", err)
	_, err = e.p.RejectQuarantined(e.ctx, q1.ID, "not ours", at(111), e.audit(), nil)
	e.ok("RejectQuarantined again", err)
	d := e.differentFile("a", "", 7)
	d.SOPInstanceUID = uidOf(3, 900) // no instance holds it, so it may be released
	_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{d})
	e.ok("HoldFiles", err)
	released, err := e.p.ReleaseQuarantined(e.ctx, d.ID, tenant.ReleaseAsIs, "", e.id("rc"), at(120), e.audit(), nil)
	e.ok("ReleaseQuarantined", err)

	failed, retry, err := e.p.RetryReception(e.ctx, r.ID, e.id("rc"), "", at(130), e.audit(), nil)
	e.ok("RetryReception", err)
	_, _, err = e.p.RetryReception(e.ctx, r.ID, e.id("rc"), "", at(131), e.audit(), nil)
	e.ok("RetryReception again", err)
	_, _, err = e.p.EnqueueWork(e.ctx, tenant.Work{ID: "archive-1", Kind: "archive", ReceptionID: retry.ID, Due: at(0), MaxAttempts: 1}, T0)
	e.ok("EnqueueWork", err)
	w, err := e.p.ClaimWork(e.ctx, "archive", "worker-1", at(140), time.Minute)
	e.ok("ClaimWork", err)
	_, err = e.p.FailWork(e.ctx, w.Fence(), "storage unreachable", at(141))
	e.ok("FailWork, the last attempt", err)
	_, err = e.p.DiscardReception(e.ctx, retry.ID, "gone", at(150), e.audit(), nil)
	e.ok("DiscardReception", err)

	got, end := e.readAll(start, 3)
	want := []tenant.EventType{
		tenant.EventReceptionStep, tenant.EventReceptionStep, tenant.EventReceptionStep, tenant.EventReceptionStep, // open, quiet, identify, fail
		tenant.EventFileQuarantined, tenant.EventFileQuarantined, tenant.EventQuarantineDecided, // hold, hold, reject
		tenant.EventFileQuarantined, tenant.EventReceptionStep, tenant.EventQuarantineDecided, // hold, release opens, release
		tenant.EventReceptionStep, tenant.EventReceptionStep, // retry, gives up
	}
	if fmt.Sprint(types(got)) != fmt.Sprint(want) {
		e.Fatalf("the events are %v, want %v", types(got), want)
	}
	if len(e.read(end, 10).Events) != 0 {
		e.Error("a read after the last cursor answered events")
	}
	open, quiet, ident, fail := e.stepOf(got[0]), e.stepOf(got[1]), e.stepOf(got[2]), e.stepOf(got[3])
	if open.Reception != r.ID || open.Receiver != "a" || open.Arrival != 1 || open.Step != reception.Receiving ||
		open.PreviousStep != "" || open.Counts != (tenant.EventCounts{}) || !got[0].Time.Equal(at(10)) {
		e.Errorf("the open's event = %+v at %v", open, got[0].Time)
	}
	if quiet.PreviousStep != reception.Receiving || ident.PreviousStep != reception.Complete || fail.PreviousStep != reception.Identified ||
		fail.Step != reception.Failed || !got[3].Time.Equal(at(90)) {
		e.Errorf("the steps left are %q, %q and %q, and the last reached %s: want receiving, complete, identified, then failed",
			quiet.PreviousStep, ident.PreviousStep, fail.PreviousStep, fail.Step)
	}
	if quiet.Step != reception.Complete || quiet.Counts.FilesReceived != 2 || quiet.Counts.BytesReceived != 2003 ||
		quiet.SubjectLabel != "" || !got[1].Time.Equal(at(70)) {
		e.Errorf("the quiet step's event = %+v at %v, want complete with 2 files of 2003 bytes", quiet, got[1].Time)
	}
	if ident.Step != reception.Identified || ident.SubjectLabel != "sub-01" || ident.SessionLabel != "ses-01" {
		e.Errorf("the identify step's event = %+v", ident)
	}
	for i, wantFiles := range map[int]int64{4: 1, 5: 2} {
		p, ok := got[i].Payload.(*tenant.FileQuarantined)
		if !ok || p.HeldFiles != wantFiles {
			e.Errorf("held file event %d = %+v, want %d files held", i, got[i].Payload, wantFiles)
		}
	}
	if p := got[5].Payload.(*tenant.FileQuarantined); p.File != q2.ID || p.Reception != r.ID || p.Receiver != "a" ||
		p.Reason != tenant.QuarantineNotDICOM || p.SizeBytes != 200 || p.HeldBytes != 300 || !got[5].Time.Equal(at(101)) {
		e.Errorf("the second held file's event = %+v at %v", p, got[5].Time)
	}
	if p := got[6].Payload.(*tenant.QuarantineDecided); p.File != q1.ID || p.State != tenant.QuarantineRejected ||
		p.ReleaseMode != "" || p.HeldFiles != 1 || p.HeldBytes != 200 || !got[6].Time.Equal(at(110)) {
		e.Errorf("the rejection's event = %+v at %v, want 1 file of 200 bytes held after it", p, got[6].Time)
	}
	if p := e.stepOf(got[8]); p.Reception != released.ReleaseReception || p.Step != reception.Receiving {
		e.Errorf("the reception the release opened has event %+v, want %s receiving", p, released.ReleaseReception)
	}
	if p := got[9].Payload.(*tenant.QuarantineDecided); p.File != d.ID || p.State != tenant.QuarantineReleased ||
		p.ReleaseMode != tenant.ReleaseAsIs || p.ReleaseReception != released.ReleaseReception || p.HeldFiles != 1 || p.HeldBytes != 200 {
		e.Errorf("the release's event = %+v", p)
	}
	// Arrival 3: the release opened the study's second.
	if p := e.stepOf(got[10]); p.Reception != retry.ID || p.RetryOf != failed.ID || p.Step != reception.Complete ||
		p.PreviousStep != "" || p.Arrival != 3 || p.Counts.FilesReceived != 2 || !got[10].Time.Equal(at(130)) {
		e.Errorf("the retry's event = %+v at %v", p, got[10].Time)
	}
	if p := e.stepOf(got[11]); p.Reception != retry.ID || p.Step != reception.Failed || p.PreviousStep != reception.Complete || !got[11].Time.Equal(at(141)) {
		e.Errorf("the event of the unit that gave up = %+v at %v", p, got[11].Time)
	}
}

// testStepEventBothOrNeither guards that a step and its event commit
// together or not at all: a hook that fails, run after the store wrote the
// event, undoes both, and a step refused writes no event.
func testStepEventBothOrNeither(e *eenv) {
	e.addReceiver("a", "store", 11112)
	r := e.openReception("a", 1, at(10))
	start := e.now()
	refuse := errors.New("refused after the event was written")
	_, err := e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet, At: at(70)},
		func(context.Context, tenant.Reception, tenant.Reception) error { return refuse })
	if !errors.Is(err, refuse) {
		e.Errorf("a step whose hook fails = %v, want that hook's error", err)
	}
	_, err = e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Complete, Event: reception.Identify, At: at(70),
		SubjectLabel: "sub-01", SessionLabel: "ses-01", Storage: "store"}, nil)
	e.wantIs("a step from a state the reception is not in", err, tenant.ErrStateChanged, fault.Conflict)
	if got, _, _ := e.p.Reception(e.ctx, r.ID); got.State != reception.Receiving {
		e.Errorf("the step whose hook failed left the reception %s", got.State)
	}
	if events, _ := e.readAll(start, 10); len(events) != 0 {
		e.Errorf("a step undone or refused left events %v", types(events))
	}
	_, err = e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet, At: at(71)}, nil)
	e.ok("StepReception", err)
	if events, _ := e.readAll(start, 10); len(events) != 1 || e.stepOf(events[0]).Step != reception.Complete {
		e.Errorf("the step taken wrote %v, want one complete event", types(events))
	}
}

// testEventPayloadsHoldNoUID guards that no event carries a UID: the
// study, series and instance UIDs of every reception and record written are
// absent from every payload, whose JSON is read back whole.
func testEventPayloadsHoldNoUID(e *eenv) {
	e.addReceiver("a", "store", 11112)
	start := e.now()
	r, _ := e.identified("a", 1, 1, 2)
	_, err := e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r.ID, From: r.State, Event: reception.Fail, At: at(200),
		Failure: "storage unreachable"}, nil)
	e.ok("StepReception", err)
	q := e.differentFile("a", r.ID, 1)
	_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{q})
	e.ok("HoldFiles", err)
	events, _ := e.readAll(start, 100)
	if len(events) == 0 {
		e.Fatal("no events were written")
	}
	for _, ev := range events {
		b, err := json.Marshal(ev.Payload)
		e.ok("json.Marshal", err)
		if strings.Contains(string(b), "2.25.") {
			e.Errorf("an event of type %s carries a UID: %s", ev.Type, b)
		}
	}
}

// testEventPaging guards reading the whole feed in small pages, with and
// without a filter: every event once, in the order written, and a reader
// that resumes from its last cursor gets only what was written after.
func testEventPaging(e *eenv) {
	start := e.now()
	var batch []tenant.EventRecord
	for i := range 250 {
		if i%3 == 0 {
			batch = append(batch, e.health("a", i))
		} else {
			batch = append(batch, e.space("store", i))
		}
	}
	e.appendAll(batch...)
	all, end := e.readAll(start, 7)
	if len(all) != 250 {
		e.Fatalf("pages of 7 read %d events, want 250", len(all))
	}
	for i, ev := range all {
		if !ev.Time.Equal(at(i)) || ev.Type != batch[i].Type() {
			e.Fatalf("event %d is %s at %v, want %s at %v", i, ev.Type, ev.Time, batch[i].Type(), at(i))
		}
	}
	healths, hend := e.readAll(start, 5, tenant.EventReceiverHealth)
	if len(healths) != 84 {
		e.Errorf("a filtered read answered %d health events, want 84", len(healths))
	}
	for i, ev := range healths {
		if !ev.Time.Equal(at(3 * i)) {
			e.Fatalf("health event %d is at %v, want %v", i, ev.Time, at(3*i))
		}
	}
	e.appendAll(e.health("b", 300), e.space("store", 301))
	if more, _ := e.readAll(end, 7); len(more) != 2 || !more[0].Time.Equal(at(300)) {
		e.Errorf("a reader resuming from the end read %d events, want the 2 written after", len(more))
	}
	if more, _ := e.readAll(hend, 5, tenant.EventReceiverHealth); len(more) != 1 || !more[0].Time.Equal(at(300)) {
		e.Errorf("a filtered reader resuming from the end read %d events, want the 1 health written after", len(more))
	}
	if both, _ := e.readAll(start, 13, tenant.EventStorageSpace, tenant.EventReceiverHealth, tenant.EventStorageSpace); len(both) != 252 {
		e.Errorf("a filter of both types, one named twice, read %d events, want 252 once each", len(both))
	}
}

// testEventFilterAdvances guards that a filtered read moves its cursor past
// the events its filter left out: an empty page still carries a cursor, and
// a read from it answers none of the events skipped.
func testEventFilterAdvances(e *eenv) {
	start := e.now()
	var batch []tenant.EventRecord
	for i := range 20 {
		batch = append(batch, e.space("store", i))
	}
	e.appendAll(batch...)
	p := e.read(start, 10, tenant.EventReceiverHealth)
	if len(p.Events) != 0 {
		e.Fatalf("a filter that keeps none answered %d events", len(p.Events))
	}
	if p.Cursor == start {
		e.Error("a filtered read that skipped 20 events left its cursor where it was")
	}
	if rest := e.read(p.Cursor, 100); len(rest.Events) != 0 {
		e.Errorf("a read from a filtered page's cursor answered %d events it had skipped", len(rest.Events))
	}
	e.appendAll(e.health("a", 30))
	if got := e.read(p.Cursor, 100, tenant.EventReceiverHealth); len(got.Events) != 1 {
		e.Errorf("a filtered reader read %d events after its cursor, want 1", len(got.Events))
	}
}

// testEventAppendOnce guards AppendEvents: a key written again with the
// same event writes nothing, within a batch and across batches; a key held
// with another event, across batches or within one, is a Conflict naming
// it; a pipeline type, an event not made by NewEvent, a bad key and a batch
// over the bound are Invalid; none of these writes anything.
func testEventAppendOnce(e *eenv) {
	start := e.now()
	h := e.health("a", 1)
	n, err := e.ev.AppendEvents(e.ctx, []tenant.EventAppend{{Key: "k1", Event: h}, {Key: "k1", Event: h}, {Key: "k2", Event: h}})
	e.ok("AppendEvents", err)
	if n != 2 {
		e.Errorf("a batch with a key twice wrote %d, want 2", n)
	}
	n, err = e.ev.AppendEvents(e.ctx, []tenant.EventAppend{{Key: "k2", Event: h}, {Key: "k3", Event: h}})
	e.ok("AppendEvents again", err)
	if n != 1 {
		e.Errorf("a batch written again wrote %d, want 1", n)
	}
	other := e.health("b", 1)
	_, err = e.ev.AppendEvents(e.ctx, []tenant.EventAppend{{Key: "k1", Event: other}})
	e.wantIs("AppendEvents of another event under a key held", err, tenant.ErrEventKeyReused, fault.Conflict)
	if err != nil && !strings.Contains(fault.Message(err), "k1") {
		e.Errorf("the refusal of a key held does not name it: %q", fault.Message(err))
	}
	_, err = e.ev.AppendEvents(e.ctx, []tenant.EventAppend{{Key: "k9", Event: h}, {Key: "k9", Event: other}})
	e.wantIs("AppendEvents of two events under one key in one batch", err, tenant.ErrEventKeyReused, fault.Conflict)
	step, err := tenant.NewEvent(&tenant.ReceptionStepReached{Reception: "rc1", Receiver: "a", Arrival: 1, Step: reception.Done}, at(1))
	e.ok("NewEvent", err)
	_, err = e.ev.AppendEvents(e.ctx, []tenant.EventAppend{{Key: "k4", Event: h}, {Key: "k5", Event: step}})
	e.wantKind("AppendEvents of a reception step", err, fault.Invalid)
	_, err = e.ev.AppendEvents(e.ctx, []tenant.EventAppend{{Key: "k6", Event: tenant.EventRecord{}}})
	e.wantKind("AppendEvents of an event not made by NewEvent", err, fault.Invalid)
	_, err = e.ev.AppendEvents(e.ctx, []tenant.EventAppend{{Key: "a key", Event: h}})
	e.wantKind("AppendEvents with a bad key", err, fault.Invalid)
	big := make([]tenant.EventAppend, tenant.MaxEventBatch+1)
	for i := range big {
		big[i] = tenant.EventAppend{Key: fmt.Sprintf("b%d", i), Event: h}
	}
	_, err = e.ev.AppendEvents(e.ctx, big)
	e.wantKind("AppendEvents over the bound", err, fault.Invalid)
	if events, _ := e.readAll(start, 100); len(events) != 3 {
		e.Errorf("the feed holds %d events, want 3", len(events))
	}
}

// testEventCursors guards the cursor: bound to the tenant and the feed (a
// cursor of another tenant's feed, of another list, altered or cut short is
// Invalid), and EventCursor starts a reader after every event committed.
func testEventCursors(e *eenv) {
	e.appendAll(e.health("a", 1), e.health("a", 2))
	now := e.now()
	if got := e.read(now, 10); len(got.Events) != 0 {
		e.Errorf("a read from the current cursor answered %d events", len(got.Events))
	}
	e.appendAll(e.health("a", 3))
	if got := e.read(now, 10); len(got.Events) != 1 || !got.Events[0].Time.Equal(at(3)) {
		e.Errorf("a read from the cursor before the last append answered %d events", len(got.Events))
	}
	if all, _ := e.readAll("", 10); len(all) != 3 {
		e.Errorf("a read from the empty cursor answered %d events, want every one kept", len(all))
	}
	other := e.openEv(e.T, OtherTenant)
	oc, err := other.EventCursor(e.ctx)
	e.ok("EventCursor of another tenant", err)
	_, err = e.ev.ReadEvents(e.ctx, oc, nil, 10)
	e.wantIs("ReadEvents with another tenant's cursor", err, tenant.ErrForeignCursor, fault.Invalid)
	_, err = other.ReadEvents(e.ctx, now, nil, 10)
	e.wantIs("ReadEvents of another tenant with this one's cursor", err, tenant.ErrForeignCursor, fault.Invalid)
	e.addReceiver("a", "store", 11112)
	e.openReception("a", 1, at(1))
	e.openReception("a", 2, at(2))
	_, list, err := e.p.ListReceptions(e.ctx, tenant.ReceptionFilter{}, master.Page{Limit: 1})
	e.ok("ListReceptions", err)
	_, err = e.ev.ReadEvents(e.ctx, list, nil, 10)
	e.wantKind("ReadEvents with a list's cursor", err, fault.Invalid)
	if errors.Is(err, tenant.ErrForeignCursor) {
		e.Error("a list's cursor is answered as another tenant's feed's")
	}
	for _, bad := range []string{now[:len(now)-2], now + "A", "not a cursor", strings.Repeat("A", 2000)} {
		_, err = e.ev.ReadEvents(e.ctx, bad, nil, 10)
		e.wantKind("ReadEvents with an altered cursor", err, fault.Invalid)
		if errors.Is(err, tenant.ErrForeignCursor) {
			e.Error("an altered cursor is answered as another tenant's feed's")
		}
	}
	// A cursor after the newest event the feed holds, as a database
	// restored from an older backup meets: another database of the same
	// tenant with more events makes one.
	restored := e.openEv(e.T, SuiteTenant)
	var batch []tenant.EventRecord
	for i := range 10 {
		batch = append(batch, e.health("a", 100+i))
	}
	e.appendTo(restored, batch...)
	ahead, err := restored.EventCursor(e.ctx)
	e.ok("EventCursor", err)
	_, err = e.ev.ReadEvents(e.ctx, ahead, nil, 10)
	e.wantIs("ReadEvents with a cursor after the newest event", err, tenant.ErrCursorExpired, fault.Invalid)
}

// testEventsExpire guards pruning: it removes the oldest events in bounded
// batches, in order, stopping at the first that is not old enough, says
// whether more are old enough, and a cursor it passed is refused with
// ErrCursorExpired while one at its mark or after reads on.
func testEventsExpire(e *eenv) {
	start := e.now()
	var batch []tenant.EventRecord
	for i := range 10 {
		batch = append(batch, e.health("a", i))
	}
	e.appendAll(batch...)
	first := e.read(start, 3)
	sixth := e.read(start, 6)
	n, more, err := e.ev.PruneEvents(e.ctx, at(6), 2)
	e.ok("PruneEvents", err)
	if n != 2 || !more {
		e.Errorf("a pruning bounded to 2 removed %d (more %v), want 2 and more", n, more)
	}
	if got := e.read(first.Cursor, 100); len(got.Events) != 7 {
		e.Errorf("a cursor past what was pruned read %d events, want 7", len(got.Events))
	}
	n, more, err = e.ev.PruneEvents(e.ctx, at(6), 100)
	e.ok("PruneEvents", err)
	if n != 4 || more {
		e.Errorf("the second pruning removed %d (more %v), want 4 and no more", n, more)
	}
	_, err = e.ev.ReadEvents(e.ctx, first.Cursor, nil, 10)
	e.wantIs("ReadEvents from a cursor that pruning passed", err, tenant.ErrCursorExpired, fault.Invalid)
	_, err = e.ev.ReadEvents(e.ctx, start, []tenant.EventType{tenant.EventStorageSpace}, 10)
	e.wantIs("a filtered ReadEvents from before every event pruned", err, tenant.ErrCursorExpired, fault.Invalid)
	if got := e.read(sixth.Cursor, 100); len(got.Events) != 4 || !got.Events[0].Time.Equal(at(6)) {
		e.Errorf("a cursor at the pruning's mark read %d events, want 4 from 6 s", len(got.Events))
	}
	if got, _ := e.readAll("", 100); len(got) != 4 {
		e.Errorf("the empty cursor read %d events, want the 4 kept", len(got))
	}
	n, more, err = e.ev.PruneEvents(e.ctx, at(6), 100)
	e.ok("PruneEvents with nothing old enough", err)
	if n != 0 || more {
		e.Errorf("a pruning with nothing old enough removed %d (more %v)", n, more)
	}
	n, _, err = e.ev.PruneEvents(e.ctx, at(1000), 100)
	e.ok("PruneEvents of every event", err)
	if n != 4 {
		e.Errorf("pruning every event removed %d, want 4", n)
	}
	// With every event gone, the feed's place does not go back: a new
	// event is after every cursor made before.
	now := e.now()
	_, err = e.ev.ReadEvents(e.ctx, sixth.Cursor, nil, 10)
	e.wantIs("ReadEvents from a cursor before an emptied feed's mark", err, tenant.ErrCursorExpired, fault.Invalid)
	e.appendAll(e.health("a", 2000))
	if got := e.read(now, 10); len(got.Events) != 1 || !got.Events[0].Time.Equal(at(2000)) {
		e.Errorf("after the feed was emptied a new event read %d events from the cursor before it", len(got.Events))
	}
}

// testPruneUnderReader guards retention under a reader that pages through
// the feed while it is pruned: each page it reads is exact, and once
// pruning passes its cursor its next read is ErrCursorExpired, never an
// answer that skips events.
func testPruneUnderReader(e *eenv) {
	start := e.now()
	var batch []tenant.EventRecord
	for i := range 100 {
		batch = append(batch, e.health("a", i))
	}
	e.appendAll(batch...)
	cursor, seen := start, 0
	for {
		p, err := e.ev.ReadEvents(e.ctx, cursor, nil, 10)
		if errors.Is(err, tenant.ErrCursorExpired) {
			break
		}
		e.ok("ReadEvents", err)
		for i, ev := range p.Events {
			if !ev.Time.Equal(at(seen + i)) {
				e.Fatalf("the reader got the event at %v as its event %d: a gap", ev.Time, seen+i)
			}
		}
		seen += len(p.Events)
		cursor = p.Cursor
		if len(p.Events) == 0 {
			e.Fatal("the reader reached the end before pruning passed it")
		}
		// Pruning moves 15 events ahead for every 10 the reader reads.
		_, _, err = e.ev.PruneEvents(e.ctx, at(1000), 15)
		e.ok("PruneEvents", err)
	}
	if seen == 0 || seen >= 100 {
		e.Errorf("the reader read %d events before its cursor expired", seen)
	}
}

// testStatusSnapshot guards the snapshot: its counts equal the running
// totals and the receptions in each step, the incoming bytes count what the
// receptions hold and the quarantine keeps, and its cursor divides the feed
// exactly: a read from it answers the changes made after it and nothing
// else.
func testStatusSnapshot(e *eenv) {
	e.addReceiver("a", "store", 11112)
	empty, err := e.ev.StatusSnapshot(e.ctx)
	e.ok("StatusSnapshot", err)
	if empty.EventCursor == "" || len(empty.InProgress) != 0 || empty.IncomingBytes != 0 || empty.Catalog != (tenant.CatalogCounts{}) {
		e.Errorf("an empty tenant's snapshot = %+v", empty)
	}
	r1 := e.openReception("a", 1, at(1))
	_, err = e.p.AddReceivedFiles(e.ctx, []tenant.ReceivedFile{file(r1, 1, 1)}, at(2))
	e.ok("AddReceivedFiles", err)
	r2, _ := e.identified("a", 2, 2, 3)
	e.commit(r2, file(r2, 2, 2))
	done := e.stepTo(e.openReception("a", 3, at(3)), reception.Done, "store")
	failed := e.failReception(e.openReception("a", 4, at(4)))
	_, err = e.p.HoldFiles(e.ctx, []tenant.QuarantinedFile{e.heldFile("a", 40, 5)})
	e.ok("HoldFiles", err)
	s, err := e.ev.StatusSnapshot(e.ctx)
	e.ok("StatusSnapshot", err)
	cat, err := e.p.CatalogTotals(e.ctx)
	e.ok("CatalogTotals", err)
	files, bytes, err := e.p.QuarantineTotals(e.ctx)
	e.ok("QuarantineTotals", err)
	wantSteps := []tenant.StepCount{{Step: reception.Receiving, Receptions: 1}, {Step: reception.Identified, Receptions: 1}}
	if s.Catalog != cat || s.QuarantinedFiles != files || s.QuarantinedBytes != bytes || fmt.Sprint(s.InProgress) != fmt.Sprint(wantSteps) {
		e.Errorf("the snapshot = %+v, want catalog %+v, quarantine %d files %d bytes, steps %v", s, cat, files, bytes, wantSteps)
	}
	// Received bytes of the receptions in progress (1001; 1002 and 1003)
	// and of the failed one (none), and the quarantine's 40.
	if want := int64(1001 + 1002 + 1003 + 40); s.IncomingBytes != want {
		e.Errorf("the snapshot's incoming bytes = %d, want %d", s.IncomingBytes, want)
	}
	_ = done
	_, err = e.p.DiscardReception(e.ctx, failed.ID, "gone", at(6), e.audit(), nil)
	e.ok("DiscardReception", err)
	_, err = e.p.StepReception(e.ctx, tenant.ReceptionStep{ID: r1.ID, From: reception.Receiving, Event: reception.Quiet, At: at(70)},
		nil)
	e.ok("StepReception", err)
	after := e.read(s.EventCursor, 100)
	if len(after.Events) != 1 || e.stepOf(after.Events[0]).Reception != r1.ID {
		e.Errorf("a read from the snapshot's cursor answered %v, want the one step after it", types(after.Events))
	}
	s2, err := e.ev.StatusSnapshot(e.ctx)
	e.ok("StatusSnapshot", err)
	if got := e.read(s2.EventCursor, 100); len(got.Events) != 0 {
		e.Errorf("a read from the newest snapshot's cursor answered %d events", len(got.Events))
	}
	wantSteps = []tenant.StepCount{{Step: reception.Complete, Receptions: 1}, {Step: reception.Identified, Receptions: 1}}
	if fmt.Sprint(s2.InProgress) != fmt.Sprint(wantSteps) {
		e.Errorf("after a step the snapshot's steps = %v, want %v", s2.InProgress, wantSteps)
	}
}

// testEventBounds guards the bounds of the feed's operations: a limit
// outside 1 to MaxEventPage, a filter of more than MaxEventTypes or of a
// type not known, and a pruning outside its bounds are Invalid.
func testEventBounds(e *eenv) {
	for _, limit := range []int{0, -1, tenant.MaxEventPage + 1} {
		_, err := e.ev.ReadEvents(e.ctx, "", nil, limit)
		e.wantKind(fmt.Sprintf("ReadEvents with limit %d", limit), err, fault.Invalid)
	}
	_, err := e.ev.ReadEvents(e.ctx, "", []tenant.EventType{"file_released"}, 10)
	e.wantKind("ReadEvents of a type not known", err, fault.Invalid)
	many := make([]tenant.EventType, tenant.MaxEventTypes+1)
	for i := range many {
		many[i] = tenant.EventReceiverHealth
	}
	_, err = e.ev.ReadEvents(e.ctx, "", many, 10)
	e.wantKind("ReadEvents with too many types", err, fault.Invalid)
	for _, limit := range []int{0, tenant.MaxPruneBatch + 1} {
		_, _, err = e.ev.PruneEvents(e.ctx, at(1), limit)
		e.wantKind(fmt.Sprintf("PruneEvents with limit %d", limit), err, fault.Invalid)
	}
	_, _, err = e.ev.PruneEvents(e.ctx, time.Time{}, 10)
	e.wantKind("PruneEvents before the zero time", err, fault.Invalid)
	if _, err := e.ev.ReadEvents(e.ctx, "", many[:tenant.MaxEventTypes], tenant.MaxEventPage); err != nil {
		e.Errorf("ReadEvents at its bounds: %v", err)
	}
}

// testLateCommit guards commit order where a driver has several writers:
// transaction A writes its event first and commits last, transaction B
// writes after it and commits first (other, the store B runs on, may be
// another instance on the same database). A reader that reads while A is
// open answers nothing it will later contradict: once both commit, a read
// from the cursor the first read gave answers every event the first did
// not, so the two reads hold both events, each once. A driver that reads
// by id alone answers B, moves its cursor past A's id, and never answers A.
// On SQLite it passes by serialization: B cannot begin until A commits, so
// the reader between them sees neither.
func testLateCommit(e *eenv, other EventStore) {
	e.addReceiver("a", "store", 11112)
	ra := e.openReception("a", 1, at(1))
	rb := e.openReception("a", 2, at(2))
	start := e.now()
	ctx, cancel := context.WithTimeout(e.ctx, time.Minute)
	defer cancel()
	wrote, release := make(chan struct{}), make(chan struct{})
	first := true
	hold := func(context.Context, tenant.Reception, tenant.Reception) error {
		// A run again after the batch was undone does not wait a second
		// time: the test waits for the first only.
		if first {
			first = false
			close(wrote)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	aDone := make(chan error, 1)
	go func() {
		_, err := e.ev.StepReception(ctx, tenant.ReceptionStep{ID: ra.ID, From: reception.Receiving, Event: reception.Quiet, At: at(70)},
			hold)
		aDone <- err
	}()
	select {
	case <-wrote:
	case <-ctx.Done():
		e.Fatal("transaction A never wrote its event")
	}
	bDone := make(chan error, 1)
	go func() {
		_, err := other.StepReception(ctx, tenant.ReceptionStep{ID: rb.ID, From: reception.Receiving, Event: reception.Quiet, At: at(71)},
			nil)
		bDone <- err
	}()
	// B commits now on a database with several writers; on one with a
	// single writer it waits for A, and the wait is all this costs.
	bFirst := false
	select {
	case err := <-bDone:
		e.ok("transaction B", err)
		bFirst = true
	case <-time.After(500 * time.Millisecond):
	}
	mid := e.read(start, 100)
	close(release)
	e.ok("transaction A", <-aDone)
	if !bFirst {
		e.ok("transaction B", <-bDone)
	}
	rest, _ := e.readAll(mid.Cursor, 100)
	got := map[string]int{}
	for _, ev := range append(mid.Events, rest...) {
		got[e.stepOf(ev).Reception]++
	}
	if got[ra.ID] != 1 || got[rb.ID] != 1 || len(got) != 2 {
		e.Errorf("a reader between the transactions read %d events and then %d; by reception %v, want each of A and B once (B committed first: %v)",
			len(mid.Events), len(rest), got, bFirst)
	}
}

// testPruneFollowsFeedOrder guards that retention follows the feed's order,
// not each event's own time: one event dated far ahead, first in the feed,
// does not keep the old events after it; it goes with them, and the mark
// moves past both, so a reader before it is told its cursor expired.
func testPruneFollowsFeedOrder(e *eenv) {
	start := e.now()
	far, err := tenant.NewEvent(&tenant.StorageSpaceChanged{Storage: "store", State: tenant.StorageRecovered}, time.Date(9000, 1, 1, 0, 0, 0, 0, time.UTC))
	e.ok("NewEvent", err)
	batch := []tenant.EventRecord{far}
	for i := range 700 {
		batch = append(batch, e.space("store", i))
	}
	batch = append(batch, e.space("store", 5000))
	e.appendAll(batch...)
	n, more, err := e.ev.PruneEvents(e.ctx, at(1000), tenant.MaxPruneBatch)
	e.ok("PruneEvents", err)
	if n != 701 || more {
		e.Errorf("a pruning behind one event dated far ahead removed %d (more %v), want 701: it and the 700 old ones", n, more)
	}
	if kept, _ := e.readAll("", 100); len(kept) != 1 || !kept[0].Time.Equal(at(5000)) {
		e.Errorf("after the pruning %d events are kept, want the one newer than the bound", len(kept))
	}
	_, err = e.ev.ReadEvents(e.ctx, start, nil, 10)
	e.wantIs("ReadEvents from before the pruned events", err, tenant.ErrCursorExpired, fault.Invalid)
	n, _, err = e.ev.PruneEvents(e.ctx, at(1000), 10)
	e.ok("PruneEvents again", err)
	if n != 0 {
		e.Errorf("a second pruning removed %d, want none", n)
	}
}

// testSnapshotDividesTheFeed guards the status snapshot's promise under
// writers working at once: the events after a snapshot's cursor, replayed
// onto its counts, give the counts of a snapshot taken once the writers
// stopped. Each step's event names the step left and the step reached,
// and each quarantine event the totals after it, so both the receptions in
// progress by step and the quarantine's totals can be replayed. A store
// whose cursor and counts come from two moments fails it.
func testSnapshotDividesTheFeed(e *eenv) {
	e.addReceiver("a", "store", 11112)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	done := make(chan error, 4)
	for w := range 4 {
		go func() {
			for i := range 15 {
				if ctx.Err() != nil {
					break
				}
				r, _, err := e.p.OpenReception(ctx, "a", uidOf(1, 1000*w+i), e.id("rc"), at(i))
				if err != nil {
					done <- err
					return
				}
				if i%2 == 0 {
					if _, err := e.p.StepReception(ctx, tenant.ReceptionStep{ID: r.ID, From: reception.Receiving, Event: reception.Quiet, At: at(i + 60)}, nil); err != nil {
						done <- err
						return
					}
				}
				q := e.heldFile("a", int64(10+i), i)
				if _, err := e.p.HoldFiles(ctx, []tenant.QuarantinedFile{q}); err != nil {
					done <- err
					return
				}
				if i%3 == 0 {
					if _, err := e.p.RejectQuarantined(ctx, q.ID, "no", at(i+1), e.audit(), nil); err != nil {
						done <- err
						return
					}
				}
			}
			done <- nil
		}()
	}
	var snaps []tenant.StatusSnapshot
	for range 10 {
		s, err := e.ev.StatusSnapshot(e.ctx)
		e.ok("StatusSnapshot", err)
		snaps = append(snaps, s)
	}
	for range 4 {
		e.ok("a writer", <-done)
	}
	final, err := e.ev.StatusSnapshot(e.ctx)
	e.ok("StatusSnapshot", err)
	for k, s := range snaps {
		steps := map[reception.State]int64{}
		for _, c := range s.InProgress {
			steps[c.Step] = c.Receptions
		}
		files, bytes := s.QuarantinedFiles, s.QuarantinedBytes
		events, _ := e.readAll(s.EventCursor, 100)
		for _, ev := range events {
			switch p := ev.Payload.(type) {
			case *tenant.ReceptionStepReached:
				if p.PreviousStep != "" {
					steps[p.PreviousStep]--
				}
				steps[p.Step]++
			case *tenant.FileQuarantined:
				files, bytes = p.HeldFiles, p.HeldBytes
			case *tenant.QuarantineDecided:
				files, bytes = p.HeldFiles, p.HeldBytes
			}
		}
		var replayed []tenant.StepCount
		for _, st := range reception.States() {
			if n := steps[st]; n != 0 && !st.Ended() {
				replayed = append(replayed, tenant.StepCount{Step: st, Receptions: n})
			}
		}
		if fmt.Sprint(replayed) != fmt.Sprint(final.InProgress) || files != final.QuarantinedFiles || bytes != final.QuarantinedBytes {
			e.Errorf("snapshot %d replayed with the %d events after its cursor gives steps %v and %d files of %d bytes held; the final snapshot has %v and %d of %d",
				k, len(events), replayed, files, bytes, final.InProgress, final.QuarantinedFiles, final.QuarantinedBytes)
		}
	}
}
