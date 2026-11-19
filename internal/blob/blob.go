package blob

import (
	"context"
	"crypto/sha256"
	"io"
)

// Store is one storage: a flat set of objects, each named by a key and
// holding a sequence of bytes. Keys look like paths ("data/s1/a/b/x.dcm"),
// and List can show them one level at a time, but the storage holds objects
// only: there is no directory to make or remove, and a prefix exists exactly
// while some key under it does.
//
// Every method validates the keys and options it is given and returns a
// fault.Invalid error, having touched nothing, for one that is wrong. A Store
// is safe for concurrent use.
type Store interface {
	// Put writes everything r yields to key and returns its size and
	// SHA-256, computed from the bytes as they passed.
	//
	// The object appears whole or not at all: until Put returns nil, no
	// reader sees any of the new bytes, and when it returns nil they are all
	// there and kept. A key that already holds an object is replaced the
	// same way, so a reader sees the old object or the new one and never a
	// mixture. When r fails, ctx ends, or the storage fails midway, Put
	// returns an error and the key holds what it held before (nothing, or
	// the old object). The one exception is a failure after the new object
	// is in place but before it is known to be kept, such as a directory
	// driver's last sync: Put still returns an error, so the caller puts
	// again, and the key may hold the new object, whole. In no case does it
	// hold a part of one. An error from r is returned wrapped, so its kind
	// passes through; an ended ctx gives an error that matches ctx.Err()
	// with errors.Is.
	//
	// Two Puts to one key at the same time both succeed, and the key then
	// holds the bytes of one of them, whole. Which one is not defined.
	Put(ctx context.Context, key string, r io.Reader) (Written, error)

	// Read returns a stream of length bytes of key, starting at offset. A
	// length of -1 means to the end of the object. A range that runs past
	// the end is cut at the end, so offset equal to the size gives an empty
	// stream; an offset past the size, a negative offset, or a length below
	// -1 is Invalid. A key that holds nothing is NotFound.
	//
	// The stream gives the bytes of the object as it was when Read was
	// called, even when a Put replaces it meanwhile. The caller closes it.
	Read(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)

	// Stat returns what is known of key without reading it, or NotFound.
	Stat(ctx context.Context, key string) (Info, error)

	// List returns one page of the keys that begin with opts.Prefix, in
	// byte order of their UTF-8 text. With opts.Delimiter set to "/", a key
	// that has another "/" after the prefix is not returned itself: the
	// part of it up to and including that "/" is returned once, in
	// Page.Prefixes, so one level can be listed. A page holds at most
	// opts.Limit keys and prefixes together.
	//
	// The order is stable across pages: a page continues exactly after the
	// last entry of the one before, so a key put or deleted behind the
	// point reached is not seen, and one ahead of it is seen as it is when
	// that page is read.
	List(ctx context.Context, opts ListOptions) (Page, error)

	// Delete removes key. Removing a key that holds nothing is not an
	// error. It never removes anything but the one object: a key that is
	// only a prefix of others holds nothing, and deleting it changes
	// nothing.
	Delete(ctx context.Context, key string) error

	// Usage reports the space of the volume the storage is on, where the
	// driver can know it, and says so in Usage.Known when it cannot.
	Usage(ctx context.Context) (Usage, error)

	// Close releases what the Store holds. A stream from Read that is
	// still open may go on being read. Closing twice is harmless; any
	// other call after Close fails as Unavailable.
	Close() error
}

// Written is what a Put wrote: its size in bytes and the SHA-256 of its
// bytes, both counted as they passed.
type Written struct {
	// Size is the number of bytes written.
	Size int64
	// SHA256 is the SHA-256 of the bytes written.
	SHA256 [sha256.Size]byte
}

// Info is what Stat knows of an object without reading it.
type Info struct {
	// Size is the object's size in bytes.
	Size int64
}

// ListOptions says which keys List returns and from where.
type ListOptions struct {
	// Prefix is the text every key returned begins with. It need not end at
	// a "/": "data/s1" matches "data/s1/x" and "data/s10/x" alike. Empty
	// means every key. It is checked by CheckPrefix.
	Prefix string
	// Delimiter is "" to list every key under Prefix, or "/" to list one
	// level, with deeper keys folded into Page.Prefixes. No other value is
	// accepted.
	Delimiter string
	// Token is Page.Next of the page before, to continue from it, or empty
	// for the first page. It is valid only with the same Prefix and
	// Delimiter it was returned for, and it means nothing to another
	// driver.
	Token string
	// Limit is the most entries a page holds, keys and prefixes together:
	// from 1 to MaxListLimit, or 0 for MaxListLimit.
	Limit int
}

// MaxListLimit is the largest page List returns. It is the largest page an
// S3 listing returns (docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html),
// so a page here is never more than one request there.
const MaxListLimit = 1000

// Page is one page of a listing.
type Page struct {
	// Keys are the keys on this page, in byte order.
	Keys []string
	// Prefixes are, with a delimiter, the distinct prefixes that end at the
	// first "/" after ListOptions.Prefix, each ending with "/", in byte
	// order. A prefix sorts among the keys as its text does, and the page's
	// limit counts both.
	Prefixes []string
	// Next is the Token for the following page, or empty when this page
	// holds the last entry. A driver never returns a non-empty Next with
	// nothing after it.
	Next string
}

// Usage is the space of the volume a storage is on.
type Usage struct {
	// Known says whether the driver could find the numbers below. A storage
	// that has no answer, such as an S3 bucket, reports false, and the
	// fields are zero; what it holds is then counted from the catalog.
	Known bool
	// Total is the volume's size in bytes.
	Total int64
	// Used is the bytes in use on the volume, by this storage and anything
	// else that shares it.
	Used int64
	// Free is the bytes this process could still write.
	Free int64
}
