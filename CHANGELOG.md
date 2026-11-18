# Changelog

All notable changes to Magnetar are written here. The form is
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The repository had a README and nothing else: no module, no license, and no
  check that a change builds, is formatted or passes its tests. It is now the Go
  module `github.com/amrshadid/magnetar`, under the Apache License 2.0, with the
  gates every change is judged by: `gofmt -s`, `go vet` on 64 and 32 bits,
  golangci-lint, govulncheck, `go test -race`, and static builds for linux/amd64,
  linux/386, linux/arm64, darwin/arm64 and windows/amd64 with their sizes
  reported. Each gate is one `make` target, and CI runs the same targets.
- A lint rule holds the architecture before its first package exists:
  outside its tests, `internal/core` may import only the standard library and
  its own packages, and from the standard library no database, network, file
  or system package (`net/netip` and `net/url`, which do no I/O, excepted).
- `magnetar version` (and `magnetar --version`) prints the version, the commit
  and the Go release the binary was built with, read from the binary's own
  build information, so a report can name the build that wrote the data. Any
  other command is a usage error: one line on standard error and exit code 2.
- A pull request into `main` is refused unless it comes from `develop`.
- The API had no written contract, so handlers, the command line and outside
  clients had nothing fixed to agree on. It is now protobuf, package
  `magnetar.v1` in `api/proto`: `TenantService`, `AccessService` (keys,
  grants, `WhoAmI`), `SettingsService` and `AuditService`. Each method's
  comment states the role it needs, what it writes to the audit log and the
  errors it can answer, and each read or repeatable change is marked as such.
  The contract also states its limits, the order in which a request is
  checked, the request id the server returns, and when a missing tenant is
  "not found" and when it is "permission denied". Removing a tenant only
  unregisters it and says where its data was left. Secrets cross the API in
  two fields only, the database address of a new tenant and a new key's
  secret; they are marked for the server's redactor, and a test fails on any
  text field of the contract that has not been listed as reviewed. `buf
  lint`, a breaking-change check against the base branch, and a check that the
  committed Go code in `gen/` is current run in CI, with Python's grpcio-tools
  compiling the same files as a second toolchain. No server serves it yet.
- An error had no way to say what sort of failure it was, so the API and the
  command line would each have had to guess a code from its text. Package
  `internal/fault` gives an error a kind (invalid, unauthenticated, denied, not
  found, conflict, unavailable, internal) and the exit code of each kind, and
  keeps the message a caller may see apart from the cause, which is for the
  log only. An error with no kind is internal.
- There was nowhere to keep a secret such as a tenant database's password
  except in plain text, and no way to make an API key. Package `internal/secret`
  adds the installation key file (one `magnetar-key-v1 <version> <key>` line
  per key version, created only where no file exists). The file must be private
  to the user magnetar runs as, or it is refused with the command that fixes
  it: on Linux and macOS a regular file owned by that user with mode 0600 or
  narrower, with no macOS access list entry granting anything, in a directory
  owned by that user that nobody else can write; on Windows a regular file
  owned by the current user whose access list names nobody else. A FIFO at the
  path is refused at once instead of hanging the start. It seals a secret with
  AES-256-GCM under a random nonce, bound to the context it is stored for, so a
  sealed value moved to another row does not open, and opening fails the same
  way whatever is wrong. `scripts/check-seal.py` checks the format against
  Python's `cryptography` package in both directions, installed in CI from
  hashed requirements. API keys look like `mgt_<id>_<secret>`, with 256 random
  bits in the secret, of which only the SHA-256 is to be stored, compared in
  constant time. Printing the loaded keys or a new API key, by any `fmt` verb,
  `log/slog` or `encoding/json`, shows no key material.
- `make lint` also lints for Windows and macOS, so files built for one system
  only are linted in CI.
- There was nowhere to keep tenants, keys, grants, settings or the audit log.
  The master database is now reached through a port stated as operations
  (`internal/master`), not SQL, with a contract suite every database driver
  must pass (`internal/master/mastertest`) and a SQLite driver that passes it
  (`internal/driver/sqlite`, on `modernc.org/sqlite`, pure Go, so the binary
  stays static on every target). Every change takes its audit entry and
  writes both or neither; setting a grant replaces its role in one step,
  returns the old one, and is refused on a revoked key; one lookup gives a
  key's roles on the installation and on a named tenant for the access check;
  lists are paged by a cursor bound to the list and its arguments, never by
  offset; times keep their microseconds in UTC; a duplicate audit entry id is
  told apart from a conflict without reading text. SQLite runs in write-ahead
  mode with one writing connection (`BEGIN IMMEDIATE`), a read-only pool,
  foreign keys on, full sync (with `F_FULLFSYNC` on macOS) and a busy
  timeout, and refuses on Unix a database file that others can read or write.
  Migrations are embedded SQL files, checksummed and applied under a lock that
  holds across processes; a changed applied file, or a database a newer binary
  upgraded, is refused. `scripts/check-master-sqlite.sh` reads what the driver wrote
  with the `sqlite3` tool and finds a key's hash but never its secret. The
  binary does not use the master database yet.
- The binary had a version command and nothing that could use the API, so a
  person had no way to make a tenant or a key without writing a client. It now
  has the single commands of the command line, built with cobra and written
  only as a client of the API: `tenant create|list|show|remove`,
  `key create|list|show|revoke`, `grant set|list|remove`, `setting
  get|set|unset|list`, `audit list`, `whoami` and `version`, with `--help` and
  shell completion. They reach the server on the local socket in the data
  directory (`--data-dir`, `MAGNETAR_DATA_DIR`) or on the network over TLS
  (`--server`, `MAGNETAR_SERVER`) with the key read from `MAGNETAR_KEY` or
  `--key-file`, which must name a file only its owner can open; no flag takes a
  key, a password or a database address, the filters that take a key's public
  id are `--key-id`, and `http://` is refused for any host that is not
  loopback. TLS is 1.2 or later, and `--ca-file` (or `MAGNETAR_CA_FILE`) names
  the authorities to trust for `--server` in place of the system's, for an
  institution with a private authority. Output is a table
  for a person, or with `--json` one document that starts with
  `"schemaVersion": 1`, checked by Python's `json` module in CI. An error is
  one line on standard error with the server's message and request id, and the
  exit code is 0 done, 1 failed, 2 wrong usage, 3 refused, 4 not found, from
  the kind of the error through one table of API codes in `internal/api/codes`;
  a code the contract names no kind for (a message over the size limit, one a
  newer server adds) is a failure, exit 1.
  `tenant remove` sends nothing without `--yes`. Lists fetch up to `--limit`
  (100) items and say when more exist, or every item with `--all`. A new key is
  written alone on its line on standard output and never again. The commands
  are tested against an in-memory server built from the generated handlers and
  served over a Unix socket and over TLS, not against a mock of the client.
  The program grew from 1.6 to 10.2 MiB (linux/amd64: 1.57 to 10.67 MiB), most
  of it the Connect and protobuf client and `net/http`; cobra and its flag
  library account for about 0.9 MiB of it.
- Nothing decided who a caller is or what it may do. `internal/access` now
  has one check that every request will go through: it reads the key by its
  id, compares its secret in constant time, refuses a revoked or expired one,
  then reads the key's roles and the tenant the request named in one
  statement, and compares them with what the request needs, in the order the
  API contract states. Every bad credential gets one answer
  (unauthenticated), and a missing grant and a missing tenant get one answer
  (permission denied) after the same two reads, so a key cannot learn which
  tenants exist; a caller who may list every tenant is told "not found"
  instead. Nothing is cached, so a revoked key is refused on its next
  request, and a check's decision is for one request: the operations on keys
  and grants read the caller's key again and refuse it once it is revoked,
  expired or no longer admin. A refusal is written to the installation's
  log, an allowed request to the log of the tenant or the installation it
  concerns, and to the installation's when a tenant's log does not answer. A
  change is audited in its own transaction; a refusal or a read goes to a
  bounded audit writer, with a queue for each so a flood of bad keys cannot
  push out reads, that appends in batches, retries a log that does not
  answer a bounded number of times, and counts every entry exactly once as
  written, failed or dropped, reporting drops in the log. Text a caller
  controls is escaped before it reaches an entry. A key's last use is
  recorded at most once a minute, by a bounded background worker. Making,
  listing and revoking keys and setting, listing and removing grants need
  the installation admin; a new key expires after 90 days unless another
  period (at most 10 years) or "never" is asked for. The binary does not use
  it yet.
- The master port's `RolesFor` now also reports whether the named tenant
  exists and its id, in one statement on SQLite and Postgres, and a name that
  is not a valid tenant name finds no tenant instead of failing, so the
  access check reads the tenant and the roles at once.
- The contract's comments now say which log each entry goes to (a refusal
  always to the installation's), that the entries of refusals and reads are
  written in batches and can be dropped by an overwhelmed server, which says
  how many, and no longer promise an order of a key's grants by tenant name.
- A tenant had a row in the master and nowhere to keep its own data, and
  nothing opened, upgraded or watched a tenant's database. Each tenant now
  has a database reached through a port stated as operations
  (`internal/tenant`): its overrides of the installation's settings and its
  own audit log, with the master's bounds, the master's entry type and the
  master's rules: each change written with its audit entry or not at all, a
  duplicate audit id told apart from a conflict, cursors bound to their list,
  their filter and their tenant, and the same error kinds. A contract suite
  every driver must pass (`internal/tenant/tenanttest`) runs on SQLite, where
  the master and the tenants now share one implementation of settings and of
  the audit log, and a tenant without an address gets the file
  `<data-dir>/tenants/<name>.db`, private to its owner, built so that no name
  can put it elsewhere. Errors from the SQLite driver now name the database
  they are about, master or tenant, and a symbolic link planted at a
  database's path is refused rather than followed. A tenant may not be named
  for a device Windows reserves (`con`, `nul`, `com1` and the like), on any
  system, so a data directory moved to Windows keeps working. The tenant
  registry (`internal/registry`) holds one open store per tenant and is the
  only way to get one: getting a ready tenant's store takes no lock and makes
  no call to the master (12 ns, no allocation, among 1,000 tenants). It opens
  every tenant in parallel, a bounded number at once, each under its own
  deadline, and checks an open tenant with a short ping that waits for no
  open, so one dead database delays neither the start nor the others; a
  tenant whose database does not answer is `unreachable` and refused while
  the others are served, and returns to `ready` on its own; one whose address
  does not open with the key file, whose database refuses its address or its
  file, whose schema cannot be upgraded or whose driver panics is `failed`
  with its reason. The registry's view of a tenant's state, with its reason
  and the time it was reached, is current the moment a check completes; the
  master records it, with an audit entry whose actor is the system, a
  flapping database at most once a minute. A reason, an error, an audit
  entry and a log line hold only the registry's own words and a driver's
  caller-safe message, never a driver's raw error, so no form of the
  database's address or password reaches them. Creating a tenant is whole or
  nothing: when its database cannot be opened or upgraded, the row is removed
  again, the undoing is audited, and the failure is answered; only a crash
  leaves an `upgrading` row, which the next start finishes, and repeating a
  creation with another address is a conflict. Removing a tenant unregisters
  it and leaves its data in place, and says where. Each round also reconciles
  the registry with the master's list of tenants, so a removal or creation
  whose answer was lost does not leave the two apart. At start it warns when
  the tenants could not all hold their files under the process's limit.
  Closing the registry stops its timer, waits for opens in progress up to the
  deadline it is given, and closes every store. `scripts/check-tenant-sqlite.sh`
  reads a tenant the registry created with the `sqlite3` tool. The server does
  not use the registry yet.
- The commands could not be browsed: finding a tenant's grants, its settings
  and its audit entries took a command each, and making a key meant copying
  its secret from standard output. `magnetar` with no arguments, when standard
  input and output are both terminals, now opens an interactive program (Bubble
  Tea 2.1.1 and Lip Gloss 2.1.1, MIT, with the modules they need) with a menu,
  scrolling lists and detail screens for tenants, keys, grants, settings and
  the audit log, as a client of the same API through the same client. A list
  fetches a page of 100 and the next when the cursor nears the end, never
  everything; a call runs beside the screen, with the commands' deadline, and
  esc cancels it; an error is one line with the server's message and request
  id. A new key is drawn on one screen only, with the words that it will not be
  shown again, leaving that screen asks once, and the secret is zeroed then;
  revoking a key and removing a grant, a setting override or a tenant need the
  id or name typed. The alternate screen is left and the cursor shown on quit,
  on SIGTERM and SIGINT, and on a panic. With any argument, or when either end
  is not a terminal, the usage line and exit 2 are as before.
  `scripts/check-tui.py` runs the built binary under a pseudo-terminal against
  the fake server (`make check-tui`, a step of its own in CI, Linux and macOS
  only). The binaries grew by 1.67 to 2.09 MiB (linux/amd64: 10.78 to 12.73
  MiB).
- The master database could only live in a SQLite file. It can now live in
  Postgres too (`internal/driver/postgres`, on `pgx/v5` and its pool, pure
  Go), at a `postgres://` address, and it passes the same contract suite
  unchanged on Postgres 16 and 18. The driver makes and upgrades its
  tables in the schema the address's `search_path` names and works with a login
  that owns only that schema; it never makes a database or a role. Every change
  and its audit entry are one transaction; a grant's old role is read and its new
  one written in one statement under a lock on the key's row, so two racing sets
  are ordered; a unique tenant name is kept by the database and its violation is
  recognized by SQLSTATE and constraint name, never by message text. Text that is
  compared or ordered is `COLLATE "C"`, so a database created with another
  collation pages through the same order. Migrations take a session advisory lock
  on one dedicated connection, released on every path, so processes that start
  together apply each file once. No error repeats the address, the password or
  the host. CI runs the driver against Postgres 16 and 18 and fails, instead of
  skipping, when the server is missing; `scripts/check-master-postgres.sh` reads
  what the driver wrote with `psql` and `pg_dump` and finds a key's hash but
  never its secret.
- A tenant's database could only be a SQLite file. It can now be a Postgres
  database too (`internal/driver/postgres`, `OpenTenant`), and a tenant created
  with type `postgres` and a `postgres://` address is served by the registry
  like any other: ready when its database answers, unreachable alone when it
  does not, and back by itself when it does again, with no restart. The tenant
  suite passes unchanged on Postgres 16 and 18, two instances on one database
  and two tenants apart included. The settings and audit code is now one
  implementation shared by the master and the tenant, as on SQLite. The schema
  records the id of the tenant it was made for and every open compares it, so
  two tenants pointed at one database and schema, or an address pasted for the
  wrong tenant, are refused as Invalid instead of sharing one audit log. A
  tenant's pool is small (four connections, idle ones let go after a minute;
  `pool_max_conns` and `pool_max_conn_idle_time` in the address change them).
  The registry's warning about the limit on open files counts each tenant by
  what its driver says it holds (an optional `FilesPerTenant`): eleven files
  for SQLite, the pool's size for Postgres. `scripts/check-tenant-postgres.sh`
  reads a tenant the registry made with `psql` and `pg_dump`, and the Postgres
  CI jobs run the registry against real tenants.
- The API contract had generated interfaces and nothing behind them.
  `internal/api` now implements its four services (tenants, keys and grants,
  settings, audit), each method a short function that takes the access
  check's decision from its context, validates the request and answers
  `INVALID_ARGUMENT` naming the field, then acts through the package that
  owns the change, with the audit entry in the same transaction. It also
  holds the table of what each method needs of its caller, which the server
  asks before every request. A handler that finds no decision answers
  `INTERNAL` and never decides by itself; a tenant missing at a lookup after
  the check is answered `PERMISSION_DENIED`, as for no grant, to a caller who
  may not list every tenant; a key that may not list every tenant sees only
  the tenants it holds a grant on, paged without reading every tenant; a
  removal needs the name twice; a tenant's database address reaches no
  answer, audit entry, error or log line, which a test shows by searching
  all of them, and the master's files, for the address, its password, user
  and host. A tenant's state is the one the registry sees now, not the
  master's row, which can lag a minute behind, with the time that state was
  reached (`Tenant.state_time`, a new field). A read costs 16 µs (a tenant)
  to 24 µs (a tenant's setting) through the check and the handler without
  the network, its audit entry batched into the log of the scope it read; a
  change, one synced commit (5 ms on a laptop). The server does not serve
  them yet.
- Settings were any name anyone wrote, with no default and no rule for their
  values, so the contract's "not found" and "a value the setting does not
  accept" could not happen. The server now knows a fixed list
  (`internal/core/settings`), each setting with a built-in default, a rule
  for its value and whether a tenant may override it. The value that applies
  is the tenant's override, else the installation's value, else the built-in
  default, and every answer says which (`Setting.source`, a new field of the
  contract). Unsetting the installation's value returns to the built-in
  default. The list's one setting, `key.default-lifetime` (whole hours or
  days, such as `90d`, from 1h to 3650d; 90 days by default), is the
  lifetime of a key made with no expiry.
- `magnetar serve` served no API method: the handlers existed and nothing
  joined them to the server, and the commands and the interactive program
  had only run against an in-memory stand-in. `serve` now builds the tenant
  registry, with the SQLite and Postgres tenant drivers, and the handlers,
  and registers them on the checked path. Every tenant is opened before the
  server listens, sixteen at a time and each within ten seconds, so a tenant
  whose database does not answer is unreachable and answers unavailable while
  the others are served, and is tried again every thirty seconds. `/metrics`
  counts the tenants in each state. The server's own settings, such as the
  installation's id, are kept under a prefix no setting may use
  (`settings.ReservedPrefix`), so the API neither lists, reads nor changes
  them: to a caller they do not exist. A test runs the real program end to
  end (`make check-e2e`, in CI's server job): init, serve with the local
  socket and TLS, the real commands as processes over both, a key with no
  grant refused, a revoked key refused on its next request, a second serve
  refused, a stop by signal that writes every queued audit entry, and a
  restart that serves the same tenants, keys and log; `make check-server` now
  also points curl, grpcurl and Python's grpcio at the real program.
- Nothing showed the installed program doing, in order, what this version
  claims: each part was checked on its own. `make prove` (`scripts/prove.sh`,
  with `scripts/prove.py`; CI's "Proof: the installed binary, from outside"
  job, on Linux, with a Postgres service container) builds every target as
  `make build` does and runs the real binary against tools that are not
  Magnetar: curl, grpcurl, openssl, sqlite3, psql and pg_dump, and Python's
  grpcio. It shows, each as a named check with what it measured: `init` and
  `serve` on the local socket and on TLS; no key or grant after `init`, the
  first key made on the socket, and all 17 methods of the contract (with
  `/metrics`) refused without a key, health answering; tenants on SQLite and
  on Postgres, and a remote address without its sslmode refused and leaving
  nothing; no key, a malformed, unknown, wrong, expired (`--expires 1s`) and
  revoked key, each with all 17 methods, through the three clients, with one
  answer per client byte for byte, "no grant" and "no such tenant" identical,
  and a caller who may list tenants told not found; no secret in the master's
  dump, the tenant's `pg_dump`, any file of the data directory, the logs at
  debug level or the audit log, and each key's hash present; a copy of the
  installation given another installation's key file refused at start,
  listening on nothing and left byte for byte as it was, and with its own key
  file put back serving both tenants with their data; the Postgres container
  stopped and started under a running server, the SQLite tenant answering in
  its usual time meanwhile; the server killed with signal 9 during tenant
  creation 24 times, each kill classed by what the master held right after
  it, and the master's integrity checked each time; every request it made
  found in the audit log with its outcome, and nothing else there; a second
  server and another operating system user refused; every list command's
  `--json` and the interactive program under a pseudo-terminal; and the size
  of each binary. It does not show Windows, a loss of power on real hardware,
  or any speed.

### Fixed

- `serve` started with a key file other than the installation's ran: every
  tenant then failed with "the database address did not open with this key
  file", SQLite tenants included, and a tenant created in that state would
  have had its address sealed with the wrong key, leaving an installation
  that no one key file could open. `init` now writes a key check into the
  master, beside the installation's id: the id sealed with the key file.
  `serve` opens it after the identity check, before it migrates, opens any
  tenant or listens, and refuses to start (exit 1) when it does not open,
  saying the key file is not the installation's and nothing was changed. A
  master with no key check is refused with its own line: a data directory
  made before this change must be made again with `init`. The check opens
  with any key file that still holds the version that sealed it, so a newer
  key version added for rotation does not refuse a start. `init` now also
  makes the lock file, and `serve` empties it when it lets go of it, so a
  refused start leaves the data directory byte-identical.

- The command line and the interactive program had been written against the
  in-memory stand-in, which disagreed with the real server. `setting unset`
  required `--tenant`, but the installation's value can now be removed;
  `setting get`, `set`, `unset` and `list` said "installation (default)" for
  a value that was the built-in default, and now say where each value comes
  from; `tenant show` shows since when a tenant is in its state; the
  examples named settings the server does not know. The stand-in now takes
  the server's own list of settings and resolves them as the server does,
  lists a key only the tenants it holds a grant on, answers an unknown key's
  grants with an empty list and a malformed tenant name as invalid.

- A failed creation of a tenant was audited as done: the request's entry
  was written with the tenant's row, before its database was opened, and
  the row's removal then had a system entry of its own with no request id,
  so the request's id led to an entry that said done for a request answered
  unavailable or invalid. The row is now written with a system entry saying
  the creation started, which carries the request's id; the request's own
  entry is written once, with the change that settles the creation: with the
  tenant's state becoming ready (done), or with the removal of its row
  (failed, with the reason). After a crash between the two, the next start
  records that it finished the creation that request began.
- The Linux job on Go 1.27 took up to 12 of its 15 minutes: the Postgres
  jobs shared its build cache key, finished first, and saved a cache that
  held only the database driver, so the Linux job recompiled everything in
  each step. They now have a key of their own. The 1,000-tenant test runs
  once, without the race detector, as a step of its own (`make test-scale`),
  instead of inside the race and 32-bit runs.
- `magnetar init` and `magnetar serve`. Nothing could make an installation or
  serve the API. `init` makes the data directory private to its user, the key
  file, the migrated master database (SQLite unless `--master-env` names an
  environment variable holding another address), `tenants/` and `run/`; it
  refuses a directory that already holds an installation and says that losing
  the key file loses every secret. `serve` takes an operating system lock on
  the data directory (a second server is refused, naming the first; a server
  killed with signal 9 leaves nothing stale), listens on the local socket
  `run/magnetar.sock` (HTTP/2 without TLS; the caller must be the server's own
  user, read from the kernel on Linux and macOS; on Windows the directory's
  access list is the only guard), and on the network only with `--listen`
  (TLS 1.2 or later; without a certificate, a loopback address only). Every
  request, RPC or plain HTTP, goes through one checked path: request id in
  `Magnetar-Request-Id`, credential, the access check with the method's
  requirement from a table tested against the contract's `Role:` lines, then
  the handler, with a panic answered as internal. Messages over 4 MiB are
  refused both ways, a call without a deadline gets 30 seconds and no call
  runs past 5 minutes, a body read before the check must arrive within 10
  seconds, and headers, streams and idle connections are bounded. Logs are
  `log/slog` JSON with the two secret fields redacted by reading
  `debug_redact`, and every answer, including those the RPC library gives
  before the check, is logged and counted; `/metrics` is Prometheus text; the
  standard gRPC health service answers Check without a key (Watch is not
  served). `init` gives the installation an id, kept in the data directory
  and in the master, and `serve` refuses a master with another id or none,
  before migrating it. A stop signal drains requests in flight, writes the
  queued audit entries, closes everything within 29 seconds even when a
  handler ignores its context, and exits; a second signal exits at once.
  `make check-server` checks it with curl,
  grpcurl and Python's grpcio. The API's handlers are not in this change, so
  until they are, every API method passes the check and then answers that
  the service is not there.

[Unreleased]: https://github.com/amrshadid/magnetar/commits/develop
