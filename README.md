# DepBisect

`git bisect`, but for dependency updates: find the smallest set of `package.json` dependency changes between two Git revisions that makes a command fail.

```text
$ depbisect run --base origin/main -- pnpm test
==> Comparing dependencies: origin/main (a1b2c3d4e5f6) -> HEAD (f6e5d4c3b2a1)
==> Analyzed 43 dependency changes
==> Baseline 1/2: all updates reverted (must pass)
==> Baseline 2/2: all updates applied (must fail)
==> Bisecting 43 changes (ddmin)
==> Minimal failing set: 1 of 43 changes

Analyzed 43 dependency changes

Minimal failing set:
  @acme/parser 3.8.1 -> 3.9.0

Reproduced 3/3 times

Report: depbisect-report.md (JSON: depbisect-report.json)
```

## Install

```sh
# Go
go install github.com/skyneticist/depbisect/cmd/depbisect@latest

# Or download a release binary (Linux/macOS/Windows, amd64/arm64) and verify:
# https://github.com/skyneticist/depbisect/releases
shasum -c --ignore-missing checksums.txt
```

## Quick start

```sh
cd your-repo                                   # tests fail since a deps bump
depbisect run --base origin/main --dry-run -- npm test   # preview the changes
depbisect run --base origin/main --runs 3 -- npm test    # bisect
```

The command after `--` is executed verbatim — no shell. For shell features:
`depbisect run --base main -- sh -c 'npm test 2>&1 | grep -v warn'`.
On Windows, `.bat` and `.cmd` verification commands are rejected unless you
invoke the shell explicitly, for example:
`depbisect run --base main -- cmd.exe /d /s /c "npm test"`.

No repo at hand? `./examples/make-demo.sh` generates an offline demo
repository with a known culprit — see [examples/README.md](examples/README.md).

## Supported package managers

| Manager | Manifest | Lockfile |
|---|---|---|
| npm | `package.json` | `package-lock.json` (v1–v3) |
| pnpm | `package.json` | `pnpm-lock.yaml` (v5/v6/v9) |

Workspaces (npm or pnpm) are not supported yet; DepBisect exits with a clear error rather than guessing.

## How it works

DepBisect diffs the direct dependencies declared in `package.json` between `--base` and `--to` (default `HEAD`). It checks out `--to` in a temporary Git worktree and confirms two baselines: the command passes with every update reverted and fails with every update applied. It then runs the ddmin delta-debugging algorithm, repeatedly installing candidate subsets of the updates and re-running your command, to shrink the failing set. The result is 1-minimal: removing any single update from it makes the command pass. `--runs N` repeats each verification to keep flaky tests from corrupting the result.

## Safety guarantees

- Your checkout is never modified; all installs happen in a DepBisect-owned temporary worktree.
- No destructive Git commands; the worktree is removed (and pruned) afterwards, even on Ctrl-C.
- Command arguments are preserved exactly; no shell evaluation unless you invoke one.
- Reports never contain environment variables or captured command output.

## Known limitations

- Only direct dependency changes in `package.json` are bisected. Lockfile-only changes (same spec, different resolution) are detected and reported, not bisected.
- Installing candidates needs registry access and uses your normal npm/pnpm configuration.
- Workspaces and yarn are not supported yet.
- On Windows, npm/pnpm batch shims are supported for DepBisect's fixed install
  commands. User-supplied `.bat`/`.cmd` verification commands require explicit
  `cmd.exe` invocation so shell parsing is never enabled accidentally.

See [docs/limitations.md](docs/limitations.md) for the full list, [docs/how-it-works.md](docs/how-it-works.md) for internals, and [docs/security.md](docs/security.md) for the threat model.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | minimal failing set found |
| 1 | usage or runtime error |
| 2 | failure did not reproduce with all updates applied |
| 3 | command fails even with all updates reverted |
| 4 | verification command too flaky to bisect |
| 5 | no direct dependency changes between the revisions |

## GitHub Action

```yaml
jobs:
  bisect:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0          # depbisect needs the base revision
      - uses: skyneticist/depbisect@v0
        with:
          base: ${{ github.event.pull_request.base.sha }}
          command: npm test       # run through bash -c by the action
          runs: "3"
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: depbisect-report
          path: depbisect-report.*
```

## Contributing & license

See [CONTRIBUTING.md](CONTRIBUTING.md). Licensed under the [MIT License](LICENSE).
