# How DepBisect works

## Pipeline

1. **Resolve revisions.** `--base` and `--to` (default `HEAD`) are resolved to commits. Uncommitted changes are ignored (and warned about if they touch the manifest or lockfile).
2. **Diff manifests.** The manifest (`package.json`, `Cargo.toml`, `go.mod`, `pyproject.toml`, `composer.json`, or `requirements.txt`) is read at both revisions with a structured parser. Direct dependency changes across the manifest's dependency sections are classified as updated, added, or removed.
3. **Read lockfiles.** `package-lock.json` (v1–v3), `pnpm-lock.yaml` (v5/v6/v9), `yarn.lock` (classic v1 and Berry), `Cargo.lock`, `go.sum`, `uv.lock`, or `composer.lock` supplies exact resolved versions for display, and exposes *lockfile-only* changes — dependencies whose spec is unchanged but whose resolution moved. Each such change is lifted back into a manifest change by pinning its old resolution as an exact version spec (see below), so a plain lockfile refresh is bisectable like any dependency-bump PR; resolutions no exact pin can express (`file:`/`link:` targets, git references, Composer branch versions) stay behind as diagnostics, as does transitive drift. `--no-lockfile-pins` disables the lifting. pip has no separate lockfile; the exact `==` pins in `requirements.txt` play this role, so lockfile-only drift cannot occur there.
4. **Create an isolated worktree.** `git worktree add --detach` checks out `--to` in a private temporary directory. The user's checkout is never touched.
5. **Verify baselines.** With all updates reverted, the command must pass every run; with all updates applied, it must fail every run. Any other combination ends the run with a specific outcome (`fails-without-updates`, `not-reproduced`, or `inconclusive` for flaky behavior) instead of producing a bogus answer.
6. **Delta-debug (ddmin).** Before every uncached trial, the owned worktree is reset to `--to` and all untracked and ignored files are removed. Candidate subsets are then applied by rewriting the manifest (structured edit, never regex), installing with npm/pnpm/yarn/cargo/go/uv/composer/pip, and re-running the command.
7. **Certify minimality.** Every one-change removal from the returned set is tested. The result is called 1-minimal only when all of those configurations resolve and pass. Otherwise the outcome is `inconclusive` with a best-known failing set.
8. **Report.** Results go to the terminal, `depbisect-report.md`, and a schema-stable `depbisect-report.json`. Each completed trial records preparation, installation, verification, and total wall time; the run summary also records cleanup and stamps completion after cleanup finishes.

## Candidate semantics

A candidate is "the `--to` source tree, with dependency specs reverted to their `--base` values for every change *not* in the subset". Reverting an added dependency removes it; reverting a removed one restores it. For a lockfile-only change the spec never differed, so its reverted value is an exact pin of the `--base` resolution instead. Installs run `npm install` / `pnpm install --no-frozen-lockfile` / `yarn install` / `cargo fetch` / `go mod download all` / `uv lock` / `composer update`, so the package manager reconciles the lockfile inside the worktree only. pip candidates first get a fresh worktree-local virtual environment (`python3 -m venv --without-pip .venv`) and then install with the host pip redirected into it (`pip --python … install -r requirements.txt`); the verification command is resolved against that `.venv`, so parallel lanes never share an interpreter.

## Reading the classic trial log

Classic (and redirected) output prints one row per trial event, and two fields encode different things. The leading tag on a *baseline* row — `EXPECTED` or `UNEXPECTED` — compares the outcome against what the bisection needs (the without-updates baseline should PASS, the with-all-updates baseline should FAIL), while the `PASS`/`FAIL` field after the change count is the raw command result itself. Candidate rows carry no expectation, so their leading tag is simply the raw outcome. Tags start each line, so prefix filters like `grep 'UNEXPECTED trial'` work on CI logs.

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

## How lockfile-only changes are bisected

DepBisect bisects the resolver's *inputs* — manifest specs, independent variables that can
be freely recombined — and never its *outputs*: the lockfile is a single coherent solution
to the whole constraint graph, so an arbitrary subset of its entries is usually not a valid
state at all (entries are coupled through their dependents' constraints — a hybrid lockfile
is a graph no resolver would produce).

So when a spec like `^1.2.0` is unchanged but the lockfile resolution moved from `1.2.3` to
`1.9.0`, DepBisect does not splice lockfiles. It lifts the drift back into resolver input: a
synthetic change whose *old* state is an exact pin of the old resolution (`1.2.3`, `=1.2.3`
for Cargo, `==1.2.3` for uv — extras and markers are preserved) and whose *new* state is the
manifest's own untouched spec. Reverting the change makes the package manager re-resolve
that one dependency to its old version; applying it leaves the target manifest byte-identical,
so the checked-out lockfile keeps supplying the new resolution. Every candidate remains a
state the resolver itself produces, and the baseline checks still gate the result: with all
pins applied the manifest *is* the `--to` manifest, and with all pins reverted every drifted
direct dependency sits at its `--base` resolution.

Two classes stay out of reach and are reported as diagnostics instead: resolutions no exact
version pin can express (`file:`/`link:` targets, git references, Composer branch versions
like `dev-main`), and purely *transitive* drift — packages that moved in the lockfile without
any direct dependency changing, which no direct-dependency manifest edit can steer one at a
time. Go is exempt by construction (under minimal version selection the `go.mod` spec *is*
the resolution), and pip has no separate lockfile to drift. `--no-lockfile-pins` restores the
old report-only behavior.
