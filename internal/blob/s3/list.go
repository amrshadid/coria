package s3

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/fault"
)

// tokenTag begins every token this driver gives, so a token from anywhere
// else, or one that was damaged, is refused as Invalid without a request.
const tokenTag = "s3."

// maxToken bounds the length of a token read back; a key is at most 1024
// bytes.
const maxToken = 2048

// maxListCalls bounds the requests one page costs. A page costs one except
// when the server returns fewer entries than asked while saying more
// follow, when everything it returned is something the port cannot name, or
// when the page ends exactly where the listing might (see List).
const maxListCalls = 50

// entry is one name of a listing, a key or a prefix that ends in "/".
type entry struct {
	name     string // without the storage's prefix
	isPrefix bool
}

// List returns one page of keys with ListObjectsV2.
//
// The token is not the server's continuation token but the last entry
// returned, and the next page asks the server for the names after it
// (start-after). Two reasons. A continuation token says "more follows" when
// a page is exactly full, on some servers even when nothing does, and the
// port forbids a token with nothing after it; so one entry more than the
// page is asked for, and the extra proves what follows. And a name is what
// the port's stability rule is about: a key put behind the last entry is not
// seen and one ahead of it is, on every server, whatever a token encodes.
// After a prefix (a group of keys folded by the delimiter) the next page
// starts after every key the prefix could hold, which is "{", the byte after
// "z", the largest a key may contain.
//
// S3 returns keys in the byte order of their UTF-8 text, which for the
// characters a key may hold is the port's order. An object under the prefix
// that no key of this storage could name (a folder marker made by a
// console, a name with a space) is left out: the port cannot read or delete
// it.
func (s *Store) List(ctx context.Context, opts blob.ListOptions) (blob.Page, error) {
	if err := s.begin(ctx, "the listing"); err != nil {
		return blob.Page{}, err
	}
	if err := blob.CheckListOptions(opts); err != nil {
		return blob.Page{}, err
	}
	floor, err := decodeToken(opts.Token, opts.Prefix)
	if err != nil {
		return blob.Page{}, err
	}
	limit := blob.ListLimit(opts)
	var entries []entry
	cursor := floor.after()
	more := false
	for calls := 0; ; calls++ {
		if calls == maxListCalls {
			if len(entries) == 0 {
				return blob.Page{}, fault.Wrap(fault.Unavailable, errMalformed, "the listing failed: the storage returned no usable entries")
			}
			more = true
			break
		}
		in := &awss3.ListObjectsV2Input{
			Bucket:  aws.String(s.set.bucket),
			MaxKeys: aws.Int32(int32(min(limit+1-len(entries), blob.MaxListLimit))), // #nosec G115 -- from 1 to 1000.
		}
		if p := s.set.prefix + opts.Prefix; p != "" {
			in.Prefix = aws.String(p)
		}
		if opts.Delimiter != "" {
			in.Delimiter = aws.String(opts.Delimiter)
		}
		if cursor != "" {
			in.StartAfter = aws.String(s.set.prefix + cursor)
		}
		lctx, cancel := context.WithTimeout(ctx, s.set.callTimeout)
		out, err := s.client.ListObjectsV2(lctx, in)
		if err != nil {
			err = fail(lctx, err, "the listing")
			cancel()
			return blob.Page{}, err
		}
		cancel()
		var got []entry
		for _, o := range out.Contents {
			if k, ok := s.ours(aws.ToString(o.Key), false); ok {
				got = append(got, entry{name: k})
			}
		}
		for _, p := range out.CommonPrefixes {
			if k, ok := s.ours(aws.ToString(p.Prefix), true); ok {
				got = append(got, entry{name: k, isPrefix: true})
			}
		}
		slices.SortFunc(got, func(a, b entry) int { return strings.Compare(a.name, b.name) })
		for _, e := range got {
			if e.name > floor.after() && (len(entries) == 0 || e.name > entries[len(entries)-1].name) {
				entries = append(entries, e)
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		// Truncated: go on from the last name the server named, kept or not,
		// until there is an entry beyond the page, which proves there is
		// more, or the server says it has no more.
		if len(entries) > limit {
			break
		}
		last := lastName(out)
		if last == "" {
			// Truncated, yet it named nothing: take it as the end rather than
			// ask the same question again.
			break
		}
		rest, ok := strings.CutPrefix(last, s.set.prefix)
		if !ok {
			break
		}
		cursor = rest
	}
	if len(entries) > limit {
		entries = entries[:limit]
		more = true
	}
	var page blob.Page
	for _, e := range entries {
		if e.isPrefix {
			page.Prefixes = append(page.Prefixes, e.name)
		} else {
			page.Keys = append(page.Keys, e.name)
		}
	}
	if more && len(entries) > 0 {
		page.Next = encodeToken(entries[len(entries)-1])
	}
	return page, nil
}

// lastName returns, without the storage's prefix, where the next request
// should start after: the greatest of the keys and prefixes the server named.
func lastName(out *awss3.ListObjectsV2Output) string {
	last := ""
	for _, o := range out.Contents {
		if k := aws.ToString(o.Key); k > last {
			last = k
		}
	}
	for _, p := range out.CommonPrefixes {
		if k := aws.ToString(p.Prefix) + "{"; k > last {
			last = k
		}
	}
	return last
}

func encodeToken(e entry) string {
	kind := "k"
	if e.isPrefix {
		kind = "p"
	}
	return tokenTag + base64.RawURLEncoding.EncodeToString([]byte(kind+e.name))
}

// after returns the name a request starts after to get everything beyond e:
// the key itself, or, for a prefix, a name above every key the prefix holds.
func (e entry) after() string {
	switch {
	case e.name == "":
		return ""
	case e.isPrefix:
		return e.name + "{"
	}
	return e.name
}

// decodeToken returns the entry a token names, checking that it is one this
// driver made for a listing under prefix.
func decodeToken(t, prefix string) (entry, error) {
	if t == "" {
		return entry{}, nil
	}
	bad := fault.New(fault.Invalid, "storage listing: the token is not one this listing gave")
	raw, ok := strings.CutPrefix(t, tokenTag)
	if !ok || len(t) > maxToken {
		return entry{}, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) < 2 || (b[0] != 'k' && b[0] != 'p') {
		return entry{}, bad
	}
	e := entry{name: string(b[1:]), isPrefix: b[0] == 'p'}
	check := e.name
	if e.isPrefix {
		if check, ok = strings.CutSuffix(check, "/"); !ok {
			return entry{}, bad
		}
	}
	if blob.CheckKey(check) != nil || !strings.HasPrefix(e.name, prefix) {
		return entry{}, bad
	}
	return e, nil
}

// ours returns the key (or, for a common prefix, the prefix) the server named
// without the storage's prefix, and whether the port could have written it.
// A name outside the storage's prefix is never returned, whatever the
// server sent.
func (s *Store) ours(name string, isPrefix bool) (string, bool) {
	rest, ok := strings.CutPrefix(name, s.set.prefix)
	if !ok || rest == "" {
		return "", false
	}
	if isPrefix {
		trimmed, ok := strings.CutSuffix(rest, "/")
		if !ok || blob.CheckKey(trimmed) != nil {
			return "", false
		}
		return rest, true
	}
	if blob.CheckKey(rest) != nil {
		return "", false
	}
	return rest, true
}

// maxSweep bounds the unfinished uploads one cleanup looks at, so Open takes
// a bounded time on a bucket that holds very many.
const maxSweep = 1000

// AbortStaleUploads aborts the unfinished multipart uploads under the
// storage's prefix that were begun more than Config.StaleUploadAge ago, and
// returns how many it aborted. Open calls it once; a long-running server can
// call it now and then. It looks at one batch of at most 1000 uploads and
// touches only uploads whose key is one this storage could have written, so
// a bucket shared with other users is left as it is.
//
// A server that does not list uploads, or an account that may not, gives an
// error the caller may choose to ignore, and then depends on the bucket's
// lifecycle rule.
func (s *Store) AbortStaleUploads(ctx context.Context) (int, error) {
	if err := s.begin(ctx, "the cleanup"); err != nil {
		return 0, err
	}
	age := s.set.staleAge
	if age <= 0 {
		age = DefaultStaleUploadAge
	}
	out, err := s.listUploads(ctx, s.set.prefix)
	if err == nil && len(out) == 0 && s.set.prefix != "" {
		// Some servers (MinIO and its forks) match an upload listing's
		// prefix against the whole key, not its start, and answer nothing
		// for a prefix like ours. Ask for the bucket's uploads instead; the
		// filter below keeps to the ones that are ours.
		out, err = s.listUploads(ctx, "")
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range out {
		if u.Initiated == nil || time.Since(*u.Initiated) < age {
			continue
		}
		if _, ok := s.ours(aws.ToString(u.Key), false); !ok {
			continue
		}
		if err := s.abort(ctx, u.Key, u.UploadId); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// listUploads returns one batch of the bucket's unfinished uploads whose keys
// begin with prefix, at most maxSweep.
func (s *Store) listUploads(ctx context.Context, prefix string) ([]types.MultipartUpload, error) {
	lctx, cancel := context.WithTimeout(ctx, s.set.callTimeout)
	defer cancel()
	in := &awss3.ListMultipartUploadsInput{Bucket: aws.String(s.set.bucket), MaxUploads: aws.Int32(maxSweep)}
	if prefix != "" {
		in.Prefix = aws.String(prefix)
	}
	out, err := s.client.ListMultipartUploads(lctx, in)
	if err != nil {
		return nil, fail(lctx, err, "the cleanup of unfinished uploads")
	}
	return out.Uploads, nil
}

// mayNotList reports whether err means the account or the server cannot list
// uploads, which Open lives with.
func mayNotList(err error) bool {
	var sc serverCode
	if !errors.As(err, &sc) {
		return false
	}
	switch sc {
	case "AccessDenied", "NotImplemented", "MethodNotAllowed", "InvalidRequest", "InvalidArgument":
		return true
	}
	return false
}
