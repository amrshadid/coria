# The quality gates, one word each. .github/workflows/checks.yml runs these same
# targets, so a gate that passes here passes there for the same reason.
#
#   make check   every gate below, in the order CI runs them
#   make fmt     gofmt -s; fails on any file it would change
#   make vet     go vet on 64 and on 32 bits
#   make lint    golangci-lint with .golangci.yml
#   make vuln    govulncheck
#   make test    go test -race
#   make build   static binaries for every target in bin/
#   make size    the size of each binary make build left in bin/, as a
#                Markdown table
#
# Two more checks, which CI runs as steps of their own and make check does
# not, because each needs more than Go:
#
#   make check-seal  the sealed-secret format against Python's cryptography
#                    package (PYTHON=... names an interpreter that has it)
#   make fuzz        each fuzz target for FUZZTIME (10s unless given)
#   make check-master-sqlite  the SQLite master database read back by the
#                    sqlite3 tool (SQLITE3=... names one)
#   make check-master-postgres  the Postgres master database read back by
#                    psql and pg_dump (PSQL=... PG_DUMP=... name them; needs
#                    MAGNETAR_TEST_POSTGRES, the URL of a scratch database)
#   make test-386    the tests as linux/386 binaries, on a Linux x86-64 host

GO      ?= go
BIN     := bin
MODULE  := github.com/amrshadid/magnetar

# Pinned, so a gate does not change under a pull request that changed nothing.
# golangci-lint's version is also in checks.yml; the two move together.
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION   := v1.10.0

# The tools are installed into bin/tools in this checkout, not into GOPATH, so
# the pinned versions never replace the ones a person has installed. Each is in
# a directory named for its version, so changing the pin installs the new one.
# CI passes GOLANGCI_LINT=golangci-lint to use the binary its action installs.
TOOLS         := $(CURDIR)/$(BIN)/tools
GOLANGCI_LINT ?= $(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
GOVULNCHECK   ?= $(TOOLS)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck

# The Go files gofmt checks: all of them, except under directories whose names
# start with a dot (which the go command ignores too) and the build output.
GOFILES = $(shell find . -path './.*' -prune -o -path './$(BIN)' -prune -o -name '*.go' -print)

# The targets a release ships. Each is built with CGO_ENABLED=0: the binary must
# start on any system of its kind without a C library beside it. linux/386 is
# here because int is 32 bits there, and an overflow that fits everywhere else
# fails to build or misbehaves.
PLATFORMS := linux/amd64 linux/386 linux/arm64 darwin/arm64 windows/amd64

# VERSION=v1.2.3 stamps a version over the one in the build information.
VERSION ?=
LDFLAGS := -s -w $(if $(VERSION),-X $(MODULE)/internal/version.Version=$(VERSION))

.DEFAULT_GOAL := check
.PHONY: check fmt vet lint vuln test build size check-seal fuzz

check: fmt vet lint vuln test build size

# gofmt's exit status is checked as well as its list: a file that does not
# parse is reported on standard error with status 2 and is not in the list, so
# looking at the list alone would pass it.
fmt:
	@files="$$(gofmt -s -l $(GOFILES))" || { echo "gofmt -s failed: a file above does not parse"; exit 1; }; \
	if [ -n "$$files" ]; then \
		echo "gofmt -s would change these files:"; echo "$$files"; \
		gofmt -s -d $$files; exit 1; \
	fi; \
	echo "gofmt -s: all $(words $(GOFILES)) Go files are formatted"

# GOOS=linux with GOARCH=386, because there is no 32-bit macOS: on a Mac the
# 386 vet would otherwise fail before it looked at the code.
vet:
	$(GO) vet ./...
	GOOS=linux GOARCH=386 $(GO) vet ./...

# Linted once per system, because a file built only for one system
# (perm_windows.go, acl_darwin.go) is invisible to a run for another; CI runs
# this on Linux, so without the GOOS runs those files would never be linted.
lint: $(filter $(TOOLS)/%,$(GOLANGCI_LINT))
	$(GOLANGCI_LINT) version
	$(GOLANGCI_LINT) config verify
	$(GOLANGCI_LINT) run ./...
	GOOS=windows $(GOLANGCI_LINT) run ./...
	GOOS=darwin $(GOLANGCI_LINT) run ./...

vuln: $(filter $(TOOLS)/%,$(GOVULNCHECK))
	$(GOVULNCHECK) -version
	$(GOVULNCHECK) ./...

# -count=1: the tests always run. CI restores Go's build cache, which also
# holds test results, and without it every package whose code did not change
# reports "(cached)": a race or a timing fault in it would never be looked
# for again. Compiled code still comes from the cache.
test:
	$(GO) test -race -count=1 ./...

build:
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(BIN)/magnetar-$$os-$$arch; \
		if [ "$$os" = windows ]; then out=$$out.exe; fi; \
		echo "CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags \"$(LDFLAGS)\" -o $$out ./cmd/magnetar"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/magnetar; \
	done

# size reads what build made and does not depend on it: CI writes this target's
# output to the job summary, and a rebuild here would put build commands there
# in front of the table.
size:
	@ls $(BIN)/magnetar-* > /dev/null 2>&1 || { echo "no binaries in $(BIN)/; run make build first" >&2; exit 1; }
	@echo "| Binary | Bytes | MiB |"
	@echo "|---|---:|---:|"
	@for f in $(BIN)/magnetar-*; do \
		bytes=$$(wc -c < "$$f" | tr -d ' '); \
		echo "| $${f#$(BIN)/} | $$bytes | $$(awk "BEGIN { printf \"%.2f\", $$bytes / 1048576 }") |"; \
	done

# The outside check of internal/secret: scripts/check-seal.py opens what Go
# sealed and seals what Go then opens. -count=1 so a cached pass is never
# reported for a Python that changed. The test skips only when the variable is
# unset; this target always sets it, so here the check runs or fails, and an
# empty PYTHON or an interpreter that is not there fails before the test.
# PYTHON is the same variable proto-python uses, defined with it below.
check-seal:
	@test -n "$(PYTHON)" || { echo "check-seal: PYTHON is empty; name an interpreter that has the cryptography package" >&2; exit 1; }
	@command -v "$(PYTHON)" > /dev/null || { echo "check-seal: no interpreter $(PYTHON)" >&2; exit 1; }
	MAGNETAR_CHECK_SEAL_PYTHON="$(PYTHON)" $(GO) test -count=1 -v -run '^TestSealOpensInPython$$' ./internal/secret

# The outside check of the SQLite master database: the driver writes the
# contract suite's sample, and the sqlite3 tool (SQLITE3=... names one) reads
# it back, checks it, and searches it for the sample key's secret.
SQLITE3 ?= sqlite3

.PHONY: check-master-sqlite test-386
check-master-sqlite:
	SQLITE3="$(SQLITE3)" GO="$(GO)" bash scripts/check-master-sqlite.sh

# The outside check of the Postgres master database: the driver writes the
# contract suite's sample into a schema of the database MAGNETAR_TEST_POSTGRES
# names, and psql and pg_dump (the commands in PSQL and PG_DUMP, which may run
# in a container so their version equals the server's) read it back.
PSQL    ?= psql
PG_DUMP ?= pg_dump

.PHONY: check-master-postgres
check-master-postgres:
	PSQL="$(PSQL)" PG_DUMP="$(PG_DUMP)" GO="$(GO)" bash scripts/check-master-postgres.sh

# The tests on 32 bits, where int is 32 bits and the SQLite library is a
# different translation of SQLite's C. Without -race, which 386 lacks. It runs
# natively on a Linux x86-64 machine (CI); macOS cannot run 386 binaries.
test-386:
	GOOS=linux GOARCH=386 CGO_ENABLED=0 $(GO) test -count=1 ./...

# Every fuzz target, as package:function. go test fuzzes one target at a
# time, so each is a run of its own; -run '^$$' keeps the other tests out.
FUZZTIME     ?= 10s
FUZZ_TARGETS := \
	internal/access:FuzzCheck \
	internal/access:FuzzCheckSQLite \
	internal/secret:FuzzOpen \
	internal/secret:FuzzParseAPIKey \
	internal/secret:FuzzParseKeyFile

fuzz:
	@set -e; for target in $(FUZZ_TARGETS); do \
		pkg=$${target%%:*}; fn=$${target#*:}; \
		echo "$(GO) test -run '^$$' -fuzz ^$$fn\$$ -fuzztime $(FUZZTIME) ./$$pkg"; \
		$(GO) test -run '^$$' -fuzz "^$$fn\$$" -fuzztime $(FUZZTIME) ./$$pkg; \
	done

# govulncheck needs a newer Go to build than this module needs, so the installs
# may fetch that toolchain (GOTOOLCHAIN=auto). The scan itself runs under the go
# command on PATH, whose standard library is the one checked.
$(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint:
	GOBIN=$(@D) GOTOOLCHAIN=auto $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(TOOLS)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck:
	GOBIN=$(@D) GOTOOLCHAIN=auto $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# ---------------------------------------------------------------------------
# The API contract: the protobuf files in api/proto and the Go code generated
# from them into gen/.
#
#   make proto           regenerate gen/ from api/proto
#   make proto-lint      buf lint with the rules in buf.yaml
#   make proto-breaking  buf breaking against the commit where HEAD and
#                        PROTO_BASE (a git revision) meet
#   make proto-check     regenerate into a scratch directory and fail on any
#                        difference from the committed gen/
#   make proto-python    compile the proto files with Python's grpcio-tools,
#                        a second toolchain, and list the services; needs a
#                        PYTHON with scripts/requirements-proto.txt installed,
#                        so it is not part of make check
#
# buf and both plugins are pinned and installed into bin/tools like the tools
# above. buf is the release binary, checked against the SHA-256 committed in
# scripts/buf.sha256 (building it from source took minutes in CI); the
# plugins are small and built with go install. buf runs with local plugins
# only: no Buf Schema Registry account, no remote plugin, no network at
# generation time.
BUF_VERSION                   := v1.75.0
PROTOC_GEN_GO_VERSION         := v1.36.12
PROTOC_GEN_CONNECT_GO_VERSION := v2.0.2

BUF                   := $(TOOLS)/buf-$(BUF_VERSION)/buf
PROTOC_GEN_GO         := $(TOOLS)/protoc-gen-go-$(PROTOC_GEN_GO_VERSION)/protoc-gen-go
PROTOC_GEN_CONNECT_GO := $(TOOLS)/protoc-gen-connect-go-$(PROTOC_GEN_CONNECT_GO_VERSION)/protoc-gen-connect-go

# buf.gen.yaml names the plugins without a path; the pinned ones are put first
# on PATH, so a plugin of another version installed elsewhere is never used.
PROTO_PATH := $(dir $(PROTOC_GEN_GO)):$(dir $(PROTOC_GEN_CONNECT_GO)):$(PATH)

# The branch the contract is compared with. The check compares with the commit
# where HEAD and this branch meet, not with its tip, so a branch that is behind
# does not see what was added since as deleted. CI passes the pull request's
# base branch, or the commit before a push.
PROTO_BASE ?= origin/develop

# Scratch space for the base's files and for regenerated code. Its name starts
# with "_", so the go command ignores it: regenerated Go code left here would
# otherwise be a package of the module, built and tested by ./... It is
# removed after each use.
PROTO_TMP := $(BIN)/_proto

PYTHON ?= python3

.PHONY: proto proto-lint proto-breaking proto-check proto-python

check: proto-lint proto-breaking proto-check

proto-python:
	$(PYTHON) scripts/check-proto-python.py api/proto

proto: $(BUF) $(PROTOC_GEN_GO) $(PROTOC_GEN_CONNECT_GO)
	@echo "buf generate (protoc-gen-go $(PROTOC_GEN_GO_VERSION), protoc-gen-connect-go $(PROTOC_GEN_CONNECT_GO_VERSION))"
	@PATH="$(PROTO_PATH)" $(BUF) generate

proto-lint: $(BUF)
	$(BUF) --version
	$(BUF) lint

# A base with no proto files has no contract to break; the check says so and
# passes rather than comparing against nothing. The base is read with git
# archive, so the check works the same in a clone, a worktree and CI.
proto-breaking: $(BUF)
	@set -e; base="$(PROTO_BASE)"; \
	git rev-parse --verify --quiet "$$base^{commit}" > /dev/null || { echo "proto-breaking: $$base is not a known revision; fetch it first" >&2; exit 1; }; \
	mb="$$(git merge-base HEAD "$$base")" || { echo "proto-breaking: HEAD and $$base have no commit in common" >&2; exit 1; }; \
	echo "proto-breaking: comparing with $$mb, where HEAD and $$base meet"; \
	if ! git ls-tree -r --name-only "$$mb" -- api/proto | grep '\.proto$$' > /dev/null; then \
		echo "proto-breaking: skipped, $$mb has no proto files under api/proto"; exit 0; \
	fi; \
	rm -rf $(PROTO_TMP); mkdir -p $(PROTO_TMP)/base; \
	git archive "$$mb" -- buf.yaml api/proto | tar -x -C $(PROTO_TMP)/base; \
	status=0; $(BUF) breaking --against $(PROTO_TMP)/base || status=$$?; \
	rm -rf $(PROTO_TMP); \
	if [ $$status -ne 0 ]; then echo "proto-breaking: the contract breaks what $$mb published" >&2; exit $$status; fi; \
	echo "proto-breaking: no breaking change"

proto-check: $(BUF) $(PROTOC_GEN_GO) $(PROTOC_GEN_CONNECT_GO)
	@set -e; rm -rf $(PROTO_TMP); mkdir -p $(PROTO_TMP); \
	PATH="$(PROTO_PATH)" $(BUF) generate -o $(PROTO_TMP); \
	status=0; diff -r gen $(PROTO_TMP)/gen || status=$$?; \
	rm -rf $(PROTO_TMP); \
	if [ $$status -ne 0 ]; then \
		echo "proto-check: gen/ is not what the proto files generate; run make proto and commit the result" >&2; exit 1; \
	fi; \
	echo "proto-check: gen/ matches the proto files"

$(BUF):
	scripts/install-buf.sh $(BUF_VERSION) $@

$(PROTOC_GEN_GO):
	GOBIN=$(@D) GOTOOLCHAIN=auto $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)

$(PROTOC_GEN_CONNECT_GO):
	GOBIN=$(@D) GOTOOLCHAIN=auto $(GO) install connectrpc.com/connect/v2/cmd/protoc-gen-connect-go@$(PROTOC_GEN_CONNECT_GO_VERSION)

# ---------------------------------------------------------------------------
# The command line (internal/cli): two checks by tools that are not Magnetar.
#
#   make check-json        every command with --json against the in-process
#                          server, each output parsed by Python's json module
#                          (scripts/check-json.py); needs python3, nothing else
#   make check-completion  the bash completion script the binary generates,
#                          parsed by bash -n
#
# Like check-seal they are steps of their own in CI, not part of make check.
# check-json always sets the variable that makes the Go test write the
# documents, and the script fails when one is missing, so the Go test being
# skipped cannot pass.
.PHONY: check-json check-completion

check-json:
	@command -v "$(PYTHON)" > /dev/null || { echo "check-json: no interpreter $(PYTHON)" >&2; exit 1; }
	@set -e; dir="$$(mktemp -d)"; \
	MAGNETAR_CHECK_JSON_DIR="$$dir" $(GO) test -count=1 -run '^TestWriteJSONOutputs$$' ./internal/cli; \
	status=0; $(PYTHON) scripts/check-json.py "$$dir" || status=$$?; \
	rm -f "$$dir"/*.json; rmdir "$$dir"; exit $$status

check-completion:
	@command -v bash > /dev/null || { echo "check-completion: bash is not installed" >&2; exit 1; }
	@set -e; out="$$(mktemp)"; \
	$(GO) run ./cmd/magnetar completion bash > "$$out"; \
	status=0; \
	test -s "$$out" || { echo "check-completion: the script is empty" >&2; status=1; }; \
	[ $$status -ne 0 ] || grep -q '_magnetar' "$$out" || { echo "check-completion: the script does not define _magnetar" >&2; status=1; }; \
	[ $$status -ne 0 ] || bash -n "$$out" || status=$$?; \
	[ $$status -ne 0 ] || echo "check-completion: bash -n accepts the completion script ($$(wc -l < "$$out" | tr -d ' ') lines)"; \
	rm -f "$$out"; exit $$status

# ---------------------------------------------------------------------------
# The interactive program (internal/tui): an outside check by Python's pty
# module, which is not Magnetar's code.
#
#   make check-tui   builds the binary and a test helper that serves the
#                    in-process fake server on a temporary socket, then runs
#                    the binary under a pseudo-terminal (scripts/check-tui.py):
#                    keys pressed, the screen read, quit, a stop signal, and
#                    the terminal checked to have been restored.
#
# Like check-json it is a step of its own in CI. The helper is built from
# internal/tui/ptyserve into a temporary directory: it is test support, it is
# not part of "make build", and it is never in the binary. Linux and macOS only
# (Windows has no pty module).
.PHONY: check-tui

check-tui:
	@command -v "$(PYTHON)" > /dev/null || { echo "check-tui: no interpreter $(PYTHON)" >&2; exit 1; }
	@set -e; dir="$$(mktemp -d)"; \
	CGO_ENABLED=0 $(GO) build -o "$$dir/magnetar" ./cmd/magnetar; \
	CGO_ENABLED=0 $(GO) build -o "$$dir/ptyserve" ./internal/tui/ptyserve; \
	status=0; $(PYTHON) scripts/check-tui.py --binary "$$dir/magnetar" --helper "$$dir/ptyserve" || status=$$?; \
	rm -f "$$dir/magnetar" "$$dir/ptyserve"; rmdir "$$dir"; exit $$status
