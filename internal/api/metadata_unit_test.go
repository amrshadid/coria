package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/amrshadid/coria/internal/receive/sidecar"
)

// sidecarLine returns a metadata file's line for an instance with the data
// set text and its pixel data at offset 1000, 4100 bytes long.
func sidecarLine(text string) sidecar.Instance {
	return sidecar.Instance{Dataset: json.RawMessage(text), PixelData: &sidecar.PixelData{Offset: 1000, Length: 4100}}
}

// answer returns the JSON datasetFromSidecar makes, or "" when it says the
// instance's file must be read.
func answer(in sidecar.Instance, ts string) string {
	return string(datasetFromSidecar(in, ts, func(o, l int64) string { return "u?" + strconv.FormatInt(o, 10) + "," + strconv.FormatInt(l, 10) }))
}

// TestDatasetFromSidecarPlacesPixelData guards the answer made from the
// metadata file alone: Pixel Data becomes a reference past the element's
// header, which is 12 bytes in an explicit syntax and 8 in implicit VR
// little endian (PS3.5 7.1), trailing padding is left out, and the other
// members are kept as they are.
func TestDatasetFromSidecarPlacesPixelData(t *testing.T) {
	const set = `{"00100010":{"vr":"PN","Value":[{"Alphabetic":"A^B"}]},"00280010":{"vr":"US","Value":[64]},"7FE00010":{"vr":"OW"},"FFFCFFFC":{"vr":"OB","InlineBinary":"AAAA"}}`
	for _, c := range []struct{ ts, want string }{
		{"1.2.840.10008.1.2.1", `u?1012,4088`},
		{"1.2.840.10008.1.2.4.50", `u?1012,4088`},
		{"1.2.840.10008.1.2", `u?1008,4092`},
	} {
		got := answer(sidecarLine(set), c.ts)
		if !strings.Contains(got, `"BulkDataURI":"`+c.want+`"`) || strings.Contains(got, "FFFCFFFC") || !strings.Contains(got, `"Value":[64]`) ||
			!strings.Contains(got, `{"Alphabetic":"A^B"}`) {
			t.Errorf("%s: %s, want a reference %s and the other members, without padding", c.ts, got, c.want)
		}
	}
}

// TestDatasetFromSidecarSendsWhatItCannotSettleToTheFile guards that an
// answer is made from the metadata file only when it settles it: a binary
// value skipped for its length or empty, text that may pass 64 KiB, a
// Pixel Data inside a sequence, a transfer syntax the standard does not
// register, and a line without a data set each return nothing, so the
// instance's file is read.
func TestDatasetFromSidecarSendsWhatItCannotSettleToTheFile(t *testing.T) {
	long := strings.Repeat("x", 17<<10)
	for name, c := range map[string]struct{ set, ts string }{
		"a skipped binary":    {`{"7FE00010":{"vr":"OW"},"7FDF1001":{"vr":"OB"}}`, "1.2.840.10008.1.2.1"},
		"long text":           {`{"7FE00010":{"vr":"OW"},"00081030":{"vr":"LO","Value":["` + long + `"]}}`, "1.2.840.10008.1.2.1"},
		"nested pixel data":   {`{"7FE00010":{"vr":"OW"},"00880200":{"vr":"SQ","Value":[{"7FE00010":{"vr":"OW"}}]}}`, "1.2.840.10008.1.2.1"},
		"nested skipped":      {`{"7FE00010":{"vr":"OW"},"00081140":{"vr":"SQ","Value":[{"7FDF1001":{"vr":"OB"}}]}}`, "1.2.840.10008.1.2.1"},
		"unregistered syntax": {`{"7FE00010":{"vr":"OW"}}`, "1.2.3"},
		"no pixel data":       {`{"00280010":{"vr":"US","Value":[1]}}`, "1.2.840.10008.1.2.1"},
		"not JSON":            {`[1]`, "1.2.840.10008.1.2.1"},
	} {
		if got := answer(sidecarLine(c.set), c.ts); got != "" {
			t.Errorf("%s: answered %.80s from the metadata file", name, got)
		}
	}
	if got := answer(sidecar.Instance{PixelData: &sidecar.PixelData{Offset: 1000, Length: 4100}, DatasetOmitted: true}, "1.2.840.10008.1.2.1"); got != "" {
		t.Errorf("a line with no data set was answered: %s", got)
	}
}
