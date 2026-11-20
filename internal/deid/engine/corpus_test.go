package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/amrshadid/go-dicom/tag"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
)

// The variables of the outside check.
const (
	// pythonEnv names the Python, with pydicom, that reads every file the
	// corpus test de-identifies (scripts/check/deid-pydicom.py).
	pythonEnv = "CORIA_CHECK_DEID_PYTHON"
	// corpusEnv names pydicom's directory of public test files, every file
	// of which the corpus test de-identifies besides the receiver's.
	corpusEnv = "CORIA_PYDICOM_CORPUS"
	// deidScript is the outside check, from this package's directory.
	deidScript = "../../../scripts/check/deid-pydicom.py"
)

// removedKeywords are the attributes the corpus profile removes or
// replaces, whose values must be nowhere in a de-identified file.
var removedKeywords = []string{
	"PatientName", "PatientID", "OtherPatientIDs", "OtherPatientNames", "IssuerOfPatientID",
	"PatientBirthDate", "PatientAddress", "AccessionNumber", "StudyID",
	"InstitutionName", "InstitutionAddress", "InstitutionalDepartmentName", "StationName",
	"ReferringPhysicianName", "PerformingPhysicianName", "OperatorsName",
	"NameOfPhysiciansReadingStudy", "RequestingPhysician", "DeviceSerialNumber",
	"StudyInstanceUID", "SeriesInstanceUID", "SOPInstanceUID", "FrameOfReferenceUID",
	"StudyDate", "SeriesDate", "AcquisitionDate", "ContentDate",
}

// corpusRules is the profile of the corpus test: names removed, the
// patient's identifier hashed, every UID remapped, every date shifted,
// identifying descriptions cleaned.
func corpusRules() []*v1.ProfileRule {
	rules := []*v1.ProfileRule{
		rule("names", vrSel("PN"), aRemove),
		rule("patient-name", kw("PatientName"), aReplace("SUBJECT")),
		rule("patient-id", kw("PatientID"), aHash),
		rule("accession", kw("AccessionNumber"), aHash),
		rule("study-id", kw("StudyID"), aHash),
		// Scanners copy the accession number into the procedure's
		// identifiers (examples_overlay).
		rule("requested-procedure", kw("RequestedProcedureID"), aHash),
		rule("scheduled-step", kw("ScheduledProcedureStepID"), aHash),
		rule("performed-step", kw("PerformedProcedureStepID"), aHash),
		rule("uids", vrSel("UI"), aUID),
		rule("dates", vrSel("DA"), aShift),
		rule("datetimes", vrSel("DT"), aShift),
		// A segmentation's label names its maker (liver_1frame: QIICR QIN
		// IOWA), as Manufacturer, removed below, does.
		rule("content-label", kw("ContentLabel"), aReplace("SEGMENT")),
	}
	for _, k := range []string{"OtherPatientIDs", "OtherPatientIDsSequence", "IssuerOfPatientID", "PatientAddress",
		"InstitutionName", "InstitutionAddress", "InstitutionalDepartmentName", "StationName", "DeviceSerialNumber",
		// A vendor's name can hold the institution's (MR_small: TOSHIBA in
		// TOSHIBA_MEC), and a model's name too (rtplan), which a search for the
		// removed name would find; they are removed at every depth.
		"Manufacturer", "ManufacturerModelName"} {
		rules = append(rules, rule("remove-"+strings.ToLower(k), kw(k), aRemove))
	}
	// A structure set's label can hold the study's ID (rtstruct: sep30).
	for _, k := range []string{"StudyDescription", "SeriesDescription", "ImageComments", "PatientComments", "StructureSetLabel", "StructureSetName"} {
		rules = append(rules, rule("clean-"+strings.ToLower(k), kw(k), aClean))
	}
	return rules
}

// corpusFiles lists the files of dir that are not directories or notes.
func corpusFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".md") || strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// TestCorpusFilesAreDeidentified guards the engine over public files: the
// receiver's test files and, with corpusEnv set, every file of pydicom's
// corpus. Each file the library reads is de-identified by the corpus
// profile or refused with a Failure, never left half done; each result is
// written, read back by the library, and holds none of the values the
// profile removed; doing it again gives the same bytes. With pythonEnv
// set, pydicom then reads every pair of original and result and finds none
// of the removed values in the result (scripts/check/deid-pydicom.py).
func TestCorpusFilesAreDeidentified(t *testing.T) {
	files := corpusFiles(t, receiveTestdata)
	if dir := os.Getenv(corpusEnv); dir != "" {
		files = append(files, corpusFiles(t, dir)...)
	}
	en := newTestEngine(t, testKeys, Options{Method: "corpus@1"}, corpusRules()...)
	out := t.TempDir()
	type pair struct {
		Original     string `json:"original"`
		Deidentified string `json:"deidentified"`
	}
	var pairs []pair
	// The needles searched for here are those of removedKeywords, as the
	// outside check's: a vendor's or model's name, removed by the profile
	// too, identifies no patient and recurs in what a file keeps.
	checked := map[string]bool{}
	for _, k := range removedKeywords {
		checked[formatTag(uint32(tag.GlobalDictionary().GetByKeyword(k)))] = true
	}
	causes := map[Cause]int{}
	unread := 0
	for i, path := range files {
		ds, err := readPath(t, path)
		if err != nil {
			unread++
			continue
		}
		if ts := ds.TransferSyntaxUID(); ts == "1.2.840.10008.1.2.2" || ts == "1.2.840.10008.1.2.1.99" {
			// Written back here as Explicit VR Little Endian, a big
			// endian data set's binary values would need swapping, and a
			// deflated one is refused by the pipeline: neither is this
			// engine's to change.
			unread++
			continue
		}
		before := writeFile(t, ds)
		rec, err := en.Apply(context.Background(), ds, Reception{})
		var f *Failure
		if errors.As(err, &f) {
			causes[f.Cause]++
			if after := writeFile(t, ds); !bytes.Equal(before, after) {
				t.Errorf("%s: a refused file was changed", path)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		b := writeFile(t, ds)
		again, err := readPath(t, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := en.Apply(context.Background(), again, Reception{}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, writeFile(t, again)) {
			t.Errorf("%s: two runs gave different bytes", path)
		}
		back := readFile(t, b)
		hay := strings.ToLower(strings.Join(allText(back), "\x00"))
		for _, n := range rec.Needles {
			if !checked[n.Path[strings.LastIndexByte(n.Path, '/')+1:]] {
				continue
			}
			if len(n.Value) >= 4 && strings.Contains(hay, strings.ToLower(n.Value)) {
				t.Errorf("%s: a removed value of %s is in the result", path, n.Path)
			}
		}
		name := filepath.Join(out, fmt.Sprintf("%03d-%s.dcm", i, filepath.Base(path)))
		if err := os.WriteFile(name, b, 0o600); err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, pair{path, name})
	}
	t.Logf("%d files: %d de-identified, %d refused %v, %d not read or not written here", len(files), len(pairs), len(files)-len(pairs)-unread, causes, unread)
	if len(pairs) < 10 {
		t.Fatalf("only %d files de-identified", len(pairs))
	}
	for c := range causes {
		if c != CauseNoSubject && c != CauseBadValue {
			t.Errorf("a refusal of cause %s", c)
		}
	}
	py, asked := os.LookupEnv(pythonEnv)
	if !asked {
		return
	}
	if py == "" {
		t.Fatalf("%s is set to nothing: the check was asked for without an interpreter", pythonEnv)
	}
	sort.Strings(removedKeywords)
	spec, err := json.Marshal(map[string]any{"keywords": removedKeywords, "files": pairs})
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(out, "manifest.json")
	if err := os.WriteFile(manifest, spec, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), py, "-I", deidScript, manifest) //nolint:gosec // G204: the interpreter is the test runner's own and the script the repository's.
	report, err := cmd.CombinedOutput()
	t.Logf("pydicom: %s", report)
	if err != nil {
		t.Fatalf("pydicom found removed values: %v", err)
	}
}

// TestStudyKeepsItsLinksAcrossFiles guards that the files of one study,
// de-identified apart, still form one study: one Study Instance UID, one
// Series Instance UID per series, distinct SOP Instance UIDs, one date
// shift for the patient; and that another tenant's keys give other UIDs.
func TestStudyKeepsItsLinksAcrossFiles(t *testing.T) {
	en := newTestEngine(t, testKeys, Options{}, corpusRules()...)
	other := newTestEngine(t, otherKeys, Options{}, corpusRules()...)
	matches, err := filepath.Glob(filepath.Join(receiveTestdata, "ct*n-*.dcm"))
	if err != nil || len(matches) < 4 {
		t.Fatalf("the study's files: %v %v", matches, err)
	}
	studies, dates, instances := map[string]bool{}, map[string]bool{}, map[string]bool{}
	series := map[string]map[string]bool{}
	for _, path := range matches {
		ds, err := readPath(t, path)
		if err != nil {
			t.Fatal(err)
		}
		origSeries := mustText(t, ds, 0x0020000E)
		apply(t, en, ds)
		studies[mustText(t, ds, tStudyUID)] = true
		dates[mustText(t, ds, tStudyDate)] = true
		instances[mustText(t, ds, tSOPInstance)] = true
		if series[origSeries] == nil {
			series[origSeries] = map[string]bool{}
		}
		series[origSeries][mustText(t, ds, 0x0020000E)] = true

		ds2, _ := readPath(t, path)
		apply(t, other, ds2)
		if mustText(t, ds2, tStudyUID) == mustText(t, ds, tStudyUID) {
			t.Errorf("%s: two tenants share a study UID", path)
		}
	}
	if len(studies) != 1 || len(dates) != 1 || len(instances) != len(matches) {
		t.Errorf("studies %d, study dates %d, instances %d of %d", len(studies), len(dates), len(instances), len(matches))
	}
	for orig, got := range series {
		if len(got) != 1 {
			t.Errorf("series %s became %d series", orig, len(got))
		}
	}
	if len(series) < 2 {
		t.Errorf("the study has %d series", len(series))
	}
}
