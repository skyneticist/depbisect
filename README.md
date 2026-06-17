# DepBisect

> **`git bisect`, but for dependency updates.** Find the smallest set of `package.json` changes between two Git revisions that makes a command fail — and prove it's minimal.

<!-- Badges render once the repository is public. -->
[![CI](https://github.com/skyneticist/depbisect/actions/workflows/ci.yml/badge.svg)](https://github.com/skyneticist/depbisect/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/skyneticist/depbisect)](https://goreportcard.com/report/github.com/skyneticist/depbisect)
[![Latest release](https://img.shields.io/github/v/release/skyneticist/depbisect?sort=semver)](https://github.com/skyneticist/depbisect/releases)
[![Go 1.20+](https://img.shields.io/badge/Go-1.20%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

You merge a PR that bumps 40 dependencies. CI goes red. **Which bump broke it?**

`git bisect` walks *commits* — it can't help when the breakage lives inside a single dependency-update commit. DepBisect bisects the *dependency changes themselves*: it diffs the direct dependencies declared in `package.json` between two revisions, then uses delta-debugging to isolate the exact subset responsible for the failure — all inside a throwaway worktree that never touches your checkout.

![DepBisect narrowing 43 dependency changes down to the one that broke the build](docs/demo.gif)

## Features

- **Provably minimal.** Runs Zeller's `ddmin` delta-debugging algorithm plus a one-by-one removal pass, so the answer is *1-minimal* — removing any single dependency from the set makes the failure stop reproducing — not merely "some failing subset."
- **Never touches your checkout.** Every install happens in a DepBisect-owned temporary git worktree. `git reset --hard` and `git clean -ffdx` run *only* there, never in your working tree.
- **Flaky-test aware.** `--runs N` repeats each check; a candidate counts as failing only if *all* N runs fail. Mixed pass/fail results are reported as diagnostics, never silently guessed.
- **Resumable.** Completed trials are checkpointed to disk. Interrupt with Ctrl-C, then pick up exactly where you left off with `--resume`.
- **Deterministic & memoized.** Identical inputs produce an identical bisection path, and no dependency subset is ever installed or tested twice.
- **CI-ready.** Meaningful exit codes (0–5), a schema-stable JSON report, and a reusable composite GitHub Action.
- **Honest by design.** Lockfile-only changes, workspaces, and flaky baselines surface as clear diagnostics — DepBisect would rather say "inconclusive" than overstate certainty.

> ⭐ **If DepBisect saves you a dependency-debugging session, please star it** — it's the main way other developers find the project.

## Why DepBisect

**Reach for it when** a dependency-update PR (Dependabot, Renovate, or a manual bump) turns CI red and you can't tell which package did it — especially when dozens changed at once, or when the culprit only breaks in combination with another bump.

| Instead of…               | The catch                                                                        | DepBisect                                  |
| ------------------------- | -------------------------------------------------------------------------------- | ------------------------------------------ |
| `git bisect`              | Bisects *commits* — useless when the break is inside one dependency-bump commit  | Bisects the dependency changes themselves  |
| Reverting bumps by hand   | O(n) reinstalls, easy to miss interacting deps, no proof you found the real cause | `ddmin` + an automatic 1-minimality proof  |
| Reading the lockfile diff | Shows *what* resolved differently, not *what broke*                              | Pinpoints the exact minimal breaking set   |
| `npm why` / `npm ls`      | Explains the dependency tree, not the failure                                    | Ties the failure to specific version bumps |

## Install

DepBisect ships as a single static binary — pick whichever method fits. You'll
also need `git` and either `npm` or `pnpm` on your `PATH`.

**npm / pnpm / yarn** — no Go toolchain required:

```sh
npm install -g depbisect                            # or: pnpm add -g depbisect
npx depbisect run --base origin/main -- npm test    # …or run without installing
```

**Homebrew** (macOS / Linux):

```sh
brew install skyneticist/tap/depbisect
```

**Scoop** (Windows):

```powershell
scoop bucket add depbisect https://github.com/skyneticist/scoop-bucket
scoop install depbisect
```

**Install script** (Linux / macOS) — downloads the release binary and verifies its checksum:

```sh
curl -fsSL https://raw.githubusercontent.com/skyneticist/depbisect/main/install.sh | sh
```

**Prebuilt binary** — download for your platform from the [releases page](https://github.com/skyneticist/depbisect/releases), then verify:

```sh
shasum -c --ignore-missing checksums.txt
```

**Go** (1.20+):

```sh
go install github.com/skyneticist/depbisect/cmd/depbisect@latest
```

**Docker** — the image bundles `git`, `node`, npm, and pnpm; mount your repo at `/work`:

```sh
docker run --rm -v "$PWD:/work" ghcr.io/skyneticist/depbisect \
  run --base origin/main --runs 3 -- npm test
```

## Quick start

```sh
cd your-repo   # your tests fail after a dependency bump

# 1. Preview which dependencies changed (runs nothing):
depbisect run --base origin/main --dry-run -- npm test

# 2. Bisect, re-running each candidate 3x to absorb flaky tests:
depbisect run --base origin/main --runs 3 -- npm test

# 3. Interrupted? Re-run the same command with --resume to continue:
depbisect run --base origin/main --runs 3 --resume -- npm test
```

> **No repo handy?** `./examples/make-demo.sh` builds a self-contained, offline demo
> repository with a known culprit. See [examples/README.md](examples/README.md).

The command after `--` is executed **verbatim — no shell is involved**, so quotes and
metacharacters in your arguments are inert. Need shell features? Invoke one yourself:

```sh
depbisect run --base main -- sh -c 'npm test 2>&1 | grep -v warn'
```

## How it works

```text
  --base ─┐
          ├──▶  diff package.json  ──▶  N direct dependency changes
  --to  ──┘
                                              │
                                              ▼
                        isolated git worktree   (your checkout is never modified)
                                              │
                  ┌───────────────────────────┴───────────────────────────┐
                  ▼                                                         ▼
        revert ALL updates                                        apply ALL updates
         (must PASS every run)                                    (must FAIL every run)
                  └───────────────────────────┬───────────────────────────┘
                                              ▼
                              ddmin delta-debugging
                  reset worktree → install a subset → run command → repeat
                                              │
                                              ▼
                       one-by-one removal pass  ──▶  certify 1-minimal
                                              │
                                              ▼
                          minimal breaking dependency set
```

1. **Resolve & diff.** `--base` and `--to` (default `HEAD`) are resolved to commits; the
   `dependencies`, `devDependencies`, and `optionalDependencies` declared in `package.json`
   are diffed with a structured JSON parser (never regex).
2. **Confirm the baselines.** In the isolated worktree, the command must *pass* with every
   update reverted and *fail* with every update applied. Anything else ends the run with a
   specific diagnostic instead of a bogus answer.
3. **Delta-debug.** `ddmin` repeatedly resets the worktree, rewrites `package.json` to apply
   a candidate subset, installs with npm/pnpm, and re-runs your command.
4. **Certify minimality.** Every one-change removal from the result is tested. The set is
   reported as 1-minimal only when all of those neighbors resolve and pass.
5. **Report.** Results go to the terminal, `depbisect-report.md`, and a schema-stable
   `depbisect-report.json` — consume the JSON in automation rather than parsing the display.

For the full algorithm, candidate semantics, and determinism guarantees, see
[docs/how-it-works.md](docs/how-it-works.md).

## Safety

DepBisect is built to be safe to point at a real repository:

- Your checkout is **never modified** — all installs happen in a DepBisect-owned temporary worktree.
- `git reset --hard` and `git clean -ffdx` run **only** inside that temporary worktree.
- The worktree is removed and pruned afterward, **even on Ctrl-C**.
- Arguments are preserved exactly; **no shell evaluation** unless you invoke one.
- Reports **never** contain environment variables or captured command output — only revisions, dependency names, outcomes, and timings.

> [!IMPORTANT]
> Installing dependencies runs their lifecycle scripts, exactly as a manual `npm install`
> would. Bisecting untrusted version ranges therefore executes untrusted code — run it in
> the same sandbox you'd trust for `npm install` of those versions. See
> [docs/security.md](docs/security.md) for the full threat model.

## Supported package managers

| Manager | Manifest       | Lockfile                          |
| ------- | -------------- | --------------------------------- |
| npm     | `package.json` | `package-lock.json` (v1–v3)       |
| pnpm    | `package.json` | `pnpm-lock.yaml` (v5 / v6 / v9)   |

Workspaces (npm or pnpm) and yarn are not supported yet; DepBisect exits with a clear error
rather than guessing.

## Configuration

Common flags — run `depbisect help` for the complete list.

| Flag                  | Description                                                      |
| --------------------- | --------------------------------------------------------------- |
| `--base <rev>`        | Base revision to compare against **(required)**                 |
| `--to <rev>`          | Target revision (default `HEAD`)                                |
| `--runs <n>`          | Verification runs per candidate; raises confidence on flaky tests (default `1`) |
| `--dry-run`           | Show detected changes and plan, then exit without bisecting     |
| `--resume`            | Resume completed trials from the checkpoint                     |
| `--quiet` / `--verbose` | Print only the final result / stream all subprocess output    |
| `--pm <npm\|pnpm>`    | Force a package manager (default: detected from lockfile)       |

<details>
<summary><b>Timeouts, checkpoints, reports, and environment</b></summary>

**Timeouts** accept Go durations (`90s`, `15m`, `2h`); `0` disables that deadline.

- `--run-timeout` bounds each verification command.
- `--install-timeout` bounds each package-manager install.
- `--overall-timeout` bounds the complete bisection. Cleanup gets its own bounded context, so a temporary worktree is still removed even after the overall deadline fires.

**Checkpoints.** Completed trials are appended to `.depbisect-checkpoint.jsonl`. The file is
removed after a successful run and kept after an interruption or failure so `--resume` can
continue. Use `--checkpoint <path>` to relocate it, or `--checkpoint ""` to disable it.

**Reports.** `--report-md` / `--report-json` set output paths; `--no-reports` writes none.

**Output.** Progress adapts to its destination: a terminal gets a width-aware, in-place
active trial, while redirected output stays plain and line-oriented for CI logs. `NO_COLOR`
and `CLICOLOR=0` disable color; `CLICOLOR_FORCE=1` forces it when redirected.

</details>

<details>
<summary><b>Exit codes</b></summary>

| Code | Meaning                                                        |
| ---- | ------------------------------------------------------------- |
| `0`  | minimal failing set found                                     |
| `1`  | usage or runtime error                                        |
| `2`  | failure did not reproduce with all updates applied            |
| `3`  | command fails even with all updates reverted                  |
| `4`  | inconclusive: flaky verification or minimality unprovable     |
| `5`  | no direct dependency changes between the revisions            |

Exit code `2` is the clean "nothing broke here" result — the command passed with every
update applied, so there was no dependency-related failure to bisect. Exit code `5` means
the revisions contained no direct dependency changes in the first place.

</details>

## GitHub Action

Drop DepBisect into CI to automatically pin the breaking dependency on a failing PR:

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
          command: npm test       # run through `bash -c` by the action
          runs: "3"
          install-timeout: 15m
          overall-timeout: 1h
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: depbisect-report
          path: depbisect-report.*
```

By default the action builds the exact DepBisect source bundled with the selected action
ref. Set the `version` input to a release tag only when intentionally testing a different
CLI release.

## Limitations

- Only **direct** dependency changes in `package.json` are bisected. Lockfile-only changes
  (same spec, different resolution) are detected and reported, not bisected.
- Installing candidates needs **registry access** and uses your normal npm/pnpm config.
- **Workspaces** and **yarn** are not supported yet.
- On Windows, implicit `.bat`/`.cmd` verification commands are rejected; invoke `cmd.exe`
  explicitly (e.g. `-- cmd.exe /d /s /c "npm test"`) when shell semantics are intended.

See [docs/limitations.md](docs/limitations.md) for the complete list and how to interpret
each non-zero exit.

## Documentation

- [How it works](docs/how-it-works.md) — the pipeline, candidate semantics, and determinism.
- [Limitations](docs/limitations.md) — what's unsupported and why, plus exit-code interpretation.
- [Security](docs/security.md) — the threat model and what DepBisect will and won't do.
- [Examples](examples/README.md) — offline demo repositories with known culprits.

## Contributing & license

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the dev setup and
ground rules. Licensed under the [MIT License](LICENSE).
