package cli_test

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
)

// seedQuarantine puts four records in lab1: two held (a bad label with a
// header, a too-large file whose bytes were never kept), one rejected and one
// released.
func seedQuarantine(r *rig) {
	caller := &v1.Caller{CallingAeTitle: "PRISMA1", CalledAeTitle: "MAGNETAR", Address: "192.0.2.7:51234"}
	r.fake.AddQuarantined("lab1", &v1.QuarantinedFile{
		Id: "q-label", Reason: v1.QuarantineReason_QUARANTINE_REASON_BAD_LABEL, Details: map[string]string{"label": "subject", "fault": "missing", "attribute": "PatientID"},
		State: v1.QuarantineState_QUARANTINE_STATE_HELD, Receiver: "scanner1", Reception: "rcp-1", Caller: caller, SizeBytes: 2048, Sha256: strings.Repeat("ab", 32),
		SopInstanceUid: "1.2.3.4", StudyInstanceUid: "1.2.3", ContentKept: true, QuarantineTime: timestamppb.New(fixedNow),
	}, `{"00080060":{"vr":"CS","Value":["MR"]}}`)
	r.fake.AddQuarantined("lab1", &v1.QuarantinedFile{
		Id: "q-large", Reason: v1.QuarantineReason_QUARANTINE_REASON_TOO_LARGE, Details: map[string]string{"limit": "1024"},
		State: v1.QuarantineState_QUARANTINE_STATE_HELD, Receiver: "scanner1", Caller: caller, SizeBytes: 1024, QuarantineTime: timestamppb.New(fixedNow.Add(-time.Hour)),
	}, "")
	r.fake.AddQuarantined("lab1", &v1.QuarantinedFile{
		Id: "q-rejected", Reason: v1.QuarantineReason_QUARANTINE_REASON_NOT_DICOM, State: v1.QuarantineState_QUARANTINE_STATE_REJECTED, RejectReason: "a text file",
		Receiver: "scanner1", SizeBytes: 10, QuarantineTime: timestamppb.New(fixedNow.Add(-2 * time.Hour)), DecideTime: timestamppb.New(fixedNow.Add(-time.Hour)),
	}, "")
	r.fake.AddQuarantined("lab1", &v1.QuarantinedFile{
		Id: "q-released", Reason: v1.QuarantineReason_QUARANTINE_REASON_LABEL_CONFLICT, State: v1.QuarantineState_QUARANTINE_STATE_RELEASED, ReleaseReceiver: "scanner1",
		ReleaseReception: "rcp-9", ReleaseMode: v1.ReleaseMode_RELEASE_MODE_AS_IS, Receiver: "scanner1", SizeBytes: 20, ContentKept: true,
		QuarantineTime: timestamppb.New(fixedNow.Add(-3 * time.Hour)), DecideTime: timestamppb.New(fixedNow.Add(-time.Hour)),
	}, "")
}

// TestQuarantineListAndShow guards the two read commands: the table of what is
// held and why, every filter that narrows it, the record with the details of
// its reason, and the header only when asked for.
func TestQuarantineListAndShow(t *testing.T) {
	r := newArchiveRig(t)
	seedQuarantine(r)

	expect(t, r.run("quarantine", "list", "lab1", "--state", "held"), 0,
		"ID       STATE  REASON     RECEIVER  RECEPTION  BYTES  QUARANTINED\n"+
			"q-label  held   bad label  scanner1  rcp-1      2048   2026-11-17 15:00:00 +03:00\n"+
			"q-large  held   too large  scanner1  -          1024   2026-11-17 14:00:00 +03:00\n", "")
	ids := func(args ...string) string {
		t.Helper()
		doc := document(t, r.run(append([]string{"quarantine", "list", "lab1", "--json"}, args...)...))
		var got []string
		for _, x := range doc["files"].([]any) {
			got = append(got, x.(map[string]any)["id"].(string))
		}
		return strings.Join(got, ",")
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "q-label,q-large,q-rejected,q-released"},
		{[]string{"--state", "rejected"}, "q-rejected"},
		{[]string{"--reason", "too-large"}, "q-large"},
		{[]string{"--reason", "label-conflict", "--state", "released"}, "q-released"},
		{[]string{"--reception", "rcp-1"}, "q-label"},
		{[]string{"--study-uid", "1.2.3"}, "q-label"},
		{[]string{"--sop-uid", "1.2.3.4"}, "q-label"},
		{[]string{"--since", "90m"}, "q-label,q-large"},
		{[]string{"--until", "2026-11-17T14:30:00+03:00"}, "q-large,q-rejected,q-released"},
		{[]string{"--receiver", "other"}, ""},
	} {
		if got := ids(c.args...); got != c.want {
			t.Errorf("quarantine list %v: %q, want %q", c.args, got, c.want)
		}
	}

	expect(t, r.run("quarantine", "show", "lab1", "q-label"), 0,
		"ID:               q-label\nState:            held\nReason:           bad label\n"+
			"  attribute:      PatientID\n  fault:          missing\n  label:          subject\n"+
			"Receiver:         scanner1\nReception:        rcp-1\nCalling AE title: PRISMA1\nCalled AE title:  MAGNETAR\nAddress:          192.0.2.7:51234\n"+
			"Size:             2048 bytes\nSHA-256:          "+strings.Repeat("ab", 32)+"\nSOP class:        -\nSOP instance:     1.2.3.4\nStudy:            1.2.3\n"+
			"Series:           -\nTransfer syntax:  -\nBytes held:       yes\nQuarantined:      2026-11-17 15:00:00 +03:00\nDecided:          -\n", "")
	with := r.run("quarantine", "show", "lab1", "q-label", "--header")
	if !strings.HasSuffix(with.stdout, "\nHeader:\n{\"00080060\":{\"vr\":\"CS\",\"Value\":[\"MR\"]}}\n") {
		t.Errorf("show --header:\n%s", with.stdout)
	}
	if res := r.run("quarantine", "show", "lab1", "q-large", "--header"); !strings.HasSuffix(res.stdout, "\nHeader:\n(not available: the file's bytes are gone or its data set could not be read)\n") {
		t.Errorf("a header that is not there:\n%s", res.stdout)
	}
	if res := r.run("quarantine", "show", "lab1", "q-rejected"); !strings.Contains(res.stdout, "Reject reason:    a text file\n") || !strings.Contains(res.stdout, "Bytes held:       no\n") {
		t.Errorf("a rejected record:\n%s", res.stdout)
	}
	if res := r.run("quarantine", "show", "lab1", "q-released"); !strings.Contains(res.stdout, "Released at:      scanner1\n") || !strings.Contains(res.stdout, "Released into:    rcp-9\n") {
		t.Errorf("a released record:\n%s", res.stdout)
	}
	// The header is asked for only with the flag, whatever the document form.
	plain := document(t, r.run("quarantine", "show", "lab1", "q-label", "--json"))
	if plain["headerJson"] != "" {
		t.Errorf("a header was answered without being asked for: %v", plain["headerJson"])
	}
	asked := document(t, r.run("quarantine", "show", "lab1", "q-label", "--header", "--json"))
	if !strings.Contains(asked["headerJson"].(string), "00080060") || asked["file"].(map[string]any)["reason"] != "QUARANTINE_REASON_BAD_LABEL" {
		t.Errorf("show --header --json: %v", asked)
	}
}

// TestQuarantineListUsageErrors guards what is refused before anything is
// sent: a state or reason that is not one, with the choices.
func TestQuarantineListUsageErrors(t *testing.T) {
	r := newArchiveRig(t)
	before := r.fake.Requests()
	res := r.run("quarantine", "list", "lab1", "--state", "kept")
	if res.code != 2 || res.stdout != "" || !strings.Contains(res.stderr, "--state \"kept\" is not one of: held, released, rejected") {
		t.Errorf("a bad state: %+v", res)
	}
	res = r.run("quarantine", "list", "lab1", "--reason", "odd")
	if res.code != 2 || !strings.Contains(res.stderr, "--reason \"odd\" is not one of: not-dicom, sop-class-not-accepted, too-large, bad-uid, bad-label, label-conflict, different-file, uid-elsewhere, storage-refused, read-back-mismatch") {
		t.Errorf("a bad reason: %+v", res)
	}
	if got := r.fake.Requests(); got != before {
		t.Errorf("%d requests were sent", got-before)
	}
}

// TestQuarantineRelease guards the release command: a held file joins a
// reception, the output says why it had been held, a repeat answers as it
// stands, a file whose bytes were never kept or that was rejected is a refusal
// (exit 5), and an unknown id is 4.
func TestQuarantineRelease(t *testing.T) {
	r := newArchiveRig(t)
	seedQuarantine(r)
	expect(t, r.run("quarantine", "release", "lab1", "q-label"), 0,
		"file q-label released (it was quarantined: bad label) into reception rec-released at receiver scanner1\n", "")
	doc := document(t, r.run("quarantine", "release", "lab1", "q-label", "--json"))
	f := doc["file"].(map[string]any)
	if f["state"] != "QUARANTINE_STATE_RELEASED" || f["releaseMode"] != "RELEASE_MODE_AS_IS" || f["releaseReception"] != "rec-released" {
		t.Errorf("release --json: %v", f)
	}
	for id, want := range map[string]int{"q-large": 5, "q-rejected": 5, "nope": 4} {
		if res := r.run("quarantine", "release", "lab1", id); res.code != want || res.stdout != "" || strings.Count(res.stderr, "\n") != 1 {
			t.Errorf("release of %s: %+v, want exit %d", id, res, want)
		}
	}
	if res := r.run("quarantine", "release", "lab1", "q-large", "--receiver", "nope"); res.code != 5 {
		t.Errorf("release at a receiver that does not exist: %+v", res)
	}
}

// TestQuarantineReject guards the reject command: the reason is required and
// kept, the id is typed again unless --yes, a wrong id sends nothing, a repeat
// answers with the first reason, and a released file is a refusal (exit 5).
func TestQuarantineReject(t *testing.T) {
	r := newArchiveRig(t)
	seedQuarantine(r)
	before := r.fake.Requests()

	expect(t, r.run("quarantine", "reject", "lab1", "q-label", "--reason", "not our study"), 2, "",
		"magnetar: quarantine reject: nothing was rejected: this would remove for good the bytes of quarantined file \"q-label\"; its record stays; run it again with --yes to do it\n")
	if res := r.run("quarantine", "reject", "lab1", "q-label"); res.code != 2 || !strings.Contains(res.stderr, "required flag(s) \"reason\" not set") {
		t.Errorf("no reason: %+v", res)
	}
	if res := r.run("quarantine", "reject", "lab1", "q-label", "--reason", "", "--yes"); res.code != 2 || !strings.Contains(res.stderr, "--reason must say why") {
		t.Errorf("an empty reason: %+v", res)
	}
	for name, input := range map[string]string{"another id": "q-large\n", "nothing": ""} {
		res := typed(r.env, input, "quarantine", "reject", "lab1", "q-label", "--reason", "not our study")
		if res.code != 2 || res.stdout != "" || !strings.Contains(res.stderr, "Type the id q-label again to go on: ") || !strings.Contains(res.stderr, "nothing was rejected") {
			t.Errorf("%s typed: %+v", name, res)
		}
	}
	if got := r.fake.Requests(); got != before {
		t.Errorf("%d requests were sent before the id was confirmed", got-before)
	}

	right := typed(r.env, "q-label\n", "quarantine", "reject", "lab1", "q-label", "--reason", "not our study")
	if right.code != 0 || right.stdout != "file q-label rejected: not our study; its bytes were removed and its record stays\n" {
		t.Errorf("the id typed: %+v", right)
	}
	if show := r.run("quarantine", "show", "lab1", "q-label"); !strings.Contains(show.stdout, "State:            rejected\n") || !strings.Contains(show.stdout, "Bytes held:       no\n") {
		t.Errorf("after the rejection:\n%s", show.stdout)
	}
	// Rejected already: answered as it stands, with its first reason.
	expect(t, r.run("quarantine", "reject", "lab1", "q-label", "--reason", "another", "--yes"), 0,
		"file q-label rejected: not our study; its bytes were removed and its record stays\n", "")
	doc := document(t, r.run("quarantine", "reject", "lab1", "q-large", "--reason", "too big", "--yes", "--json"))
	if doc["file"].(map[string]any)["rejectReason"] != "too big" {
		t.Errorf("reject --json: %v", doc)
	}
	if res := r.run("quarantine", "reject", "lab1", "q-released", "--reason", "x", "--yes"); res.code != 5 {
		t.Errorf("reject of a released file: %+v", res)
	}
	if res := r.run("quarantine", "reject", "lab1", "nope", "--reason", "x", "--yes"); res.code != 4 {
		t.Errorf("reject of an unknown id: %+v", res)
	}
}

// TestArchiveCommandsComplete guards the shell completion of the choices a
// person types: the names of states, reasons and steps are offered for their
// flags, as the choices the commands accept.
func TestArchiveCommandsComplete(t *testing.T) {
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"quarantine", "list", "lab1", "--state", ""}, []string{"held", "released", "rejected"}},
		{[]string{"quarantine", "list", "lab1", "--reason", ""}, []string{"not-dicom", "sop-class-not-accepted", "too-large"}},
		{[]string{"reception", "list", "lab1", "--step", ""}, []string{"receiving", "complete"}},
		{[]string{"storage", "add", "lab1", "x", "--type", ""}, []string{"dir", "s3"}},
	} {
		res := runWith(nil, append([]string{"__complete"}, c.args...)...)
		lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
		var got []string
		for _, l := range lines {
			if !strings.HasPrefix(l, ":") && !strings.HasPrefix(l, "Completion ended") {
				got = append(got, l)
			}
		}
		if res.code != 0 || len(got) < len(c.want) {
			t.Errorf("%v: %+v", c.args, res)
			continue
		}
		for i, w := range c.want {
			if !strings.HasPrefix(got[i], w) {
				t.Errorf("%v: offered %q, want %q first", c.args, got, c.want)
			}
		}
	}
}
