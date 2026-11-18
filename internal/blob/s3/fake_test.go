package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	fakeBucket = "fake-bucket"
	fakeKey    = "AKIAFAKETESTKEY0000"
	fakeSecret = "fake/secret+value/for/tests/only0000000000"
)

// request is what a fake server saw of one request.
type request struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   []byte
}

// fakeS3 is a small in-memory S3 server for what the real servers cannot be
// made to do: answer wrongly, slowly or with a redirect, and show exactly
// what the driver sent. It holds objects and multipart uploads, and a hook
// that may answer a request itself first.
type fakeS3 struct {
	*httptest.Server

	mu      sync.Mutex
	seen    []request
	objects map[string][]byte
	uploads map[string]map[int][]byte
	aborted int
	nextID  int

	// hook, when set, may answer a request, returning true when it did.
	hook   func(w http.ResponseWriter, r *http.Request, n int) bool
	secure bool
	// badCRC makes every GET carry a CRC32 that is not the object's.
	badCRC atomic.Bool
	// badEcho makes every PUT answer with a checksum that is not the one sent.
	badEcho atomic.Bool
	// hits counts requests, for hooks that fail the first few.
	hits atomic.Int64
}

func newFakeS3(tb testing.TB) *fakeS3 {
	tb.Helper()
	f := &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	tb.Cleanup(f.Close)
	return f
}

// newFakeS3TLS is a fake that speaks https with a certificate of its own,
// which config trusts through Config.CABundle.
func newFakeS3TLS(tb testing.TB) *fakeS3 {
	tb.Helper()
	f := &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	f.secure = true
	tb.Cleanup(f.Close)
	return f
}

// config returns a configuration that reaches the fake.
func (f *fakeS3) config() Config {
	c := Config{
		Endpoint: f.URL, Region: "us-east-1", Bucket: fakeBucket, PathStyle: true,
		AccessKeyID: fakeKey, SecretAccessKey: fakeSecret, AllowHTTP: true, AllowAddress: allowAll,
	}
	if f.secure {
		c.CABundle = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Certificate().Raw})
	}
	return c
}

func (f *fakeS3) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.seen...)
}

// count returns how many requests had this method and, when sub is not
// empty, a query that has the parameter sub.
func (f *fakeS3) count(method, sub string) int {
	n := 0
	for _, r := range f.requests() {
		if r.method != method {
			continue
		}
		if _, ok := r.query[sub]; sub == "" || ok {
			n++
		}
	}
	return n
}

func xmlError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message><RequestId>REQ123</RequestId></Error>`, code, message)
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	n := int(f.hits.Add(1))
	f.mu.Lock()
	f.seen = append(f.seen, request{r.Method, r.URL.Path, r.URL.Query(), r.Header.Clone(), body})
	hook := f.hook
	f.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if hook != nil && hook(w, r, n) {
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/"+fakeBucket+"/")
	q := r.URL.Query()
	w.Header().Set("Connection", "close")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/"+fakeBucket+"/" || r.Method == http.MethodGet && r.URL.Path == "/"+fakeBucket:
		if _, ok := q["uploads"]; ok {
			fmt.Fprint(w, `<ListMultipartUploadsResult><Bucket>`+fakeBucket+`</Bucket><IsTruncated>false</IsTruncated></ListMultipartUploadsResult>`)
			return
		}
		fmt.Fprint(w, `<ListBucketResult><Name>`+fakeBucket+`</Name><KeyCount>0</KeyCount><MaxKeys>1</MaxKeys><IsTruncated>false</IsTruncated></ListBucketResult>`)
	case r.Method == http.MethodPut && q.Get("partNumber") != "":
		num, _ := strconv.Atoi(q.Get("partNumber"))
		f.mu.Lock()
		if u := f.uploads[q.Get("uploadId")]; u != nil {
			u[num] = body
		}
		f.mu.Unlock()
		w.Header().Set("ETag", `"part`+q.Get("partNumber")+`"`)
		w.Header().Set("x-amz-checksum-sha256", f.echo(r))
	case r.Method == http.MethodPut:
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		w.Header().Set("ETag", `"obj"`)
		w.Header().Set("x-amz-checksum-sha256", f.echo(r))
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.mu.Lock()
		f.nextID++
		id := "upload" + strconv.Itoa(f.nextID)
		f.uploads[id] = map[int][]byte{}
		f.mu.Unlock()
		// #nosec G705 -- a test server answering with fixed text and its own names.
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, fakeBucket, key, id)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		f.mu.Lock()
		u := f.uploads[q.Get("uploadId")]
		var all []byte
		for i := 1; i <= len(u); i++ {
			all = append(all, u[i]...)
		}
		f.objects[key] = all
		delete(f.uploads, q.Get("uploadId"))
		f.mu.Unlock()
		// #nosec G705 -- a test server answering with fixed text and its own names.
		fmt.Fprintf(w, `<CompleteMultipartUploadResult><Location>x</Location><Bucket>%s</Bucket><Key>%s</Key><ETag>"done"</ETag></CompleteMultipartUploadResult>`, fakeBucket, key)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		f.mu.Lock()
		delete(f.uploads, q.Get("uploadId"))
		f.aborted++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		f.mu.Lock()
		obj, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			xmlError(w, http.StatusNotFound, "NoSuchKey", "no such key")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(obj)))
		if r.Header.Get("X-Test-Bad-Crc") != "" || f.badCRC.Load() {
			w.Header().Set("x-amz-checksum-crc32", "AAAAAA==")
		}
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(obj)
	default:
		xmlError(w, http.StatusNotImplemented, "NotImplemented", "not implemented")
	}
}

// echo is the checksum a PUT's answer carries: the one sent, or, when
// badEcho is set, another, as a server that stored other bytes would give.
func (f *fakeS3) echo(r *http.Request) string {
	if f.badEcho.Load() {
		return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	}
	return r.Header.Get("x-amz-checksum-sha256")
}

// state returns how many uploads were aborted, how many are unfinished and
// how many objects the fake holds.
func (f *fakeS3) state() (aborted, unfinished, objects int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aborted, len(f.uploads), len(f.objects)
}

// sha256b64 is a checksum as S3 writes it.
func sha256b64(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// openFake opens a store on f.
func openFake(tb testing.TB, f *fakeS3, mod func(*Config)) *Store {
	tb.Helper()
	cfg := f.config()
	if mod != nil {
		mod(&cfg)
	}
	s, err := Open(context.Background(), cfg)
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	return s
}
