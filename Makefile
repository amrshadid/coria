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
.PHONY: check fmt vet lint vuln test build size

check: fmt vet lint vuln test build size

fmt:
	@files="$$(gofmt -s -l $(GOFILES))"; \
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

lint: $(filter $(TOOLS)/%,$(GOLANGCI_LINT))
	$(GOLANGCI_LINT) version
	$(GOLANGCI_LINT) config verify
	$(GOLANGCI_LINT) run ./...

vuln: $(filter $(TOOLS)/%,$(GOVULNCHECK))
	$(GOVULNCHECK) -version
	$(GOVULNCHECK) ./...

test:
	$(GO) test -race ./...

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

# govulncheck needs a newer Go to build than this module needs, so the installs
# may fetch that toolchain (GOTOOLCHAIN=auto). The scan itself runs under the go
# command on PATH, whose standard library is the one checked.
$(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint:
	GOBIN=$(@D) GOTOOLCHAIN=auto $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(TOOLS)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck:
	GOBIN=$(@D) GOTOOLCHAIN=auto $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
