package api

import (
	"bytes"
	"encoding/json"

	"github.com/amrshadid/coria/internal/receive/sidecar"
)

// The DICOM JSON members of an element (PS3.18 F.2.2).
const (
	memberVR      = "vr"
	memberValue   = "Value"
	memberInline  = "InlineBinary"
	memberBulkURI = "BulkDataURI"
)

// keyPaddingMember is Data Set Trailing Padding (FFFC,FFFC) as a member
// name; an answer leaves it out (PS3.5 7.1.3, PS3.10 7.2).
const keyPaddingMember = "FFFCFFFC"

// pixelMembers are the member names of the elements that hold an image's
// pixels: Pixel Data, Float Pixel Data and Double Float Pixel Data (PS3.3
// C.7.6.3).
var pixelMembers = map[string]bool{"7FE00010": true, "7FE00008": true, "7FE00009": true}

// datasetFromSidecar returns the DICOM JSON of the instance the metadata
// file's line in describes, answered as the contract says, with a
// BulkDataURI in place of the pixel data the line points at. It returns nil
// when the line does not settle the answer and the instance's file must be
// read: the line holds no data set, its Pixel Data cannot be placed, or it
// holds an element that may be bulk data whose place the file does not
// keep. The file records the place of Pixel Data and of no other value, so
// any other value it did not hold (a binary value over 1 KiB), and any
// text that may pass 64 KiB as the file holds it, is found in the file.
func datasetFromSidecar(in sidecar.Instance, ts string, uri func(offset, length int64) string) []byte {
	hdr, known := headerLength(ts)
	if !known {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(in.Dataset))
	dec.UseNumber()
	var set map[string]any
	if err := dec.Decode(&set); err != nil {
		return nil
	}
	delete(set, keyPaddingMember)
	pixel := false
	if !settle(set, 0, in.PixelData, hdr, uri, &pixel) {
		return nil
	}
	if in.PixelData != nil && !pixel {
		return nil
	}
	out, err := json.Marshal(set)
	if err != nil {
		return nil
	}
	return out
}

// settle goes through the elements of a data set, depth 0 being its top,
// and puts a BulkDataURI in place of the top level's pixel data. It
// reports false when an element needs the instance's file.
func settle(set map[string]any, depth int, px *sidecar.PixelData, hdr int64, uri func(offset, length int64) string, pixel *bool) bool {
	for key, v := range set {
		el, ok := v.(map[string]any)
		if !ok {
			return false
		}
		vr, _ := el[memberVR].(string)
		if vr == "SQ" {
			items, _ := el[memberValue].([]any)
			for _, it := range items {
				nested, ok := it.(map[string]any)
				if !ok || !settle(nested, depth+1, px, hdr, uri, pixel) {
					return false
				}
			}
			continue
		}
		_, hasValue := el[memberValue]
		inline, hasInline := el[memberInline].(string)
		_, hasURI := el[memberBulkURI]
		switch {
		case hasURI:
			return false
		case hasInline && len(inline) > trustedInline:
			return false
		case hasValue && sizeOf(el[memberValue]) > trustedInline:
			return false
		case hasValue || hasInline:
			// Held in the file as it is answered.
		case pixelMembers[key]:
			// Skipped when the file was written, whatever its length.
			if depth != 0 || px == nil || *pixel || px.Length < hdr {
				return false
			}
			set[key] = map[string]any{memberVR: vr, memberBulkURI: uri(px.Offset+hdr, px.Length-hdr)}
			*pixel = true
		case isBinary(vr):
			// A binary value skipped for its length, or an empty one: the
			// file cannot tell them apart and does not keep its place.
			return false
		}
	}
	return true
}

// isBinary reports whether vr is a representation PS3.18 F.2.2 carries as
// InlineBinary.
func isBinary(vr string) bool {
	switch vr {
	case "OB", "OD", "OF", "OL", "OV", "OW", "UN":
		return true
	}
	return false
}

// sizeOf returns about how many bytes the JSON text of a Value takes.
func sizeOf(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case json.Number:
		return len(x)
	case []any:
		n := 0
		for _, e := range x {
			n += sizeOf(e) + 1
		}
		return n
	case map[string]any:
		n := 0
		for k, e := range x {
			n += len(k) + sizeOf(e) + 4
		}
		return n
	}
	return 4
}
