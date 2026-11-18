package servecmd

import (
	"context"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/master"
)

// AccessStore returns the master database as the access package's Store.
//
// The access package needs RolesFor, one indexed read of a key's role on
// the installation and on one tenant, which the master port is gaining in
// the review of pull request #9. Until the port has it, this adapter gives
// it by listing the key's grants, as the access package's own tests do.
// When master.Store has RolesFor, this function becomes "return m" and the
// adapter is deleted.
func AccessStore(m master.Store) access.Store {
	if s, ok := m.(access.Store); ok {
		return s
	}
	return rolesAdapter{m}
}

// rolesAdapter gives a master.Store the RolesFor operation.
type rolesAdapter struct {
	master.Store
}

// RolesFor returns the key's role on the installation and on tenantID, read
// from the key's grants page by page.
func (r rolesAdapter) RolesFor(ctx context.Context, keyID, tenantID string) (master.Role, master.Role, error) {
	var inst, ten master.Role
	cursor := ""
	for {
		page, next, err := r.GrantsByKey(ctx, keyID, master.Page{Cursor: cursor, Limit: master.MaxPageLimit})
		if err != nil {
			return "", "", err
		}
		for _, g := range page {
			switch {
			case g.Scope.IsInstallation():
				inst = g.Role
			case tenantID != "" && g.Scope.TenantID == tenantID:
				ten = g.Role
			}
		}
		if next == "" {
			return inst, ten, nil
		}
		cursor = next
	}
}
