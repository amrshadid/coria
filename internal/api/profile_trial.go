package api

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/sequence"
	"github.com/amrshadid/go-dicom/tag"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/core/naming"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/receive"
)

// trialResult returns one file's trial as the API answers it, its
// elements' values included, spending budget (bytes of the answer left);
// elements past it, or past maxTrialElements, are counted, not answered.
func trialResult(t receive.Trial, budget *int) *v1.TrialResult {
	r := &v1.TrialResult{Verdict: v1.TrialVerdict_TRIAL_VERDICT_PASSED, Receiver: t.Receiver, DeidentificationOnly: t.Receiver == ""}
	if t.Undecided {
		r.Verdict, r.Cause = v1.TrialVerdict_TRIAL_VERDICT_UNDECIDED, receive.TrialCauseLookupTable
	}
	if t.Hold != nil {
		r.Verdict = v1.TrialVerdict_TRIAL_VERDICT_HELD
		r.Reason, r.Cause = string(t.Hold.Reason), holdCause(t.Hold)
		r.Path, r.Rule = t.Hold.Details["path"], t.Hold.Details["rule"]
	}
	r.ValuesSearched = int32(t.Report.Searched) //nolint:gosec // G115: bounded by the check's needles.
	r.ValuesSkipped = int32(t.Report.Skipped)   //nolint:gosec // G115: bounded by the check's needles.
	if t.Record == nil {
		return r
	}
	for i, e := range t.Record.Entries {
		el := &v1.TrialElement{
			Path: e.Path, Keyword: keywordOf(e.Tag), Vr: e.VR, Action: e.Action, Rule: int32(e.Rule + 1), //nolint:gosec // G115: a position under profile.MaxExpandedRules.
			RuleName: e.RuleName, Origin: e.Origin, Reason: e.Reason, Changed: e.Changed,
		}
		el.Before, _ = valueAt(t.Before, e.Path)
		if t.After != nil {
			el.After, el.PresentAfter = valueAt(t.After, e.Path)
		}
		size := len(el.Path) + len(el.Keyword) + len(el.Action) + len(el.RuleName) + len(el.Origin) + len(el.Reason) + len(el.Before) + len(el.After) + 32
		if i >= maxTrialElements || size > *budget {
			r.ElementsOmitted = int32(len(t.Record.Entries) - i) //nolint:gosec // G115: bounded by engine.MaxElements.
			break
		}
		*budget -= size
		r.Elements = append(r.Elements, el)
	}
	return r
}

// holdCause is the fixed cause of a hold: the record's cause, or for a
// value found the kind of value, or for a rule's refusal "rule".
func holdCause(h *receive.TrialHold) string {
	if c := h.Details["cause"]; c != "" {
		return c
	}
	if k := h.Details["kind"]; k != "" {
		return k
	}
	return "rule"
}

// keywordOf is a standard element's keyword in the data dictionary; empty
// for a private or unknown one.
func keywordOf(t uint32) string {
	tt := tag.Tag(t)
	if tt.Group()%2 == 1 {
		return ""
	}
	return tt.GetKeyword()
}

// textVRs are the VRs whose value is text, which a trial run answers.
var textVRs = map[string]bool{
	"AE": true, "AS": true, "CS": true, "DA": true, "DS": true, "DT": true, "IS": true, "LO": true, "LT": true,
	"PN": true, "SH": true, "ST": true, "TM": true, "UC": true, "UI": true, "UR": true, "UT": true,
}

// valueAt returns the text of the element at path (a record's path, such
// as "00081115[0]/0020000E") in ds, its padding removed, and whether ds
// holds the element there. A value that is not text, or not in memory, is
// answered empty.
func valueAt(ds *dataset.Dataset, path string) (string, bool) {
	steps := strings.Split(path, "/")
	cur := ds
	for i, s := range steps {
		if cur == nil {
			return "", false
		}
		ts, index, item := strings.Cut(s, "[")
		t, err := strconv.ParseUint(ts, 16, 32)
		if err != nil || len(ts) != 8 {
			return "", false
		}
		elem, ok := cur.Get(tag.Tag(t))
		if !ok {
			return "", false
		}
		if i == len(steps)-1 {
			if item {
				return "", false
			}
			vr := string(elem.GetVR())
			if !textVRs[vr] {
				return "", true
			}
			b, ok := elem.Bytes()
			if !ok {
				return "", true
			}
			return naming.TrimValue(vr, string(b)), true
		}
		n, err := strconv.Atoi(strings.TrimSuffix(index, "]"))
		seq, isSeq := elem.GetValue().(*sequence.Sequence)
		if !item || err != nil || !isSeq || n < 0 || n >= seq.Length() {
			return "", false
		}
		it, err := seq.Get(n)
		if err != nil {
			return "", false
		}
		cur, _ = it.(*dataset.Dataset)
	}
	return "", false
}

// trialSummary counts a trial run: files, verdicts, the elements each rule
// decided and the files held for each reason. It holds no value.
type trialSummary struct {
	files, passed, held, undecided int
	byRule, byReason               map[string]int
}

// newTrialSummary returns an empty summary.
func newTrialSummary() *trialSummary {
	return &trialSummary{byRule: map[string]int{}, byReason: map[string]int{}}
}

// add counts one file.
func (s *trialSummary) add(t receive.Trial, r *v1.TrialResult) {
	s.files++
	switch r.GetVerdict() {
	case v1.TrialVerdict_TRIAL_VERDICT_HELD:
		s.held++
		s.byReason[r.GetReason()+"/"+r.GetCause()]++
	case v1.TrialVerdict_TRIAL_VERDICT_UNDECIDED:
		s.undecided++
	default:
		s.passed++
	}
	if t.Record == nil {
		return
	}
	for _, e := range t.Record.Entries {
		switch {
		case e.Rule < 0:
			s.byRule["default"]++
		case e.RuleName != "":
			s.byRule[e.RuleName]++
		default:
			s.byRule["rule "+strconv.Itoa(e.Rule+1)]++
		}
	}
}

// counts returns m by count, most first, then by key.
func counts(m map[string]int) []*v1.TrialCount {
	out := make([]*v1.TrialCount, 0, len(m))
	for k, n := range m {
		out = append(out, &v1.TrialCount{Key: k, Count: int32(n)}) //nolint:gosec // G115: bounded by the files' elements.
	}
	slices.SortFunc(out, func(a, b *v1.TrialCount) int {
		return cmp.Or(cmp.Compare(b.GetCount(), a.GetCount()), cmp.Compare(a.GetKey(), b.GetKey()))
	})
	return out
}

// proto returns the summary as the API answers it.
func (s *trialSummary) proto() *v1.TrialSummary {
	return &v1.TrialSummary{Files: int32(s.files), Passed: int32(s.passed), Held: int32(s.held), Undecided: int32(s.undecided), //nolint:gosec // G115: at most MaxTrialFiles.
		ByRule: counts(s.byRule), ByReason: counts(s.byReason)}
}

// The keys of a trial run's audit details.
const (
	detailChecksum  = "checksum"
	detailTried     = "files"
	detailRecords   = "quarantine_records"
	detailHeld      = "held"
	detailUndecided = "undecided"
	detailByRule    = "by_rule"
	detailByReason  = "by_reason"
)

// details returns the summary as an audit entry's details: the checksum
// tried, the counts, and the counts per rule and per reason written
// "key=count" by count, most first, cut at an entry's bound with how many
// more there were. Rule names and reasons are a profile's words and the
// server's, never a file's.
func (s *trialSummary) details(checksum string, records int) map[string]string {
	return map[string]string{
		detailChecksum: checksum, detailTried: strconv.Itoa(s.files), detailRecords: strconv.Itoa(records),
		detailPassed: strconv.Itoa(s.passed), detailHeld: strconv.Itoa(s.held), detailUndecided: strconv.Itoa(s.undecided),
		detailByRule: countsText(counts(s.byRule)), detailByReason: countsText(counts(s.byReason)),
	}
}

// countsText writes counts as "key=count,..." within an audit detail's
// bound, ending with "+N more" when they do not all fit.
func countsText(cs []*v1.TrialCount) string {
	var b strings.Builder
	for i, c := range cs {
		item := c.GetKey() + "=" + strconv.Itoa(int(c.GetCount()))
		more := "+" + strconv.Itoa(len(cs)-i) + " more"
		if b.Len()+len(item)+1+len(more)+1 > master.MaxAuditDetailValueLength {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(more)
			break
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(item)
	}
	return b.String()
}
