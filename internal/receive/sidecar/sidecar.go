package sidecar

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
	// PixelData is where its pixel data is, nil when it has none.
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
	w.write(line)
	w.last = sopUID
	w.count++
	return w.err
}

// write writes b, keeping the first error.
func (w *Writer) write(b []byte) {
	if w.err == nil {
		_, w.err = w.w.Write(b)
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
		if _, err := r.r.ReadByte(); !errors.Is(err, io.EOF) {
			return Instance{}, nil, fmt.Errorf("%w: bytes after the end", ErrFormat)
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

// line reads one line without its newline, at most maxLine bytes.
func (r *Reader) line() ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.r.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > maxLine {
			return nil, fmt.Errorf("%w: a line is longer than %d bytes", ErrFormat, maxLine)
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
