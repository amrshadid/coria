// Package s3 is the S3 storage driver: a blob.Store on a bucket of Amazon S3
// or of any server that speaks its API, below an optional prefix of the
// bucket. A key is an object name below that prefix, so the archive's tree
// can be read, listed and copied with any S3 tool.
//
// # Whole or not at all
//
// An object up to one part (8 MiB by default) is one PutObject request, which
// S3 applies whole or not at all. A larger one is a multipart upload: parts
// are sent in parallel from a bounded set of buffers, and the object appears
// only when CompleteMultipartUpload succeeds
// (docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html). The
// upload is aborted when a part fails, when the reader fails, when the
// context ends, and when the object is too large for 10,000 parts, so a Put
// that returns an error leaves no upload behind unless the process dies
// while it runs. The bytes of a Put are held in memory, one part per buffer
// and at most Config.Concurrency buffers at a time, never the whole object.
// Each request carries the SHA-256 of its bytes (x-amz-checksum-sha256), so
// a server that supports it refuses bytes damaged in transit; the SHA-256
// of the whole object is computed here as the bytes pass and is what Put
// returns.
//
// # Uploads left by a crash
//
// A process that dies midway leaves an unfinished multipart upload, whose
// parts cost storage until it is aborted. Open lists the unfinished uploads
// under the storage's prefix and aborts those begun more than
// Config.StaleUploadAge ago (a day by default), so another process still
// finishing a large Put is not cut off; it looks at one bounded batch, and
// AbortStaleUploads does the same on demand. A bucket should also carry a
// lifecycle rule that aborts incomplete multipart uploads after a few days
// (docs.aws.amazon.com/AmazonS3/latest/userguide/mpu-abort-incomplete-mpu-lifecycle-config.html),
// which covers an account that may not list uploads.
//
// # Credentials and addresses
//
// A storage uses only the access key it is given. The driver never reads the
// environment, the shared configuration or credential files, or instance
// metadata, and never uses a proxy from the environment: it does not import
// the SDK's configuration loader. The address of every connection it makes
// is passed to Config.AllowAddress after the name is resolved, in the
// dialer, so a name that resolves to an address the caller refuses is
// refused whenever it resolves so, at any call. A redirect is never
// followed: the server a storage is configured for is the only one it
// talks to. A connection already open is reused, as it was checked when it
// was made.
//
// # Errors
//
// Errors carry the port's kinds. Their text is the driver's own: the
// server's message, the endpoint's address, the bucket's host name and every
// part of a credential are left out of Error and of fault.Message, because
// both go to logs and to callers who must not learn them.
//
// # What this package must never do
//
// It must never read the environment or a credential file, follow a
// redirect, dial an address Config.AllowAddress has not passed, hold a
// whole object in memory, leave a multipart upload it began unfinished when
// it can abort it, touch a key outside the storage's prefix, or put a
// server's text, an address or a credential in an error.
package s3
