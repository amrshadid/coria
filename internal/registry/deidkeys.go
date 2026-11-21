package registry

import (
	"context"

	"github.com/amrshadid/coria/internal/deid/engine"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/tenant"
)

// DeidKeys returns the keys the tenant named name de-identifies with: its
// UID key, its date key and its naming key (the key of a hash action, so a
// hashed Patient ID equals its subject's label code), each read sealed
// from its database and opened with the installation key, as NamingHasher
// opens the naming key. The keys are never logged or put in an error. A
// tenant not ready is Unavailable, as Store answers; one whose store keeps
// no secrets, or whose secrets do not open with this key file, is
// Internal.
func (r *Registry) DeidKeys(ctx context.Context, name string) (engine.Keys, error) {
	st, err := r.Store(name)
	if err != nil {
		return engine.Keys{}, err
	}
	e := (*r.entries.Load())[name]
	if e == nil {
		return engine.Keys{}, fault.New(fault.NotFound, "no such tenant")
	}
	ks, ok := st.(tenant.Secrets)
	if !ok {
		return engine.Keys{}, errNoSecrets
	}
	open := func(n tenant.SecretName) ([]byte, error) {
		sealed, err := ks.TenantSecret(ctx, n)
		if err != nil {
			return nil, err
		}
		key, err := r.cfg.Keys.Open(SecretContext(e.id, n), sealed)
		if err != nil {
			return nil, fault.Wrap(fault.Internal, err, "registry: the tenant's "+string(n)+" key does not open with this key file")
		}
		return key, nil
	}
	var keys engine.Keys
	if keys.UID, err = open(tenant.SecretUID); err != nil {
		return engine.Keys{}, err
	}
	if keys.Date, err = open(tenant.SecretDate); err != nil {
		return engine.Keys{}, err
	}
	if keys.Hash, err = open(tenant.SecretNaming); err != nil {
		return engine.Keys{}, err
	}
	return keys, nil
}
