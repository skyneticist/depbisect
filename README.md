# DepBisect

`git bisect`, but for dependency updates: find the smallest set of `package.json` dependency changes between two Git revisions that makes a command fail.

```text
$ depbisect run --base origin/main -- pnpm test
     Compare origin/main (a1b2c3d4e5f6) -> HEAD (f6e5d4c3b2a1)
     Changes 43 direct dependency changes
    Baseline 1/2 | without updates (expect PASS)
     Trial 1 baseline without updates | 0/43 changes | preparing
    EXPECTED trial 1 | baseline without updates | 0/43 changes | PASS | 12.1s
    Baseline 2/2 | with all updates (expect FAIL)
     Trial 2 baseline with all updates | 43/43 changes | preparing
    EXPECTED trial 2 | baseline with all updates | 43/43 changes | FAIL | 13.4s
      Bisect 43 changes with ddmin
         ...
    Complete minimal failing set contains 1 of 43 changes

      Result Minimal breaking dependency set found

Breaking dependencies
  - @acme/parser 3.8.1 -> 3.9.0

     Command pnpm test
     Manager pnpm 9.15.0
    Evidence 3/3 failing runs
     Changes 43 analyzed
      Trials 17
    Duration 2m14.3s
     Outcome minimal-set-found
      Report depbisect-report.md
        JSON depbisect-report.json
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
# If interrupted, rerun the same command with --resume.
depbisect run --base origin/main --runs 3 --resume -- npm test
# Final result only, useful in quieter CI jobs:
depbisect run --base origin/main --quiet -- npm test
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

DepBisect diffs the direct dependencies declared in `package.json` between `--base` and `--to` (default `HEAD`). It checks out `--to` in a temporary Git worktree and confirms two baselines: the command passes with every update reverted and fails with every update applied. It then runs the ddmin delta-debugging algorithm, repeatedly resetting the owned worktree, installing candidate subsets of the updates, and re-running your command. A final one-change-removal pass proves that the result is 1-minimal. If a required neighboring configuration cannot be installed or is flaky, DepBisect reports an inconclusive best-known set instead of overstating certainty. `--runs N` repeats each verification to keep flaky tests from corrupting the result.

Completed trials are appended to `.depbisect-checkpoint.jsonl`. The file is
removed after a completed run and retained after interruption or runtime
failure. Re-run the same command with `--resume` to continue. Use
`--checkpoint <path>` to choose another location or `--checkpoint ""` to
disable checkpointing.

Progress adapts to its destination: terminals get a width-aware, in-place
active trial, while redirected output remains plain and line-oriented for CI
logs.
`--quiet` suppresses progress and keeps the final result; `--verbose` shows
every prepare/install/verify phase and streams subprocess output. `NO_COLOR`
and `CLICOLOR=0` disable color, while `CLICOLOR_FORCE=1` enables color when
redirected. For automation, consume `depbisect-report.json` rather than
parsing the human-oriented terminal display.

Use `--run-timeout` to bound each verification command,
`--install-timeout` to bound each package-manager install, and
`--overall-timeout` to bound the complete bisection. All accept Go duration
values such as `90s`, `15m`, or `2h`; zero leaves that deadline disabled.
Cleanup gets its own bounded context, so an overall timeout still attempts to
remove the temporary worktree. Reports break completed trials into preparation,
installation, and verification time and include final cleanup in completed
wall time.

## Safety guarantees

- Your checkout is never modified; all installs happen in a DepBisect-owned temporary worktree.
- `git reset --hard` and `git clean -ffdx` run only inside the temporary
  worktree owned by DepBisect, never in your checkout.
- The worktree is removed (and pruned) afterwards, even on Ctrl-C.
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
| 4 | inconclusive: flaky verification or minimality could not be proven |
| 5 | no direct dependency changes between the revisions |

Exit code `2` is the clean "nothing broke here" result: the verification
command passed with every dependency update applied, so there was no
dependency-related failure to bisect. Exit code `5` means the revisions did
not contain any direct dependency changes in the first place.

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
          install-timeout: 15m
          overall-timeout: 1h
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: depbisect-report
          path: depbisect-report.*
```

By default the action builds the exact DepBisect source bundled with the
selected action ref. Set its `version` input to an explicit release tag only
when intentionally testing a different CLI release.

## Contributing & license

See [CONTRIBUTING.md](CONTRIBUTING.md). Licensed under the [MIT License](LICENSE).
