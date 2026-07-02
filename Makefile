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

# need — guard a recipe that shells out to an optional tool. Prints an
# actionable install hint instead of make's cryptic "<tool>: No such file or
# directory" when the tool is absent. Usage as the first line of a recipe:
#   $(call need,goreleaser,go install github.com/goreleaser/goreleaser/v2@latest)
define need
@command -v $(1) >/dev/null 2>&1 || { printf '\n\033[1;31m%s not found\033[0m — needed by make %s\n  install: %s\n\n' '$(1)' '$@' '$(2)'; exit 1; }
endef

##@ General

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*## "} /^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Build & run

.PHONY: build
build: ## Build the depbisect binary into ./bin.
	$(GO) build -o $(BINARY) ./cmd/depbisect

.PHONY: install
install: ## Install the binary with `go install` (honors GOBIN/GOPATH).
	$(GO) install ./cmd/depbisect

.PHONY: run
run: build ## Build, then run it with ARGS (e.g. make run ARGS="run --base HEAD~1 -- npm test").
	$(BINARY) $(ARGS)

##@ Test & quality

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

.PHONY: cover-html
cover-html: cover ## Open the HTML coverage report in a browser (after `cover`).
	$(GO) tool cover -html=coverage.out

.PHONY: fmt
fmt: ## Format all Go source in place.
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any tracked Go source is not gofmt-clean.
	@bad="$$(gofmt -l $$(git ls-files '*.go'))"; test -z "$$bad" || { echo "gofmt needed:"; echo "$$bad"; exit 1; }

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint (bundles gofmt, govet, staticcheck, errcheck, ...).
	$(call need,golangci-lint,see https://golangci-lint.run/welcome/install/)
	golangci-lint run

.PHONY: vuln
vuln: ## Scan dependencies and stdlib for known vulnerabilities.
	$(call need,govulncheck,go install golang.org/x/vuln/cmd/govulncheck@latest)
	govulncheck $(PKG)

# go.mod/go.sum parsing has no fuzz target by design: it delegates to
# golang.org/x/mod, which is fuzzed upstream.
.PHONY: fuzz
fuzz: ## Fuzz each target for $(FUZZTIME) (override: make fuzz FUZZTIME=2m).
	$(GO) test -run='^$$' -fuzz='^FuzzMinimize$$'         -fuzztime=$(FUZZTIME) ./internal/ddmin
	$(GO) test -run='^$$' -fuzz='^FuzzParsePackageJSON$$' -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParsePackageLock$$' -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParsePnpmLock$$'    -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParseCargoToml$$'   -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParseCargoLock$$'   -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParsePyproject$$'   -fuzztime=$(FUZZTIME) ./internal/manifest
	$(GO) test -run='^$$' -fuzz='^FuzzParseUvLock$$'      -fuzztime=$(FUZZTIME) ./internal/manifest

.PHONY: bench
bench: ## Run all benchmarks.
	$(GO) test -run='^$$' -bench=. -benchmem ./...

.PHONY: test-make
test-make: ## Lint the Makefile's optional-tool install hints.
	./scripts/test-make-guards.sh

.PHONY: check
check: fmt-check vet lint test-make test test-race ## Run the full pre-PR gate.

.PHONY: ci
ci: check cover vuln fuzz ## Full local pre-push mirror: check + cover + vuln + fuzz smoke.
	@echo "ci: all local checks passed."

##@ Demos
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

##@ Docker

.PHONY: docker-build
docker-build: ## Build the Docker image as depbisect:dev.
	$(call need,docker,see https://docs.docker.com/get-docker/)
	docker build -t depbisect:dev .

.PHONY: docker-run
docker-run: docker-build ## Run the image against your repo (e.g. make docker-run ARGS="run --base HEAD~1 -- npm test").
	docker run --rm -v "$$PWD:$$PWD" -w "$$PWD" depbisect:dev $(ARGS)

.PHONY: docker-smoke
docker-smoke: docker-build ## Mirror the CI docker image behaviour check (needs docker + node).
	@mkdir -p $(BIN_DIR)
	docker run --rm depbisect:dev help
	docker run --rm --entrypoint sh depbisect:dev -eu -c 'git --version; node --version; npm --version; pnpm --version'
	./examples/make-demo.sh
	docker run --rm -v "$$PWD:$$PWD" -w "$$PWD" depbisect:dev run --repo "$$PWD/examples/demo/app" --base HEAD~1 --runs 3 -- node test.js | tee $(BIN_DIR)/bisect.log
	grep -q leftpad $(BIN_DIR)/bisect.log

##@ Release & media

.PHONY: release-snapshot
release-snapshot: ## Dry-run a release build with goreleaser (snapshot, no publish; needs goreleaser).
	$(call need,goreleaser,go install github.com/goreleaser/goreleaser/v2@latest)
	goreleaser release --snapshot --clean

.PHONY: gifs
gifs: ## Re-render the demo gifs from docs/assets/vhs/**/*.tape (needs vhs).
	$(call need,vhs,see https://github.com/charmbracelet/vhs)
	@for tape in docs/assets/vhs/*/*.tape; do echo ">> vhs $$tape"; vhs "$$tape"; done

##@ Maintenance

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum.
	$(GO) mod tidy

.PHONY: install-tools
install-tools: ## Install the auxiliary dev tools (see notes for golangci-lint).
	# golangci-lint bundles gofmt, govet, staticcheck, errcheck and more.
	# Recent releases need a current Go toolchain to build from source, so
	# install the prebuilt binary per the official instructions:
	#   https://golangci-lint.run/welcome/install/
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GO) install github.com/goreleaser/goreleaser/v2@latest

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(BIN_DIR) dist
