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

[Unreleased]: https://github.com/amrshadid/magnetar/commits/develop
