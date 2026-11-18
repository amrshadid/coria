package registry

// filesPerTenant is the most files one SQLite tenant's store holds open: its
// database file and its write-ahead log for each of its five connections
// (one writer and four readers, internal/driver/sqlite), and its
// shared-memory file, which SQLite opens once per database in a process
// (unix VFS, os_unix.c). An idle store, as measured with 1,000 tenants
// (TestThousandTenants), holds five; the count here is the most, so the
// warning comes before the limit is met under load, not after. A Postgres
// tenant holds sockets instead, as many as its pool's connections, which
// are fewer.
const filesPerTenant = 11

// fileMargin is the files the process needs besides the tenants: the master
// database (as many as a tenant), the key file, the listening sockets and
// the local socket, the API's client connections, and the logs. 256 leaves
// room for a few hundred concurrent clients on top of them.
const fileMargin = 256

// warnFileLimit logs one warning, with both numbers, when n tenants would
// need more open files than the process may have. It does not refuse to
// start: beyond the limit a tenant whose open fails is unreachable and
// retried, and the others are served (package documentation).
func (r *Registry) warnFileLimit(n int) {
	limit := r.fileLimit()
	need := int64(n)*filesPerTenant + fileMargin
	if limit < 0 || need <= limit {
		return
	}
	r.log.Warn("the limit on open files may be too low for every tenant's database: raise it (ulimit -n, LimitNOFILE)",
		"tenants", n, "files_needed", need, "files_per_tenant", filesPerTenant, "limit", limit)
}
