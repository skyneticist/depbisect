# How DepBisect works

## Pipeline

1. **Resolve revisions.** `--base` and `--to` (default `HEAD`) are resolved to commits. Uncommitted changes are ignored (and warned about if they touch the manifest or lockfile).
2. **Diff manifests.** The manifest (`package.json`, `Cargo.toml`, `go.mod`, `pyproject.toml`, or `composer.json`) is read at both revisions with a structured parser. Direct dependency changes across the manifest's dependency sections are classified as updated, added, or removed.
3. **Read lockfiles.** `package-lock.json` (v1–v3), `pnpm-lock.yaml` (v5/v6/v9), `yarn.lock` (classic v1 and Berry), `Cargo.lock`, `go.sum`, `uv.lock`, or `composer.lock` supplies exact resolved versions for display, and exposes *lockfile-only* changes — dependencies whose spec is unchanged but whose resolution moved. These cannot be bisected (see below) and are reported as diagnostics.
4. **Create an isolated worktree.** `git worktree add --detach` checks out `--to` in a private temporary directory. The user's checkout is never touched.
5. **Verify baselines.** With all updates reverted, the command must pass every run; with all updates applied, it must fail every run. Any other combination ends the run with a specific outcome (`fails-without-updates`, `not-reproduced`, or `inconclusive` for flaky behavior) instead of producing a bogus answer.
6. **Delta-debug (ddmin).** Before every uncached trial, the owned worktree is reset to `--to` and all untracked and ignored files are removed. Candidate subsets are then applied by rewriting the manifest (structured edit, never regex), installing with npm/pnpm/yarn/cargo/go/uv/composer, and re-running the command.
7. **Certify minimality.** Every one-change removal from the returned set is tested. The result is called 1-minimal only when all of those configurations resolve and pass. Otherwise the outcome is `inconclusive` with a best-known failing set.
8. **Report.** Results go to the terminal, `depbisect-report.md`, and a schema-stable `depbisect-report.json`. Each completed trial records preparation, installation, verification, and total wall time; the run summary also records cleanup and stamps completion after cleanup finishes.

## Candidate semantics

A candidate is "the `--to` source tree, with dependency specs reverted to their `--base` values for every change *not* in the subset". Reverting an added dependency removes it; reverting a removed one restores it. Installs run `npm install` / `pnpm install --no-frozen-lockfile` / `yarn install` / `cargo fetch` / `go mod download all` / `uv lock` / `composer update`, so the package manager reconciles the lockfile inside the worktree only.

## Flakiness handling

`--runs N` executes the command up to N times per candidate:

- A candidate counts as **failing** only if all N runs fail. This strict predicate keeps ddmin's 1-minimality guarantee meaningful.
- During bisection, the first passing run short-circuits the remaining runs (it already refutes "fails every run").
- Mixed pass/fail results are treated as "did not reproduce" and surfaced in diagnostics; flaky baselines abort the run as `inconclusive`.

## Timeouts

`--run-timeout` applies to each verification process, `--install-timeout`
applies to each package-manager install, and `--overall-timeout` applies to
the complete bisection. The first two are independent per-operation budgets;
the overall deadline can interrupt any phase. Worktree cleanup uses a separate
two-minute context so it is still attempted after cancellation or timeout.

## Parallel trials (`--jobs`)

`--jobs N` maintains N isolated worktrees ("lanes") and evaluates each ddmin batch of
candidates across them concurrently. Batch selection always adopts the lowest-indexed
failing candidate, so the minimal set is identical at any job count — parallelism changes
wall-clock time only.

The speedup has two ceilings:

- **Batch width.** ddmin is adaptive: each round's outcomes decide the next round's
  candidates, so only the candidates *within* one batch run concurrently, and every lane
  waits at the batch boundary. Early rounds test just two halves, no batch is ever wider
  than the number of changes still under suspicion, and the closing minimality check is
  one probe per member of the (usually tiny) minimal set. Lanes beyond the current batch
  width sit idle.
- **Machine capacity.** Every lane runs a full dependency install plus the verification
  command, both of which are typically multi-core and I/O-hungry themselves. Once lanes
  saturate the CPU, disk, or registry connection, additional lanes only contend.

There is also a work-versus-time trade: a sequential run stops a batch at its first
failure, while a parallel run evaluates the whole batch. Wall-clock time still drops, but
total compute rises with the job count.

In practice returns diminish past roughly 4–8 jobs for typical dependency batches
(5–30 changes); a reasonable ceiling is `min(number of changes, cores available to spare)`.
The README's 28-change demo illustrates the curve: 12 lanes buy about a 2.8× speedup, not
12×. The default stays `1` because parallel lanes require a verification command that is
safe to run concurrently (no shared ports, files, or databases).

## Determinism

The ddmin core is pure and deterministic; identical test outcomes produce identical bisection paths. Subset outcomes are memoized, so no configuration is installed or verified twice. Completed trials are also appended to a checkpoint. `--resume` validates the revisions, dependency set, package manager and its reported version, command, run count, and all timeout settings before restoring that memoized state.

## Why lockfile-only changes cannot be bisected

DepBisect bisects the resolver's *inputs* — manifest specs, independent variables that can
be freely recombined — and never its *outputs*: the lockfile is a single coherent solution
to the whole constraint graph, so an arbitrary subset of its entries is usually not a valid
state at all (entries are coupled through their dependents' constraints — a hybrid lockfile
is a graph no resolver would produce).

DepBisect materializes candidates by editing version specs in the manifest. If a spec like `^1.2.0` is unchanged but the lockfile resolution moved from `1.2.3` to `1.9.0`, there is no manifest edit that pins the old resolution without also changing the spec — and synthesizing per-candidate lockfiles is package-manager-version-specific and fragile. DepBisect reports these changes explicitly so you can investigate them manually (e.g. by inspecting the lockfile diff or pinning an override).
