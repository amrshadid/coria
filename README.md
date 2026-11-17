# Magnetar

A research imaging archive in one Go binary. It receives DICOM, de-identifies it,
routes it to a storage, catalogs it as subject / session / scan, and serves it to
other systems through a gRPC API with keys.

**State: early development. Nothing here is usable yet.** The binary only says
which build it is.

## Build

Magnetar needs Go 1.25 or later and nothing else: it is pure Go and builds with
`CGO_ENABLED=0` into one static binary.

```sh
go build ./cmd/magnetar
./magnetar version
```

`magnetar version` prints the version, the commit and the Go release, for
example `magnetar v0.1.0 (commit 0123456789ab, go1.26.1)`. A part the build did
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
| `make fmt` | `gofmt -s`: fails on any file it would change |
| `make vet` | `go vet` on 64 bits and on 32 bits (linux/386) |
| `make lint` | golangci-lint with `.golangci.yml`, including the rule that `internal/core` imports no database, network or storage package |
| `make vuln` | govulncheck: no known vulnerability reachable from the code |
| `make test` | `go test -race ./...` |
| `make build` | static binaries for linux/amd64, linux/386, linux/arm64, darwin/arm64 and windows/amd64, in `bin/` |
| `make size` | the size of each binary, as a table |
| `make check` | all of the above, in that order |

`make lint` and `make vuln` install the pinned golangci-lint and govulncheck
into `bin/tools/` the first time; nothing is installed outside the checkout.
`make` needs a Unix shell; on Windows run `go vet ./...` and `go test ./...`
directly.

CI runs the gates on Linux with Go 1.25 and 1.26, and `go vet` and the tests on
Windows, for every pull request. The tests run on macOS after each merge into
`develop` or `main`.

## Branches

`develop` is where work is merged; `main` holds releases and accepts pull
requests from `develop` only.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
