# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

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
