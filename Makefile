# DepBisect developer tasks. Run `make help` for the list.
#
# CI does not depend on this Makefile (the workflows call the underlying tools
# directly), but every target here mirrors what CI runs, so a clean `make check`
# locally means a green pipeline.

GO       ?= go
BIN_DIR  ?= bin
BINARY   ?= $(BIN_DIR)/depbisect
PKG      ?= ./...
FUZZTIME ?= 30s
COVER_MIN ?= 80.0

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the depbisect binary into ./bin.
	$(GO) build -o $(BINARY) ./cmd/depbisect

.PHONY: test
test: ## Run the test suite (executes fuzz seed corpora too).
	$(GO) test $(PKG)

.PHONY: test-race
test-race: ## Run the test suite with the race detector.
	$(GO) test -race $(PKG)

.PHONY: cover
cover: ## Run tests with coverage and enforce the floor (override: make cover COVER_MIN=85).
	$(GO) test -coverprofile=coverage.out $(PKG)
	./scripts/check-coverage.sh coverage.out $(COVER_MIN)

.PHONY: fmt
fmt: ## Format all Go source in place.
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is not gofmt-clean.
	@test -z "$$(gofmt -l . | grep -v '^\.cache/')" || { echo "gofmt needed:"; gofmt -l . | grep -v '^\.cache/'; exit 1; }

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint (bundles gofmt, govet, staticcheck, errcheck, ...).
	golangci-lint run

.PHONY: vuln
vuln: ## Scan dependencies and stdlib for known vulnerabilities.
	govulncheck $(PKG)

.PHONY: fuzz
fuzz: ## Fuzz each target for $(FUZZTIME) (override: make fuzz FUZZTIME=2m).
	$(GO) test -run='^$$' -fuzz='^FuzzMinimize$$'         -fuzztime=$(FUZZTIME) ./internal/ddmin
	$(GO) test -run='^$$' -fuzz='^FuzzParsePackageJSON$$' -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParsePackageLock$$' -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParsePnpmLock$$'    -fuzztime=$(FUZZTIME) ./internal/manifest

.PHONY: bench
bench: ## Run all benchmarks.
	$(GO) test -run='^$$' -bench=. -benchmem ./...

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum.
	$(GO) mod tidy

.PHONY: check
check: fmt-check vet lint test test-race ## Run the full pre-PR gate.

.PHONY: install-tools
install-tools: ## Install the auxiliary dev tools (see notes for golangci-lint).
	# golangci-lint bundles gofmt, govet, staticcheck, errcheck and more.
	# Recent releases need a current Go toolchain to build from source, so
	# install the prebuilt binary instead, e.g.:
	#   brew install golangci-lint
	# or see https://golangci-lint.run/welcome/install/
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(BIN_DIR) dist
