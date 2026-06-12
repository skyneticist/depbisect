# Limitations

## Not supported (DepBisect refuses with a clear error)

- **Workspaces.** npm/yarn `workspaces` in `package.json` and `pnpm-workspace.yaml` repositories.
- **Yarn.** Only npm and pnpm lockfiles are recognized.
- **Missing lockfile.** A `package-lock.json` or `pnpm-lock.yaml` must exist at `--to` (or pass `--pm` explicitly to proceed without resolution info).
- **Ambiguous lockfiles.** If both lockfiles exist, choose with `--pm npm` or `--pm pnpm`.
- **Windows.** The codebase avoids Unix-isms where practical, but Windows is untested and unsupported in this release.

## Supported but with caveats (reported as diagnostics, never silent)

- **Lockfile-only changes.** Dependencies whose `package.json` spec is unchanged but whose resolved version moved are listed in reports and diagnostics, not bisected. See [how-it-works.md](how-it-works.md).
- **Transitive dependencies.** Candidate installs resolve transitive deps fresh from the registry; if the registry state changed since the lockfile was written, a candidate's transitive tree may differ from what either revision originally installed.
- **Registry access.** Candidate installation requires network access to your configured registry. (DepBisect's own test suite does not.)
- **`peerDependencies`.** Changes there are not diffed or bisected.
- **Uncommitted changes.** DepBisect compares committed revisions only; dirty `package.json`/lockfile files trigger a warning.
- **Flaky tests.** With `--runs 1` a flaky failure can be misattributed. Use `--runs 3` or more; mixed results are detected and reported.
- **Version spec edge cases.** pnpm v5 lockfile versions with both build metadata and a peer suffix (e.g. `1.2.3+build_meta`) may display a truncated resolved version. Display-only; does not affect bisection.

## Interpreting non-zero exits

| Exit | Outcome | What it means |
|---|---|---|
| 2 | `not-reproduced` | The command passed with all updates applied in a clean worktree. Suspect environment differences, lockfile-only changes, or staleness. |
| 3 | `fails-without-updates` | The command fails at `--to` even with all dependency updates reverted: the cause is your code or a lockfile-only change, not a direct dependency bump. |
| 4 | `inconclusive` | The command is flaky even at a fixed configuration. Increase `--runs` or stabilize the test. |
| 5 | `no-dependency-changes` | The manifests are identical; nothing to bisect. |
