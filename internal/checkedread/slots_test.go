package checkedread

import (
	"strconv"
	"testing"

	"github.com/amrshadid/coria/internal/fault"
)

// TestOpenReadsBounds guards the bounds of open reads: a caller's 17th is
// refused as resource exhausted while another caller's first is not; one
// ended frees its place, once however often its end is called; and the
// server's 257th is refused whoever asks.
func TestOpenReadsBounds(t *testing.T) {
	var o Slots
	var ends []func()
	for range MaxOpenReads {
		end, err := o.Take("key/a")
		if err != nil {
			t.Fatal(err)
		}
		ends = append(ends, end)
	}
	if _, err := o.Take("key/a"); fault.KindOf(err) != fault.Exhausted {
		t.Fatalf("the 17th read of one caller: %v, want exhausted", err)
	}
	endB, err := o.Take("key/b")
	if err != nil {
		t.Fatalf("another caller's first read: %v", err)
	}
	ends[0]()
	ends[0]()
	if _, err := o.Take("key/a"); err != nil {
		t.Fatalf("a read after one ended: %v", err)
	}
	if _, err := o.Take("key/a"); fault.KindOf(err) != fault.Exhausted {
		t.Fatalf("an end called twice freed two places: %v", err)
	}
	endB()
	var all Slots
	for i := range MaxServerReads {
		if _, err := all.Take("key/" + strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := all.Take("local"); fault.KindOf(err) != fault.Exhausted {
		t.Fatalf("the server's 257th read: %v, want exhausted", err)
	}
}
