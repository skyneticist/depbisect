# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

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

### Changed

- Added a `modern` output style — glyph lifecycle rows (`baseline` /
  `reproduced` / `ddmin`) and a dressed result summary — now the default,
  alongside the original `classic` layout. Select with `--style` or the
  `DEPBISECT_STYLE` environment variable; redirected/CI output stays plain
  either way.

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
