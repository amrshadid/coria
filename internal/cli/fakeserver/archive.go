package fakeserver

import (
	"sort"
	"strings"

	connect "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/gen/magnetar/v1/magnetarv1connect"
	"github.com/amrshadid/magnetar/internal/fault"
)

// archive is what the fake keeps for the four services of a tenant's
// archive: storages, receivers, receptions and quarantined files, each kept
// under its tenant. Its zero value is empty and ready.
type archive struct {
	storages   map[string]*v1.Storage
	secrets    map[string]string
	holdsFiles map[string]bool
	testFaults map[string]v1.StorageTestStep
	receivers  map[string]*v1.Receiver
	receptions map[string]*v1.Reception
	files      map[string]*v1.QuarantinedFile
	headers    map[string]string
	health     map[string]*v1.ReceiverHealth
}

// key joins a tenant and an item's name into the key of the tables above.
func key(tenant, name string) string { return tenant + "/" + name }

// forTenant lists the keys of a table that belong to one tenant, in order.
func forTenant[T any](table map[string]T, tenant string) []string {
	var keys []string
	for k := range table {
		if strings.HasPrefix(k, tenant+"/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// ensure makes the tables.
func (a *archive) ensure() {
	if a.storages == nil {
		a.storages = map[string]*v1.Storage{}
		a.secrets = map[string]string{}
		a.holdsFiles = map[string]bool{}
		a.testFaults = map[string]v1.StorageTestStep{}
		a.receivers = map[string]*v1.Receiver{}
		a.receptions = map[string]*v1.Reception{}
		a.files = map[string]*v1.QuarantinedFile{}
		a.headers = map[string]string{}
		a.health = map[string]*v1.ReceiverHealth{}
	}
}

// StorageSecret returns the secret access key AddStorage or SetStorage
// received for a storage, so a test can show it reached the server and
// nowhere else.
func (s *Server) StorageSecret(tenant, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arch.ensure()
	return s.arch.secrets[key(tenant, name)]
}

// StorageHoldsFiles makes RemoveStorage refuse a storage, as the server does
// for one that holds archived files.
func (s *Server) StorageHoldsFiles(tenant, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arch.ensure()
	s.arch.holdsFiles[key(tenant, name)] = true
}

// FailStorageTest makes TestStorage report a storage's step as failed,
// unreachable, with the later steps skipped as the contract says.
func (s *Server) FailStorageTest(tenant, name string, step v1.StorageTestStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arch.ensure()
	s.arch.testFaults[key(tenant, name)] = step
}

// SetReceiverHealth makes a receiver answer the given health from now on, as
// a server whose listener could not start would.
func (s *Server) SetReceiverHealth(tenant, name string, h *v1.ReceiverHealth) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arch.ensure()
	s.arch.health[key(tenant, name)] = h
}

// AddReception puts a reception in a tenant, as the server's pipeline would.
func (s *Server) AddReception(tenant string, r *v1.Reception) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arch.ensure()
	s.arch.receptions[key(tenant, r.GetId())] = r
}

// AddQuarantined puts a quarantined file in a tenant, with the header its
// record would answer when asked for.
func (s *Server) AddQuarantined(tenant string, f *v1.QuarantinedFile, headerJSON string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arch.ensure()
	s.arch.files[key(tenant, f.GetId())] = f
	s.arch.headers[key(tenant, f.GetId())] = headerJSON
}

// Reception returns a copy of a reception as the fake holds it now.
func (s *Server) Reception(tenant, id string) *v1.Reception {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arch.ensure()
	if r := s.arch.receptions[key(tenant, id)]; r != nil {
		return proto.Clone(r).(*v1.Reception)
	}
	return nil
}

// registerArchive adds the four archive services to a Connect server.
func (s *Server) registerArchive(srv *connect.Server) {
	magnetarv1connect.RegisterStorageServiceHandler(srv, storageHandler{s})
	magnetarv1connect.RegisterReceiverServiceHandler(srv, receiverHandler{s})
	magnetarv1connect.RegisterReceptionServiceHandler(srv, receptionHandler{s})
	magnetarv1connect.RegisterQuarantineServiceHandler(srv, quarantineHandler{s})
}

// begin takes the lock, makes the tables and checks the tenant: what every
// method of the four services starts with.
func (s *Server) begin(tenant string) error {
	s.mu.Lock()
	s.arch.ensure()
	if !tenantName.MatchString(tenant) {
		return fault.New(fault.Invalid, "tenant: is 1 to 63 of a-z, 0-9 and -, starting with a letter")
	}
	return s.checkTenant(tenant)
}

// itemOK checks an item's name against the tenant-name rule, which storages
// and receivers share.
func itemOK(field, name string) error {
	if !tenantName.MatchString(name) {
		return fault.New(fault.Invalid, field+": is 1 to 63 of a-z, 0-9 and -, starting with a letter")
	}
	return nil
}

// pageOfKeys pages a tenant's keys the way the contract pages every list.
func pageOfKeys(keys []string, size int32, token string) (page []string, next string, err error) {
	from, to, next, err := pageOf(len(keys), size, token)
	if err != nil {
		return nil, "", err
	}
	return keys[from:to], next, nil
}
