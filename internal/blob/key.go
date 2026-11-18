package blob

import (
	"strings"
	"unicode/utf8"

	"github.com/amrshadid/magnetar/internal/fault"
)

// The bounds of a key. MaxKeyLength is the longest key S3 accepts
// (docs.aws.amazon.com/AmazonS3/latest/userguide/object-keys.html), so a key
// valid here is valid on every storage. MaxSegmentLength is the longest name
// of one file or directory on the common local file systems (ext4, XFS, APFS
// and NTFS all stop at 255), so a segment never fails on a disk after passing
// here. MaxKeySegments bounds the depth of a key: a directory driver makes
// and examines each directory of a key, so a key of hundreds of one-letter
// segments, still within MaxKeyLength, would cost hundreds of directories and
// a Put taking seconds, while the archive's own layout uses five.
const (
	MaxKeyLength     = 1024
	MaxSegmentLength = 255
	MaxKeySegments   = 64
)

// ReservedPrefix begins no segment of a key, in any case. Drivers name their
// own working files with it (a directory driver's temporary files, for
// one), so no key can ever name, list or remove one of them. The check
// ignores case because a disk on macOS or Windows usually does.
const ReservedPrefix = ".blob-"

// CheckKey returns nil when key is one a storage can hold, and a
// fault.Invalid error saying why otherwise. The message names the rule, not
// the key.
//
// A key is 1 to MaxKeyLength bytes of UTF-8, in segments separated by "/".
// The rules make a key mean the same object on every storage and keep it
// inside the storage's root on a disk:
//
//   - No segment is empty, "." or "..", so a key has no leading, trailing
//     or doubled "/" and cannot climb out of a directory or name one twice.
//   - No backslash, which Windows reads as a separator, and none of
//     : * ? " < > |, which Windows refuses in a name or reads as an
//     alternate data stream (learn.microsoft.com/windows/win32/fileio/naming-a-file).
//   - No control character (C0, DEL or C1), which no file system needs and
//     a log or a terminal could be fooled by.
//   - No segment ends in "." or a space, which Windows drops, so that
//     "a." and "a" would be one file there and two objects elsewhere.
//   - No segment is a Windows device name (CON, PRN, AUX, NUL, COM0-9,
//     LPT0-9, with or without an extension), which opens the device
//     instead of a file.
//   - No segment begins with ReservedPrefix.
//   - No segment is longer than MaxSegmentLength bytes, and there are at
//     most MaxKeySegments segments.
func CheckKey(key string) error {
	if key == "" {
		return keyError("is empty")
	}
	if len(key) > MaxKeyLength {
		return keyError("is longer than 1024 bytes")
	}
	if !utf8.ValidString(key) {
		return keyError("is not valid UTF-8")
	}
	if strings.Count(key, "/") >= MaxKeySegments {
		return keyError("has more than 64 segments")
	}
	for seg := range strings.SplitSeq(key, "/") {
		if err := checkSegment(seg); err != nil {
			return err
		}
	}
	return nil
}

// CheckPrefix returns nil when prefix is one List accepts, and a
// fault.Invalid error otherwise. A prefix is empty, or the start of a valid
// key: every segment before its last "/" obeys CheckKey's rules, and the
// part after it, which may be empty or partial, holds no character a key
// cannot.
func CheckPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > MaxKeyLength {
		return prefixError("is longer than 1024 bytes")
	}
	if !utf8.ValidString(prefix) {
		return prefixError("is not valid UTF-8")
	}
	head, tail := "", prefix
	if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
		head, tail = prefix[:i], prefix[i+1:]
		if err := CheckKey(head); err != nil {
			return prefixError("has a segment that is not valid in a key: " + fault.Message(err))
		}
	}
	if len(tail) > MaxSegmentLength {
		return prefixError("has a segment longer than 255 bytes")
	}
	if reason := badCharacter(tail); reason != "" {
		return prefixError(reason)
	}
	return nil
}

// CheckListOptions returns nil when opts is one List accepts, and a
// fault.Invalid error otherwise. It checks the prefix, the delimiter and the
// limit; the token is the driver's to check, since only it knows what one
// means.
func CheckListOptions(opts ListOptions) error {
	if err := CheckPrefix(opts.Prefix); err != nil {
		return err
	}
	if opts.Delimiter != "" && opts.Delimiter != "/" {
		return fault.New(fault.Invalid, `storage listing: the delimiter must be "" or "/"`)
	}
	if opts.Limit < 0 || opts.Limit > MaxListLimit {
		return fault.New(fault.Invalid, "storage listing: the limit must be from 0 to 1000")
	}
	return nil
}

// ListLimit returns the page size opts asks for: its Limit, or MaxListLimit
// when that is 0. It assumes CheckListOptions has passed.
func ListLimit(opts ListOptions) int {
	if opts.Limit == 0 {
		return MaxListLimit
	}
	return opts.Limit
}

// CheckRange returns nil when offset and length are a range Read accepts
// before the object's size is known, and a fault.Invalid error otherwise.
// Whether offset is past the end is known only to the driver, which reports
// it the same way.
func CheckRange(offset, length int64) error {
	if offset < 0 {
		return fault.New(fault.Invalid, "storage read: the offset is negative")
	}
	if length < -1 {
		return fault.New(fault.Invalid, "storage read: the length is below -1")
	}
	return nil
}

// ErrPastEnd returns the error Read gives for an offset past the end of the
// object, so every driver words it alike.
func ErrPastEnd() error {
	return fault.New(fault.Invalid, "storage read: the offset is past the end of the object")
}

// checkSegment applies CheckKey's rules to one segment.
func checkSegment(seg string) error {
	switch {
	case seg == "":
		return keyError(`has an empty segment (a leading, trailing or doubled "/")`)
	case seg == "." || seg == "..":
		return keyError(`has a "." or ".." segment`)
	case len(seg) > MaxSegmentLength:
		return keyError("has a segment longer than 255 bytes")
	}
	if reason := badCharacter(seg); reason != "" {
		return keyError(reason)
	}
	if last := seg[len(seg)-1]; last == '.' || last == ' ' {
		return keyError(`has a segment that ends in "." or a space`)
	}
	if isDeviceName(seg) {
		return keyError("has a segment that is a Windows device name")
	}
	if len(seg) >= len(ReservedPrefix) && strings.EqualFold(seg[:len(ReservedPrefix)], ReservedPrefix) {
		return keyError(`has a segment that begins with the reserved ".blob-"`)
	}
	return nil
}

// badCharacter returns why s holds a character no key may hold, or "" when
// it holds none. s holds no "/".
func badCharacter(s string) string {
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return "has a control character"
		case r == '\\':
			return "has a backslash"
		case strings.ContainsRune(`:*?"<>|`, r):
			return `has one of : * ? " < > |`
		}
	}
	return ""
}

// isDeviceName reports whether seg names a Windows device: its part before
// the first ".", with trailing spaces dropped as Windows drops them, is one
// of the reserved names in any case. The superscript digits are included
// because Windows treats COM¹ to COM³ and LPT¹ to LPT³ as devices too.
func isDeviceName(seg string) bool {
	base, _, _ := strings.Cut(seg, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) < 4 || (base[:3] != "COM" && base[:3] != "LPT") {
		return false
	}
	switch base[3:] {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

func keyError(reason string) error {
	return fault.New(fault.Invalid, "storage key "+reason)
}

func prefixError(reason string) error {
	return fault.New(fault.Invalid, "storage listing prefix "+reason)
}
