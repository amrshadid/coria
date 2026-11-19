# Architecture

How CORIA's code is laid out, what each package does, and the rules that hold
the layout together, each with what enforces it. Read this first; each
package's own documentation (`go doc ./internal/<package>`) goes deeper.

## The shape

CORIA is one binary, `coria`, built from `cmd/coria`. It is three things at
once:

- **a server** (`coria serve`): the API over TLS and a local socket, DICOM
  receivers, and the reception pipeline of every tenant;
- **a command line** (`coria tenant create ...`), a client of that API;
- **an interactive program** (`coria` with no arguments), also a client.

Underneath, the code is ports and adapters. A *port* states what a kind of
thing holds or does, as Go interfaces with their rules, and has a *contract
suite* every implementation passes. An *adapter* (a driver) implements a port
for one technology. Everything else depends on ports, never on a driver.

```
            cmd/coria
                │
     ┌──────────┴───────────┐
 internal/cli          internal/tui              (clients of the API)
     │  internal/cli/servecmd (wires the server)
     ▼
 internal/server ── internal/api ── internal/access ── internal/obs
     │                   │
     │          internal/receive/supervise ── internal/receive/dimse
     │                   │
     │          internal/receive ── internal/receive/{incoming,header,sidecar}
     │                   │
     ├── ports: internal/master   internal/tenant   internal/blob
     │             │                  │                │
     │      drivers: internal/driver/{sqlite,postgres}  internal/blob/{dir,s3}
     │
 internal/core/* (rules with no I/O)   internal/fault   internal/secret
```

## The packages

### Rules without I/O

| Package | What it holds |
|---|---|
| `internal/core/label` | Whether a string may be a subject, session or scan label |
| `internal/core/naming` | A receiver's naming rule: how labels are made from a file's header |
| `internal/core/layout` | The one place that builds and parses the keys of objects on a storage |
| `internal/core/manifest` | The format of a scan's manifest: its files with their checksums |
| `internal/core/reception` | The life of a reception: its states and the events that move it |
| `internal/core/settings` | The fixed list of settings, their defaults, bounds and scopes |
| `internal/core/uid`, `ascii`, `pathname` | Well-formed UIDs, text classes, names and paths a person writes |
| `internal/fault` | An error's kind (invalid, not found, precondition, unavailable, ...), so every layer answers the same way |

### Ports and their suites

| Port | What it states | Suite | Drivers |
|---|---|---|---|
| `internal/master` | The installation's database: tenants, keys, grants, settings, audit | `master/mastertest` | `driver/sqlite`, `driver/postgres` |
| `internal/tenant` | One tenant's database: storages, receivers, receptions, received files, catalog, quarantine, leased work, events | `tenant/tenanttest` | `driver/sqlite`, `driver/postgres` |
| `internal/blob` | A storage: put, read a range, list, delete, usage | `blob/blobtest` | `blob/dir`, `blob/s3` |

`internal/driver` opens a database from its address and picks the driver;
`internal/driver/sqlkit` holds what only the two SQL drivers share (the
migration runner, cursors, value encodings).

### The server and its services

| Package | What it does |
|---|---|
| `internal/server` | The process: listeners, limits, the checked path of every request, shutdown, metrics |
| `internal/access` | Who is calling (key or local operator), what they may do, and the audit entry of every request |
| `internal/api` | The handlers of the API contract's services; `api/codes` maps a fault kind to an API code |
| `internal/registry` | One open store per tenant, and the only way a handler reaches a tenant's database |
| `internal/events` | The live feed of tenants' events, and the keeper that checkpoints and prunes it |
| `internal/place` | Where a tenant's data may be: roots, endpoints, ports, bind addresses; opens a storage |
| `internal/settingvalue` | The value of a setting that applies: tenant override, installation value or default |
| `internal/secret` | The installation's key file; sealing and opening secrets; the one type that holds a plaintext key |
| `internal/obs`, `obs/redact` | The logger, the metrics, and what a log may never show |

### Reception

| Package | What it does |
|---|---|
| `internal/receive/dimse` | The DICOM listener of one receiver, on go-dicom: admission, C-ECHO, C-STORE |
| `internal/receive/supervise` | Starts, restarts and stops listeners and each tenant's pipeline as configuration and space change |
| `internal/receive` | A tenant's reception pipeline: a landed file to a verified scan, its work queue, its recovery at start |
| `internal/receive/incoming` | A tenant's incoming area: a file synced before it is acknowledged |
| `internal/receive/header`, `sidecar` | Reading what the pipeline needs from a file's header; the scan's metadata sidecar |

### Clients

| Package | What it does |
|---|---|
| `internal/cli` | The commands; `cli/client` reaches the API; `cli/output` writes tables and JSON |
| `internal/cli/servecmd` | `coria init` and `coria serve`: makes an installation, wires the server's parts together |
| `internal/tui` | The interactive program |

### Tests only

`internal/testkit` (helpers more than one package's tests use: a DICOM file
generator, a heap sampler, scratch Postgres schemas and S3 addresses),
`internal/cli/fakeserver`, `internal/server/servertest`, and the test-only
programs under them. Nothing the binary runs imports them.

## The rules, and what enforces each

| Rule | Enforced by |
|---|---|
| One static binary, pure Go | `CGO_ENABLED=0` in every build; `make check-static`; lint forbids `unsafe` and `plugin` in the core |
| The core does no I/O and imports nothing of CORIA's | lint rule `core` (`.golangci.yml`) |
| A port knows no database or file system and never imports a driver | lint rules `master`, `tenant`, `blob` |
| A database is reached only through its port; no code outside a driver knows which | lint rules `master`, `tenant`, `registry`, `receive`, `api-driver`; only `driver/postgres` imports pgx (`postgres-library`) |
| A storage is reached only through `blob.Store` | lint rules `blob`, `receive` |
| Every driver passes its port's contract suite | each driver's tests run the suite; CI runs both databases and both S3 servers |
| Only `receive/dimse` speaks DICOM on the network | lint rule `dicom-network` |
| Test helpers never reach the binary | lint rule `testkit` |
| One error kind per kind the contract names, mapped in one place | `internal/api/codes` and its tests |
| A secret never appears in a log, an error or an answer | `internal/secret` (unprintable type), `obs/redact`, the proof's searches of logs, audit and databases (`make prove`) |
| Every access and change is audited | `internal/access`; the proof reconciles every request with the audit log |
| A file acknowledged to a sender is never lost or archived twice | the incoming area's sync before acknowledgement; the pipeline's crash matrix test; `make prove-reception`'s kills at each step |
| State lives in the database, not memory | recovery at start (`receive.Recover`, the work queue's repair and reclaim); restart tests |
| Reception does not get slower unseen | `make bench-gate` in CI |
| Nothing in the repository refers to notes outside it | `make check-comments` |

## Where state lives

- **The master database** (SQLite file or Postgres): tenants, keys (hashed),
  grants, installation settings, the installation's audit log.
- **Each tenant's database** (its own SQLite file or Postgres schema):
  storages, receivers, receptions, received files, the catalog, quarantine,
  the work queue, events, the tenant's audit log.
- **The data directory**: the key file, the SQLite files, the local socket,
  the incoming area.
- **Storages**: the archived objects, a manifest and a sidecar per scan.

A restart loses nothing held only in memory: a reception in progress, a
unit of work and an event not yet delivered are all rows.

## A reception, end to end

1. A sender opens an association to a receiver; `receive/dimse` admits it
   only if the caller is listed.
2. Each C-STORE is streamed into the tenant's incoming area, synced, and
   only then answered success (`receive/incoming`).
3. The pipeline reads the header, records the file in a batch, or holds it
   in quarantine with its reason and its bytes.
4. When no file has come for the receiver's quiet period, the reception is
   complete; it is identified (labels from the naming rule), archived
   (files put and read back, sidecar and manifest written, catalog
   committed) and verified (every listed object checked), each step a unit
   of work in the database.
5. Each step writes an event in the same transaction; `internal/events`
   streams them to watchers.

## How to add something

- [A database driver](guides/database-driver.md)
- [A storage driver](guides/storage-driver.md)
- [An API method](guides/api-method.md)
- [A migration](guides/migration.md)
- [An event type](guides/event-type.md)
