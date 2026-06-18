# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Windows support, including native CI coverage, npm/pnpm batch-shim handling,
  prompt subprocess cancellation, and amd64/arm64 release archives.
- `--quiet` to suppress progress while retaining the final result.
- `--jobs` / `-j` to evaluate candidate trials in parallel, each in its own
  isolated worktree. The minimized set is identical at any job count; the
  verification command must be safe to run concurrently. Each ddmin
  granularity level is now evaluated as a batch, so a pool of worktrees can
  test candidates concurrently while preserving 1-minimality.

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
  `--verbose`, `--run-timeout`, `--pm`, `--repo`, `--to`.
- Markdown and schema-stable JSON reports (`schemaVersion: 1`).
- Diagnostics for lockfile-only and ambiguous changes; explicit unsupported
  errors for workspaces.
- Meaningful exit codes (0–5), safe Ctrl-C cleanup, bash/zsh completion.
- Reusable composite GitHub Action.
