package engine

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"time"
)

// errBadDate is a date that is not one of its VR, or that a shift would move
// before the year 1.
var errBadDate = errors.New("not a date of its VR")

// ShiftDays returns a subject's date shift under a tenant's date key: a
// whole number of days from 1 to most, the same for every file of the
// subject. The subject is its Patient ID (0010,0020) and the Issuer of
// Patient ID (0010,0021), each as received without padding, so two patients
// of one ID from two issuers get their own shifts. The bytes hashed are the
// issuer's length as an unsigned varint, the issuer, then the ID, so no
// issuer and ID can be read as another pair. The offset is the first 64
// bits of HMAC-SHA-256 under the key, modulo most, plus one: never zero, so
// no date is kept as it was; the bias of the modulo is below 2^-50 for any
// most the profile allows.
//
// The shift is never written into a file: Longitudinal Temporal
// Information Modified (0028,0303) says dates were moved, and nothing says
// by how much (PS3.15 E.3.6).
func ShiftDays(key []byte, issuer, patientID string, most int32) int {
	m := hmac.New(sha256.New, key)
	var n [binary.MaxVarintLen64]byte
	m.Write(n[:binary.PutUvarint(n[:], uint64(len(issuer)))])
	m.Write([]byte(issuer))
	m.Write([]byte(patientID))
	sum := m.Sum(nil)
	if most < 1 {
		most = 1
	}
	return 1 + int(binary.BigEndian.Uint64(sum[:8])%uint64(most)) // #nosec G115 -- the remainder is below most, an int32.
}

// shiftDA moves every date of a DA value back by days (PS3.5 6.2: YYYYMMDD,
// values separated by backslashes). An empty value stays empty.
func shiftDA(value string, days int) (string, error) {
	return eachValue(value, func(v string) (string, error) {
		if !validText("DA", v) {
			return "", errBadDate
		}
		return shiftDate(v, days)
	})
}

// shiftDT moves the date part of every value of a DT value back by days,
// keeping the time of day, its fraction and its offset from UTC (PS3.5 6.2:
// YYYY[MM[DD[HH[MM[SS[.F{1-6}]]]]]][&ZZXX], & a sign). A value of only a
// year, or a year and month, is shifted from the first day of that period
// and written back with the same precision. A value that is not a DT
// fails.
func shiftDT(value string, days int) (string, error) {
	return eachValue(value, func(v string) (string, error) {
		if !validText("DT", v) {
			return "", errBadDate
		}
		digits := 0
		for digits < len(v) && digits < 8 && v[digits] >= '0' && v[digits] <= '9' {
			digits++
		}
		datePart := v[:digits]
		shifted, err := shiftDate(datePart, days)
		if err != nil {
			return "", err
		}
		return shifted + v[digits:], nil
	})
}

// shiftDate moves a date of 4, 6 or 8 digits back by days and writes it
// with the same number of digits; a year or a year and month is taken from
// its first day.
func shiftDate(v string, days int) (string, error) {
	for i := range len(v) {
		if v[i] < '0' || v[i] > '9' {
			return "", errBadDate
		}
	}
	year, _ := strconv.Atoi(v[:4])
	month, day := 1, 1
	if len(v) >= 6 {
		month, _ = strconv.Atoi(v[4:6])
	}
	if len(v) == 8 {
		day, _ = strconv.Atoi(v[6:8])
	}
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	// time.Date normalizes 20250230 into March: such a date is not a date.
	if t.Year() != year || int(t.Month()) != month || t.Day() != day || year < 1 {
		return "", errBadDate
	}
	t = t.AddDate(0, 0, -days)
	if t.Year() < 1 {
		return "", errBadDate
	}
	out := t.Format("20060102")
	return out[:len(v)], nil
}

// generalizeAge makes an age over 89 years 090Y (PS3.15 E.3.7, as HIPAA
// 164.514(b)(2)(i)(C) groups ages over 89): an AS value is nnnD, nnnW,
// nnnM or nnnY (PS3.5 6.2). A younger age is kept.
func generalizeAge(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) != 4 {
		return "", errBadDate
	}
	n, err := strconv.Atoi(value[:3])
	if err != nil || strings.ContainsAny(value[:3], "+-") {
		return "", errBadDate
	}
	// Three digits of days, weeks or months are at most 999 months, under
	// 84 years, so only an age in years can be over 89.
	switch value[3] {
	case 'D', 'W', 'M':
	case 'Y':
		if n > 89 {
			return "090Y", nil
		}
	default:
		return "", errBadDate
	}
	return value, nil
}

// generalizeDate makes every date of a DA or DT value the first of January
// of its year (DA YYYY0101; DT YYYY0101, its time of day dropped).
func generalizeDate(value string) (string, error) {
	return eachValue(value, func(v string) (string, error) {
		if len(v) < 4 {
			return "", errBadDate
		}
		for i := range 4 {
			if v[i] < '0' || v[i] > '9' {
				return "", errBadDate
			}
		}
		return v[:4] + "0101", nil
	})
}

// eachValue applies f to every value of a multi-valued text value (PS3.5
// 6.4), leaving empty values empty.
func eachValue(value string, f func(string) (string, error)) (string, error) {
	parts := strings.Split(value, `\`)
	for i, p := range parts {
		p = strings.Trim(p, " \x00")
		if p == "" {
			parts[i] = ""
			continue
		}
		out, err := f(p)
		if err != nil {
			return "", err
		}
		parts[i] = out
	}
	return strings.Join(parts, `\`), nil
}
