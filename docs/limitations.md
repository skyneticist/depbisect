# Limitations

## Not supported (DepBisect refuses with a clear error)

- **Workspaces.** npm/yarn `workspaces` in `package.json`, `pnpm-workspace.yaml` repositories, Cargo `[workspace]` manifests (including virtual manifests), Go `go.work` workspaces, and uv `[tool.uv.workspace]` workspaces.
- **Missing JavaScript lockfile.** For npm/pnpm/yarn a `package-lock.json`, `pnpm-lock.yaml`, or `yarn.lock` must exist at `--to` (or pass `--pm` explicitly). Cargo needs no lockfile — `Cargo.lock` is optional and only enriches diagnostics.
- **Ambiguous detection.** If multiple manifests exist (for example a `package.json` and a `Cargo.toml`), or more than one JavaScript lockfile exists, choose with `--pm npm|pnpm|yarn|cargo|go|uv|composer|pip`. Exception: `pyproject.toml` and `requirements.txt` together are not ambiguous — they routinely coexist in one Python project, so a `uv.lock` selects uv and otherwise the `requirements.txt` selects pip.
- **pip hash-checking mode.** A `requirements.txt` using `--hash` pins is rejected up front: any hash puts pip's whole install into hash-checking mode, and a candidate that rewrites a version without its hash could never install. Bisect from a hash-free export instead (e.g. `pip-compile` without `--generate-hashes`).

## Supported but with caveats (reported as diagnostics, never silent)

- **Lockfile-only changes.** Dependencies whose `package.json` spec is unchanged but whose resolved version moved are listed in reports and diagnostics, not bisected. See [how-it-works.md](how-it-works.md).
- **Transitive dependencies.** Candidate installs resolve transitive deps fresh from the registry; if the registry state changed since the lockfile was written, a candidate's transitive tree may differ from what either revision originally installed.
- **Registry access.** Candidate installation requires network access to your configured registry. (DepBisect's own test suite does not.)
- **`peerDependencies`.** Changes there are not diffed or bisected.
- **Uncommitted changes.** DepBisect compares committed revisions only; dirty `package.json`/lockfile files trigger a warning.
- **Flaky tests.** With `--runs 1` a flaky failure can be misattributed. Use `--runs 3` or more; mixed results are detected and reported. If a required minimality check is flaky, the result is an inconclusive best-known set.
- **Concurrent checkpoints.** Do not run multiple DepBisect processes with the same `--checkpoint` path. Choose a distinct path for each concurrent run.
- **Windows batch verification commands.** Implicit `.bat` and `.cmd`
  execution is rejected because `cmd.exe` does not preserve arbitrary argument
  vectors safely. Invoke `cmd.exe /d /s /c` explicitly when shell semantics
  are intended. DepBisect handles npm/pnpm's fixed install arguments through a
  restricted internal batch-command path.
- **Process descendants on cancellation.** DepBisect stops the direct
  subprocess on timeout or interruption. Programs that detach descendants may
  leave those independent processes running.
- **Cleanup after timeout.** Git worktree removal gets a fresh two-minute
  context after interruption or `--overall-timeout`. Standard-library
  filesystem deletion is synchronous and cannot be canceled, so a stalled
  filesystem can extend process runtime beyond the overall timeout.
- **Version spec edge cases.** pnpm v5 lockfile versions with both build metadata and a peer suffix (e.g. `1.2.3+build_meta`) may display a truncated resolved version. Display-only; does not affect bisection.
- **Yarn multi-version resolutions.** `yarn.lock` is keyed by version range, so one package can resolve to several versions at once when transitive ranges conflict. Such packages have no single resolved version: their annotations are left blank and lockfile-only diagnostics skip them. Display-only; does not affect bisection.
- **Cargo without a lockfile.** `Cargo.lock` is optional; without it, resolved-version annotations and lockfile-only diagnostics are unavailable, but bisection still works (candidates resolve via `cargo fetch`).
- **Cargo non-versioned dependencies.** `git`, `path`, and workspace-inherited (`foo.workspace = true`) dependencies carry no version requirement and are not bisected.
- **Cargo candidate formatting.** Candidate `Cargo.toml` files are re-serialized, so comments and original formatting are not preserved (visible only under `--keep-worktrees`). Reverting a *removed* table-form dependency restores its version but not sibling keys such as `features`.
- **Python beyond uv and pip.** uv projects are detected by a `uv.lock` beside `pyproject.toml`, pip projects by a `requirements.txt`; Poetry, PDM, and other resolvers are not recognized (pass `--pm uv` only for uv projects). For uv, only the PEP 621 `[project.dependencies]` array is bisected — optional-dependencies (extras) and dependency groups are not — and direct-reference requirements (`name @ url`) carry no bisectable version, so they are skipped.
- **pip scope.** Only plain requirement lines of the top-level `requirements.txt` are bisected. `-r`/`-c` includes are preserved verbatim but **not followed** — a change inside an included file is invisible and yields "no dependency changes". Editables (`-e`), direct references (`name @ url`), bare URL/path requirements, option lines (`--index-url`, ...), and requirements carrying per-requirement options are likewise preserved but not bisected. pip has no separate lockfile: exact `==`/`===` pins double as the resolved versions (ranges like `>=` are still bisected as spec changes, but resolve at install time, so what a range installed at `--base` back then is not reproducible — pinned files are the reliable input). Rewritten candidate lines drop trailing comments and continuation layout (visible only under `--keep-worktrees`).
- **pip trial environments.** Every trial creates a `.venv` inside its worktree (`python3 -m venv --without-pip`) and installs into it with the host pip via `--python`, which needs pip ≥ 22.3 and a `python3`/`python` on `PATH`. The verification command is resolved against that `.venv` first — plain `python` or `pytest` just works, but the test runner must therefore be listed in `requirements.txt`. A command not found in the venv falls back to normal `PATH` lookup with `VIRTUAL_ENV` and `PATH` still pointing at the venv.
- **Composer scope.** Only the `require` and `require-dev` sections of `composer.json` are bisected; per-package `repositories`, `provide`/`replace`/`conflict`, and platform overrides are not. Platform requirements (`php`, `ext-*`, `lib-*`) are diffed like any other constraint but have no `composer.lock` entry, so they carry no resolved version. Candidates install via `composer update` (which re-resolves from the manifest and rewrites `composer.lock`), not `composer install` (which would ignore the reverted manifest and reinstall the locked versions). `composer.lock` is optional and only enriches diagnostics; candidate `composer.json` files are re-serialized, so comments and key order are not preserved (visible only under `--keep-worktrees`).

## Interpreting non-zero exits

| Exit | Outcome | What it means |
|---|---|---|
| 2 | `not-reproduced` | The command passed with all updates applied in a clean worktree. Suspect environment differences, lockfile-only changes, or staleness. |
| 3 | `fails-without-updates` | The command fails at `--to` even with all dependency updates reverted: the cause is your code or a lockfile-only change, not a direct dependency bump. |
| 4 | `inconclusive` | A baseline is flaky, or a best-known failing set could not be proven 1-minimal because a required neighboring candidate was unresolved or flaky. |
| 5 | `no-dependency-changes` | The manifests are identical; nothing to bisect. |
