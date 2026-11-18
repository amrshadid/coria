package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/fault"
)

// Store is a blob.Store on a bucket. Open makes one.
type Store struct {
	set    settings
	client *awss3.Client
	tr     *http.Transport
	closed atomic.Bool
}

var _ blob.Store = (*Store)(nil)

// Open checks cfg, connects to the bucket, aborts the unfinished uploads a
// crash left under the prefix (see Config.StaleUploadAge), and returns the
// store. It fails as Invalid when cfg is wrong or the bucket does not exist
// or refuses the credentials, and as Unavailable when the server cannot be
// reached or the address check refuses it.
//
// The cleanup of old uploads is best effort: an account that may not list
// uploads still opens, and relies on the bucket's lifecycle rule.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	set, err := cfg.check()
	if err != nil {
		return nil, err
	}
	s := &Store{set: set, tr: newTransport(set)}
	s.client = awss3.New(clientOptions(set, s.tr))
	if err := s.probe(ctx); err != nil {
		s.tr.CloseIdleConnections()
		return nil, err
	}
	if set.staleAge > 0 {
		if _, err := s.AbortStaleUploads(ctx); err != nil && !mayNotList(err) {
			s.tr.CloseIdleConnections()
			return nil, err
		}
	}
	return s, nil
}

// clientOptions builds the SDK client's options from nothing but set. It is
// the one place a client is configured, so what the driver does not do (read
// the environment, follow a redirect, use a proxy, send a checksum a server
// may not know) is visible in one function.
func clientOptions(set settings, tr *http.Transport) awss3.Options {
	o := awss3.Options{
		Region:       set.region,
		UsePathStyle: set.pathStyle,
		HTTPClient:   &http.Client{Transport: tr, CheckRedirect: noRedirect},
		// Static credentials, from the configuration and nowhere else.
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: set.accessKey, SecretAccessKey: set.secretKey, Source: "magnetar storage configuration"}, nil
		}),
		// Since early 2025 the SDK adds a CRC32 checksum to every upload as an
		// aws-chunked trailer, and validates one on every download, which
		// several servers other than Amazon's reject or cannot produce.
		// "When required" sends and checks one only where the API demands it;
		// the driver sets x-amz-checksum-sha256 itself on every upload.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		// Three attempts in all, backing off exponentially to five seconds,
		// for what is safe to repeat: every request here is idempotent, a
		// body is a buffer the SDK can send again, and a refused address is
		// never retried. The SDK's own default is three attempts too, with
		// backoff up to twenty seconds and a retry budget that fails calls
		// when many fail at once; neither suits a service that needs a
		// prompt, clear answer.
		Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = 3
			o.MaxBackoff = 5 * time.Second
			o.RateLimiter = ratelimit.None
			o.Retryables = append([]retry.IsErrorRetryable{retry.IsErrorRetryableFunc(func(err error) aws.Ternary {
				if errors.Is(err, errAddressRefused) {
					return aws.FalseTernary
				}
				return aws.UnknownTernary
			})}, o.Retryables...)
		}),
	}
	if set.endpoint != nil {
		o.BaseEndpoint = aws.String(set.endpoint.String())
	}
	return o
}

// probe checks that the bucket can be listed under the prefix, which is what
// every other call depends on, so a wrong endpoint, bucket or key shows at
// Open and not at the first Put.
func (s *Store) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.set.callTimeout)
	defer cancel()
	in := &awss3.ListObjectsV2Input{Bucket: aws.String(s.set.bucket), MaxKeys: aws.Int32(1)}
	if s.set.prefix != "" {
		in.Prefix = aws.String(s.set.prefix)
	}
	if _, err := s.client.ListObjectsV2(ctx, in); err != nil {
		return openError(fail(ctx, err, "opening the storage"))
	}
	return nil
}

// openError turns the failures that mean the configuration is wrong, a
// bucket that does not exist and credentials refused, into Invalid, since
// trying again changes nothing; the rest stay as they are.
func openError(err error) error {
	var sc serverCode
	if errors.As(err, &sc) {
		switch sc {
		case "NoSuchBucket", "InvalidBucketName":
			return fault.Wrap(fault.Invalid, sc, "opening the storage failed: the bucket does not exist")
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccountProblem", "AllAccessDisabled":
			return fault.Wrap(fault.Invalid, sc, "opening the storage failed: the storage refused the credentials it was given or their permissions")
		}
	}
	return err
}

// begin is the start of every call: the store is open and ctx has not ended.
func (s *Store) begin(ctx context.Context, what string) error {
	if s.closed.Load() {
		return closedError()
	}
	if ctx.Err() != nil {
		return canceled(ctx, what)
	}
	return nil
}

// object returns the name of key in the bucket.
func (s *Store) object(key string) *string { return aws.String(s.set.prefix + key) }

// Close releases the store's idle connections. A stream from Read that is
// open may go on being read.
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.tr.CloseIdleConnections()
	return nil
}

// Usage says the volume's space is unknown: a bucket has no size to report,
// and what it holds is counted from the catalog.
func (s *Store) Usage(ctx context.Context) (blob.Usage, error) {
	if err := s.begin(ctx, "the usage call"); err != nil {
		return blob.Usage{}, err
	}
	return blob.Usage{}, nil
}

// Stat returns the size of key with a HEAD request.
func (s *Store) Stat(ctx context.Context, key string) (blob.Info, error) {
	if err := s.begin(ctx, "the stat"); err != nil {
		return blob.Info{}, err
	}
	if err := blob.CheckKey(key); err != nil {
		return blob.Info{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.set.callTimeout)
	defer cancel()
	out, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.set.bucket), Key: s.object(key)})
	if err != nil {
		return blob.Info{}, fail(ctx, err, "the stat")
	}
	return blob.Info{Size: aws.ToInt64(out.ContentLength)}, nil
}

// Delete removes key with a DELETE request, which S3 answers with success
// for a key that holds nothing, as the port requires.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.begin(ctx, "the delete"); err != nil {
		return err
	}
	if err := blob.CheckKey(key); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.set.callTimeout)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(s.set.bucket), Key: s.object(key)})
	if err == nil {
		return nil
	}
	if err = fail(ctx, err, "the delete"); fault.KindOf(err) == fault.NotFound {
		return nil
	}
	return err
}

// Read returns a range of key with a ranged GET.
func (s *Store) Read(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := s.begin(ctx, "the read"); err != nil {
		return nil, err
	}
	if err := blob.CheckKey(key); err != nil {
		return nil, err
	}
	if err := blob.CheckRange(offset, length); err != nil {
		return nil, err
	}
	if length == 0 {
		// A range of no bytes cannot be asked of S3. The size answers whether
		// the key exists and whether the offset is past the end.
		info, err := s.Stat(ctx, key)
		if err != nil {
			return nil, err
		}
		if offset > info.Size {
			return nil, blob.ErrPastEnd()
		}
		return io.NopCloser(eofReader{}), nil
	}
	in := &awss3.GetObjectInput{Bucket: aws.String(s.set.bucket), Key: s.object(key)}
	if offset > 0 || length > 0 {
		in.Range = aws.String(rangeHeader(offset, length))
	}
	rctx, cancel := context.WithCancel(ctx)
	out, err := s.client.GetObject(rctx, in)
	if err != nil {
		cancel()
		var ae smithy.APIError
		if errors.As(err, &ae) && ae.ErrorCode() == "InvalidRange" {
			return s.pastEnd(ctx, key, offset)
		}
		return nil, fail(ctx, err, "the read")
	}
	if in.Range != nil {
		// A server that ignores Range answers 200 with the whole object,
		// which would be taken for the range asked.
		if start, ok := rangeStart(aws.ToString(out.ContentRange)); !ok || start != offset {
			_ = out.Body.Close() // The answer is refused whatever closing says.
			cancel()
			return nil, fault.Wrap(fault.Unavailable, errMalformed, "the read failed: the storage did not answer the range asked")
		}
	}
	return &body{idle: s.set.idleTimeout, caller: ctx, cancel: cancel, rc: out.Body}, nil
}

// pastEnd handles a range S3 refused as unsatisfiable: with the offset at the
// end of the object the answer is an empty stream, past it Invalid. The
// object's size says which.
func (s *Store) pastEnd(ctx context.Context, key string, offset int64) (io.ReadCloser, error) {
	info, err := s.Stat(ctx, key)
	if err != nil {
		return nil, err
	}
	if offset > info.Size {
		return nil, blob.ErrPastEnd()
	}
	if offset == info.Size {
		return io.NopCloser(eofReader{}), nil
	}
	return nil, fault.Wrap(fault.Unavailable, errMalformed, "the read failed: the storage refused a range inside the object")
}

// rangeHeader writes the Range header for offset and length (-1 for to the
// end), with the end clamped so that offset+length-1 cannot overflow.
func rangeHeader(offset, length int64) string {
	const maxInt64 = 1<<63 - 1
	if length < 0 || length > maxInt64-offset {
		return fmt.Sprintf("bytes=%d-", offset)
	}
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
}

// rangeStart returns the first byte of a Content-Range header "bytes a-b/c".
func rangeStart(h string) (int64, bool) {
	var a, b, c int64
	if n, _ := fmt.Sscanf(h, "bytes %d-%d/%d", &a, &b, &c); n == 3 {
		return a, true
	}
	return 0, false
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// body is the stream of a Read. It ends the request's context when it is
// closed, and cuts the stream when the server stalls: a Read that moves no
// byte for idleTimeout fails, so a hung server cannot hold a caller that has
// no deadline of its own.
type body struct {
	idle   time.Duration
	caller context.Context
	cancel context.CancelFunc
	rc     io.ReadCloser
	once   sync.Once
}

func (b *body) Read(p []byte) (int, error) {
	t := time.AfterFunc(b.idle, b.cancel)
	n, err := b.rc.Read(p)
	stalled := !t.Stop()
	if err == nil || errors.Is(err, io.EOF) && !stalled {
		return n, err
	}
	switch {
	case b.caller.Err() != nil:
		return n, canceled(b.caller, "the read")
	case stalled:
		return n, fault.Wrap(fault.Unavailable, errTimedOut, "the read failed: the storage stopped sending the object")
	}
	return n, fault.Wrap(fault.Unavailable, errUnreachable, "the read failed: the storage could not be reached")
}

func (b *body) Close() error {
	var err error
	b.once.Do(func() {
		err = b.rc.Close()
		b.cancel()
	})
	if err != nil {
		return fault.Wrap(fault.Unavailable, errUnreachable, "the read's stream could not be closed")
	}
	return nil
}
