# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Lockfile-only bisection: dependencies whose manifest spec is unchanged but
  whose lockfile resolution moved between the revisions (a plain `uv lock -U`,
  `composer update`, or `npm update` refresh) are now bisected instead of only
  reported. Each drifted dependency becomes a synthetic change whose reverted
  state pins the old resolution as an exact version spec (`1.2.3`, `=1.2.3`
  for Cargo, `==1.2.3` for uv/PEP 508 with extras and markers preserved,
  verbatim lock versions for Composer); applying it leaves the target manifest
  untouched so the checked-out lockfile keeps supplying the new resolution.
  Supported for npm/pnpm/yarn, Cargo, uv, and Composer; Go is exempt by
  construction (MVS specs are the resolution) and pip has no separate
  lockfile. Pinned changes carry a `(lockfile-only)` marker in terminal and
  Markdown output and a `lockfileOnly` field in the JSON report; `depbisect
  run --no-lockfile-pins` restores the previous report-only behavior.
  Resolutions no exact pin can express (`file:`/`link:` targets, git
  references, Composer branch versions) remain diagnostics, and purely
  transitive resolution drift is now counted and named in a diagnostic of its
  own. A hermetic uv demo (`examples/make-demo-python-lockonly.sh`, `make
  demo-python-lockonly`) exercises the path in CI.
- A `next` line in the run summary: when a minimal set is found, DepBisect now
  suggests the copy-paste command that holds each culprit at its last good
  version (`npm install --save-exact lodash@4.17.21`, `cargo add serde@=1.0.100`,
  `uv add numpy==1.26.4`, `composer require acme/lib:v2.9.1`, `go get
  mod@v1.2.3`, …), with exact section flags (`--save-dev`, `--dev`, `--build`)
  and removal commands for culprits whose *addition* broke the build. The
  suggestion is omitted entirely when any culprit's last good version is not
  exactly known — partial advice would mislead.
- `next` suggestions for the outcomes where the bisection could not convict
  anything: `not-reproduced` points at environment differences, `fails-without-
  updates` at bisecting the code itself, and `inconclusive` distinguishes
  flaky verification (raise `--runs`) from failed candidate installs (rerun
  with `--verbose`) using the run's own trial evidence.
- Multi-change verdicts now state what the minimality proof established:
  "these N changes break only in combination — removing any one of them makes
  the failure disappear."
- Failure evidence in the verdict: the run summary now shows the tail of the
  failing command's output from the trial that convicted the minimal set (or,
  for `fails-without-updates`, from the failing all-reverted baseline) — the
  symptom behind the verdict, without rerunning anything. The excerpt is
  ANSI-stripped and capped at three lines in the terminal; the full captured
  tail is stored per failing trial in the checkpoint and surfaced as
  `failureExcerpt` in the JSON report and a "Failure evidence" section in the
  Markdown report.
- Registry links for convicted culprits: one line per culprit pointing at the
  registry page of the breaking version (npmjs.com, crates.io, pkg.go.dev,
  PyPI, Packagist) — one click from its changelog. Culprits whose resolution
  is not a registry version (`file:`, git references) are skipped.

### Changed

- Human run summaries no longer repeat the machine outcome token (`outcome
  minimal-set-found`) below the result headline; scripts should use the exit
  code or the JSON report, which are unchanged.
- Diagnostics in the modern style render as their own `⚠ diagnostics` block
  with one bullet per item at full contrast, instead of dim `note` rows in the
  fact column. Version pairs in diagnostics are now unspaced tokens
  (`1.0.0->2.0.0`, `→` in modern output) so wrapping can never split a pair
  across lines.
- Summary footers merge the trial and change counts into one line
  (`trials 3 across 2 changes`), and the live reproduction row is labeled
  `reproduce` while running, flipping to `reproduced` only once the verdict
  lands.
- Modern styling now tracks certainty: warning outcomes get a `!` headline
  glyph instead of recycling the in-progress spinner, dry-run listings use a
  neutral bullet and inconclusive best-known sets yellow marks (red is
  reserved for the certified minimal set), long footer facts wrap with a
  hanging indent, the manager row dims its build provenance, and the
  `(lockfile-only)` tag carries a subtle tint so it reads as a category.
- The modern summary's divider now stretches to the widest fact row (never
  narrower than before, capped at the terminal width), so it underlines the
  column instead of stopping short of it.
- Failure evidence is tidied for display: temporary-worktree paths are
  rewritten to repo-relative form at capture time (a stack frame reads
  `test.js:5:11`, not a temp path that no longer exists), a tiny fixed set of
  runner epilogues (npm's "complete log" pointer and its log path, Node's
  version trailer, yarn's help link) is filtered from the terminal preview,
  and each evidence line is truncated to one row instead of wrapping. Reports
  keep the full captured tail.
- `next` commands are never hard-wrapped: classic prints them on one physical
  line, and the modern grid gives an over-wide command its own full-width
  line under a bare label — selecting that line copies the exact command.
- Failure evidence keeps both output streams (stdout first, stderr last):
  runners disagree about where failures go — Rust's libtest reports on stdout
  while cargo's rerun epilogue is stderr — so cargo verdicts now show the
  failed test instead of the generic "to rerun pass" hint, whose epilogue
  (and libtest's backtrace note) joined the filtered boilerplate.
- The transitive-drift diagnostic is calibrated to what the bisection can
  actually miss: with candidates in play it notes that reverting a candidate
  also reverts the transitives it pulls in, reserving "results may be
  incomplete" for drift with no direct changes to ride on.

- The npm and yarn demo generators now place their offline packages inside
  the generated app repository with relative `file:./pkgs/...` specifiers,
  matching the pnpm demo. Generated fixtures are machine-portable: they work
  from git worktrees, other checkouts, and mounted into the Docker image,
  where the previous absolute host paths could not resolve.

### Fixed

- `package-lock.json` version annotations follow npm's link chain (the
  `node_modules/` entry to its linked directory target), so a lockfile that
  lists several same-named `file:` package directories — as the regenerated
  demos now do — annotates the version actually referenced instead of an
  arbitrary one.

## [0.1.5] - 2026-07-07

### Added

- Yarn support: `yarn.lock` is auto-detected alongside the other JavaScript
  lockfiles — both the classic v1 format and the Berry (v2+) YAML format are
  parsed — or forced with `--pm yarn`. Installs run `yarn install` with
  Berry's automatic CI immutable-installs mode disabled, since candidate
  manifests must be allowed to update the lockfile. Yarn workspaces remain
  unsupported, matching npm/pnpm.
- PHP (Composer) support: bisect direct `composer.json` `require` and
  `require-dev` changes, with `composer update` candidate installs and
  resolved-version annotations from `composer.lock` (optional — it only
  enriches diagnostics). Auto-detected from `composer.json`, or select with
  `--pm composer`.
- Python (pip) support: bisect the requirement lines of `requirements.txt`,
  which doubles as the lockfile — exact `==` pins are read as the resolved
  versions, so pinned files (`pip-compile` or `pip freeze` output) work best.
  Each trial installs into its own worktree-local `.venv` using the host pip
  (≥ 22.3, via `pip --python`), and plain `python`/`pytest` verification
  commands are resolved inside that venv automatically. Auto-detected from
  `requirements.txt`, or select with `--pm pip`; files using `--hash` pins
  are rejected up front.
- The live `ddmin` progress row now animates a smooth scanner sweep (a bright
  head with a fading tail), and its tested count and elapsed time update
  continuously between trial events instead of freezing during long installs.
  The sweep speeds up with `--jobs` — a visual echo of trial throughput.
  Interactive color terminals only; redirected/CI output is unchanged.
- `DEPBISECT_JOBS` environment variable sets the default for `--jobs`
  (the flag still wins). The built-in default stays `1`: parallel trials
  require a verification command that is safe to run concurrently.
- The GitHub Action gained a `jobs` input, mirroring `--jobs`.
- Per-ecosystem Docker image variants: `go`, `rust`, `python` (with uv and
  pip), and `php` (with composer) tags alongside the default JavaScript image
  (`latest`), each bundling `git` plus that ecosystem's toolchain.
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
- End-to-end pnpm coverage: an offline pnpm example repo (`make demo-pnpm`,
  `examples/make-demo-pnpm.sh`), a matching CI smoke job, and an engine-level
  pnpm test — every supported package manager now completes a real-binary
  bisection in CI. The example commits its `file:` packages inside the repo
  because pnpm stores lockfile-relative paths.
- The pip demo re-runs under `--jobs` (in `make demo-pip` and CI) to exercise
  concurrent per-worktree virtual environments, and a dedicated
  `windows-pip-smoke` CI job covers pip's Windows-only code paths (`Scripts/`
  venv layout, `.exe` resolution) on a real Windows runner. The pip demo
  generator now falls back to `python -m zipfile` when `zip` is unavailable
  (notably Git Bash on Windows).
- Demo GIFs for every remaining ecosystem — pnpm, yarn, Python (uv and pip),
  and PHP (composer) — embedded in the examples guide.
- Parser benchmarks now cover all ecosystems (pnpm/yarn/cargo/go/uv/composer
  /pip lockfiles and manifests), not just the JavaScript parsers.

### Changed

- Verbose trial detail now says when verification stopped early at the first
  passing run (e.g. `failed 0/1 runs (stopped at first pass; 3 planned)`), so
  a shortened run count is not mistaken for a full flakiness sample.
- The live ddmin row sizes itself to the terminal, dropping trailing fields
  on narrow terminals instead of wrapping.
- Classic-style (and redirected/CI) output aligns its columns like C/Rust
  tooling: trial numbers and change counters pad to a stable width, and
  change lists render as aligned name/version columns instead of bullets.
- The default (JavaScript) Docker image moved from Node 20 (end-of-life
  April 2026) to Node 26. Since corepack is no longer bundled with Node,
  the image's pinned pnpm and yarn are now installed directly via npm.

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
