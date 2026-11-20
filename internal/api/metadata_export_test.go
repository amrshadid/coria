package api

import (
	"context"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/blob"
	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/core/layout"
	"github.com/amrshadid/coria/internal/receive/sidecar"
	"github.com/amrshadid/coria/internal/tenant"
)

// MetaInstance answers the header of one instance held in store as the
// metadata service does, for the line the instance has in the scan's
// metadata file and a header bound of limit bytes, so a test can give it a
// file and a line that reception does not make.
func MetaInstance(ctx context.Context, store blob.Store, tenantName string, path label.Path, limit int64, held tenant.ScanInstance, in sidecar.Instance) (*v1.InstanceMetadata, error) {
	s := &metaSource{store: store, tenant: tenantName, path: path, limit: limit}
	return s.instance(ctx, held, in)
}

// MetaPage answers the headers of held, instances of the scan at path whose
// metadata file store holds, as one page of the metadata service does.
func MetaPage(ctx context.Context, store blob.Store, tenantName string, path label.Path, held []tenant.ScanInstance) ([]*v1.InstanceMetadata, error) {
	key, err := layout.MetadataKey(path)
	if err != nil {
		return nil, err
	}
	s := &metaSource{store: store, tenant: tenantName, path: path, key: key, limit: 1 << 20}
	return s.page(ctx, held)
}
