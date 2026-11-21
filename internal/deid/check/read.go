package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/amrshadid/go-dicom/dataelem"
	"github.com/amrshadid/go-dicom/filebase"
	"github.com/amrshadid/go-dicom/filereader/attributes"
	"github.com/amrshadid/go-dicom/tag"
)

// pixelTags are the elements whose value is image data, never searched:
// Float Pixel Data, Double Float Pixel Data and Pixel Data (PS3.3
// C.7.6.3). Their bytes can hold any sequence of characters by chance.
var pixelTags = map[tag.Tag]bool{0x7FE00008: true, 0x7FE00009: true, 0x7FE00010: true}

// preambleLength is the length of the preamble of a Part 10 file (PS3.10
// 7.1).
const preambleLength = 128

// span is the folded text of one element, or of the preamble, in the
// haystack.
type span struct {
	start, end int
	path       string
}

// parsed is a file read for the search.
type parsed struct {
	// data is the file, its ASCII letters upper-cased once the text was
	// read, for the raw search.
	data []byte
	file *attributes.File
	// hay is the folded text of every element, separated by NUL, which no
	// form holds; spans say whose text is where, in order.
	hay   []byte
	spans []span
	// segs are the ranges of data outside the image data, in order.
	segs [][2]int64
	// binary are the ranges of the values of standard elements of a binary
	// VR (a LUT, an overlay, an encapsulated document), in order, where a
	// short raw form is not taken.
	binary [][2]int64
	// shifted are the runs of data written in a 7-bit set of two-byte
	// characters, each from just after the escape sequence that shifted
	// into it, in order.
	shifted [][2]int64
}

// parse reads data with the codec-free reader and builds what the search
// needs: values within MaxValueBytes, image data left unread. The text of
// every element is decoded under its data set's character set; a value
// that may be text the file did not say was text (VR UN, OB, or none),
// and the preamble, also as UTF-8 and under source, the character set the
// file was received in.
func (c *Checker) parse(ctx context.Context, data []byte, source []string) (*parsed, error) {
	f, err := attributes.Read(filebase.NewFileReader(bytes.NewReader(data)), attributes.ReadOptions{
		SkipBulkData:      true,
		BulkDataThreshold: c.opts.MaxValueBytes,
		MaxValueLength:    c.opts.MaxValueBytes,
		Logger:            slog.New(slog.DiscardHandler),
	})
	if err != nil {
		if errors.Is(err, attributes.ErrValueTooLong) {
			return nil, &Refusal{Cause: CauseTooLarge}
		}
		return nil, &Refusal{Cause: CauseUnreadable}
	}
	if f.Deflated {
		// The positions of a deflated data set's elements count in the
		// inflated bytes, and its raw bytes on disk are compressed: what
		// a parser misses could not be searched for.
		return nil, &Refusal{Cause: CauseUnreadable}
	}
	if !wholeRead(f, int64(len(data))) || !wellFormed(f.MetaElements) || !wellFormed(f.DataElements) {
		return nil, &Refusal{Cause: CauseUnreadable}
	}
	p := &parsed{data: data, file: f}
	w := walker{ctx: ctx, p: p, source: source}
	if f.HasPreamble && len(data) >= preambleLength {
		w.addText("preamble", w.loose(data[:preambleLength], nil))
	}
	var cut [][2]int64
	if err := w.walk(f.MetaElements, nil, "", 0, &cut); err != nil {
		return nil, err
	}
	if err := w.walk(f.DataElements, nil, "", 0, &cut); err != nil {
		return nil, err
	}
	// The text is read; the raw search compares ASCII letters in upper
	// case, in place, so the file is held once.
	upperASCII(data)
	p.segs = outside(cut, int64(len(data)))
	p.shifted = shiftedRuns(data)
	sort.Slice(p.binary, func(i, j int) bool { return p.binary[i][0] < p.binary[j][0] })
	return p, nil
}

// wholeRead reports whether the reader read the file to its end. The
// reader keeps what it read of a truncated file, and of one whose
// sequences nest past its maximum, and drops the rest; an element dropped
// so would not be searched as text, so such a file is not judged.
func wholeRead(f *attributes.File, size int64) bool {
	end := f.DataSetOffset
	if n := len(f.DataElements); n > 0 {
		end = f.DataElements[n-1].Position.End
	}
	return end == size
}

// wellFormed reports whether every sequence of elements, at any depth, was
// read whole (PS3.5 7.5): a sequence or item of undefined length ends with
// its delimitation item; an item of defined length ends where its length
// says, at the end of its last element; a sequence of defined length ends
// with its last item. The reader keeps the items before one cut short and
// says so only in words, so a file whose last item was dropped would
// otherwise pass with that item's text never searched.
func wellFormed(elements []*attributes.DataElementValue) bool {
	for _, e := range elements {
		if e.Items == nil && e.VR != "SQ" {
			continue
		}
		pos := e.Position
		if e.UndefinedLength {
			if pos.DelimiterOffset == 0 || pos.End != pos.DelimiterOffset+8 {
				return false
			}
		} else if n := len(e.Items); n > 0 && e.Items[n-1].End != pos.ValueOffset+int64(e.Length) {
			return false
		}
		for _, it := range e.Items {
			end := it.End
			if it.DeclaredLength == attributes.UndefinedLength {
				if it.DelimiterOffset == 0 || it.End != it.DelimiterOffset+8 {
					return false
				}
				end = it.DelimiterOffset
			} else if it.End != it.Offset+8+int64(it.DeclaredLength) {
				return false
			}
			last := it.Offset + 8
			if n := len(it.Elements); n > 0 {
				last = it.Elements[n-1].Position.End
			}
			if last != end || !wellFormed(it.Elements) {
				return false
			}
		}
	}
	return true
}

// walker walks the elements of a file into the haystack.
type walker struct {
	ctx    context.Context
	p      *parsed
	source []string
}

// walk adds the text of elements, at depth, under prefix, decoded by the
// encodings in force for their data set (PS3.3 C.12.1.1.2: an item without
// its own Specific Character Set takes its parent's), and the ranges of
// image data to cut.
func (w walker) walk(elements []*attributes.DataElementValue, inherited []string, prefix string, depth int, cut *[][2]int64) error {
	if depth > maxDepth {
		return &Refusal{Cause: CauseTooLarge}
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	encodings := attributes.TextEncodingsWithContext(w.ctx, elements, inherited)
	for _, e := range elements {
		path := prefix + fmt.Sprintf("%08X", uint32(e.Tag))
		pos := e.Position
		if pixelTags[e.Tag] {
			if pos.End > pos.ValueOffset {
				*cut = append(*cut, [2]int64{pos.ValueOffset, pos.End})
			}
			continue
		}
		if e.VR == "SQ" || e.Items != nil {
			for i, it := range e.Items {
				if err := w.walk(it.Elements, encodings, fmt.Sprintf("%s[%d]/", path, i), depth+1, cut); err != nil {
					return err
				}
			}
			continue
		}
		text := dataelem.IsTextVR(dataelem.VR(e.VR))
		if !text && e.VR != "UN" && e.VR != "" && !e.Tag.IsPrivate() && pos.End > pos.ValueOffset {
			w.p.binary = append(w.p.binary, [2]int64{pos.ValueOffset, pos.End})
		}
		if e.ValueSkipped || len(e.Value) == 0 {
			continue
		}
		switch {
		case text:
			w.addText(path, []string{attributes.DecodeTextWithContext(w.ctx, e.VR, e.Value, encodings)})
		case e.VR == "UN" || e.VR == "OB" || e.VR == "":
			w.addText(path, w.loose(e.Value, encodings))
		}
	}
	return nil
}

// looseMax is the longest value of unknown kind decoded under a character
// set: the library's decoder is slow on long binary values (some 90 ms a
// megabyte, against a few for the search), and a longer value is searched
// as UTF-8 and in its raw bytes, in every received encoding, all the same.
const looseMax = 256 << 10

// loose returns the texts a value of unknown kind may be (PS3.5 6.2.2): as
// UTF-8, and as LO under the data set's encodings and under the character
// set the file was received in; each once. A value of ASCII without an
// escape reads the same in all of them, and one past looseMax is taken as
// UTF-8 only.
func (w walker) loose(value []byte, encodings []string) []string {
	out := []string{string(value)}
	if len(value) > looseMax || plainASCII(value) {
		return out
	}
	for _, enc := range [][]string{encodings, w.source} {
		if enc == nil {
			continue
		}
		s := attributes.DecodeTextWithContext(w.ctx, "LO", value, enc)
		if s != out[0] && (len(out) < 2 || s != out[1]) {
			out = append(out, s)
		}
	}
	return out
}

// plainASCII reports whether b is ASCII without the escape that begins an
// ISO 2022 code extension (PS3.5 6.1.2.5.3).
func plainASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 || c == 0x1B {
			return false
		}
	}
	return true
}

// addText adds texts of path to the haystack, each folded and followed by
// a NUL.
func (w walker) addText(path string, texts []string) {
	for _, t := range texts {
		start := len(w.p.hay)
		w.p.hay = append(w.p.hay, foldText(t)...)
		w.p.spans = append(w.p.spans, span{start: start, end: len(w.p.hay), path: path})
		w.p.hay = append(w.p.hay, 0)
	}
}

// outside returns the ranges of [0, n) outside cut.
func outside(cut [][2]int64, n int64) [][2]int64 {
	sort.Slice(cut, func(i, j int) bool { return cut[i][0] < cut[j][0] })
	var out [][2]int64
	var at int64
	for _, r := range cut {
		if r[0] > at {
			out = append(out, [2]int64{at, min(r[0], n)})
		}
		at = max(at, r[1])
	}
	if at < n {
		out = append(out, [2]int64{at, n})
	}
	return out
}

// pathAt returns the path of the text at position at of the haystack.
func (p *parsed) pathAt(at int) string {
	i := sort.Search(len(p.spans), func(i int) bool { return p.spans[i].end > at })
	if i < len(p.spans) {
		return p.spans[i].path
	}
	return "file"
}

// shiftedRuns returns the runs of data a file wrote in a 7-bit set of
// two-byte characters (PS3.5 6.1.2.5.3: JIS X 0208 after ESC $ B or ESC $
// @, JIS X 0212 after ESC $ ( D): from the byte after the escape sequence
// to the first byte that is not a character of such a set (0x21 to 0x7E),
// the next escape among them.
func shiftedRuns(data []byte) [][2]int64 {
	var out [][2]int64
	for i := 0; i+2 < len(data); i++ {
		if data[i] != 0x1B || data[i+1] != '$' {
			continue
		}
		j := i + 2
		if data[j] == '(' || data[j] == ')' {
			j++
		}
		if j >= len(data) || data[j] < 0x40 || data[j] > 0x7E {
			continue
		}
		start := j + 1
		end := start
		for end < len(data) && data[end] >= 0x21 && data[end] <= 0x7E {
			end++
		}
		if end > start {
			out = append(out, [2]int64{int64(start), int64(end)})
		}
		i = end - 1
	}
	return out
}

// inShifted reports whether [off, end) lies in one shifted run, at an even
// distance from its start, so a match falls on whole two-byte characters.
func (p *parsed) inShifted(off, end int64) bool {
	i := sort.Search(len(p.shifted), func(i int) bool { return p.shifted[i][1] > off })
	return i < len(p.shifted) && p.shifted[i][0] <= off && end <= p.shifted[i][1] && (off-p.shifted[i][0])%2 == 0
}

// inBinary reports whether offset is in the value of a standard binary
// element.
func (p *parsed) inBinary(off int64) bool {
	i := sort.Search(len(p.binary), func(i int) bool { return p.binary[i][1] > off })
	return i < len(p.binary) && p.binary[i][0] <= off
}

// locate returns the path of the innermost element whose bytes hold
// offset: "preamble" in the first 128 bytes of a file that has one, "file"
// between elements.
func (p *parsed) locate(off int64) string {
	if p.file.HasPreamble && off < preambleLength {
		return "preamble"
	}
	if path, ok := locateIn(p.file.MetaElements, off, ""); ok {
		return path
	}
	if path, ok := locateIn(p.file.DataElements, off, ""); ok {
		return path
	}
	return "file"
}

// locateIn finds the element of elements, at any depth, whose bytes hold
// off.
func locateIn(elements []*attributes.DataElementValue, off int64, prefix string) (string, bool) {
	for _, e := range elements {
		pos := e.Position
		if off < pos.Offset || off >= pos.End {
			continue
		}
		path := prefix + fmt.Sprintf("%08X", uint32(e.Tag))
		for i, it := range e.Items {
			if inner, ok := locateIn(it.Elements, off, fmt.Sprintf("%s[%d]/", path, i)); ok {
				return inner, true
			}
		}
		return path, true
	}
	return "", false
}
