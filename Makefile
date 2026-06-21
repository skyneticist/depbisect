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

# --- Example demos --------------------------------------------------------
# Build, generate an offline example repo, and bisect it. Each target mirrors
# the matching job in .github/workflows/ci.yml (demo / demo-cargo / demo-go /
# demo-python), so a green `make demo*` predicts a green pipeline. Everything is
# offline (file: deps, vendored registries, a file:// GOPROXY, committed wheels);
# the only requirement is the relevant toolchain (node+npm, cargo, go, or
# uv+python) on PATH.

# Constant env for the Go demos; GOPROXY is per-demo and set inline below.
GO_DEMO_ENV ?= GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local

.PHONY: demo
demo: build ## npm: bisect the simple example repo (culprit: leftpad).
	./examples/make-demo.sh
	$(BINARY) run --repo examples/demo/app --base HEAD~1 --runs 3 -- node test.js

.PHONY: demo-complex
demo-complex: build ## npm: 12-package / 5-culprit interacting-failure demo.
	./examples/make-demo-complex.sh
	$(BINARY) run --repo examples/demo-complex/app --base HEAD~1 --runs 3 -- node test.js

.PHONY: demo-jobs
demo-jobs: build ## npm: 18-package / 8-consensus demo, bisected with --jobs.
	./examples/make-demo-jobs.sh
	$(BINARY) run --repo examples/demo-jobs/app --base HEAD~1 --runs 3 --jobs 4 -- node test.js

.PHONY: demo-swarm
demo-swarm: build ## npm: 28-package / 12-replica demo, bisected with --jobs.
	./examples/make-demo-swarm.sh
	$(BINARY) run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- node test.js

.PHONY: demo-cargo
demo-cargo: build ## Cargo: simple + complex + jobs + swarm demos.
	./examples/make-demo-cargo.sh
	$(BINARY) run --repo examples/demo-cargo/app --base HEAD~1 --runs 3 -- cargo test
	./examples/make-demo-cargo-complex.sh
	$(BINARY) run --repo examples/demo-cargo-complex/app --base HEAD~1 --runs 3 --jobs 4 -- cargo test
	./examples/make-demo-cargo-jobs.sh
	$(BINARY) run --repo examples/demo-cargo-jobs/app --base HEAD~1 --runs 3 --jobs 4 -- cargo test
	./examples/make-demo-cargo-swarm.sh
	$(BINARY) run --repo examples/demo-cargo-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- cargo test

.PHONY: demo-go
demo-go: build ## Go: simple + complex + jobs + swarm demos (offline file:// proxy).
	./examples/make-demo-go.sh
	GOPROXY=file://$(CURDIR)/examples/demo-go/proxy $(GO_DEMO_ENV) \
		$(BINARY) run --repo examples/demo-go/app --base HEAD~1 --runs 3 -- $(GO) test ./...
	./examples/make-demo-go-complex.sh
	GOPROXY=file://$(CURDIR)/examples/demo-go-complex/proxy $(GO_DEMO_ENV) \
		$(BINARY) run --repo examples/demo-go-complex/app --base HEAD~1 --runs 3 --jobs 4 -- $(GO) test ./...
	./examples/make-demo-go-jobs.sh
	GOPROXY=file://$(CURDIR)/examples/demo-go-jobs/proxy $(GO_DEMO_ENV) \
		$(BINARY) run --repo examples/demo-go-jobs/app --base HEAD~1 --runs 3 --jobs 4 -- $(GO) test ./...
	./examples/make-demo-go-swarm.sh
	GOPROXY=file://$(CURDIR)/examples/demo-go-swarm/proxy $(GO_DEMO_ENV) \
		$(BINARY) run --repo examples/demo-go-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- $(GO) test ./...

.PHONY: demo-python
demo-python: export UV_PYTHON_PREFERENCE = only-system
demo-python: build ## Python (uv): bisect the offline example repo (culprit: breakage).
	./examples/make-demo-python.sh
	$(BINARY) run --repo examples/demo-python/app --base HEAD~1 --runs 3 -- uv run -- python check.py

.PHONY: demos
demos: demo demo-complex demo-jobs demo-swarm demo-cargo demo-go demo-python ## Run every demo above.
	@echo "All demos passed."

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(BIN_DIR) dist
