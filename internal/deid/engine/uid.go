package engine

import (
	"crypto/hmac"
	"crypto/sha256"
	"math/big"
	"strings"

	dicomuid "github.com/amrshadid/go-dicom/uid"
)

// uidRoot is the root of a UID derived from a UUID (PS3.5 B.2): "2.25."
// followed by the UUID as one decimal integer.
const uidRoot = "2.25."

// RemapUID returns the UID that replaces original under a tenant's UID key:
// "2.25." followed by the decimal of a version 8 UUID (RFC 9562 5.8) whose
// 122 free bits are the first bits of HMAC-SHA-256(key, original). The same
// original always gives the same UID under one key, so references between
// files, and a file received twice, still match; another tenant's key gives
// another UID; and nobody without the key can tell the original from the
// result. It is at most 44 characters, a valid UID by PS3.5 9.1 and B.2,
// and needs no table.
//
// original is the value without its padding. RemapUID does not check that
// it is a well-formed UID: a malformed value identifies as well as a good
// one and is replaced the same way. Whether a UID is remapped at all
// (Registered) is the caller's question.
func RemapUID(key []byte, original string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(original))
	sum := m.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	// RFC 9562 5.8: version 8 in the high nibble of octet 6, the variant
	// 10 in the two high bits of octet 8.
	u[6] = u[6]&0x0F | 0x80
	u[8] = u[8]&0x3F | 0x80
	return uidRoot + new(big.Int).SetBytes(u[:]).String()
}

// Registered reports whether a UID is one the standard registers (PS3.6
// Annex A): a SOP class, a transfer syntax, a coding scheme, a well-known
// instance. It identifies a kind of object, never a patient, and replacing
// it would make the file unreadable, so it is never remapped. The library's
// registry does not list every UID the standard defines (Secondary Capture,
// RT Dose, RT Plan and Segmentation among those missing), so the root the
// standard keeps for its own UIDs, 1.2.840.10008 (PS3.5 B.1), counts too:
// no identifier of a patient or a study can be made under it.
func Registered(u string) bool {
	return dicomuid.GetUIDInfo(u) != nil || strings.HasPrefix(u, standardRoot)
}

// standardRoot is the UID root PS3.5 B.1 keeps for UIDs the standard
// defines.
const standardRoot = "1.2.840.10008."

// remapUIDValue remaps each UID of a value of VR UI, values separated by
// backslashes (PS3.5 6.4), keeping the registered ones and the empty ones.
// It returns the new value and the originals it replaced.
func remapUIDValue(key []byte, value string) (string, []string) {
	parts := strings.Split(value, `\`)
	var replaced []string
	for i, p := range parts {
		p = strings.Trim(p, "\x00 ")
		parts[i] = p
		if p == "" || Registered(p) {
			continue
		}
		parts[i] = RemapUID(key, p)
		replaced = append(replaced, p)
	}
	return strings.Join(parts, `\`), replaced
}
