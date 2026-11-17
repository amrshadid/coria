package secret

import (
	"testing"

	"github.com/amrshadid/magnetar/internal/fault"
)

// FuzzParseAPIKey guards the key parser against any presented text: it never
// panics; a refusal is Unauthenticated with the one fixed message; and
// whatever it accepts matches the documented pattern, which is written
// independently of the parser, and splits back into the text it came from.
func FuzzParseAPIKey(f *testing.F) {
	for range 3 {
		k, err := MakeAPIKey()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(k.Key())
	}
	f.Add("")
	f.Add("mgt_")
	f.Add("mgt_111111111111_11111111111111111111111111111111111111111110")
	f.Add("mgt_0OIl0OIl0OIl_0OIl0OIl0OIl0OIl0OIl0OIl0OIl0OIl0OIl0OIl0OIl")

	f.Fuzz(func(t *testing.T, presented string) {
		id, secret, err := ParseAPIKey(presented)
		if err != nil {
			if id != "" || secret != "" {
				t.Fatal("a refusal returned parts of the key")
			}
			if fault.KindOf(err) != fault.Unauthenticated || err.Error() != invalidAPIKey {
				t.Fatalf("refusal %v of kind %v", err, fault.KindOf(err))
			}
			return
		}
		if !apiKeyPattern.MatchString(presented) {
			t.Fatalf("accepted %q, which does not have the documented shape", presented)
		}
		if "mgt_"+id+"_"+secret != presented {
			t.Fatalf("%q split into %q and %q", presented, id, secret)
		}
	})
}
