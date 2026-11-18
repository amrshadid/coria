package api

import (
	"context"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// messageNoReceiver answers a receiver name the tenant does not have.
const messageNoReceiver = "the tenant has no receiver with this name"

// receiverService is ReceiverService: a tenant's receivers, kept in its
// store, on the ports and addresses the installation allows. Whether a
// receiver listens is the part of the server that runs receivers; a handler
// only stores what it is to do, and answers the health it is told
// (Receivers).
type receiverService struct{ c *core }

var _ magnetarv1connect.ReceiverServiceHandler = receiverService{}

// lookupReceiver reads a receiver, answering one the tenant does not have as
// NotFound with the contract's message.
func lookupReceiver(ctx context.Context, p tenant.Pipeline, name string) (tenant.Receiver, error) {
	r, err := p.Receiver(ctx, name)
	if fault.KindOf(err) == fault.NotFound {
		return r, fault.Wrap(fault.NotFound, err, messageNoReceiver)
	}
	return r, err
}

// message returns r as the contract writes it, with its health now.
func (s receiverService) message(tenantName string, r tenant.Receiver) *v1.Receiver {
	return receiverMessage(r, s.c.receivers.Health(tenantName, r))
}

// AddReceiver adds a receiver to a tenant, with the contract's defaults
// filled in and stored, on a port the installation allotted to the tenant
// and an address it lists.
func (s receiverService) AddReceiver(ctx context.Context, req *v1.AddReceiverRequest) (*v1.AddReceiverResponse, error) {
	c := s.c
	d, err := c.changing(ctx, magnetarv1connect.ReceiverServiceAddReceiverProcedure)
	if err != nil {
		return nil, err
	}
	p, st, err := c.inTenant(ctx, d, req.GetTenant(), requestReceiverDetails(req.GetReceiver()))
	if err != nil {
		return nil, err
	}
	in, err := receiverFromRequest("receiver", req.GetReceiver())
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	c.places.RLock()
	defer c.places.RUnlock()
	if err := c.checkReceiverPlace(ctx, st, in); err != nil {
		return nil, c.failIn(d, st, err)
	}
	out, err := p.AddReceiver(ctx, in, c.now(), changeEntry(d, access.Facts{Details: receiverDetails(in, true)}), c.adminGuard(d))
	if err != nil {
		return nil, c.failIn(d, st, fieldOf("receiver", portError(err)))
	}
	return &v1.AddReceiverResponse{Receiver: s.message(req.GetTenant(), out)}, nil
}

// GetReceiver answers one receiver of a tenant, with its health now.
func (s receiverService) GetReceiver(ctx context.Context, req *v1.GetReceiverRequest) (*v1.GetReceiverResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.ReceiverServiceGetReceiverProcedure)
	if err != nil {
		return nil, err
	}
	p, st, err := c.inTenant(ctx, d, req.GetTenant(), nil)
	if err != nil {
		return nil, err
	}
	if err := itemName("name", req.GetName()); err != nil {
		return nil, c.failIn(d, st, err)
	}
	out, err := lookupReceiver(ctx, p, req.GetName())
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	c.done(d, st)
	return &v1.GetReceiverResponse{Receiver: s.message(req.GetTenant(), out)}, nil
}

// ListReceivers answers a page of a tenant's receivers, ordered by name,
// each with its health now.
func (s receiverService) ListReceivers(ctx context.Context, req *v1.ListReceiversRequest) (*v1.ListReceiversResponse, error) {
	c := s.c
	d, err := c.decision(ctx, magnetarv1connect.ReceiverServiceListReceiversProcedure)
	if err != nil {
		return nil, err
	}
	p, st, err := c.inTenant(ctx, d, req.GetTenant(), nil)
	if err != nil {
		return nil, err
	}
	page, err := pageOf(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	items, next, err := p.ListReceivers(ctx, page)
	if err != nil {
		return nil, c.failIn(d, st, listError(err, page.Cursor))
	}
	c.done(d, st)
	out := &v1.ListReceiversResponse{Receivers: make([]*v1.Receiver, 0, len(items)), NextPageToken: next}
	for _, it := range items {
		out.Receivers = append(out.Receivers, s.message(req.GetTenant(), it))
	}
	return out, nil
}

// SetReceiver changes the fields of a receiver its update_mask names, under
// every rule AddReceiver applies.
func (s receiverService) SetReceiver(ctx context.Context, req *v1.SetReceiverRequest) (*v1.SetReceiverResponse, error) {
	c := s.c
	d, err := c.changing(ctx, magnetarv1connect.ReceiverServiceSetReceiverProcedure)
	if err != nil {
		return nil, err
	}
	p, st, err := c.inTenant(ctx, d, req.GetTenant(), maskDetails(req.GetUpdateMask().GetPaths()))
	if err != nil {
		return nil, err
	}
	in := req.GetReceiver()
	if in == nil {
		return nil, c.failIn(d, st, invalid("receiver", "is required: its name names the receiver to change"))
	}
	if err := itemName("receiver.name", in.GetName()); err != nil {
		return nil, c.failIn(d, st, err)
	}
	paths, err := receiverMask.paths(req.GetUpdateMask())
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	fields := make([]tenant.ReceiverField, len(paths))
	for i, p := range paths {
		fields[i] = tenant.ReceiverField(p)
	}
	change, err := receiverChange(in, fields)
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	c.places.RLock()
	defer c.places.RUnlock()
	stored, err := lookupReceiver(ctx, p, in.GetName())
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	result, err := tenant.ApplyReceiver(stored, change, fields)
	if err != nil {
		return nil, c.failIn(d, st, fieldOf("receiver", err))
	}
	if err := c.checkReceiverPlace(ctx, st, result); err != nil {
		return nil, c.failIn(d, st, err)
	}
	out, err := p.SetReceiver(ctx, change, fields, c.now(), changeEntry(d, access.Facts{}), c.adminGuard(d))
	if err != nil {
		return nil, c.failIn(d, st, fieldOf("receiver", portError(err)))
	}
	return &v1.SetReceiverResponse{Receiver: s.message(req.GetTenant(), out)}, nil
}

// RemoveReceiver removes a receiver's configuration. Its receptions and
// quarantined files stay, naming it.
func (s receiverService) RemoveReceiver(ctx context.Context, req *v1.RemoveReceiverRequest) (*v1.RemoveReceiverResponse, error) {
	c := s.c
	d, err := c.changing(ctx, magnetarv1connect.ReceiverServiceRemoveReceiverProcedure)
	if err != nil {
		return nil, err
	}
	p, st, err := c.inTenant(ctx, d, req.GetTenant(), nil)
	if err != nil {
		return nil, err
	}
	if err := itemName("name", req.GetName()); err != nil {
		return nil, c.failIn(d, st, err)
	}
	// Read first for the type, port and AE title the entry records.
	stored, err := lookupReceiver(ctx, p, req.GetName())
	if err != nil {
		return nil, c.failIn(d, st, err)
	}
	facts := access.Facts{Details: receiverDetails(stored, false)}
	out, err := p.RemoveReceiver(ctx, req.GetName(), changeEntry(d, facts), c.adminGuard(d))
	if fault.KindOf(err) == fault.NotFound {
		err = fault.Wrap(fault.NotFound, err, messageNoReceiver)
	}
	if err != nil {
		return nil, c.failWith(d, st, facts, portError(err))
	}
	// The receiver as it was, stopped: its configuration as stored, and a
	// health that says it no longer runs, whatever ran it.
	gone := Health{State: HealthDown, Reason: DownOther, Detail: detailRemoved, Since: c.now()}
	return &v1.RemoveReceiverResponse{Receiver: receiverMessage(out, gone)}, nil
}

// detailRemoved is the phrase of a removed receiver's health.
const detailRemoved = "removed"
