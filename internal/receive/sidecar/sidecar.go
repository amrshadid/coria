package sidecar

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/amrshadid/coria/internal/core/layout"
	"github.com/amrshadid/coria/internal/core/uid"
)

// Version is the format version this package writes and the only one it
// reads.
const Version = 1

// MaxDataset is the longest data set text an instance's line holds; a longer
// one is omitted (see the package documentation).
const MaxDataset = 1 << 20

// maxLine is the longest line the reader takes: the longest data set and
// the fixed fields around it.
const maxLine = MaxDataset + 1024 + layout.MaxKeyLength + 2*uid.MaxLength

const (
	header = `{"version":1,"instances":[` + "\n"
	footer = "]}\n"
)

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
)

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
	return nil
}

// Writer writes a sidecar as a stream.
type Writer struct {
	w     *bufio.Writer
	last  string
	count int
	err   error
	// off counts the bytes written; idx holds the index entries so far.
	off int64
	idx []IndexEntry
}

// NewWriter returns a writer of a sidecar to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: bufio.NewWriterSize(w, 64<<10)}
}

// Add writes one instance's line. Instances must come in increasing order of
// SOP Instance UID (byte order), each once. A data set longer than
// MaxDataset is omitted.
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
	if err := check(in); err != nil {
		return err
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
		w.write([]byte(header))
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
}

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
		if err != nil || string(h) != header[:len(header)-1] {
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
func Merge(w io.Writer, old io.Reader, added Added, drop func(sopUID string) bool) (int, error) {
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
