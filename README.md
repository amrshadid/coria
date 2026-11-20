# CORIA

Core Research Imaging Archive: a research imaging archive in one Go binary. It
receives DICOM, de-identifies it, routes it to a storage, catalogs it as subject /
session / scan, and serves it to other systems through an API with keys, as a
core that research platforms are built on.

**State: early development, no release yet.** What runs today:

- an installation made with `coria init` and served with `coria serve`, over TLS
  and a local socket;
- tenants, each with its own database on SQLite or Postgres; keys, grants and
  an audit log of every request;
- DICOM receivers per tenant, closed to callers they do not list, that archive
  each study to a directory or an S3 bucket as subject / session / scan, with a
  manifest and checksums, verified against what was sent; a quarantine for what
  cannot be archived, with its reason; a crash at any step loses no file a
  sender was told was received;
- a tenant's status and a live stream of its events;
- the archive read back: its catalog of subjects, sessions and scans with
  filters and pages, each instance's DICOM header as JSON, the files
  themselves checked against their manifest as they stream (a scan's files
  over gRPC, or one instance over HTTP, resumable by range), and DICOMweb
  under `/tenants/<name>/dicom-web`: QIDO-RS search, WADO-RS retrieval of
  instances, metadata, frames and bulk data, and STOW-RS storage through a
  receiver that lists the keys it takes (PS3.18);
- the commands and an interactive program for all of it, and generated
  clients for Python (`clients/python`) and TypeScript (`clients/typescript`).

Not yet: **de-identification. Until it exists, send only synthetic or public
test data: what a receiver gets is archived as it was sent.**

## Documents

- [Architecture](docs/architecture.md): the packages, the rules that hold them
  together and what enforces each, where state lives, a reception end to end.
- Guides: [a database driver](docs/guides/database-driver.md),
  [a storage driver](docs/guides/storage-driver.md),
  [an API method](docs/guides/api-method.md),
  [a migration](docs/guides/migration.md),
  [an event type](docs/guides/event-type.md).
- [Receiving a study, step by step](docs/api-walkthrough-reception.md), and
  which changes take effect without a restart.
- [Testing](docs/testing.md): the kinds of test, how a slow one is marked, and
  every environment variable the tests and scripts read.

## Build

CORIA needs Go 1.26 or later and nothing else: it is pure Go and builds with
`CGO_ENABLED=0` into one static binary.

```sh
go build ./cmd/coria
./coria version
```

`coria version` prints the version, the commit and the Go release, for
example `coria v0.1.0 (commit 0123456789ab, go1.27.4)`. A part the build did
not record reads `unknown`. A release stamps its version with:

```sh
make build VERSION=v0.1.0
```

## Databases

The master database and each tenant's live in SQLite files in the data
directory or in Postgres. One process writes a SQLite database: the server's
lock on its data directory keeps a second server away, a second process on the
same file would be safe for the data but could wait for as long as the first
keeps writing and then fail as busy, and Postgres is the database for anything
that needs several writing processes.

## The commands

`coria <noun> <verb> [name] [flags]`; `coria --help` lists them and
`coria completion bash` writes the shell completion.

```
coria version | whoami
coria tenant   create <name> [--db-env VAR | --db-file PATH] | list | show <name> | remove <name> --yes
coria key      create --holder TEXT [--expires 90d | --no-expiry] [--network CIDR]... [--client-cert FILE | --client-cert-sha256 HEX] | list | show <id> | rotate <id> [--overlap 24h] | set <id> [--network CIDR]... [--client-cert FILE | --client-cert-sha256 HEX] | revoke <id>
coria grant    set <key-id> --role ROLE (--tenant NAME | --installation) | list (--key-id ID | --tenant NAME | --installation) | remove <key-id> (--tenant NAME | --installation)
coria setting  get <name> [--tenant NAME] | set <name> <value> [--tenant NAME] | unset <name> --tenant NAME | list [--tenant NAME]
coria audit    list [--tenant NAME] [--key-id ID] [--outcome done|refused|failed] [--since TIME] [--until TIME]
coria storage    add <tenant> <name> --type dir --root ROOT [--path P] | --type s3 --endpoint URL --region R --bucket B --access-key-id ID (--credential-env VAR | --credential-file PATH) [--capacity SIZE] | list <tenant> | show <tenant> <name> | set <tenant> <name> [flags] | remove <tenant> <name> --yes | test <tenant> <name>
coria receiver   add <tenant> <name> --storage S [--port N] --aet TITLE [--allow-aet T] [--allow-from CIDR] [--allow-aet-from T@CIDR] [--any-caller] [--enabled] [--tls-certificate-file F --tls-private-key-file F] [--quiet-period 5m] [--max-open-time 1h] [--max-file-size 4GiB] | add <tenant> <name> --type stow --storage S [--allow-key ID]... [--enabled] | list <tenant> | show <tenant> <name> | set <tenant> <name> [flags] | remove <tenant> <name> --yes
coria reception  list <tenant> [--receiver NAME] [--step STEP]... [--in-progress] [--since TIME] [--until TIME] | show <tenant> <id> | retry <tenant> <id> [--receiver NAME] | discard <tenant> <id> --reason TEXT [--yes]
coria quarantine list <tenant> [--state held|released|rejected] [--reason REASON] [--receiver NAME] [--reception ID] [--since TIME] [--until TIME] | show <tenant> <id> [--header] | release <tenant> <id> [--receiver NAME] [--reason TEXT] | reject <tenant> <id> --reason TEXT [--yes]
coria catalog    subjects <tenant> [--label-prefix P] [--from TIME] [--to TIME] [--order ORDER] [--limit N | --all] | sessions <tenant> [<subject>] [--label-prefix P] [--study-date-from D] [--study-date-to D] [--modality M] [--receiver NAME] [--from TIME] [--to TIME] [--order ORDER] [--limit N | --all] | scans <tenant> [<subject> [<session>]] [--label-prefix P] [--modality M] [--description-prefix P] [--receiver NAME] [--from TIME] [--to TIME] [--order ORDER] [--limit N | --all]
coria scan       show <tenant> <subject>/<session>/<scan> [--metadata]
coria download   <tenant> <subject>[/<session>[/<scan>]] --to DIR [--quiet]
```

- **Where it connects.** By default the local socket `run/coria.sock` in the
  data directory (`--data-dir` or `CORIA_DATA_DIR`), as the user that runs the
  server, with no key. With `--server https://host:port` (or `CORIA_SERVER`)
  it connects over TLS with the key from `CORIA_KEY` or the file named by
  `--key-file`. `http://` is accepted only for `localhost`, `127.0.0.0/8` and
  `::1`.
- **A secret is never an argument.** No flag takes a key, a password or a
  database address; a tenant's Postgres address is read from the variable named
  by `--db-env` or the file named by `--db-file`, and an S3 storage's secret
  access key from `--credential-env` or `--credential-file`. The filters that take a key's
  public id are `--key-id`, and a whole key given where an id goes is refused.
  `--key-file` must name a file only you can open (`chmod 600`).
- **TLS.** TLS 1.2 or later. `--ca-file PATH` (or `CORIA_CA_FILE`) names a
  PEM file of certificate authorities to trust for `--server` instead of the
  system's, for a private authority. A key that works only with a TLS client
  certificate is used with `--tls-client-cert FILE` and `--tls-client-key FILE`
  (or `CORIA_TLS_CLIENT_CERT` and `CORIA_TLS_CLIENT_KEY`); the key file must be
  private to you.
- **Output.** A table, or with `--json` one document that starts with
  `"schemaVersion": 1`. Times are shown in the local zone with their offset;
  there is no color when `NO_COLOR` is set or the output is not a terminal.
- **Errors** are one line on standard error with the server's message and its
  request id. Exit codes: 0 done, 1 failed, 2 wrong usage, 3 refused
  (unauthenticated or denied), 4 not found, 5 a state that forbids the change
  (a storage that holds files, a port not allotted), 6 a position that can no
  longer be answered (an event cursor too old), 7 a bound of the server
  reached (a message too large, too many streams).
- **Lists** fetch up to `--limit` (100) items and say when more exist; `--all`
  fetches every item.
- **The catalog** serves only the combinations of filter and order an index
  answers (the server names them when it refuses one): newest first by
  default; a label prefix with `--order label`, a study date range with
  `--order study-date`, a description prefix with `--order
  series-description`. `catalog scans` of a subject named alone lists by
  label.
- **Downloads** are laid out `<dir>/<subject>/<session>/<scan>/<instance>.dcm`,
  each written as `.part` and renamed only once its SHA-256 matches the one
  the server announced; run again, a download skips what is complete and
  resumes a `.part` by range.

### The interactive program

`coria` with no arguments, when standard input and standard output are both
a terminal, opens a program with a menu, lists that scroll and detail screens:
tenants (with each tenant's grants, settings and audit log, its status that
follows its events, and its storages, receivers, receptions and quarantine,
where a held file is released or rejected), the subjects, sessions and scans of a tenant with a scan's headers and a download of
what is shown, keys, settings, and the audit log. It finds the server with the same flags and environment as
the commands (`--data-dir`, `--server`, `--key-file`, `--ca-file`) and is a
client of the same API, so it can do nothing a command cannot. With any
argument, or with either end not a terminal, `coria` prints the usage line
and exits 2 as before.

- **Keys.** Arrows or `j`/`k`, page up and down, enter to open, esc to go back
  (or to cancel a call that is waiting), `/` to filter the loaded rows, `?` for
  the keys of the screen, `q` to quit. A list loads 100 rows and the next 100
  when the cursor nears the end; the count reads `100+` until the last page is in.
- **A new key** (`n` on the key list) is shown once, alone, with the words that
  it will not be shown again, and leaving that screen asks once more.
- **Nothing destructive happens on one key.** Revoking a key and removing a
  grant, a setting override or a tenant need its id or name typed.
- **Errors** are one line at the bottom: the server's message and the request id,
  in the words the command prints.
- **Terminal.** It fits 80 by 24 and redraws on resize; there is no color when
  `NO_COLOR` is set, with `TERM=dumb`, or on Windows. The alternate screen is
  left and the cursor shown on quit, on SIGTERM and SIGINT, and on a panic.
- **Windows.** The screens and their tests run there; `make check-tui`, which
  needs Python's `pty` module, does not.

## The quality gates

Each gate is one `make` target, run locally before a change is pushed.
`make gates` runs the ones every push needs and never the scale tests:
`check-comments`, `fmt`, `go vet` on 64 bits, linux/386 and windows, `lint`,
and `go test -race` over every package (whose scale tests skip unless
`CORIA_TEST_SCALE` is set).

| Command | What it checks |
|---|---|
| `make fmt` | `gofmt -s`: fails on any file it would change or cannot parse |
| `make vet` | `go vet` on 64 bits and on 32 bits (linux/386) |
| `make lint` | golangci-lint with `.golangci.yml`, run for Linux, Windows and macOS so that files built for one system only are linted too, including the rule that `internal/core` imports only the standard library and itself, and from the standard library no database, network, file or system package |
| `make vuln` | govulncheck: no known vulnerability reachable from the code |
| `make test` | `go test -race -count=1 ./...`: always run, never a cached result |
| `make test-scale` | the tenant registry at 1,000 tenants (`TestScaleThousandTenants`), the drivers' tests at scale and the pipeline model under every seed, without the race detector, which that test skips; its time to ready, memory and open files are in the log |
| `make build` | static binaries for linux/amd64, linux/386, linux/arm64, darwin/arm64 and windows/amd64, in `bin/` |
| `make size` | the size of each binary `make build` left, as a table |
| `make proto-lint` | `buf lint` on the API contract in `api/proto` |
| `make proto-breaking` | `buf breaking` against the commit where `HEAD` and `PROTO_BASE` (default `origin/develop`) meet; skipped, saying so, when that commit has no proto files; a package renamed in `api/renamed.txt` is read under its new name |
| `make proto-check` | regenerates the Go code and fails if it differs from the committed `gen/` |
| `make check` | all of the above but `make test-scale`, in that order; the scale tests run on their own, on a machine meant for them |

The API contract is protobuf, package `coria.v1`, in `api/proto`; `make
proto` regenerates the Go code in `gen/`, which is committed and never edited
by hand. `make proto-python` compiles the same files with Python's
grpcio-tools as a second toolchain (it needs a `PYTHON` with
`scripts/requirements/proto.txt` installed, so it is not in `make check`).

These checks need more than Go, so `make check` leaves them out; each runs on
its own:

| Command | What it checks |
|---|---|
| `make check-seal PYTHON=python3` | the format of a sealed secret against another implementation: `scripts/check/seal.py`, with Python's `cryptography` package, opens a value Go sealed, and Go opens a value Python sealed. `PYTHON` names an interpreter that has the package; `pip install --require-hashes -r scripts/requirements/check-seal.txt` installs it at the versions CI uses. An empty `PYTHON` or a missing interpreter fails |
| `make fuzz` | each fuzz target for `FUZZTIME` (10 seconds unless given) |
| `make test-386` | the tests as linux/386 binaries, which a Linux x86-64 machine runs natively (macOS cannot) |
| `make check-server PYTHON=... GRPCURL=grpcurl` | the server against three clients that are not CORIA: `scripts/check/server.sh` runs a real server with stand-in handlers, then curl, grpcurl and Python's grpcio (`scripts/requirements/check-server.txt`) each get the same refusal for no key, a malformed key, an unknown key and a key with no grant, and success for an admin key over TLS and for the local operator on the socket; `/metrics` is parsed by the Prometheus Python client; SIGTERM must exit 0 |
| `make check-master-sqlite SQLITE3=sqlite3` | the SQLite master database against SQLite's own `sqlite3` tool: `scripts/check/master-sqlite.sh` has the driver write a sample installation, then lists its tables and rows, runs `PRAGMA integrity_check` and `PRAGMA foreign_key_check`, reads times and audit details with SQLite's functions, and searches a dump for the sample key's secret (absent) and its hash (present). The master's tables are `STRICT`, so the `sqlite3` tool must be 3.37 or later to open the database at all |
| `make check-json` | every command with `--json` against the in-process server, each output parsed by Python's `json` module (`scripts/check/cli-json.py`, standard library only) and checked for `schemaVersion` |
| `make check-completion` | the bash completion script the binary writes, parsed by `bash -n` |
| `make check-tenant-sqlite SQLITE3=sqlite3` | a tenant's SQLite database against the `sqlite3` tool: `scripts/check/tenant-sqlite.sh` has the tenant registry create a tenant on SQLite in a fresh data directory and write a sample into it, then checks that its file is `<data-dir>/tenants/<name>.db` and private, lists its tables and rows, runs `PRAGMA integrity_check`, and reads the tenant's row and its state change from the master |
| `make check-tui` | the interactive program, as built, run under a pseudo-terminal by Python's `pty` module against the in-process fake server (`scripts/check/tui.py`, standard library only): keys pressed, the screen read, quit, SIGTERM and SIGINT mid-session, and the terminal checked to be restored. Linux and macOS only |
| `make check-master-postgres` | the Postgres master database against `psql` and `pg_dump`: `scripts/check/master-postgres.sh` has the driver write a sample installation into a schema of the database `CORIA_TEST_POSTGRES` names (a `postgres://` URL), then lists its tables and rows, checks every constraint and foreign key, reads times and audit details with Postgres's functions, and searches a `pg_dump` for the sample key's secret (absent) and its hash (present). `PSQL=` and `PG_DUMP=` name the commands, which may run in a container. The Postgres tests of `go test` use the same variable and skip without it; `CORIA_REQUIRE_POSTGRES=1` makes a missing address a failure, as CI sets it |
| `make check-tenant-postgres` | a tenant on Postgres against `psql` and `pg_dump`: `scripts/check/tenant-postgres.sh` has the registry create a tenant on Postgres, in a schema of the database `CORIA_TEST_POSTGRES` names, with a Postgres master in another, writes the tenant suite's sample, then lists the tables and rows, checks every constraint and index, reads times and audit details, reads the tenant's row and state change from the master, checks the schema records the tenant's id, and searches both dumps for the login's password (absent) |
| `make prove PYTHON=... GRPCURL=grpcurl` | the proof of this version, from outside: `scripts/prove/run.sh` builds every target as `make build` does, then runs the real binary: `init` and `serve` on the local socket and TLS; the first key made on the socket; every method of the contract refused without a key; tenants on SQLite and on Postgres (in a container it starts, or the one `CORIA_PROVE_PG_CONTAINER` names); no key, a malformed, unknown, wrong, expired and revoked key, and a key with no grant tried with grpcurl, Python's grpcio and curl, the answers compared byte for byte; secrets searched for in the databases' dumps, every file, the logs and the audit log; the server with another key file; Postgres stopped and started under a running server; the server killed with signal 9 during tenant creation at least 20 times; every request reconciled with the audit log; a second server and another operating system user refused; `--json` and the interactive program; the size of each binary. One line per check and a table at the end; a check it cannot run here (a second user without sudo) is skipped and says so, and `CORIA_PROVE_STRICT=1` (CI) fails on any skip. It needs docker and leaves nothing behind |
| `make prove-reception PYTHON=... DCMTK=dir` | the reception, from outside: `scripts/prove/reception.py` serves the real binary, sends public studies with dcmtk's storescu and pynetdicom, and checks with dcmdump, pydicom, hashlib and sqlite3 what was archived, quarantined and counted: byte for byte, manifests, duplicates, a corrected and a broken file, strangers refused, kills by signal 9 while receiving, in the quiet period and while archiving, a nearly full storage, a released file, the status and the event stream. `PROVE_S3` adds a tenant on an S3 bucket read back by GETs the proof signs itself, and `PROVE_POSTGRES` with `PROVE_PSQL` a tenant on Postgres read by psql (`scripts/requirements/prove-reception.txt`) |
| `make check-metadata`, `check-catalog`, `check-frames`, `check-dicomweb`, `check-stow` `PYTHON=...` | reading the archive against tools that are not CORIA: the Metadata service against pydicom's `to_json_dict`, the Catalog service against what pydicom reads of the received files, each file's frame table against pydicom's fragments, QIDO-RS and WADO-RS from dicomweb-client against pydicom, and STOW-RS from dicomweb-client into a real server process (`docs/testing.md` names the packages) |
| `make client-python-test`, `make client-ts-test` | the generated Python and TypeScript clients against a real server |
| `make bench-gate` | reception's speed, files a second from landing to verified, measured on the base branch's tree and this one in turns on the same machine; fails when this one is slower by more than `BENCH_MARGIN` (25) percent |

`make lint` and `make vuln` install the pinned golangci-lint and govulncheck
into `bin/tools/` the first time, and the proto targets do the same for buf,
protoc-gen-go and protoc-gen-connect-go; nothing is installed outside the
checkout. buf is the release binary for Linux or macOS (x86-64 or ARM),
installed only if its SHA-256 matches the one in `scripts/tools/buf.sha256`.
`make` needs a Unix shell; on Windows run `go vet ./...` and `go test ./...`
directly.

The workflows in `.github/workflows` describe the same gates on Linux with Go
1.26 and 1.27, on Windows and on macOS, with Postgres 16 and 18 and two S3
servers (Silo and RustFS); they are not run on each push, and the gates above
are run locally instead. `make test-postgres` and `make test-s3` run the
database drivers, the S3 driver and the reception end to end against those
services, and fail if the service they need is missing. Without one, the
tests that need it skip and say so in the log.

## Branches

`develop` is where work goes, pushed once the gates pass; `main` holds
releases and takes changes from `develop` only. The branch policy check
(`.github/workflows/branch-policy.yml`) fails on a pull request into `main`
from any other branch.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
