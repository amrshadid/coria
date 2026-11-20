package profile

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/amrshadid/go-dicom/tag"
	"github.com/amrshadid/go-dicom/valuerep"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/core/pathname"
)

// Bounds of an action's text.
const (
	// MaxValueLength is the bound of a constant and of a template.
	MaxValueLength = 1024
	// MaxPlaceholders is the most placeholders of a template.
	MaxPlaceholders = 8
)

// The VRs each action fits. An action outside its list would write a value
// the element cannot hold (PS3.5 6.2), or would do nothing the person meant.
var (
	// textVRs hold text a keyed code can replace.
	textVRs = []string{"AE", "CS", "LO", "LT", "PN", "SH", "ST", "UC", "UT"}
	// freeTextVRs hold free text names can hide in.
	freeTextVRs = []string{"LO", "LT", "SH", "ST", "UC", "UT"}
	// stringVRs hold a value written as text, which a constant can be.
	stringVRs = []string{"AE", "AS", "CS", "DA", "DS", "DT", "IS", "LO", "LT", "PN", "SH", "ST", "TM", "UC", "UI", "UR", "UT"}
	uidVRs    = []string{"UI"}
	dateVRs   = []string{"DA", "DT"}
	coarseVRs = []string{"AS", "DA", "DT"}
)

// checkAction checks an action against what the selector says of the
// elements it selects, and returns its normalized form, in which a
// template's keywords are their tags.
func checkAction(a at, act *v1.ProfileAction, sel selector) *v1.ProfileAction {
	a = a.in("action")
	norm, _ := proto.Clone(act).(*v1.ProfileAction)
	switch k := norm.GetKind().(type) {
	case nil:
		a.add(CodeRequired, "a rule needs an action")
	case *v1.ProfileAction_Keep, *v1.ProfileAction_Remove, *v1.ProfileAction_Empty, *v1.ProfileAction_Quarantine:
		// Fit any element.
	case *v1.ProfileAction_Replace:
		if t, ok := checkValue(a.in("replace"), sel, k.Replace.GetConstant(), k.Replace.GetTemplate(), k.Replace.GetValue() != nil, isTemplate(k.Replace.GetValue())); ok {
			k.Replace.Value = &v1.ReplaceAction_Template{Template: t}
		}
	case *v1.ProfileAction_Set:
		if sel.tier != TierTag {
			a.in("set").add(CodeActionSelector, "set makes an element where it is absent, so it needs one place: a tag or keyword selector")
			return norm
		}
		if len(sel.vrs) != 1 {
			a.in("set").add(CodeActionVR, "set makes an element, so the data dictionary must give it one VR; %s has %q", formatTag(sel.tag), tag.Tag(sel.tag).GetVR())
			return norm
		}
		if t, ok := checkValue(a.in("set"), sel, k.Set.GetConstant(), k.Set.GetTemplate(), k.Set.GetValue() != nil, isTemplate(k.Set.GetValue())); ok {
			k.Set.Value = &v1.SetAction_Template{Template: t}
		}
	case *v1.ProfileAction_Hash:
		fits(a.in("hash"), sel, textVRs, "hash")
	case *v1.ProfileAction_Uid:
		fits(a.in("uid"), sel, uidVRs, "uid")
	case *v1.ProfileAction_Shift:
		fits(a.in("shift"), sel, dateVRs, "shift")
	case *v1.ProfileAction_Generalize:
		fits(a.in("generalize"), sel, coarseVRs, "generalize")
	case *v1.ProfileAction_Clean:
		fits(a.in("clean"), sel, freeTextVRs, "clean")
	case *v1.ProfileAction_Lookup:
		a := a.in("lookup")
		fits(a, sel, textVRs, "lookup")
		if !pathname.ValidName(k.Lookup.GetTable()) {
			a.in("table").add(CodeBadName, "a lookup table's name is 1 to 63 of a-z, 0-9 and -, starting with a letter")
		}
		if _, known := v1.LookupMissing_name[int32(k.Lookup.GetOnMissing())]; !known {
			a.in("onMissing").add(CodeBadEnum, "%d is not a value of LookupMissing", k.Lookup.GetOnMissing())
		}
	default:
		a.add(CodeBadEnum, "an action this version does not know")
	}
	return norm
}

// isTemplate reports whether a value choice is a template.
func isTemplate(v any) bool {
	switch v.(type) {
	case *v1.ReplaceAction_Template, *v1.SetAction_Template:
		return true
	}
	return false
}

// checkValue checks the value of a replace or set: a constant valid for the
// element's VR, or a template for a text element. For a valid template it
// returns the template normalized and true.
func checkValue(a at, sel selector, constant, template string, given, isTmpl bool) (string, bool) {
	if !given {
		a.add(CodeOneOf, "give the new value as constant or template")
		return "", false
	}
	if isTmpl {
		fieldA := a.in("template")
		if !fits(fieldA, sel, textVRs, "a template") {
			return "", false
		}
		return checkTemplate(fieldA, template)
	}
	fieldA := a.in("constant")
	if len(constant) > MaxValueLength {
		fieldA.add(CodeTooLong, "a constant is at most %d bytes", MaxValueLength)
		return "", false
	}
	if !fits(fieldA, sel, stringVRs, "a constant") {
		return "", false
	}
	// A constant is checked against the element's VR when that is one: the
	// library's rules of PS3.5 6.2 for its form and length.
	if len(sel.vrs) == 1 && constant != "" {
		if err := valuerep.ValidateValue(sel.vrs[0], constant); err != nil {
			fieldA.add(CodeBadValue, "%q is not a value of VR %s: %v", clipText(constant), sel.vrs[0], err)
		}
	}
	return "", false
}

// fits reports whether every VR the selector may select is in allowed,
// recording a problem when not. A content item's value has a VR only the
// file says, so the engine checks it there and refuses the file when it
// does not fit.
func fits(a at, sel selector, allowed []string, what string) bool {
	if sel.concept != nil {
		return true
	}
	if sel.vrs == nil {
		a.add(CodeActionVR, "%s needs to know the VR of what the rule selects, and a group, a private element or a tag the data dictionary does not know says nothing of it: such a rule may keep, remove, empty or quarantine", what)
		return false
	}
	for _, vr := range sel.vrs {
		if !slices.Contains(allowed, vr) {
			a.add(CodeActionVR, "%s fits VR %s, and the rule selects VR %s", what, strings.Join(allowed, ", "), strings.Join(sel.vrs, " or "))
			return false
		}
	}
	return true
}

// checkTemplate checks a template's grammar: literal text, "{{" and "}}"
// for a brace, and "{#Keyword}" or "{#GGGGEEEE}" for the keyed hash of a
// top-level text element. It returns the template with each placeholder's
// keyword written as its tag, and whether it is valid.
func checkTemplate(a at, s string) (string, bool) {
	if len(s) > MaxValueLength {
		a.add(CodeTooLong, "a template is at most %d bytes", MaxValueLength)
		return "", false
	}
	var out strings.Builder
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '}':
			if i+1 < len(s) && s[i+1] == '}' {
				out.WriteString("}}")
				i++
				continue
			}
			a.add(CodeBadTemplate, "a lone } at byte %d; write }} for a brace", i+1)
			return "", false
		case '{':
			if i+1 < len(s) && s[i+1] == '{' {
				out.WriteString("{{")
				i++
				continue
			}
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				a.add(CodeBadTemplate, "a { at byte %d is not closed", i+1)
				return "", false
			}
			inner := s[i+1 : i+end]
			name, hashed := strings.CutPrefix(inner, "#")
			if !hashed {
				a.add(CodeBadTemplate, "{%s} is not a placeholder: a template holds {#Keyword}, the keyed hash of an element, and nothing read as received", clipText(inner))
				return "", false
			}
			t, ok := checkPlaceholder(a, name)
			if !ok {
				return "", false
			}
			out.WriteString("{#" + formatTag(t) + "}")
			n++
			i += end
		default:
			out.WriteByte(s[i])
		}
	}
	if n > MaxPlaceholders {
		a.add(CodeTooMany, "a template has at most %d placeholders", MaxPlaceholders)
		return "", false
	}
	return out.String(), true
}

// checkPlaceholder checks the element of a placeholder: a standard element
// whose value is text, and returns its tag.
func checkPlaceholder(a at, name string) (uint32, bool) {
	var t uint32
	var ok bool
	if len(name) == 8 && isHex(name) {
		t, ok = parseTag(a, name)
	} else {
		t, ok = keywordTag(a, name)
	}
	if !ok || !checkStandard(a, t) {
		return 0, false
	}
	vrs := dictVRs(t)
	if len(vrs) != 1 || !slices.Contains(textVRs, vrs[0]) {
		a.add(CodeBadTemplate, "{#%s} is not a text element: a placeholder hashes an element of VR %s", clipText(name), strings.Join(textVRs, ", "))
		return 0, false
	}
	return t, true
}
