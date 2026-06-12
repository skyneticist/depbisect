# Contributing

## Setup

Go 1.20+ and git are required; `node` enables the end-to-end tests (they are skipped without it). No network access is needed to run tests. CI covers the minimum Go version plus Go's current stable and oldstable releases on Linux, macOS, and Windows.

```sh
go build ./...
go test ./...
```

## Before sending a PR

```sh
gofmt -l .            # must print nothing
go vet ./...
go test ./...
go test -race ./...
staticcheck ./...     # go install honnef.co/go/tools/cmd/staticcheck@latest
```

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

Tag `vX.Y.Z` (semver). CI builds reproducible artifacts with goreleaser, including checksums.
