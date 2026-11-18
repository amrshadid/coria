# Magnetar

A research imaging archive in one Go binary. It receives DICOM, de-identifies it,
routes it to a storage, catalogs it as subject / session / scan, and serves it to
other systems through a gRPC API with keys.

**State: early development. Nothing here is usable yet.** The binary only says
which build it is.

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

These checks need more than Go, so `make check` leaves them out and CI runs
them as steps of their own on Linux:

| Command | What it checks |
|---|---|
| `make check-seal PYTHON=python3` | the format of a sealed secret against another implementation: `scripts/check-seal.py`, with Python's `cryptography` package, opens a value Go sealed, and Go opens a value Python sealed. `PYTHON` names an interpreter that has the package; `pip install --require-hashes -r scripts/requirements-check-seal.txt` installs it at the versions CI uses. An empty `PYTHON` or a missing interpreter fails |
| `make fuzz` | each fuzz target for `FUZZTIME` (10 seconds unless given) |
| `make test-386` | the tests as linux/386 binaries, which a Linux x86-64 machine runs natively (macOS cannot) |
| `make check-master-sqlite SQLITE3=sqlite3` | the SQLite master database against SQLite's own `sqlite3` tool: `scripts/check-master-sqlite.sh` has the driver write a sample installation, then lists its tables and rows, runs `PRAGMA integrity_check` and `PRAGMA foreign_key_check`, reads times and audit details with SQLite's functions, and searches a dump for the sample key's secret (absent) and its hash (present). The master's tables are `STRICT`, so the `sqlite3` tool must be 3.37 or later to open the database at all |

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
