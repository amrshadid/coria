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
  writes both or neither; setting a grant replaces its role in one step and
  returns the old one; lists are paged by cursor, never by offset; times keep
  their microseconds in UTC. SQLite runs in write-ahead mode with one writing
  connection (`BEGIN IMMEDIATE`), a read-only pool, foreign keys on, full
  sync (with `F_FULLFSYNC` on macOS) and a busy timeout. Migrations are
  embedded SQL files, checksummed and applied under a lock that holds across
  processes; a changed applied file, or a database a newer binary upgraded,
  is refused. `scripts/check-master-sqlite.sh` reads what the driver wrote
  with the `sqlite3` tool and finds a key's hash but never its secret. The
  binary does not use the master database yet.

[Unreleased]: https://github.com/amrshadid/magnetar/commits/develop
