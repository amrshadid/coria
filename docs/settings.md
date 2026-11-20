# Settings

[Documentation index](README.md) · [Running CORIA](running.md) · [The command line](command-line.md)

The installation's settings and the tenants' overrides of them. A tenant's admin
may override only the ones marked so; the rest, and every value that decides what
the installation's disk, network or ports are used for, are the installation
admin's.

## Where data and receivers may go

Where a tenant's data may go is the installation's decision, not the
tenant's. Settings are read and changed with `coria setting` or
`SettingsService` (`GetSetting`, `SetSetting`, `UnsetSetting`, `ListSettings`); the
fixed list, with each default, bound and scope, is
[internal/core/settings](../internal/core/settings/doc.go).

| Setting | Scope | Who sets it | Default | Form |
|---|---|---|---|---|
| `storage.directory-roots` | installation only | admin on the installation | empty: no directory storage | one `name=path` per line; no two roots may lead to one directory once their symbolic links are followed; a caller who is not an admin on the installation reads the names only |
| `storage.s3-endpoints` | installation only | admin on the installation | empty: no S3 storage | one URL per line, optionally followed by `plain-http` and `private-address`; a storage at a `private-address` endpoint must set `path_style` |
| `receiver.ports` | each tenant, no installation value | admin on the installation only | empty: no DIMSE receiver | ports and ranges separated by commas |
| `receiver.bind-addresses` | installation only | admin on the installation | empty: listen on every address | IP addresses separated by commas; the first is the default |
| `storage.reserve` | installation only | admin on the installation | `10%,10GiB`: the larger of the two | one or two sizes or percentages, comma-separated |
| `incoming.limit` | installation (the default for every tenant) and each tenant | admin on the installation only | `0`: no limit of the tenant's own | a size, such as `200GiB` |
| `events.retention` | installation, and each tenant may override | admin on the installation; the tenant's admin for the tenant | `90d` | a period, `1d` to `3650d` |
| `receiver.header-limit` | installation only | admin on the installation | `64MiB` | a size, `1MiB` to `4GiB`: the most a file's header may hold before the file is quarantined |
| `api.cors-origins` | installation only | admin on the installation | empty: no page in a browser may read an answer | origins, one per line, as a browser writes them (`https://app.example.org`), or `*` alone for any |

## Where a key may be used from, and how long it lives

| Setting | Scope | Who sets it | Default | Form |
|---|---|---|---|---|
| `access.denied-networks` | installation only | admin on the installation | empty: none refused | networks separated by commas, each an address or `address/bits` in its one written form; a request from one is refused before its key is read |
| `access.allowed-networks` | installation only | admin on the installation | empty: every network | networks, as above; a request from outside them is refused |
| `access.tenant-networks` | installation (the default for every tenant) and each tenant | admin on the installation; the tenant's admin for the tenant | empty: every network | networks, as above; every key used on the tenant must come from one |
| `access.trusted-proxies` | installation only | admin on the installation | empty: no forwarded address is believed | networks, as above; from one of them the client named in the header `access.forwarded-header` chooses is the caller |
| `access.forwarded-header` | installation only | admin on the installation | `x-forwarded-for` | `x-forwarded-for` or `forwarded` (RFC 7239): the one header a trusted proxy's client address is read from; the other is ignored, because a proxy that appends to one commonly passes a client's copy of the other through |
| `access.failure-limit` | installation only | admin on the installation | `10` | failed calls an address or a key may make in the window before its refusals are held one second; at twice as many it is blocked |
| `access.failure-window` | installation only | admin on the installation | `1m` | a period, `1s` to `24h` |
| `access.block-time` | installation only | admin on the installation | `15m` | a period, `1s` to `7d` |
| `key.default-lifetime` | installation only | admin on the installation | `90d` | a period, `1h` to `3650d`: how long a key made with no expiry works |
| `key.max-lifetime` | installation only | admin on the installation | `3650d` | a period, `1h` to `3650d`; no key is made or rotated to live longer, but a key asked for with no expiry |
| `key.expiry-warning` | installation only | admin on the installation | `7d` | a period, `1h` to `365d`; a key expiring within it is listed in the status of every tenant and reported in the server's log once a day |

A request on the local socket is never refused by a network rule or a limit.
A period is a whole number followed by `s`, `m`, `h` or `d`.

## Which changes take effect without a restart

The settings listed here are read from the master database each time they are
used, or kept for at most the few seconds the table says, so a value set
through `SetSetting` applies without a restart. What "next use" means differs, and the files and flags the
server reads when it starts do not change until it is started again.

| What | When a change takes effect |
|---|---|
| `storage.directory-roots` | At the next use of a directory storage: adding or changing a storage, and every time a storage is opened |
| `storage.s3-endpoints` | At the next use of an S3 storage, in the same places |
| `receiver.ports` | At the next `AddReceiver` or `SetReceiver` on the tenant; a listener that is running keeps its port, and a value that would take the port from a receiver is refused |
| `receiver.bind-addresses` | When a receiver that names no address of its own is started or started again (the first address listed is used); a listener that is running keeps the address it has |
| `storage.reserve` | Within five seconds, by default: the free space of the incoming area and of each storage a receiver writes to is checked on that interval |
| `incoming.limit` | Within five seconds, by default, on the same check |
| `receiver.header-limit` | Within a second, for the next file read |
| `api.cors-origins` | Within ten seconds: the server keeps the value it read for that long |
| `key.default-lifetime` | At the next key made without an expiry; keys already made keep theirs |
| `key.max-lifetime` | At the next key made or rotated; keys already made keep theirs |
| `key.expiry-warning` | At the next status, and within an hour for the report in the log |
| `access.denied-networks`, `access.allowed-networks`, `access.trusted-proxies`, `access.forwarded-header`, `access.failure-limit`, `access.failure-window`, `access.block-time` | Within five seconds: the server keeps the values it read for that long |
| `access.tenant-networks` | Within five seconds, on the same terms, for each tenant |
| the server's `--listen` address | Needs a restart: the address is opened once, when the server starts |
| the server's `--tls-cert` and `--tls-key` | A certificate renewed in the same files: within five seconds, at the next connection; files that change into a pair that does not load are logged and the last good pair is still served. Other paths need a restart |
| the key file | Needs a restart: it is read once, when the server starts, and the keys are held in memory |
| the data directory (`--data-dir` or `CORIA_DATA_DIR`) | Needs a restart: it is read once, when the command starts |
| the server's `--master-env`, `--log-format`, `--log-level`, `--drain` | Need a restart: they are read once, when the command starts |
| the certificate and private key files of a receiver's TLS | When the receiver's listener is started or started again: a change to the receiver does that, as does a retry after a failure; a listener that is running keeps the pair it loaded |
