package sqlkit

import (
	"reflect"
	"testing"
	"time"

	"github.com/amrshadid/magnetar/internal/core/naming"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// TestTimeCursor guards the one time cursor both drivers page newest-first
// lists by: a cursor made after a row reads back as that row's time to the
// microsecond and its id, the first page has none, a cursor of another
// binding is Invalid, and a filter's bound writes as its microseconds or as
// nothing for no bound.
func TestTimeCursor(t *testing.T) {
	at := time.Date(2026, 11, 18, 9, 30, 0, 123456789, time.UTC)
	c := TimeCursor("list", "b1", at, "rc-7")
	pos, err := PageAfterTime(master.Page{Limit: 10, Cursor: c}, "list", "b1")
	if err != nil || pos == nil || !pos.At.Equal(at.Truncate(time.Microsecond)) || pos.ID != "rc-7" {
		t.Fatalf("PageAfterTime = %+v, %v", pos, err)
	}
	if pos, err := PageAfterTime(master.Page{Limit: 10}, "list", "b1"); pos != nil || err != nil {
		t.Errorf("the first page = %+v, %v", pos, err)
	}
	if _, err := PageAfterTime(master.Page{Limit: 10, Cursor: c}, "list", "b2"); fault.KindOf(err) != fault.Invalid {
		t.Errorf("another binding's cursor: %v", err)
	}
	if MicrosText(time.Time{}) != "" || MicrosText(at) != "1794994200123456" {
		t.Errorf("MicrosText = %q, %q", MicrosText(time.Time{}), MicrosText(at))
	}
}

// TestCut guards the page cut every list makes from limit+1 rows.
func TestCut(t *testing.T) {
	rows, last, more := Cut([]int{1, 2, 3}, 2)
	if !reflect.DeepEqual(rows, []int{1, 2}) || last != 2 || !more {
		t.Errorf("Cut of 3 to 2 = %v, %d, %v", rows, last, more)
	}
	if rows, _, more := Cut([]int{1, 2}, 2); len(rows) != 2 || more {
		t.Errorf("Cut of 2 to 2 = %v, %v", rows, more)
	}
}

// TestReceiverConfig guards that a receiver's configuration reads back as
// it was written, every field, with and without a naming rule and TLS, so
// the one stored format serves both drivers.
func TestReceiverConfig(t *testing.T) {
	spec := naming.Spec{Subject: "PatientID", Session: "StudyDate", Scan: "SeriesNumber", PatternAttribute: "StudyDescription", Pattern: "(.*)"}
	full := tenant.Receiver{DIMSE: tenant.DIMSEReceiver{AETitle: "ARCHIVE", AnyCaller: true,
		Callers:    []tenant.CallerRule{{CallingAETitle: "SCANNER1", AddressRange: "192.0.2.0/24"}},
		TLS:        &tenant.ReceiverTLS{CertificateFile: "c.pem", PrivateKeyFile: "k.pem"},
		SOPClasses: []string{"1.2.840.10008.5.1.4.1.1.4"}, TransferSyntaxes: []string{"1.2.840.10008.1.2.1"}}, Naming: &spec}
	for _, r := range []tenant.Receiver{full, {DIMSE: tenant.DIMSEReceiver{AETitle: "PLAIN"}}} {
		config, namingText, err := ReceiverConfigText(r)
		if err != nil {
			t.Fatal(err)
		}
		var got tenant.Receiver
		if err := ReadReceiverConfig(&got, config, namingText); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.DIMSE, r.DIMSE) || !reflect.DeepEqual(got.Naming, r.Naming) {
			t.Errorf("read back %+v %+v, want %+v %+v", got.DIMSE, got.Naming, r.DIMSE, r.Naming)
		}
	}
	if err := ReadReceiverConfig(&tenant.Receiver{}, "{", ""); fault.KindOf(err) != fault.Internal {
		t.Errorf("a column that does not read: %v", err)
	}
}

// TestToSHA256 guards the reading of a checksum column: 32 bytes are the
// checksum, anything else (NULL included) is none.
func TestToSHA256(t *testing.T) {
	b := make([]byte, 32)
	b[0] = 7
	if s := ToSHA256(b); s[0] != 7 || s.IsZero() {
		t.Errorf("32 bytes read as %v", s)
	}
	if !ToSHA256(nil).IsZero() || !ToSHA256(b[:31]).IsZero() {
		t.Error("a short or NULL column is not none")
	}
}
