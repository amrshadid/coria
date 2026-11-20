package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	connect "connectrpc.com/connect/v2"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/gen/coria/v1/coriav1connect"
	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/api"
	"github.com/amrshadid/coria/internal/blob"
	"github.com/amrshadid/coria/internal/blob/dir"
	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/core/layout"
	"github.com/amrshadid/coria/internal/master"
	"github.com/amrshadid/coria/internal/receive/header"
	"github.com/amrshadid/coria/internal/receive/sidecar"
	"github.com/amrshadid/coria/internal/tenant"
)

// metaFiles are the files the metadata tests have lab1 receive: a native
// and two encapsulated images of one series (the same public file with its
// pixels stored three ways), CT slices of two series, an image of another
// subject, and an image that holds a binary value over 1 KiB and a text
// value over 64 KiB before its pixel data and trailing padding after it,
// none of which the metadata file keeps the place of.
func metaFiles(t *testing.T) (files [][]byte, synthetic []byte) {
	t.Helper()
	extra, _, _ := privateBulk()
	synthetic = reUID(t, insertBeforePixelData(t, corpusFile(t, "MR_small.dcm"), extra), '6')
	synthetic = append(synthetic, explicitLong(0xFFFC, 0xFFFC, "OB", bytes.Repeat([]byte{0}, 64))...)
	files = [][]byte{
		corpusFile(t, "MR_small.dcm"),
		reUID(t, corpusFile(t, "MR_small_RLE.dcm"), '8'),
		reUID(t, corpusFile(t, "MR_small_jp2klossless.dcm"), '9'),
		synthetic,
		corpusFile(t, "ct2n-6293.dcm"),
		corpusFile(t, "ct5n-2062.dcm"),
		corpusFile(t, "liver_1frame.dcm"),
	}
	return files, synthetic
}

// dataset decodes a DICOM JSON answer.
func dataset(t testing.TB, text string) map[string]map[string]any {
	t.Helper()
	var m map[string]map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("the answer is not DICOM JSON: %v", err)
	}
	return m
}

// uidOf returns the SOP Instance UID of a file.
func uidOf(t testing.TB, file []byte) string {
	t.Helper()
	h, err := header.Read(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := h.UID(header.SOPInstanceUID)
	return uid
}

// bulkOf returns the bytes a BulkDataURI of an answer names in the file
// the instance was received as.
func bulkOf(t testing.TB, el map[string]any, p label.Path, id string, file []byte) []byte {
	t.Helper()
	s, _ := el["BulkDataURI"].(string)
	u := parseURI(t, s, p, id)
	if u.offset < 0 || u.length < 0 || u.offset+u.length > int64(len(file)) {
		t.Fatalf("BulkDataURI %q lies outside a file of %d bytes", s, len(file))
	}
	return file[u.offset : u.offset+u.length]
}

// TestMetadataAnswersEveryInstanceOfAScan guards the whole answer on an
// archive made by reception: each scan's page holds exactly the instances
// the catalog holds, in order, with the transfer syntax its file states,
// the DICOM JSON of its header with a BulkDataURI in place of its pixel
// data (whose bytes, read at that offset of the file that was received,
// start where the contract says), no file meta information and no trailing
// padding, and the checksum of the scan's manifest on the storage.
func TestMetadataAnswersEveryInstanceOfAScan(t *testing.T) {
	f := newPlaceFixture(t)
	files, _ := metaFiles(t)
	a := receiveArchive(t, f, files)
	md := f.as(f.lab1Reader).metadata
	if len(a.scans(t)) < 3 {
		t.Fatalf("lab1 holds %d scans, want at least 3", len(a.scans(t)))
	}
	for _, path := range a.scans(t) {
		o := call(md.GetScanMetadata, scanRequest(path))
		if o.err != nil {
			t.Fatalf("%v: %v", path, o.err)
		}
		res := o.res.(*v1.GetScanMetadataResponse)
		held := a.held(t, path)
		if len(res.GetInstances()) != len(held) || res.GetNextPageToken() != "" {
			t.Fatalf("%v: %d instances and token %q, want %d and none", path, len(res.GetInstances()), res.GetNextPageToken(), len(held))
		}
		sum := sha256.Sum256(a.object(t, mustKey(t, path)))
		if res.GetManifestSha256() != hex.EncodeToString(sum[:]) {
			t.Errorf("%v: manifest_sha256 %s, the manifest on the storage has %x", path, res.GetManifestSha256(), sum)
		}
		for i, m := range res.GetInstances() {
			id := held[i].SOPInstanceUID
			file := a.sent[id]
			if m.GetInstanceId() != id || m.GetSopInstanceUid() != id {
				t.Fatalf("%v: instance %d is %q / %q, want %q", path, i, m.GetInstanceId(), m.GetSopInstanceUid(), id)
			}
			h, err := header.Read(bytes.NewReader(file), int64(len(file)))
			if err != nil {
				t.Fatal(err)
			}
			if m.GetTransferSyntaxUid() != h.TransferSyntaxUID || m.GetJsonTooLarge() || m.GetBulkDataOmitted() {
				t.Errorf("%v %s: transfer syntax %q too_large %v omitted %v, want %q false false", path, id, m.GetTransferSyntaxUid(), m.GetJsonTooLarge(), m.GetBulkDataOmitted(), h.TransferSyntaxUID)
			}
			set := dataset(t, m.GetDicomJson())
			for _, key := range []string{"00080018", "0020000D", "0020000E"} {
				if set[key] == nil {
					t.Errorf("%v %s: the answer has no %s", path, id, key)
				}
			}
			if set["00020010"] != nil || set["FFFCFFFC"] != nil {
				t.Errorf("%v %s: the answer holds file meta information or trailing padding", path, id)
			}
			px := set["7FE00010"]
			if px == nil {
				t.Fatalf("%v %s: the answer has no Pixel Data", path, id)
			}
			if _, inline := px["InlineBinary"]; inline || px["Value"] != nil || px["BulkDataURI"] == nil {
				t.Fatalf("%v %s: Pixel Data is %v, want a BulkDataURI alone", path, id, px)
			}
			checkPixelBytes(t, px, path, id, file, h.TransferSyntaxUID)
		}
	}
}

// mustKey returns the key of a scan's manifest.
func mustKey(t testing.TB, p label.Path) string {
	t.Helper()
	k, err := layout.ManifestKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// checkPixelBytes checks a Pixel Data reference against the file that was
// received, as the contract defines it: the bytes from offset are the
// value field of the element, whose header ends there; an encapsulated
// value starts with its first item and its length runs through the
// Sequence Delimitation Item.
func checkPixelBytes(t testing.TB, px map[string]any, p label.Path, id string, file []byte, ts string) {
	t.Helper()
	s, _ := px["BulkDataURI"].(string)
	u := parseURI(t, s, p, id)
	hdr := int64(12)
	if ts == "1.2.840.10008.1.2" {
		hdr = 8
	}
	if !bytes.Equal(file[u.offset-hdr:u.offset-hdr+4], []byte{0xE0, 0x7F, 0x10, 0x00}) {
		t.Errorf("%s: the bytes before offset %d are not the header of Pixel Data", id, u.offset)
	}
	encapsulated := strings.HasPrefix(ts, "1.2.840.10008.1.2.4.") || ts == "1.2.840.10008.1.2.5"
	value := bulkOf(t, px, p, id, file)
	if encapsulated {
		if !bytes.HasPrefix(value, []byte{0xFE, 0xFF, 0x00, 0xE0}) || !bytes.HasSuffix(value, []byte{0xFE, 0xFF, 0xDD, 0xE0, 0, 0, 0, 0}) {
			t.Errorf("%s: an encapsulated value does not run from its first item through its delimiter", id)
		}
	} else if want := binary.LittleEndian.Uint32(file[u.offset-4 : u.offset]); int64(want) != u.length && hdr == 12 {
		t.Errorf("%s: length %d, the element states %d", id, u.length, want)
	}
}

// TestMetadataReadsFromTheFileWhatTheSidecarDoesNotHold guards the bounded
// read of an instance's header: a binary value over 1 KiB and a text value
// over 64 KiB, whose place the metadata file does not keep, are answered
// as BulkDataURIs naming exactly their bytes in the file, and an element
// after the Pixel Data (trailing padding) is left out.
func TestMetadataReadsFromTheFileWhatTheSidecarDoesNotHold(t *testing.T) {
	f := newPlaceFixture(t)
	files, synthetic := metaFiles(t)
	a := receiveArchive(t, f, files)
	_, bin, text := privateBulk()
	id := uidOf(t, synthetic)
	var path label.Path
	for _, p := range a.scans(t) {
		for _, h := range a.held(t, p) {
			if h.SOPInstanceUID == id {
				path = p
			}
		}
	}
	o := call(f.as(f.lab1Reader).metadata.GetInstanceMetadata, instanceRequest(path, id))
	if o.err != nil {
		t.Fatal(o.err)
	}
	m := o.res.(*v1.GetInstanceMetadataResponse).GetInstance()
	set := dataset(t, m.GetDicomJson())
	for _, c := range []struct {
		key  string
		vr   string
		want []byte
	}{{"7FDF1001", "OB", bin}, {"7FDF1002", "UT", text}} {
		el := set[c.key]
		if el == nil || el["vr"] != c.vr || el["Value"] != nil || el["InlineBinary"] != nil {
			t.Fatalf("%s: %v, want a BulkDataURI alone", c.key, el)
		}
		if got := bulkOf(t, el, path, id, synthetic); !bytes.Equal(got, c.want) {
			t.Errorf("%s: the bytes at the URI are not the value that was sent", c.key)
		}
	}
	if set["7FDF0010"] == nil || set["7FDF0010"]["Value"] == nil {
		t.Errorf("the private creator, a short value, is not inlined: %v", set["7FDF0010"])
	}
	if set["FFFCFFFC"] != nil {
		t.Error("trailing padding is answered")
	}
	checkPixelBytes(t, set["7FE00010"], path, id, synthetic, m.GetTransferSyntaxUid())
}

// TestMetadataPagesAreWholeAndOrdered guards paging: pages of three over a
// scan of four join, in order of instance id, to the one answer of the
// scan, a page token made for another scan is invalid, and a page size of
// 1001 is refused.
func TestMetadataPagesAreWholeAndOrdered(t *testing.T) {
	f := newPlaceFixture(t)
	files, _ := metaFiles(t)
	a := receiveArchive(t, f, files)
	md := f.as(f.lab1Reader).metadata
	var big, other label.Path
	for _, p := range a.scans(t) {
		if len(a.held(t, p)) == 4 {
			big = p
		} else {
			other = p
		}
	}
	if big == (label.Path{}) {
		t.Fatal("no scan holds four instances")
	}
	whole := mustCall[*v1.GetScanMetadataResponse](t, "whole", call(md.GetScanMetadata, scanRequest(big)))
	var got []*v1.InstanceMetadata
	req := scanRequest(big)
	req.PageSize = 3
	for i := 0; ; i++ {
		res := mustCall[*v1.GetScanMetadataResponse](t, "page", call(md.GetScanMetadata, req))
		got = append(got, res.GetInstances()...)
		if res.GetNextPageToken() == "" {
			if i != 1 {
				t.Errorf("the scan ended after %d pages, want 2", i+1)
			}
			break
		}
		if i > 2 {
			t.Fatal("the pages do not end")
		}
		req.PageToken = res.GetNextPageToken()
	}
	if len(got) != 4 || len(whole.GetInstances()) != 4 {
		t.Fatalf("%d instances paged, %d whole, want 4", len(got), len(whole.GetInstances()))
	}
	ids := make([]string, 0, 4)
	for i, m := range got {
		ids = append(ids, m.GetInstanceId())
		if m.GetDicomJson() != whole.GetInstances()[i].GetDicomJson() {
			t.Errorf("instance %d differs between the pages and the whole answer", i)
		}
	}
	if !slices.IsSorted(ids) {
		t.Errorf("instance ids %v are not in byte order", ids)
	}
	first := scanRequest(big)
	first.PageSize = 1
	token := mustCall[*v1.GetScanMetadataResponse](t, "first", call(md.GetScanMetadata, first)).GetNextPageToken()
	foreign := scanRequest(other)
	foreign.PageToken = token
	if o := call(md.GetScanMetadata, foreign); o.code() != connect.CodeInvalidArgument || !strings.Contains(o.message(), "page_token") {
		t.Errorf("a token made for another scan: %v, want invalid_argument naming page_token", o.err)
	}
	for _, size := range []int32{1001, -1} {
		r := scanRequest(big)
		r.PageSize = size
		if o := call(md.GetScanMetadata, r); o.code() != connect.CodeInvalidArgument || !strings.Contains(o.message(), "page_size") {
			t.Errorf("page_size %d: %v, want invalid_argument naming page_size", size, o.err)
		}
	}
}

// testGetInstanceMetadata guards GetInstanceMetadata: the
// header of one instance is what its scan's page holds for it, and the
// instance named by its id in another scan is not found.
func testGetInstanceMetadata(t *testing.T) {
	f := newPlaceFixture(t)
	files, _ := metaFiles(t)
	a := receiveArchive(t, f, files)
	md := f.as(f.lab1Reader).metadata
	scans := a.scans(t)
	for _, path := range scans {
		whole := mustCall[*v1.GetScanMetadataResponse](t, "scan", call(md.GetScanMetadata, scanRequest(path)))
		for _, want := range whole.GetInstances() {
			got := mustCall[*v1.GetInstanceMetadataResponse](t, "instance", call(md.GetInstanceMetadata, instanceRequest(path, want.GetInstanceId())))
			if got.GetInstance().GetDicomJson() != want.GetDicomJson() || got.GetInstance().GetTransferSyntaxUid() != want.GetTransferSyntaxUid() {
				t.Errorf("%v %s: the instance's answer differs from its entry in the scan's", path, want.GetInstanceId())
			}
		}
	}
	other := a.held(t, scans[1])[0].SOPInstanceUID
	if o := call(md.GetInstanceMetadata, instanceRequest(scans[0], other)); o.code() != connect.CodeNotFound {
		t.Errorf("an instance of another scan: %v, want not_found", o.err)
	}
}

// testGetScanMetadata guards what is not answered: no key, no grant, a
// scan or an instance the tenant does not hold, and a request that is not
// valid; and that a read that answers is audited in the tenant's log with
// the scan's path and the count of instances.
func testGetScanMetadata(t *testing.T) {
	f := newPlaceFixture(t)
	files, _ := metaFiles(t)
	a := receiveArchive(t, f, files)
	path := a.scans(t)[0]
	id := a.held(t, path)[0].SOPInstanceUID
	scan := func(mod func(*v1.GetScanMetadataRequest)) func(caller) outcome {
		return func(c caller) outcome {
			r := scanRequest(path)
			mod(r)
			return call(f.as(c).metadata.GetScanMetadata, r)
		}
	}
	instance := func(mod func(*v1.GetInstanceMetadataRequest)) func(caller) outcome {
		return func(c caller) outcome {
			r := instanceRequest(path, id)
			mod(r)
			return call(f.as(c).metadata.GetInstanceMetadata, r)
		}
	}
	none := func(*v1.GetScanMetadataRequest) {}
	for _, c := range []struct {
		name string
		as   caller
		do   func(caller) outcome
		code connect.Code
		msg  string
	}{
		{"scan without a key", nobody, scan(none), connect.CodeUnauthenticated, ""},
		{"scan without a grant", f.noGrant, scan(none), connect.CodePermissionDenied, ""},
		{"scan with a grant on another tenant", f.lab2Admin, scan(none), connect.CodePermissionDenied, ""},
		{"instance without a grant", f.noGrant, instance(func(*v1.GetInstanceMetadataRequest) {}), connect.CodePermissionDenied, ""},
		{"a scan that is not there", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.ScanLabel = "nothere" }), connect.CodeNotFound, "no scan"},
		{"a session that is not there", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.SessionLabel = "nothere" }), connect.CodeNotFound, "no scan"},
		{"a subject that is not there", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.SubjectLabel = "nothere" }), connect.CodeNotFound, "no scan"},
		{"a label in another case", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.SubjectLabel = strings.ToLower(r.SubjectLabel) + "x" }), connect.CodeNotFound, "no scan"},
		{"an instance that is not there", f.lab1Reader, instance(func(r *v1.GetInstanceMetadataRequest) { r.InstanceId = "1.2.3.4.5" }), connect.CodeNotFound, "no instance"},
		{"a scan label that is not one", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.ScanLabel = "Smith^John" }), connect.CodeInvalidArgument, "label"},
		{"no subject", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.SubjectLabel = "" }), connect.CodeInvalidArgument, "label"},
		{"an instance id that is not a UID", f.lab1Reader, instance(func(r *v1.GetInstanceMetadataRequest) { r.InstanceId = "../x" }), connect.CodeInvalidArgument, "instance_id"},
		{"no instance id", f.lab1Reader, instance(func(r *v1.GetInstanceMetadataRequest) { r.InstanceId = "" }), connect.CodeInvalidArgument, "instance_id"},
		{"a page token that is not one", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.PageToken = "x" }), connect.CodeInvalidArgument, "page_token"},
		{"a page token over 1 KiB", f.lab1Reader, scan(func(r *v1.GetScanMetadataRequest) { r.PageToken = strings.Repeat("x", 1025) }), connect.CodeInvalidArgument, "page_token"},
		{"a tenant that is not a name", f.lab1Reader, func(c caller) outcome {
			r := scanRequest(path)
			r.Tenant = "Lab 1"
			return call(f.as(c).metadata.GetScanMetadata, r)
		}, connect.CodePermissionDenied, ""},
	} {
		o := c.do(c.as)
		if o.code() != c.code || !strings.Contains(o.message(), c.msg) {
			t.Errorf("%s: %v, want %v with %q", c.name, o.err, c.code, c.msg)
		}
	}
	// An admin who may list every tenant is told a tenant does not exist.
	r := scanRequest(path)
	r.Tenant = "nolab"
	if o := call(f.as(f.instAdmin).metadata.GetScanMetadata, r); o.code() != connect.CodeNotFound {
		t.Errorf("a tenant that does not exist, for an installation admin: %v, want not_found", o.err)
	}

	o := scan(none)(f.lab1Reader)
	n := len(mustCall[*v1.GetScanMetadataResponse](t, "scan", o).GetInstances())
	f.checkAudit(t, "GetScanMetadata", o.requestID, wantAudit{log: "lab1", outcome: master.OutcomeDone, tenant: "lab1",
		object: path.Subject + "/" + path.Session + "/" + path.Scan, details: map[string]string{"instances": strconv.Itoa(n)}})
	o = instance(func(*v1.GetInstanceMetadataRequest) {})(f.lab1Reader)
	mustCall[*v1.GetInstanceMetadataResponse](t, "instance", o)
	f.checkAudit(t, "GetInstanceMetadata", o.requestID, wantAudit{log: "lab1", outcome: master.OutcomeDone, tenant: "lab1",
		object: path.Subject + "/" + path.Session + "/" + path.Scan, details: map[string]string{"instance": "present"}})
	for _, en := range f.tenantLog("lab1") {
		for k, v := range en.Details {
			if strings.Contains(v, id) {
				t.Errorf("an entry's detail %s holds the instance id", k)
			}
		}
	}
}

// hugeHeader returns the MR file with a private sequence of n items before
// its pixel data, so its DICOM JSON is about 70 bytes an item, and the SOP
// Instance UID ending in last.
func hugeHeader(t testing.TB, n int, last byte) []byte {
	t.Helper()
	var items []byte
	elem := explicitShort(0x0008, 0x1155, "UI", []byte("1.2.840.10008.5.1.4.1.1.2\x00"))
	for range n {
		items = binary.LittleEndian.AppendUint16(items, 0xFFFE)
		items = binary.LittleEndian.AppendUint16(items, 0xE000)
		items = binary.LittleEndian.AppendUint32(items, uint32(len(elem))) // #nosec G115 -- a test's small value.
		items = append(items, elem...)
	}
	extra := append(explicitShort(0x7FDF, 0x0010, "LO", []byte("CORIA TEST ")), explicitLong(0x7FDF, 0x1010, "SQ", items)...)
	return reUID(t, insertBeforePixelData(t, corpusFile(t, "MR_small.dcm"), extra), last)
}

// TestMetadataSizes guards the bounds of an answer: an instance whose DICOM
// JSON passes 1 MiB is answered without it and says so, never cut; a page
// stops before its DICOM JSON passes 3 MiB, with a token that resumes with
// the instance it stopped before, so no instance is lost or repeated.
func TestMetadataSizes(t *testing.T) {
	f := newPlaceFixture(t)
	// Four instances of about 0.85 MiB each: three fit a page, four do
	// not. One of 1.2 MiB is too large to answer.
	files := [][]byte{hugeHeader(t, 12500, '1'), hugeHeader(t, 12500, '2'), hugeHeader(t, 12500, '3'), hugeHeader(t, 12500, '4'), hugeHeader(t, 17000, '5')}
	a := receiveArchive(t, f, files)
	md := f.as(f.lab1Reader).metadata
	path := a.scans(t)[0]
	res := mustCall[*v1.GetScanMetadataResponse](t, "first page", call(md.GetScanMetadata, scanRequest(path)))
	total := 0
	for _, m := range res.GetInstances() {
		total += len(m.GetDicomJson())
	}
	if total > 3<<20 || len(res.GetInstances()) != 3 || res.GetNextPageToken() == "" {
		t.Fatalf("the first page holds %d instances and %d bytes of JSON with token %q, want 3, at most 3 MiB, and a token", len(res.GetInstances()), total, res.GetNextPageToken())
	}
	for _, m := range res.GetInstances() {
		if m.GetJsonTooLarge() || len(m.GetDicomJson()) < 700<<10 || len(m.GetDicomJson()) > 1<<20 {
			t.Errorf("%s: %d bytes of JSON, too_large %v", m.GetInstanceId(), len(m.GetDicomJson()), m.GetJsonTooLarge())
		}
	}
	r := scanRequest(path)
	r.PageToken = res.GetNextPageToken()
	rest := mustCall[*v1.GetScanMetadataResponse](t, "second page", call(md.GetScanMetadata, r))
	seen := map[string]bool{}
	for _, m := range append(slices.Clone(res.GetInstances()), rest.GetInstances()...) {
		if seen[m.GetInstanceId()] {
			t.Errorf("%s is answered twice", m.GetInstanceId())
		}
		seen[m.GetInstanceId()] = true
	}
	if len(seen) != 5 || rest.GetNextPageToken() != "" {
		t.Fatalf("the pages hold %d instances, next %q, want 5 and the end", len(seen), rest.GetNextPageToken())
	}
	tooLarge := 0
	for _, m := range rest.GetInstances() {
		if m.GetJsonTooLarge() {
			tooLarge++
			if m.GetDicomJson() != "" || m.GetTransferSyntaxUid() == "" {
				t.Errorf("%s: too large but holds %d bytes of JSON and transfer syntax %q", m.GetInstanceId(), len(m.GetDicomJson()), m.GetTransferSyntaxUid())
			}
		}
	}
	if tooLarge != 1 {
		t.Errorf("%d instances are too large, want 1", tooLarge)
	}
}

// TestMetadataDataLoss guards what the catalog holds and the storage does
// not: a scan whose metadata file is gone, and an instance whose file is
// gone, are data loss, never not found and never an empty answer.
func TestMetadataDataLoss(t *testing.T) {
	f := newPlaceFixture(t)
	files, _ := metaFiles(t)
	a := receiveArchive(t, f, files)
	md := f.as(f.lab1Reader).metadata
	var path label.Path
	for _, p := range a.scans(t) {
		if len(a.held(t, p)) > 1 {
			path = p
		}
	}
	held := a.held(t, path)
	// A file of the storage is removed behind the archive's back.
	root := filepath.Join(f.root, "lab1", "mri")
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(held[0].Key))); err != nil {
		t.Fatal(err)
	}
	if o := call(md.GetInstanceMetadata, instanceRequest(path, held[0].SOPInstanceUID)); o.code() != connect.CodeDataLoss {
		t.Errorf("an instance whose file is gone: %v, want data_loss", o.err)
	}
	if o := call(md.GetScanMetadata, scanRequest(path)); o.code() != connect.CodeDataLoss {
		t.Errorf("a scan one of whose files is gone: %v, want data_loss", o.err)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(sidecarKey(t, path)))); err != nil {
		t.Fatal(err)
	}
	if o := call(md.GetScanMetadata, scanRequest(path)); o.code() != connect.CodeDataLoss || !strings.Contains(o.message(), "metadata file") {
		t.Errorf("a scan whose metadata file is gone: %v, want data_loss naming the metadata file", o.err)
	}
	if o := call(md.GetInstanceMetadata, instanceRequest(path, held[1].SOPInstanceUID)); o.code() != connect.CodeDataLoss {
		t.Errorf("an instance of a scan whose metadata file is gone: %v, want data_loss", o.err)
	}
}

// storedInstance puts file on a directory storage under the key of the
// instance it holds and returns the catalog's row for it and the line of
// the metadata file for it, which holds no data set, as one does that was
// archived before the file kept it.
func storedInstance(t *testing.T, file []byte) (blob.Store, label.Path, tenant.ScanInstance, sidecar.Instance) {
	t.Helper()
	store, err := dir.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	path := label.Path{Subject: "S1", Session: "E1", Scan: "T1"}
	uid := uidOf(t, file)
	key, err := layout.Key{Kind: layout.Instance, Path: path, SOPInstanceUID: uid}.String()
	if err != nil {
		t.Fatal(err)
	}
	w, err := store.Put(context.Background(), key, bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	held := tenant.ScanInstance{SOPInstanceUID: uid, Key: key, Length: w.Size, Size: w.Size, SHA256: tenant.SHA256(w.SHA256)}
	return store, path, held, sidecar.Instance{SOPInstanceUID: uid, Key: key, Size: w.Size, SHA256: held.SHA256.String(), DatasetOmitted: true}
}

// TestMetadataOfADeflatedFile guards the deflated transfer syntax, where no
// offset in the file names a value: the instance is answered with every
// value that would be bulk data left out, attribute and all, and
// bulk_data_omitted set, while the rest of its header is answered.
func TestMetadataOfADeflatedFile(t *testing.T) {
	file := deflatedFile(t, corpusFile(t, "MR_small.dcm"), '0')
	store, path, held, line := storedInstance(t, file)
	got, err := api.MetaInstance(context.Background(), store, "lab1", path, 1<<20, held, line)
	if err != nil {
		t.Fatal(err)
	}
	set := dataset(t, got.GetDicomJson())
	if got.GetTransferSyntaxUid() != "1.2.840.10008.1.2.1.99" || !got.GetBulkDataOmitted() || got.GetJsonTooLarge() {
		t.Errorf("transfer syntax %q, omitted %v, too large %v", got.GetTransferSyntaxUid(), got.GetBulkDataOmitted(), got.GetJsonTooLarge())
	}
	if set["7FE00010"] != nil || strings.Contains(got.GetDicomJson(), "BulkDataURI") {
		t.Errorf("a deflated file's answer refers to bulk data: %.200s", got.GetDicomJson())
	}
	if set["00100020"] == nil || set["00280010"] == nil {
		t.Errorf("the rest of the header is not answered: %.200s", got.GetDicomJson())
	}
}

// TestDeflatedFileIsReceivedArchivedAndAnswered guards a deflated file going
// through real reception: its pixel data lies at a place counted in inflated
// bytes, which the scan's metadata file cannot hold, so the file must still
// end archived byte for byte (not retried for ever) with no pixel data place
// and its transfer syntax recorded, and the service must answer it with bulk
// data omitted.
func TestDeflatedFileIsReceivedArchivedAndAnswered(t *testing.T) {
	file := deflatedFile(t, corpusFile(t, "MR_small.dcm"), '0')
	f := newPlaceFixture(t)
	a := receiveArchive(t, f, [][]byte{file})
	scans := a.scans(t)
	if len(scans) != 1 {
		t.Fatalf("%d scans, want 1", len(scans))
	}
	held := a.held(t, scans[0])
	if len(held) != 1 {
		t.Fatalf("%d instances held, want 1", len(held))
	}
	if got := a.object(t, held[0].Key); !bytes.Equal(got, file) {
		t.Errorf("the archived file is %d bytes and differs from the %d bytes received", len(got), len(file))
	}
	key, err := layout.MetadataKey(scans[0])
	if err != nil {
		t.Fatal(err)
	}
	rd := sidecar.NewReader(bytes.NewReader(a.object(t, key)))
	in, _, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if in.PixelData != nil || in.TransferSyntax != "1.2.840.10008.1.2.1.99" {
		t.Errorf("the metadata file's line has pixel data %+v and transfer syntax %q, want none and the deflated syntax", in.PixelData, in.TransferSyntax)
	}
	md := f.as(f.lab1Reader).metadata
	res := mustCall[*v1.GetScanMetadataResponse](t, "scan", call(md.GetScanMetadata, scanRequest(scans[0])))
	if len(res.GetInstances()) != 1 {
		t.Fatalf("%d instances answered, want 1", len(res.GetInstances()))
	}
	m := res.GetInstances()[0]
	if m.GetTransferSyntaxUid() != "1.2.840.10008.1.2.1.99" || !m.GetBulkDataOmitted() || m.GetJsonTooLarge() {
		t.Errorf("transfer syntax %q, omitted %v, too large %v", m.GetTransferSyntaxUid(), m.GetBulkDataOmitted(), m.GetJsonTooLarge())
	}
	if strings.Contains(m.GetDicomJson(), "BulkDataURI") || dataset(t, m.GetDicomJson())["00100020"] == nil {
		t.Errorf("the answer refers to bulk data or lacks the header: %.200s", m.GetDicomJson())
	}
}

// TestMetadataHeaderLimit guards the bound of a header read from a file:
// with receiver.header-limit under what the file's header takes, the
// instance is answered without its JSON, with json_too_large set and its
// transfer syntax, and with the limit raised it is answered whole.
func TestMetadataHeaderLimit(t *testing.T) {
	store, path, held, line := storedInstance(t, corpusFile(t, "MR_small.dcm"))
	small, err := api.MetaInstance(context.Background(), store, "lab1", path, 1024, held, line)
	if err != nil {
		t.Fatal(err)
	}
	if !small.GetJsonTooLarge() || small.GetDicomJson() != "" || small.GetTransferSyntaxUid() != "1.2.840.10008.1.2.1" {
		t.Errorf("under the bound: too large %v, %d bytes of JSON, transfer syntax %q", small.GetJsonTooLarge(), len(small.GetDicomJson()), small.GetTransferSyntaxUid())
	}
	whole, err := api.MetaInstance(context.Background(), store, "lab1", path, 1<<20, held, line)
	if err != nil || whole.GetJsonTooLarge() || !strings.Contains(whole.GetDicomJson(), "7FE00010") {
		t.Errorf("within the bound: %v, too large %v", err, whole.GetJsonTooLarge())
	}
}

// TestMetadataAuditFacts guards what the audit entries of the two methods
// name before the handler runs: reader on the tenant the request names, the
// scan's path of labels as the object with a label that is not one written
// "invalid", and an instance id recorded only as present, never as given.
func TestMetadataAuditFacts(t *testing.T) {
	var m api.Methods
	const uid = "2.25.4711"
	scan, instance := coriav1connect.MetadataServiceGetScanMetadataProcedure, coriav1connect.MetadataServiceGetInstanceMetadataProcedure
	need, f := m.Requirement(instance, &v1.GetInstanceMetadataRequest{Tenant: "lab1", SubjectLabel: "S01", SessionLabel: "E01", ScanLabel: "T1", InstanceId: uid})
	if need != access.OnTenant("lab1", master.RoleReader) || f.Object != "S01/E01/T1" || f.Details["instance"] != "present" {
		t.Errorf("an instance's requirement %+v and facts %+v", need, f)
	}
	for k, v := range f.Details {
		if strings.Contains(v, uid) {
			t.Errorf("the detail %s holds the instance id: %q", k, v)
		}
	}
	need, f = m.Requirement(scan, &v1.GetScanMetadataRequest{Tenant: "lab1", SubjectLabel: "Smith^John", SessionLabel: "E01", ScanLabel: "T1"})
	if need != access.OnTenant("lab1", master.RoleReader) || f.Object != "invalid/E01/T1" {
		t.Errorf("a scan's requirement %+v and facts %+v", need, f)
	}
	if need, _ := m.Requirement(scan, &v1.GetScanMetadataRequest{Tenant: "Lab 1"}); need != access.OnInstallation(master.RoleReader) {
		t.Errorf("a malformed tenant needs %+v, want reader on the installation", need)
	}
}
