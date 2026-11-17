package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// printVerbs are the fmt verbs a value may meet in a log line or an error.
// %s, %q, %t and %d matter as much as %v: on a value with no String method,
// fmt walks into its fields and prints what it finds there with that verb.
var printVerbs = []string{"%v", "%+v", "%#v", "%s", "%q", "%t", "%d", "%x", "%X"}

// printedForms returns every way v may be printed by mistake: each verb of
// fmt, the text and JSON handlers of log/slog, and encoding/json.
func printedForms(t *testing.T, v any) map[string]string {
	t.Helper()
	forms := make(map[string]string)
	for _, verb := range printVerbs {
		forms[verb] = fmt.Sprintf(verb, v)
	}
	var text, js bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("m", "v", v)
	slog.New(slog.NewJSONHandler(&js, nil)).Info("m", "v", v)
	forms["slog text"] = text.String()
	forms["slog json"] = js.String()
	if b, err := json.Marshal(v); err == nil {
		forms["encoding/json"] = string(b)
	} else {
		forms["encoding/json"] = "error: " + err.Error()
	}
	return forms
}

// The four ways a value can sit inside another: as a value or a pointer, in
// an exported or an unexported field. fmt calls a field's methods only when
// the field is exported; an unexported one it reads by reflection.
type (
	keysValueExported struct{ K Keys }
	keysValueHidden   struct{ k Keys }
	keysPtrExported   struct{ K *Keys }
	keysPtrHidden     struct{ k *Keys }
	apiValueExported  struct{ K NewAPIKey }
	apiValueHidden    struct{ k NewAPIKey }
	apiPtrExported    struct{ K *NewAPIKey }
	apiPtrHidden      struct{ k *NewAPIKey }
)

// TestKeysPrintNoKey guards that no way of printing Keys, on its own or
// inside another value, carries key material: not the raw bytes, not base64
// or hex, and not the key's 32-bit words in decimal, which is how fmt once
// printed the cipher's expanded key when Keys sat in an unexported field.
func TestKeysPrintNoKey(t *testing.T) {
	keys := mustKeys(t, keyLine(1, 1)+keyLine(2, 2))
	needles := append(keyForms(testKey(1)), keyForms(testKey(2))...)
	values := map[string]any{
		"value":                   *keys,
		"pointer":                 keys,
		"value, exported field":   keysValueExported{*keys},
		"value, unexported field": keysValueHidden{*keys},
		"pointer, exported field": keysPtrExported{keys},
		"pointer, hidden field":   keysPtrHidden{keys},
	}
	for name, v := range values {
		for form, out := range printedForms(t, v) {
			if found, ok := containsAny(out, needles...); ok {
				t.Errorf("%s, %s: prints key material %q", name, form, found)
			}
		}
	}
	for _, verb := range printVerbs {
		if out := fmt.Sprintf(verb, keys); out != "secret.Keys{2 versions}" {
			t.Errorf("Sprintf(%q) = %q, want the fixed text", verb, out)
		}
	}
}

// TestNewAPIKeyPrintsOnlyID guards that a made API key printed in any way,
// on its own or inside another value, shows its id and never its secret or
// the whole key, which only the Key method returns.
func TestNewAPIKeyPrintsOnlyID(t *testing.T) {
	k, err := MakeAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	full := k.Key()
	secret := full[len(full)-secretLength:]
	values := map[string]any{
		"value":                   k,
		"pointer":                 &k,
		"value, exported field":   apiValueExported{k},
		"value, unexported field": apiValueHidden{k},
		"pointer, exported field": apiPtrExported{&k},
		"pointer, hidden field":   apiPtrHidden{&k},
	}
	for name, v := range values {
		for form, out := range printedForms(t, v) {
			if strings.Contains(out, secret) {
				t.Errorf("%s, %s: prints the key's secret: %s", name, form, out)
			}
		}
	}
	for _, verb := range printVerbs {
		if out := fmt.Sprintf(verb, k); out != "secret.NewAPIKey{ID: "+k.ID+"}" {
			t.Errorf("Sprintf(%q) = %q, want the id alone", verb, out)
		}
	}
	if b, _ := json.Marshal(k); string(b) != `{"id":"`+k.ID+`"}` {
		t.Errorf("json.Marshal = %s, want the id alone", b)
	}
	if !strings.HasPrefix(full, "mgt_"+k.ID+"_") {
		t.Errorf("Key() = a %d-character value that does not start with the id", len(full))
	}
}
