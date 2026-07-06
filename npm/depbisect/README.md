<p align="center">
  <img src="https://raw.githubusercontent.com/skyneticist/depbisect/main/docs/assets/images/axol_detective.png" alt="DepBisect mascot — a cartoon axolotl detective in a deerstalker hat holding scissors" width="180">
</p>

<h1 align="center">DepBisect</h1>

<p align="center">
  <strong><code>git bisect</code>, but for dependency updates.</strong><br>
  Find the smallest set of dependency changes between two Git revisions that makes a command fail — and prove it's minimal.
</p>

<p align="center">
  <a href="https://github.com/skyneticist/depbisect/actions/workflows/ci.yml"><img src="https://github.com/skyneticist/depbisect/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/skyneticist/depbisect/releases"><img src="https://img.shields.io/github/v/release/skyneticist/depbisect?sort=semver" alt="Latest release"></a>
  <a href="https://github.com/skyneticist/depbisect/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"></a>
</p>

You merge a PR that bumps 40 dependencies. CI goes red. **Which bump broke it?**

`git bisect` walks *commits* — DepBisect bisects the *dependency changes themselves*: it diffs the direct dependencies in your manifest (`package.json`, `Cargo.toml`, `go.mod`, `pyproject.toml`, or `composer.json`) between two revisions, then narrows them to the exact minimal subset that makes your command fail — and proves no smaller set does. Every install runs in a throwaway git worktree, so it never touches your checkout.

![DepBisect narrowing 12 dependency changes down to the minimal 5-package set that broke the build](https://raw.githubusercontent.com/skyneticist/depbisect/main/docs/assets/gifs/js/demo.gif)

## Why DepBisect

**Reach for it when** a dependency-update PR (Dependabot, Renovate, or a manual bump) turns CI red and you can't tell which bump did it — especially when dozens changed at once, or when the culprit only breaks in combination with another bump.

| Instead of…               | The catch                                                                         | DepBisect                                  |
| ------------------------- | --------------------------------------------------------------------------------- | ------------------------------------------ |
| `git bisect`              | Bisects *commits* — useless when the break is inside one dependency-bump commit   | Bisects the dependency changes themselves  |
| Reverting bumps by hand   | O(n) reinstalls, easy to miss interacting deps, no proof you found the real cause | `ddmin` + an automatic 1-minimality proof  |
| Reading the lockfile diff | Shows *what* resolved differently, not *what broke*                               | Pinpoints the exact minimal breaking set   |
| `npm why` / `npm ls`      | Explains the dependency tree, not the failure                                     | Ties the failure to specific version bumps |

## How it works

DepBisect diffs the direct dependencies declared in your manifest between two revisions, then runs Zeller's `ddmin` delta-debugging algorithm to find the smallest failing subset — followed by a one-by-one removal pass that certifies the result is *1-minimal* (removing any single package from the set makes the failure stop reproducing).

## Install

```sh
npm install -g depbisect
# or: pnpm add -g depbisect

# …or run without installing:
npx depbisect run --base origin/main -- npm test
```

This package ships a **prebuilt binary** for your platform as an optional dependency. There is no Go toolchain to install, no post-install script, and no network fetch at install time — just the binary for your `os`/`cpu`.

## Quick start

```sh
cd your-repo   # your tests fail after a dependency bump

# Preview which dependencies changed (installs nothing):
depbisect run --base origin/main --dry-run -- npm test

# Bisect, re-running each candidate 3x to absorb flaky tests:
depbisect run --base origin/main --runs 3 -- npm test

# Interrupted? Resume where you left off:
depbisect run --base origin/main --runs 3 --resume -- npm test
```

![DepBisect's --dry-run listing the two changed dependencies and the bisection plan, then exiting without installing anything](https://raw.githubusercontent.com/skyneticist/depbisect/main/docs/assets/gifs/js/dry-run.gif)

### Example output

```
$ depbisect run --base origin/main --runs 3 -- npm test

  baseline  all reverted  → pass  (3/3)
  baseline  all applied   → fail  (3/3)

  ddmin     12 changes → 6 → 3 → 5 → 4 → …
  reproduced            → 5 packages

  minimality  removing eslint-plugin-react        → pass ✓
  minimality  removing @typescript-eslint/parser  → pass ✓
  minimality  removing webpack                    → pass ✓
  minimality  removing react-scripts              → pass ✓
  minimality  removing jest-environment-jsdom     → pass ✓

  result  1-minimal failing set (5 of 12 changes)

    react-scripts              4.0.3  →  5.0.1
    jest-environment-jsdom    27.5.1  →  29.7.0
    webpack                    5.75.0  →  5.97.1
    @typescript-eslint/parser  5.62.0  →  7.18.0
    eslint-plugin-react       11.3.2  →  7.37.2

  report  depbisect-report.md
  report  depbisect-report.json
```

## Supported ecosystems

| Manager | Manifest         | Lockfile                        |
| ------- | ---------------- | ------------------------------- |
| npm     | `package.json`   | `package-lock.json` (v1–v3)     |
| pnpm    | `package.json`   | `pnpm-lock.yaml` (v5/v6/v9)     |
| yarn    | `package.json`   | `yarn.lock` (classic + Berry)   |
| cargo   | `Cargo.toml`     | `Cargo.lock`                    |
| go      | `go.mod`         | `go.sum`                        |
| uv      | `pyproject.toml` | `uv.lock`                       |
| composer | `composer.json` | `composer.lock`                |

Auto-detected from the manifest, or force one with `--pm <npm|pnpm|yarn|cargo|go|uv|composer>`.

## Parallel bisection with `--jobs`

Candidate subsets are independent — DepBisect can evaluate them across several isolated worktrees at once. Same minimal result at any job count; only the wall time changes.

Sequential (`--jobs 1`):

![DepBisect bisecting 28 dependency changes sequentially — one worktree, one candidate at a time — isolating the twelve-package culprit in 15.6 seconds](https://raw.githubusercontent.com/skyneticist/depbisect/main/docs/assets/gifs/js/sequential.gif)

Parallel (`--jobs 12`):

![The same 28-change bisection with --jobs 12 — twelve worktrees evaluating candidates concurrently — reaching the identical result in 5.5 seconds](https://raw.githubusercontent.com/skyneticist/depbisect/main/docs/assets/gifs/js/parallel-only.gif)

## Resume after interrupt

Press Ctrl-C mid-bisection and DepBisect checkpoints completed trials to disk. Re-run with `--resume` to restore them instead of starting over.

![DepBisect interrupted with Ctrl-C partway through a bisection, then resumed with --resume, restoring the completed trials from the on-disk checkpoint](https://raw.githubusercontent.com/skyneticist/depbisect/main/docs/assets/gifs/js/resume.gif)

## Common flags

| Flag                    | Description                                                                   |
| ----------------------- | ----------------------------------------------------------------------------- |
| `--base <rev>`          | Base revision to compare against **(required)**                               |
| `--to <rev>`            | Target revision (default `HEAD`)                                              |
| `--runs <n>`            | Verification runs per candidate; raises confidence on flaky tests (default 1) |
| `--jobs` / `-j <n>`     | Evaluate candidates in parallel across isolated worktrees (default 1)         |
| `--dry-run`             | Show detected changes and plan, then exit without bisecting                   |
| `--resume`              | Resume completed trials from the checkpoint                                   |
| `--quiet` / `--verbose` | Print only the final result / stream all subprocess output                    |
| `--pm <manager>`        | Force a package manager (default: auto-detected)                              |

## Exit codes

| Code | Meaning                                                    |
| ---- | ---------------------------------------------------------- |
| `0`  | Minimal failing set found                                  |
| `1`  | Usage or runtime error                                     |
| `2`  | Failure did not reproduce with all updates applied         |
| `3`  | Command fails even with all updates reverted               |
| `4`  | Inconclusive: flaky verification or minimality unprovable  |
| `5`  | No direct dependency changes between the revisions         |

## GitHub Action

```yaml
- uses: skyneticist/depbisect@v0
  with:
    base: ${{ github.event.pull_request.base.sha }}
    command: npm test
    runs: "3"
```

## Safety

- Your checkout is **never modified** — all installs happen in a DepBisect-owned temporary worktree.
- `git reset --hard` and `git clean -ffdx` run **only** inside that worktree.
- The worktree is removed and pruned afterward, **even on Ctrl-C**.
- Arguments are preserved exactly — **no shell evaluation** unless you invoke one.

> Installing dependencies runs their lifecycle scripts, exactly as a manual `npm install` would. Bisecting untrusted version ranges therefore executes untrusted code — run it in the same sandbox you'd trust for `npm install` of those versions.

## More

- [Full documentation & how it works](https://github.com/skyneticist/depbisect)
- [Releases](https://github.com/skyneticist/depbisect/releases)
- [Issues](https://github.com/skyneticist/depbisect/issues)

Licensed under the [MIT License](https://github.com/skyneticist/depbisect/blob/main/LICENSE).
