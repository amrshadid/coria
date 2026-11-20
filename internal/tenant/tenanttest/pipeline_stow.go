package tenanttest

import (
	"reflect"

	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/tenant"
)

// stowReceiverOf returns a stow receiver with the contract's defaults that
// takes requests from the keys named.
func stowReceiverOf(name, storage string, keys ...string) tenant.Receiver {
	return tenant.Receiver{
		Name: name, Type: tenant.ReceiverSTOW, Storage: storage,
		STOW:        tenant.STOWReceiver{Keys: keys, MaxRequestBytes: tenant.DefaultMaxRequestBytes},
		QuietPeriod: tenant.DefaultQuietPeriod, MaxOpenTime: tenant.DefaultMaxOpenTime, MaxFileBytes: tenant.DefaultMaxFileBytes,
	}
}

// testSTOWReceivers guards the stow receiver as a store keeps it: added and
// read back field for field beside a DIMSE receiver, on no port; a key
// listed by two stow receivers of the tenant refused, so a request is never
// taken by a choice between two; a change of its keys and largest request
// kept; a field of the other type refused in a change; and a stow receiver
// with a DIMSE field refused as invalid.
func testSTOWReceivers(e *penv) {
	e.addStorage("store")
	e.addReceiver("dimse", "store", 11112)
	r := stowReceiverOf("web", "store", "key-a", "key-b")
	r.Enabled = true
	r.MaxFileBytes = 1 << 20
	got, err := e.p.AddReceiver(e.ctx, r, at(1), e.audit(), nil)
	e.ok("AddReceiver stow", err)
	want := r
	want.Created, want.Updated = at(1), at(1)
	if !reflect.DeepEqual(got, want) {
		e.Errorf("AddReceiver answered\n%+v\nwant\n%+v", got, want)
	}
	read, err := e.p.Receiver(e.ctx, "web")
	e.ok("Receiver", err)
	if !reflect.DeepEqual(read, want) {
		e.Errorf("Receiver read back\n%+v\nwant\n%+v", read, want)
	}

	_, err = e.p.AddReceiver(e.ctx, stowReceiverOf("web2", "store", "key-c", "key-b"), at(2), e.audit(), nil)
	e.wantPrecondition("AddReceiver stow listing a key another stow receiver lists", err)
	_, err = e.p.AddReceiver(e.ctx, stowReceiverOf("web3", "store", "key-c"), at(2), e.audit(), nil)
	e.ok("AddReceiver stow with keys of its own", err)
	_, err = e.p.AddReceiver(e.ctx, stowReceiverOf("closed", "store"), at(2), e.audit(), nil)
	e.ok("AddReceiver stow listing no key", err)

	change := tenant.Receiver{Name: "web", STOW: tenant.STOWReceiver{Keys: []string{"key-c"}}}
	_, err = e.p.SetReceiver(e.ctx, change, []tenant.ReceiverField{tenant.FieldSTOWKeys}, at(3), e.audit(), nil)
	e.wantPrecondition("SetReceiver stow to a key another stow receiver lists", err)
	change = tenant.Receiver{Name: "web", STOW: tenant.STOWReceiver{Keys: []string{"key-d"}, MaxRequestBytes: 1 << 30}}
	set, err := e.p.SetReceiver(e.ctx, change, []tenant.ReceiverField{tenant.FieldSTOWKeys, tenant.FieldSTOWMaxRequest}, at(3), e.audit(), nil)
	e.ok("SetReceiver stow keys and largest request", err)
	if !reflect.DeepEqual(set.STOW, tenant.STOWReceiver{Keys: []string{"key-d"}, MaxRequestBytes: 1 << 30}) || set.Type != tenant.ReceiverSTOW {
		e.Errorf("SetReceiver kept %+v", set.STOW)
	}
	_, err = e.p.SetReceiver(e.ctx, tenant.Receiver{Name: "web", DIMSE: tenant.DIMSEReceiver{Port: 104}},
		[]tenant.ReceiverField{tenant.FieldDIMSEPort}, at(4), e.audit(), nil)
	e.wantKind("SetReceiver stow naming a DIMSE field", err, fault.Invalid)
	_, err = e.p.SetReceiver(e.ctx, tenant.Receiver{Name: "dimse", STOW: tenant.STOWReceiver{Keys: []string{"key-e"}}},
		[]tenant.ReceiverField{tenant.FieldSTOWKeys}, at(4), e.audit(), nil)
	e.wantKind("SetReceiver dimse naming a stow field", err, fault.Invalid)

	bad := stowReceiverOf("bad", "store", "key-f")
	bad.DIMSE.Port = 104
	_, err = e.p.AddReceiver(e.ctx, bad, at(5), e.audit(), nil)
	e.wantKind("AddReceiver stow with a port", err, fault.Invalid)
	bad = stowReceiverOf("bad", "store", "key f")
	_, err = e.p.AddReceiver(e.ctx, bad, at(5), e.audit(), nil)
	e.wantKind("AddReceiver stow with a key id that is not one", err, fault.Invalid)
	bad = stowReceiverOf("bad", "store", "key-g", "key-g")
	_, err = e.p.AddReceiver(e.ctx, bad, at(5), e.audit(), nil)
	e.wantKind("AddReceiver stow listing a key twice", err, fault.Invalid)
	bad = stowReceiverOf("bad", "store", "key-h")
	bad.STOW.MaxRequestBytes = 0
	_, err = e.p.AddReceiver(e.ctx, bad, at(5), e.audit(), nil)
	e.wantKind("AddReceiver stow with no largest request", err, fault.Invalid)
}
