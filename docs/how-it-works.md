# How DepBisect works

## Pipeline

1. **Resolve revisions.** `--base` and `--to` (default `HEAD`) are resolved to commits. Uncommitted changes are ignored (and warned about if they touch `package.json` or the lockfile).
2. **Diff manifests.** `package.json` is read at both revisions with a structured JSON parser. Direct dependency changes in `dependencies`, `devDependencies`, and `optionalDependencies` are classified as updated, added, or removed.
3. **Read lockfiles.** `package-lock.json` (v1–v3) or `pnpm-lock.yaml` (v5/v6/v9) supplies exact resolved versions for display, and exposes *lockfile-only* changes — dependencies whose spec is unchanged but whose resolution moved. These cannot be bisected (see below) and are reported as diagnostics.
4. **Create an isolated worktree.** `git worktree add --detach` checks out `--to` in a private temporary directory. The user's checkout is never touched.
5. **Verify baselines.** With all updates reverted, the command must pass every run; with all updates applied, it must fail every run. Any other combination ends the run with a specific outcome (`fails-without-updates`, `not-reproduced`, or `inconclusive` for flaky behavior) instead of producing a bogus answer.
6. **Delta-debug (ddmin).** Candidate subsets of the updates are applied by rewriting the worktree's `package.json` (structured edit, never regex), installing with npm/pnpm, and re-running the command. ddmin shrinks the failing set until it is 1-minimal: removing any single element makes the command pass.
7. **Report.** Results go to the terminal, `depbisect-report.md`, and a schema-stable `depbisect-report.json`.

## Candidate semantics

A candidate is "the `--to` source tree, with dependency specs reverted to their `--base` values for every change *not* in the subset". Reverting an added dependency removes it; reverting a removed one restores it. Installs run `npm install` / `pnpm install --no-frozen-lockfile`, so the package manager reconciles the lockfile inside the worktree only.

## Flakiness handling

`--runs N` executes the command up to N times per candidate:

- A candidate counts as **failing** only if all N runs fail. This strict predicate keeps ddmin's 1-minimality guarantee meaningful.
- During bisection, the first passing run short-circuits the remaining runs (it already refutes "fails every run").
- Mixed pass/fail results are treated as "did not reproduce" and surfaced in diagnostics; flaky baselines abort the run as `inconclusive`.

## Determinism

The ddmin core is pure and deterministic; identical test outcomes produce identical bisection paths. Subset outcomes are memoized, so no configuration is installed or verified twice.

## Why lockfile-only changes cannot be bisected

DepBisect materializes candidates by editing version specs in `package.json`. If a spec like `^1.2.0` is unchanged but the lockfile resolution moved from `1.2.3` to `1.9.0`, there is no manifest edit that pins the old resolution without also changing the spec — and synthesizing per-candidate lockfiles is package-manager-version-specific and fragile. DepBisect reports these changes explicitly so you can investigate them manually (e.g. with `npm ls` or pinned overrides).
