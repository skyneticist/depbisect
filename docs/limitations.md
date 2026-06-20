# Limitations

## Not supported (DepBisect refuses with a clear error)

- **Workspaces.** npm/yarn `workspaces` in `package.json`, `pnpm-workspace.yaml` repositories, and Cargo `[workspace]` manifests (including virtual manifests).
- **Yarn.** For JavaScript, only npm and pnpm lockfiles are recognized.
- **Missing JavaScript lockfile.** For npm/pnpm a `package-lock.json` or `pnpm-lock.yaml` must exist at `--to` (or pass `--pm` explicitly). Cargo needs no lockfile — `Cargo.lock` is optional and only enriches diagnostics.
- **Ambiguous detection.** If both a `package.json` and a `Cargo.toml` exist, or both JavaScript lockfiles exist, choose with `--pm npm|pnpm|cargo`.

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
- **Cargo without a lockfile.** `Cargo.lock` is optional; without it, resolved-version annotations and lockfile-only diagnostics are unavailable, but bisection still works (candidates resolve via `cargo fetch`).
- **Cargo non-versioned dependencies.** `git`, `path`, and workspace-inherited (`foo.workspace = true`) dependencies carry no version requirement and are not bisected.
- **Cargo candidate formatting.** Candidate `Cargo.toml` files are re-serialized, so comments and original formatting are not preserved (visible only under `--keep-worktrees`). Reverting a *removed* table-form dependency restores its version but not sibling keys such as `features`.

## Interpreting non-zero exits

| Exit | Outcome | What it means |
|---|---|---|
| 2 | `not-reproduced` | The command passed with all updates applied in a clean worktree. Suspect environment differences, lockfile-only changes, or staleness. |
| 3 | `fails-without-updates` | The command fails at `--to` even with all dependency updates reverted: the cause is your code or a lockfile-only change, not a direct dependency bump. |
| 4 | `inconclusive` | A baseline is flaky, or a best-known failing set could not be proven 1-minimal because a required neighboring candidate was unresolved or flaky. |
| 5 | `no-dependency-changes` | The manifests are identical; nothing to bisect. |
