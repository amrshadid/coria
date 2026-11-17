# Magnetar

A research imaging archive in one Go binary. It receives DICOM, de-identifies it,
routes it to a storage, catalogs it as subject / session / scan, and serves it to
other systems through a gRPC API with keys.

**State: early development. Nothing here is usable yet.** The binary says
which build it is and has the commands that operate an installation through its
API (see below); there is no server for them to talk to yet.

## Build

Magnetar needs Go 1.26 or later and nothing else: it is pure Go and builds with
`CGO_ENABLED=0` into one static binary.

```sh
go build ./cmd/magnetar
./magnetar version
```

`magnetar version` prints the version, the commit and the Go release, for
example `magnetar v0.1.0 (commit 0123456789ab, go1.27.4)`. A part the build did
not record reads `unknown`. A release stamps its version with:

```sh
make build VERSION=v0.1.0
```

## The commands

`magnetar <noun> <verb> [name] [flags]`; `magnetar --help` lists them and
`magnetar completion bash` writes the shell completion.

```
magnetar version | whoami
magnetar tenant   create <name> [--db-env VAR | --db-file PATH] | list | show <name> | remove <name> --yes
magnetar key      create --holder TEXT [--expires 90d | --no-expiry] | list | show <id> | revoke <id>
magnetar grant    set <key-id> --role ROLE (--tenant NAME | --installation) | list (--key ID | --tenant NAME | --installation) | remove <key-id> (--tenant NAME | --installation)
magnetar setting  get <name> [--tenant NAME] | set <name> <value> [--tenant NAME] | unset <name> --tenant NAME | list [--tenant NAME]
magnetar audit    list [--tenant NAME] [--key ID] [--outcome done|refused|failed] [--since TIME] [--until TIME]
```

- **Where it connects.** By default the local socket `run/magnetar.sock` in the
  data directory (`--data-dir` or `MAGNETAR_DATA_DIR`), as the user that runs the
  server, with no key. With `--server https://host:port` (or `MAGNETAR_SERVER`)
  it connects over TLS with the key from `MAGNETAR_KEY` or the file named by
  `--key-file`. `http://` is accepted only for `localhost`, `127.0.0.0/8` and
  `::1`.
- **A secret is never an argument.** No flag takes a key, a password or a
  database address; a tenant's Postgres address is read from the variable named
  by `--db-env` or the file named by `--db-file`. (`--key` on `grant list` and
  `audit list` takes a key's public id, and a whole key given there is refused.)
- **Output.** A table, or with `--json` one document that starts with
  `"schemaVersion": 1`. Times are shown in the local zone with their offset;
  there is no color when `NO_COLOR` is set or the output is not a terminal.
- **Errors** are one line on standard error with the server's message and its
  request id. Exit codes: 0 done, 1 failed, 2 wrong usage, 3 refused
  (unauthenticated or denied), 4 not found.
- **Lists** fetch up to `--limit` (100) items and say when more exist; `--all`
  fetches every item.

## The quality gates

Every pull request must pass these. Each is one `make` target, and CI
(`.github/workflows/checks.yml`) runs the same targets, so a gate that passes
locally passes there for the same reason.

| Command | What it checks |
|---|---|
| `make fmt` | `gofmt -s`: fails on any file it would change or cannot parse |
| `make vet` | `go vet` on 64 bits and on 32 bits (linux/386) |
| `make lint` | golangci-lint with `.golangci.yml`, run for Linux, Windows and macOS so that files built for one system only are linted too, including the rule that `internal/core` imports only the standard library and itself, and from the standard library no database, network, file or system package |
| `make vuln` | govulncheck: no known vulnerability reachable from the code |
| `make test` | `go test -race ./...` |
| `make build` | static binaries for linux/amd64, linux/386, linux/arm64, darwin/arm64 and windows/amd64, in `bin/` |
| `make size` | the size of each binary `make build` left, as a table |
| `make proto-lint` | `buf lint` on the API contract in `api/proto` |
| `make proto-breaking` | `buf breaking` against the commit where `HEAD` and `PROTO_BASE` (default `origin/develop`) meet; skipped, saying so, when that commit has no proto files |
| `make proto-check` | regenerates the Go code and fails if it differs from the committed `gen/` |
| `make check` | all of the above, in that order |

The API contract is protobuf, package `magnetar.v1`, in `api/proto`; `make
proto` regenerates the Go code in `gen/`, which is committed and never edited
by hand. `make proto-python` compiles the same files with Python's
grpcio-tools as a second toolchain (it needs a `PYTHON` with
`scripts/requirements-proto.txt` installed, so it is not in `make check`).

Two more checks need more than Go, so `make check` leaves them out and CI runs
them as steps of their own on Linux:

| Command | What it checks |
|---|---|
| `make check-seal PYTHON=python3` | the format of a sealed secret against another implementation: `scripts/check-seal.py`, with Python's `cryptography` package, opens a value Go sealed, and Go opens a value Python sealed. `PYTHON` names an interpreter that has the package; `pip install --require-hashes -r scripts/requirements-check-seal.txt` installs it at the versions CI uses. An empty `PYTHON` or a missing interpreter fails |
| `make fuzz` | each fuzz target for `FUZZTIME` (10 seconds unless given) |
| `make check-json` | every command with `--json` against the in-process server, each output parsed by Python's `json` module (`scripts/check-json.py`, standard library only) and checked for `schemaVersion` |
| `make check-completion` | the bash completion script the binary writes, parsed by `bash -n` |

`make lint` and `make vuln` install the pinned golangci-lint and govulncheck
into `bin/tools/` the first time, and the proto targets do the same for buf,
protoc-gen-go and protoc-gen-connect-go; nothing is installed outside the
checkout. buf is the release binary for Linux or macOS (x86-64 or ARM),
installed only if its SHA-256 matches the one in `scripts/buf.sha256`.
`make` needs a Unix shell; on Windows run `go vet ./...` and `go test ./...`
directly.

CI runs the gates on Linux with Go 1.26 and 1.27, and `go vet` and the tests on
Windows with Go 1.27, for every pull request. The tests run on macOS with Go
1.27 after each merge into `develop` or `main`.

## Branches

`develop` is where work is merged; `main` holds releases and takes pull
requests from `develop` only. The branch policy check
(`.github/workflows/branch-policy.yml`) fails on a pull request into `main` from
any other branch. It advises and cannot block: while the repository is private
on the free plan, GitHub refuses branch protection on it (the API answers 403),
so no check can be made required. When the repository is public, a protection
rule on `main` makes it required.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
