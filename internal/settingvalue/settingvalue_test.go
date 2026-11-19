package settingvalue_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/amrshadid/coria/internal/core/settings"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/settingvalue"
)

// stored is a store of settings: a value per name, and an error for every
// read when fail is set.
type stored struct {
	v    map[string]string
	fail error
}

func (s stored) Setting(_ context.Context, name string) (string, error) {
	if s.fail != nil {
		return "", s.fail
	}
	if v, ok := s.v[name]; ok {
		return v, nil
	}
	return "", fault.New(fault.NotFound, "no such setting")
}

// TestTheValueThatAppliesIsReadInOrder guards the order of the values: a
// tenant's own, then the installation's, then the built-in default; and a
// setting with no installation value goes from the tenant's straight to the
// default.
func TestTheValueThatAppliesIsReadInOrder(t *testing.T) {
	ctx := context.Background()
	master := stored{v: map[string]string{settings.IncomingLimit: "10GiB", settings.ReceiverPorts: "4000-4010"}}
	r := settingvalue.Reader{Master: master}
	none := stored{v: map[string]string{}}
	own := stored{v: map[string]string{settings.IncomingLimit: "1GiB", settings.ReceiverPorts: "5000"}}
	for _, c := range []struct {
		st   stored
		name string
		want string
	}{
		{own, settings.IncomingLimit, "1GiB"},
		{none, settings.IncomingLimit, "10GiB"},
		{none, settings.EventsRetention, "90d"},
		{own, settings.ReceiverPorts, "5000"},
		// receiver.ports has no installation value: what the master holds
		// under that name never applies to a tenant.
		{none, settings.ReceiverPorts, ""},
	} {
		got, err := r.Tenant(ctx, c.st, c.name)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if got, err := r.Installation(ctx, settings.ReceiverHeaderLimit); err != nil || got != "64MiB" {
		t.Errorf("header limit %q, %v; want the default", got, err)
	}
	n, err := r.OnTenant(none).IncomingLimit(ctx)
	if err != nil || n != 10<<30 {
		t.Errorf("incoming limit %d, %v", n, err)
	}
}

// TestABrokenValueIsTheServersFault guards the check as a value is read: a
// stored value its rule refuses, or a name the list does not hold, is an
// Internal fault naming the setting and never the value; a store that does
// not answer is its own error, passed on.
func TestABrokenValueIsTheServersFault(t *testing.T) {
	ctx := context.Background()
	broken := "not-a-size-but-a-value-to-keep-out-of-errors"
	r := settingvalue.Reader{Master: stored{v: map[string]string{settings.ReceiverHeaderLimit: broken}}}
	_, err := r.HeaderLimit(ctx)
	if fault.KindOf(err) != fault.Internal || !strings.Contains(err.Error(), settings.ReceiverHeaderLimit) || strings.Contains(err.Error(), broken) {
		t.Fatalf("a broken value: %v", err)
	}
	if _, err := r.Tenant(ctx, stored{v: map[string]string{settings.IncomingLimit: broken}}, settings.IncomingLimit); fault.KindOf(err) != fault.Internal ||
		strings.Contains(err.Error(), broken) {
		t.Fatalf("a broken tenant value: %v", err)
	}
	if _, err := r.Installation(ctx, "no.such"); fault.KindOf(err) != fault.Internal {
		t.Fatalf("an unknown name: %v", err)
	}
	down := errors.New("the master does not answer")
	if _, err := (settingvalue.Reader{Master: stored{fail: down}}).Reserve(ctx); !errors.Is(err, down) {
		t.Fatalf("a store that fails: %v", err)
	}
}

// TestAChangeIsReadAtOnce guards that nothing is cached: a value changed in
// the store is the one the next read returns.
func TestAChangeIsReadAtOnce(t *testing.T) {
	ctx := context.Background()
	m := stored{v: map[string]string{}}
	r := settingvalue.Reader{Master: m}
	if n, _ := r.HeaderLimit(ctx); n != 64<<20 {
		t.Fatalf("default %d", n)
	}
	m.v[settings.ReceiverHeaderLimit] = "128MiB"
	if n, err := r.HeaderLimit(ctx); err != nil || n != 128<<20 {
		t.Fatalf("after a change %d, %v", n, err)
	}
}
