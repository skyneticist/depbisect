# Contributing

## Setup

Go 1.25+ and git are required; `node` enables the end-to-end tests (they are skipped without it). No network access is needed to run tests. CI covers Go's current stable and oldstable releases on Linux, macOS, and Windows.

```sh
go build ./...
go test ./...
```

## Before sending a PR

Run the full gate — formatting, lint, tests, and the race detector:

```sh
make check
```

It wraps the tools below, which you can also run on their own:

```sh
gofmt -l .            # must print nothing (or: make fmt)
golangci-lint run     # gofmt, go vet, staticcheck, errcheck, ... (or: make lint)
go test ./...
go test -race ./...
```

golangci-lint ships as a prebuilt binary — install it with `brew install
golangci-lint` or see <https://golangci-lint.run/welcome/install/>. Its
configuration lives in [.golangci.yml](.golangci.yml).

## Commit messages

This repository follows [Conventional Commits](https://www.conventionalcommits.org):
a `type(optional scope): summary` subject, where common types are `feat`, `fix`,
`perf`, `docs`, `test`, `refactor`, `build`, `ci`, and `chore`. `feat`, `fix`,
and `perf` appear in the generated release notes; the rest are kept out of them.
PR titles should follow the same convention, since branches are squash- or
merge-committed using the title.

## Fuzzing and benchmarks

The parsers (`internal/manifest`) and the ddmin core (`internal/ddmin`) carry
fuzz targets and benchmarks. Their seed corpora run as part of `go test ./...`;
to fuzz actively or to benchmark:

```sh
make fuzz                 # 30s per target; override with FUZZTIME=2m
make bench
```

Extend the relevant `FuzzXxx`/`BenchmarkXxx` when you change parsing or the
algorithm.

## Ground rules

- Tests first. New behavior lands with a failing test that the change turns green.
- The ddmin core (`internal/ddmin`) must stay free of I/O, processes, and clocks.
- Lockfiles and manifests are parsed structurally — never with regexes or string replacement.
- Subprocesses take argument vectors; never build shell strings from data.
- Anything that writes outside a DepBisect-owned temp directory is a bug.
- Golden files (`internal/report/testdata`) are regenerated with `go test ./internal/report/ -run Golden -update`; review the diff.

## Layout

| Package | Purpose |
|---|---|
| `internal/ddmin` | pure delta-debugging algorithm |
| `internal/manifest` | package.json + lockfile parsing, diffing, candidate rendering |
| `internal/execx` | subprocess runner (real + fake) |
| `internal/gitx` | git operations over the runner |
| `internal/pm` | package-manager detection and installs |
| `internal/verify` | repeated verification runs + flakiness classification |
| `internal/engine` | orchestration |
| `internal/report` | JSON/Markdown reports |
| `internal/cli` | argument parsing, output, exit codes |
| `cmd/depbisect` | main + end-to-end tests |

## Releases

Tag `vX.Y.Z` (semver). Pushing the tag runs four workflows in parallel:

- **`release`** (goreleaser) — builds reproducible binaries + `checksums.txt`, publishes the GitHub Release, and (once the bucket/tap repos are configured) the Homebrew formula and Scoop manifest.
- **`npm publish`** — `scripts/build-npm.mjs` cross-compiles every target and publishes the `depbisect` launcher plus its per-platform `@depbisect/*` packages to npm with provenance. See [npm/](npm/).
- **`Docker`** — builds the multi-arch, per-ecosystem image variants from the [Dockerfile](Dockerfile) (`latest` for JavaScript; `go`, `rust`, and `python` aliases plus `X.Y.Z[-suffix]` tags) and pushes them to `ghcr.io/skyneticist/depbisect` using the built-in `GITHUB_TOKEN`.
- **`Move version aliases`** — re-points the sliding `v0` / `v0.1` tags at the new release so `uses: skyneticist/depbisect@v0` stays current.

`install.sh` installs the latest release binary with checksum verification. The
npm and Docker pipelines are independent of the GitHub Release: each compiles
from the tagged source itself.
