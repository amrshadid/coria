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

[Unreleased]: https://github.com/amrshadid/magnetar/commits/develop
