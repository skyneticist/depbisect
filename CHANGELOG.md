# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Yarn support: `yarn.lock` is auto-detected alongside the other JavaScript
  lockfiles — both the classic v1 format and the Berry (v2+) YAML format are
  parsed — or forced with `--pm yarn`. Installs run `yarn install` with
  Berry's automatic CI immutable-installs mode disabled, since candidate
  manifests must be allowed to update the lockfile. Yarn workspaces remain
  unsupported, matching npm/pnpm.
- The live `ddmin` progress row now animates a smooth scanner sweep (a bright
  head with a fading tail), and its tested count and elapsed time update
  continuously between trial events instead of freezing during long installs.
  The sweep speeds up with `--jobs` — a visual echo of trial throughput.
  Interactive color terminals only; redirected/CI output is unchanged.
- `DEPBISECT_JOBS` environment variable sets the default for `--jobs`
  (the flag still wins). The built-in default stays `1`: parallel trials
  require a verification command that is safe to run concurrently.
- The GitHub Action gained a `jobs` input, mirroring `--jobs`.
- Per-ecosystem Docker image variants: `go`, `rust`, and `python` (with uv)
  tags alongside the default JavaScript image (`latest`), each bundling `git`
  plus that ecosystem's toolchain.
- The repository dogfoods its own GitHub Action: a `self-bisect` workflow
  bisects any dependency-update PR that breaks `go test` and posts the report
  to the job summary.
- README and docs/how-it-works.md document the practical `--jobs` ceiling
  (roughly 4–8) and why returns diminish beyond it.
- Shell completion for `--base` and `--to` now suggests git refs (branches,
  tags, remotes) from the repository being bisected, honoring an earlier
  `--repo <path>` on the command line. Both bash and zsh.
- Homebrew installs now bundle bash and zsh completions automatically,
  generated from the installed binary — no shell-profile editing needed.

### Changed

- Verbose trial detail now says when verification stopped early at the first
  passing run (e.g. `failed 0/1 runs (stopped at first pass; 3 planned)`), so
  a shortened run count is not mistaken for a full flakiness sample.
- The live ddmin row sizes itself to the terminal, dropping trailing fields
  on narrow terminals instead of wrapping.
- Classic-style (and redirected/CI) output aligns its columns like C/Rust
  tooling: trial numbers and change counters pad to a stable width, and
  change lists render as aligned name/version columns instead of bullets.
- The Docker runtime image moved from Node 20 (end-of-life April 2026) to
  Node 22 LTS.

### Fixed

- A failed checkpoint write (disk full, file removed mid-run) no longer
  aborts the bisection: checkpointing is disabled with a diagnostic and the
  run completes normally. A checkpoint missing trials still resumes safely.
- npm package metadata: the README mascot image now uses an absolute URL
  (npmjs.com cannot resolve repo-relative paths) and the package description
  reflects all supported ecosystems, not just npm.

## [0.1.3] - 2026-07-01

### Added

- Guided `--base` recovery: omitting `--base` (or the `--` separator) now
  lists recent dependency-changing commits to pick from and prints a
  copy-pasteable corrected command; common git failures (not a repository, no
  commits, unknown revision) get actionable hints.
- Windows end-to-end smoke coverage in CI; all manifest/lockfile parsers are
  fuzzed in CI.

### Changed

- npm publishing switched to Trusted Publishing (OIDC) with provenance — no
  registry token in CI.
- README overhaul (mascot, tightened hook and comparisons) and re-rendered
  demo GIFs.

## [0.1.2] - 2026-06-25

### Added

- Rust / Cargo support: bisect direct `Cargo.toml` dependency changes, with
  `cargo fetch` candidate installs and resolved-version annotations from
  `Cargo.lock`. Auto-detected from `Cargo.toml`, or select with `--pm cargo`.
- Go modules support: bisect direct `go.mod` `require` changes, parsed and
  rendered with `golang.org/x/mod/modfile` so candidates preserve formatting,
  with `go mod download all` candidate installs (`GOFLAGS=-mod=mod`) and
  resolved-version annotations from `go.sum`. Auto-detected from `go.mod`, or
  select with `--pm go`; indirect and `replace`d requires are skipped, and
  `go.work` workspaces are unsupported. DepBisect is now multi-ecosystem
  (npm, pnpm, cargo, go, uv) behind one engine.
- Python (uv) support: bisect direct `pyproject.toml` `[project.dependencies]`
  (PEP 621) changes, with `uv lock` candidate installs and resolved-version
  annotations from `uv.lock`. Auto-detected from a `pyproject.toml` with a
  `uv.lock`, or select with `--pm uv`; verify with `uv run -- <cmd>`. Only the
  `[project.dependencies]` array is bisected (optional-dependencies and
  dependency groups are not), and other Python tools (Poetry, PDM) are not yet
  recognized.
- `--jobs` / `-j` to evaluate candidate trials in parallel, each in its own
  isolated worktree. The minimized set is identical at any job count; the
  verification command must be safe to run concurrently. Each ddmin
  granularity level is now evaluated as a batch, so a pool of worktrees can
  test candidates concurrently while preserving 1-minimality.
- Homebrew and Scoop packages, published from releases by goreleaser.

### Changed

- Added a `modern` output style — glyph lifecycle rows (`baseline` /
  `reproduced` / `ddmin`) and a dressed result summary — now the default,
  alongside the original `classic` layout. Select with `--style` or the
  `DEPBISECT_STYLE` environment variable; redirected/CI output stays plain
  either way.
- The minimum supported Go version for `go install` builds is 1.25.

## [0.1.1] - 2026-06-13

### Added

- Windows support, including native CI coverage, npm/pnpm batch-shim handling,
  prompt subprocess cancellation, and amd64/arm64 release archives.
- `--quiet` to suppress progress while retaining the final result.
- `--install-timeout` bounds each dependency install and `--overall-timeout`
  bounds the complete bisection; cleanup keeps a separate budget so temporary
  worktrees are removed even after the overall deadline fires.
- `--checkpoint` / `--resume` persist completed trials to disk and resume an
  interrupted run, validated against the revisions, dependency set, package
  manager and version, command, run count, and timeout settings.

### Changed

- Terminal output now uses aligned status labels, width-aware TTY live
  progress, explicit baseline expectations, readable result metadata, and
  wrapped diagnostics. Real terminal detection avoids sending live redraw
  sequences to character devices such as `/dev/null` or Windows `NUL`. Dry
  runs list the dependency changes they would bisect.

## [0.1.0] - 2026-06-11

### Added

- `depbisect run`: bisect direct `package.json` dependency changes between two
  Git revisions with the ddmin algorithm, in an isolated temporary worktree.
- npm (`package-lock.json` v1–v3) and pnpm (`pnpm-lock.yaml` v5/v6/v9) support.
- `--runs` for flaky-test detection, `--dry-run`, `--keep-worktrees`,
  `--verbose`, `--run-timeout`, `--pm`, `--repo`, `--to`, `--no-reports`.
- Markdown and schema-stable JSON reports (`schemaVersion: 1`).
- Diagnostics for lockfile-only and ambiguous changes; explicit unsupported
  errors for workspaces.
- Meaningful exit codes (0–5), safe Ctrl-C cleanup, bash/zsh completion.
- Reusable composite GitHub Action.
