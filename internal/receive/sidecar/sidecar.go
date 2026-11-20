package sidecar

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/amrshadid/coria/internal/core/layout"
	"github.com/amrshadid/coria/internal/core/uid"
)

// Version is the format version this package writes. It reads it and
// version 1, the form before frame tables, whose lines need not have one
// (see the package documentation).
const Version = 2

// MaxDataset is the longest data set text an instance's line holds; a longer
// one is omitted (see the package documentation).
const MaxDataset = 1 << 20

// MaxFrameRanges is the most byte ranges an instance's frame table holds,
// fragments of all its frames together; an instance with more has its
// table left empty (see the package documentation).
const MaxFrameRanges = 1 << 16

// maxRangeText is the longest one range's text may be in a frame table:
// two numbers of at most 19 digits, the brackets and commas around them,
// and its frame's brackets when it is a frame's only range.
const maxRangeText = 2*19 + 6

// maxSumText is the longest one frame's checksum may be in a line: 64 hex
// digits, its quotes and a comma.
const maxSumText = 64 + 3

// maxLine is the longest line the reader takes: the longest data set, the
// longest frame table with a checksum for each of its frames (a frame has
// at least one range), and the fixed fields around them.
const maxLine = MaxDataset + MaxFrameRanges*(maxRangeText+maxSumText) + 1024 + layout.MaxKeyLength + 2*uid.MaxLength

const (
	headerV1 = `{"version":1,"instances":[` + "\n"
	header   = `{"version":2,"instances":[` + "\n"
	footer   = "]}\n"
)

// HeadLength is how many first bytes of a sidecar say its version
// (VersionOf).
const HeadLength = len(header)

// VersionOf returns the version a sidecar's first HeadLength bytes state,
// or 0 when they are not the first line of a sidecar of a version this
// package reads. A reader that needs only to know whether a scan's frame
// tables are all there reads these bytes, not the sidecar.
func VersionOf(head []byte) int {
	switch string(head) {
	case header:
		return Version
	case headerV1:
		return 1
	}
	return 0
}

// IndexEvery is how many instances lie between two entries of the index a
// sidecar of more than IndexEvery instances ends with: the byte offset of
// every IndexEvery-th instance's line, so a reader of one page of a large
// scan starts near it instead of at the first line.
const IndexEvery = 256

// maxIndexLine is the longest the index line may be: 80 million instances'
// worth of entries.
const maxIndexLine = 16 << 20

// IndexEntry is the place of one instance's line in the sidecar.
type IndexEntry struct {
	// UID is the instance's SOP Instance UID.
	UID string `json:"uid"`
	// Offset is the first byte of its line.
	Offset int64 `json:"offset"`
}

// indexLine is the line that follows the footer.
type indexLine struct {
	Index []IndexEntry `json:"index"`
}

// PixelData is where an instance's pixel data element is in its file.
type PixelData struct {
	// Offset is the first byte of the element's tag.
	Offset int64 `json:"offset"`
	// Length runs to the first byte after the element.
	Length int64 `json:"length"`
}

// Range is a run of bytes of an instance's file: Length bytes from Offset.
// A frame table writes it as the pair [offset,length].
type Range struct {
	// Offset is its first byte.
	Offset int64
	// Length is how many bytes it has.
	Length int64
}

// MarshalJSON writes the range as [offset,length].
func (r Range) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 40)
	b = append(b, '[')
	b = strconv.AppendInt(b, r.Offset, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.Length, 10)
	return append(b, ']'), nil
}

// UnmarshalJSON reads a range written as [offset,length], and nothing
// else.
func (r *Range) UnmarshalJSON(b []byte) error {
	var pair []int64
	if err := json.Unmarshal(b, &pair); err != nil || len(pair) != 2 {
		return fmt.Errorf("%w: a range is [offset,length]", ErrFormat)
	}
	r.Offset, r.Length = pair[0], pair[1]
	return nil
}

// Instance is one instance's line.
type Instance struct {
	// SOPInstanceUID names the instance.
	SOPInstanceUID string `json:"sop_instance_uid"`
	// Key is the instance's key on the storage.
	Key string `json:"key"`
	// Size is the instance file's size in bytes.
	Size int64 `json:"size"`
	// SHA256 is the file's checksum in lowercase hex.
	SHA256 string `json:"sha256"`
	// TransferSyntax is the Transfer Syntax UID of the instance's file,
	// empty in a line written before the field existed.
	TransferSyntax string `json:"transfer_syntax,omitempty"`
	// PixelData is where its pixel data is, nil when it has none or when the
	// file's data set is deflated, where no offset in the file names it.
	PixelData *PixelData `json:"pixel_data"`
	// Frames are where each frame of its pixel data lies in its file,
	// frame 1 first, each frame the ranges that joined in order are its
	// bytes (Frame). Nil in a line written before frame tables existed;
	// empty when it has pixel data whose frames are not byte ranges of the
	// file, or more than MaxFrameRanges ranges.
	Frames [][]Range `json:"frames,omitzero"`
	// FrameSHA256 is the SHA-256 of each frame of Frames in lowercase hex,
	// frame 1 first: of the frame's ranges' bytes joined in order, as the
	// file was archived. Every line with a frame table that is not empty
	// has one for each of its frames, and no other line has any.
	FrameSHA256 []string `json:"frame_sha256,omitempty"`
	// Dataset is its data set's DICOM JSON, nil when omitted.
	Dataset json.RawMessage `json:"dataset"`
	// DatasetOmitted says the data set was too long to keep.
	DatasetOmitted bool `json:"dataset_omitted,omitempty"`
}

// The errors of this package.
var (
	// ErrFormat is a sidecar that is not in the one form this package
	// writes, or an instance that breaks a rule of it.
	ErrFormat = errors.New("sidecar: not a valid metadata sidecar")
	// ErrOrder is an instance added out of the order of SOP Instance UIDs,
	// or twice.
	ErrOrder = errors.New("sidecar: instances out of order or repeated")
	// ErrIncomplete is a line with pixel data and no frame table where the
	// form requires one: in a sidecar of this version.
	ErrIncomplete = errors.New("sidecar: an instance with pixel data has no frame table")
	// ErrNoFrames is a frame asked of a line that holds no frame table.
	ErrNoFrames = errors.New("sidecar: the instance's line holds no frame table")
	// ErrNoSuchFrame is a frame number that is not one of the instance's.
	ErrNoSuchFrame = errors.New("sidecar: no such frame")
)

// NeedsFrames reports whether the line has pixel data and no frame table,
// as a line written before frame tables existed has.
func (in Instance) NeedsFrames() bool { return in.PixelData != nil && in.Frames == nil }

// Frame returns where frame n of the instance lies in its file, counted
// from 1 as a request names frames (PS3.18 10.4.1.1.4): the ranges whose
// bytes, joined in order, are the frame as stored. A line without a frame
// table, or with an empty one, is ErrNoFrames; n outside 1 to the number
// of frames is ErrNoSuchFrame.
func (in Instance) Frame(n int) ([]Range, error) {
	if len(in.Frames) == 0 {
		return nil, ErrNoFrames
	}
	if n < 1 || n > len(in.Frames) {
		return nil, ErrNoSuchFrame
	}
	return in.Frames[n-1], nil
}

// frameRanges counts the ranges of a frame table.
func frameRanges(frames [][]Range) int {
	n := 0
	for _, f := range frames {
		n += len(f)
	}
	return n
}

// checkFrames returns nil if the frame table may be written: each frame
// has a range and a checksum, every range lies within the pixel data
// element, and each starts at or after the end of the one before.
func checkFrames(in Instance) error {
	if len(in.Frames) == 0 {
		if len(in.FrameSHA256) > 0 {
			return fmt.Errorf("%w: frame checksums without a frame table", ErrFormat)
		}
		if in.Frames == nil {
			return nil
		}
	}
	p := in.PixelData
	if p == nil {
		return fmt.Errorf("%w: frames without pixel data", ErrFormat)
	}
	if frameRanges(in.Frames) > MaxFrameRanges {
		return fmt.Errorf("%w: more than %d frame ranges", ErrFormat, MaxFrameRanges)
	}
	if len(in.FrameSHA256) != len(in.Frames) {
		return fmt.Errorf("%w: a frame table without a checksum for each frame", ErrFormat)
	}
	for _, sum := range in.FrameSHA256 {
		if !lowerHex(sum, 64) {
			return fmt.Errorf("%w: a frame's checksum is not 64 lowercase hex digits", ErrFormat)
		}
	}
	end := p.Offset + p.Length
	at := p.Offset
	for _, f := range in.Frames {
		if len(f) == 0 {
			return fmt.Errorf("%w: a frame with no range", ErrFormat)
		}
		for _, r := range f {
			if r.Offset < at || r.Length < 0 || r.Length > end-r.Offset {
				return fmt.Errorf("%w: a frame range lies outside the pixel data or before the one ahead of it", ErrFormat)
			}
			at = r.Offset + r.Length
		}
	}
	return nil
}

// lowerHex reports whether s is n lowercase hex digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// check returns nil if in may be written.
func check(in Instance) error {
	if err := uid.Check(in.SOPInstanceUID); err != nil {
		return fmt.Errorf("%w: sop_instance_uid: %w", ErrFormat, err)
	}
	k, err := layout.Parse(in.Key)
	if err != nil || k.Kind != layout.Instance || k.SOPInstanceUID != in.SOPInstanceUID {
		return fmt.Errorf("%w: key is not the instance's key of the layout", ErrFormat)
	}
	if in.Size < 0 || len(in.SHA256) != 64 {
		return fmt.Errorf("%w: size or sha256", ErrFormat)
	}
	if in.TransferSyntax != "" {
		if err := uid.Check(in.TransferSyntax); err != nil {
			return fmt.Errorf("%w: transfer_syntax: %w", ErrFormat, err)
		}
	}
	if p := in.PixelData; p != nil && (p.Offset < 0 || p.Length < 0 || p.Offset+p.Length > in.Size) {
		return fmt.Errorf("%w: pixel_data lies outside the file", ErrFormat)
	}
	if in.DatasetOmitted != (in.Dataset == nil) {
		return fmt.Errorf("%w: a data set is either there or said to be omitted", ErrFormat)
	}
	return checkFrames(in)
}

// Writer writes a sidecar as a stream.
type Writer struct {
	w     *bufio.Writer
	last  string
	count int
	err   error
	// version is the version written: Version, or 1 for a merge into a
	// sidecar of version 1 (Merge).
	version int
	// off counts the bytes written; idx holds the index entries so far.
	off int64
	idx []IndexEntry
}

// NewWriter returns a writer of a sidecar to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: bufio.NewWriterSize(w, 64<<10), version: Version}
}

// Add writes one instance's line. Instances must come in increasing order of
// SOP Instance UID (byte order), each once. A data set longer than
// MaxDataset is omitted, and a frame table of more than MaxFrameRanges
// ranges is left empty. An instance with pixel data and no frame table is
// ErrIncomplete.
func (w *Writer) Add(in Instance) error {
	if w.err != nil {
		return w.err
	}
	if len(in.Dataset) > MaxDataset {
		in.Dataset, in.DatasetOmitted = nil, true
	}
	if in.Dataset == nil {
		in.DatasetOmitted = true
	}
	if frameRanges(in.Frames) > MaxFrameRanges {
		in.Frames, in.FrameSHA256 = [][]Range{}, nil
	}
	if err := check(in); err != nil {
		return err
	}
	if in.NeedsFrames() {
		return ErrIncomplete
	}
	if w.count > 0 && in.SOPInstanceUID <= w.last {
		return ErrOrder
	}
	line, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("%w: an instance cannot be encoded", ErrFormat)
	}
	return w.addLine(in.SOPInstanceUID, line)
}

// addLine writes an encoded line.
func (w *Writer) addLine(sopUID string, line []byte) error {
	if w.count == 0 {
		if w.version == 1 {
			w.write([]byte(headerV1))
		} else {
			w.write([]byte(header))
		}
	} else {
		w.write([]byte(",\n"))
	}
	if w.count%IndexEvery == 0 {
		w.idx = append(w.idx, IndexEntry{UID: sopUID, Offset: w.off})
	}
	w.write(line)
	w.last = sopUID
	w.count++
	return w.err
}

// write writes b, keeping the first error.
func (w *Writer) write(b []byte) {
	if w.err == nil {
		var n int
		n, w.err = w.w.Write(b)
		w.off += int64(n)
	}
}

// Close ends the sidecar and flushes it. A sidecar with no instance is an
// error: a scan is made with what it holds.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}
	if w.count == 0 {
		return fmt.Errorf("%w: no instances", ErrFormat)
	}
	w.write([]byte("\n" + footer))
	if len(w.idx) > 1 {
		line, err := json.Marshal(indexLine{Index: w.idx})
		if err != nil {
			return fmt.Errorf("%w: the index cannot be encoded", ErrFormat)
		}
		w.write(line)
		w.write([]byte("\n"))
	}
	if w.err == nil {
		w.err = w.w.Flush()
	}
	return w.err
}

// Reader reads a sidecar as a stream, one instance at a time.
type Reader struct {
	r     *bufio.Reader
	start bool
	done  bool
	last  string
	// version is the version its first line states, 0 for a reader that
	// starts at an instance's line (NewReaderFrom).
	version int
}

// Version returns the version of the sidecar, known once Next has read
// its first line: 0 before, and for a reader started at an instance's line.
func (r *Reader) Version() int { return r.version }

// NewReader returns a reader of the sidecar r holds.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next instance and its line as written, or io.EOF after
// the last. A sidecar not in the form Writer writes is ErrFormat.
func (r *Reader) Next() (Instance, []byte, error) {
	if r.done {
		return Instance{}, nil, io.EOF
	}
	if !r.start {
		h, err := r.line()
		switch {
		case err == nil && string(h) == header[:len(header)-1]:
			r.version = Version
		case err == nil && string(h) == headerV1[:len(headerV1)-1]:
			r.version = 1
		default:
			return Instance{}, nil, fmt.Errorf("%w: header", ErrFormat)
		}
		r.start = true
	}
	line, err := r.line()
	if err != nil {
		return Instance{}, nil, err
	}
	if string(line) == footer[:len(footer)-1] {
		if err := r.end(); err != nil {
			return Instance{}, nil, err
		}
		r.done = true
		if r.last == "" {
			return Instance{}, nil, fmt.Errorf("%w: no instances", ErrFormat)
		}
		return Instance{}, nil, io.EOF
	}
	line = bytes.TrimSuffix(line, []byte(","))
	var in Instance
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Instance{}, nil, fmt.Errorf("%w: an instance's line", ErrFormat)
	}
	if string(in.Dataset) == "null" {
		in.Dataset = nil
	}
	if err := check(in); err != nil {
		return Instance{}, nil, err
	}
	if r.version == Version && in.NeedsFrames() {
		return Instance{}, nil, fmt.Errorf("%w: %w", ErrFormat, ErrIncomplete)
	}
	if r.last != "" && in.SOPInstanceUID <= r.last {
		return Instance{}, nil, fmt.Errorf("%w: %w", ErrFormat, ErrOrder)
	}
	r.last = in.SOPInstanceUID
	return in, line, nil
}

// end checks what follows the footer: nothing, or the index line and then
// nothing.
func (r *Reader) end() error {
	if _, err := r.r.ReadByte(); errors.Is(err, io.EOF) {
		return nil
	}
	_ = r.r.UnreadByte()
	line, err := r.lineWithin(maxIndexLine)
	if err != nil {
		return err
	}
	if _, err := decodeIndex(line); err != nil {
		return err
	}
	if _, err := r.r.ReadByte(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: bytes after the end", ErrFormat)
	}
	return nil
}

// decodeIndex decodes the index line, which must be in the one form.
func decodeIndex(line []byte) ([]IndexEntry, error) {
	var il indexLine
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&il); err != nil || len(il.Index) == 0 {
		return nil, fmt.Errorf("%w: the index", ErrFormat)
	}
	prev := int64(0)
	for i, e := range il.Index {
		if e.Offset <= prev || uid.Check(e.UID) != nil || (i > 0 && e.UID <= il.Index[i-1].UID) {
			return nil, fmt.Errorf("%w: the index is out of order", ErrFormat)
		}
		prev = e.Offset
	}
	return il.Index, nil
}

// ParseIndex returns the index a sidecar ends with, given its last bytes
// and whether they are the whole file. complete is false when tail is too
// short to hold the whole index line, and a longer tail should be read;
// when it is true and entries is nil, the sidecar has no index (a small
// one, or one written before indexes existed) or its index is not in its
// form, and a reader starts at the first line.
func ParseIndex(tail []byte, whole bool) (entries []IndexEntry, complete bool) {
	if len(tail) == 0 || tail[len(tail)-1] != '\n' || bytes.HasSuffix(tail, []byte("\n"+footer)) {
		return nil, true
	}
	body := tail[:len(tail)-1]
	i := bytes.LastIndexByte(body, '\n')
	if i < 0 && !whole {
		return nil, false
	}
	entries, err := decodeIndex(body[i+1:])
	if err != nil {
		return nil, true
	}
	return entries, true
}

// Locate returns the offset to start reading at for the instance with the
// SOP Instance UID uid: the line of the last indexed instance not after it.
func Locate(entries []IndexEntry, uid string) int64 {
	i := sort.Search(len(entries), func(i int) bool { return entries[i].UID > uid })
	if i == 0 {
		return entries[0].Offset
	}
	return entries[i-1].Offset
}

// NewReaderFrom returns a reader of the sidecar that r holds from the first
// byte of an instance's line, an offset an index gives, to the end.
func NewReaderFrom(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64<<10), start: true}
}

// line reads one line without its newline, at most maxLine bytes.
func (r *Reader) line() ([]byte, error) { return r.lineWithin(maxLine) }

// lineWithin reads one line without its newline, at most limit bytes.
func (r *Reader) lineWithin(limit int) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.r.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > limit {
			return nil, fmt.Errorf("%w: a line is longer than %d bytes", ErrFormat, limit)
		}
		switch {
		case err == nil:
			return out[:len(out)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, fmt.Errorf("%w: cut short", ErrFormat)
		default:
			return nil, fmt.Errorf("sidecar: read: %w", err)
		}
	}
}

// Added yields the instances a merge adds, in increasing order of SOP
// Instance UID, each once: false when there are no more. It is called once
// per instance, as the merge reaches it, so an instance's data set is made
// only when it is written and memory follows one line, not the scan.
type Added func() (Instance, bool, error)

// List returns an Added that yields the instances given, for a caller that
// has them at hand.
func List(ins []Instance) Added {
	i := 0
	return func() (Instance, bool, error) {
		if i == len(ins) {
			return Instance{}, false, nil
		}
		i++
		return ins[i-1], true, nil
	}
}

// Merge writes to w the instances of the sidecar old (nil for none) and of
// added, in order: an instance of old whose UID is in drop or among added is
// left out, and added's take their place. It returns how many instances it
// wrote. Memory follows the longest line.
//
// Every instance added must have its frame table (ErrIncomplete
// otherwise). The sidecar written is of this version when old is nil or of
// this version, and of version 1 when old is, since the lines it keeps of
// old may have none; Complete is the rewrite that gives them theirs.
func Merge(w io.Writer, old io.Reader, added Added, drop func(sopUID string) bool) (int, error) {
	return merge(w, old, added, drop, false)
}

// Complete is Merge for the rewrite that gives a sidecar of version 1 the
// frame tables its lines lack: added holds a line, with its table, for
// every line of old that has pixel data and no table, and the sidecar
// written is of this version. A line it would keep without a table is
// ErrIncomplete, and what was written to w is then not a sidecar.
func Complete(w io.Writer, old io.Reader, added Added) (int, error) {
	return merge(w, old, added, nil, true)
}

// merge is Merge, writing this version whatever old's when complete.
func merge(w io.Writer, old io.Reader, added Added, drop func(sopUID string) bool, complete bool) (int, error) {
	out := NewWriter(w)
	var rd *Reader
	if old != nil {
		rd = NewReader(old)
	}
	next := func() (Instance, []byte, bool, error) {
		if rd == nil {
			return Instance{}, nil, false, nil
		}
		in, line, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return Instance{}, nil, false, nil
		}
		return in, line, err == nil, err
	}
	cur, line, ok, err := next()
	if err != nil {
		return 0, err
	}
	if rd != nil && rd.version == 1 && !complete {
		out.version = 1
	}
	add, more, err := added()
	if err != nil {
		return 0, err
	}
	for ok || more {
		switch {
		case ok && (!more || cur.SOPInstanceUID < add.SOPInstanceUID):
			if drop == nil || !drop(cur.SOPInstanceUID) {
				if out.count > 0 && cur.SOPInstanceUID <= out.last {
					return 0, ErrOrder
				}
				if out.version == Version && cur.NeedsFrames() {
					return 0, ErrIncomplete
				}
				if err := out.addLine(cur.SOPInstanceUID, line); err != nil {
					return 0, err
				}
			}
			if cur, line, ok, err = next(); err != nil {
				return 0, err
			}
		default:
			if ok && cur.SOPInstanceUID == add.SOPInstanceUID {
				if cur, line, ok, err = next(); err != nil {
					return 0, err
				}
			}
			if err := out.Add(add); err != nil {
				return 0, err
			}
			if add, more, err = added(); err != nil {
				return 0, err
			}
		}
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	return out.count, nil
}
