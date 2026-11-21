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
| `receiver.allow-as-received` | installation (the default for every tenant) and each tenant | admin on the installation only | `false`: every receiver de-identifies | `true` or `false`: whether a receiver of the tenant may archive what it receives as received; a tenant that had receivers before receivers could de-identify was given `true` when its database was upgraded |
| `api.cors-origins` | installation only | admin on the installation | empty: no page in a browser may read an answer | origins, one per line, as a browser writes them (`https://app.example.org`), or `*` alone for any |
| `sender.destinations` | installation only | admin on the installation | empty: no sender can be added | one line per destination: a host name (letters, digits, hyphens and dots; a name whose last label is a number or `0x…` is refused, since a resolver reads it as an address), or a range of addresses (CIDR, never `/0`) or one address, then optionally `ports=104,11112-11120` (every port when left out) and, for a host name, `private-address`. Without `private-address` a host name must resolve to a global unicast address. With it, it may also resolve to loopback (`127.0.0.0/8`, `::1`), link-local (`169.254.0.0/16`, `fe80::/10`), private use (RFC 1918, `fc00::/7`) or shared address space (`100.64.0.0/10`), and to nothing else special-purpose. The unspecified (`0.0.0.0`, `::`), multicast and broadcast addresses are never a destination, whatever a line says. A value that would take away a destination a sender uses is refused, as is unsetting it |
| `sender.allow-plain-http` | installation only | admin on the installation | `false` | `true` or `false`: whether a stow sender's URL may be http; turning it off while a sender uses plain http is refused |
| `sender.max-associations` | installation only | admin on the installation | `32` | `1` to `1024`: the most associations (stow requests) all senders of all tenants hold open at once |
| `sender.tenant-max-associations` | installation (the default for every tenant) and each tenant | admin on the installation only | `8` | `1` to `1024`: the most associations (stow requests) one tenant's senders hold open at once |
| `job.failures-kept` | installation, and each tenant may override | admin on the installation; the tenant's admin for the tenant | `10000` | `100` to `1000000`: how many refused instances (and instances sent with a warning) a job lists; the rest are counted, not listed |

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
| `receiver.allow-as-received` | At the next `AddReceiver`, or `SetReceiver` that changes the receiver's de-identification; a receiver that archives as received keeps doing so when it is turned off |
| `api.cors-origins` | Within ten seconds: the server keeps the value it read for that long |
| `sender.destinations`, `sender.allow-plain-http` | At the next `AddSender` or `SetSender` that changes a destination; a sender that is configured already is checked again when the value is changed |
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
