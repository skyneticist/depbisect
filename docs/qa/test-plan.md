# DepBisect test plan

A living checklist for pre-release QA. It maps DepBisect's behaviour to concrete
tests across every layer — the ddmin core, manifest parsing/rendering, git
worktree lifecycle, verification, checkpoint/resume, reports, and the CLI — and
records what is covered automatically versus what is exercised by hand before a
release.

Legend: **[auto]** covered by the Go suite or CI · **[exp]** manual/exploratory
· **[gate]** must pass before tagging a release.

## 1. Scope and objectives

DepBisect finds the smallest set of direct dependency changes between two git
revisions that makes a command fail, and proves the set is 1-minimal. The QA
objectives, in priority order:

1. **Safety** — never mutate the user's working tree; clean up all worktrees.
2. **Correctness** — the reported minimal set is a true 1-minimal failing set.
3. **Contract stability** — exit codes and JSON report schema are stable.
4. **Robustness** — malformed input and mid-run interruption degrade to clear
   diagnostics, never a crash or a wrong answer.
5. **Portability** — identical behaviour on Linux, macOS, and Windows.

## 2. Core algorithm and correctness

- [auto] `ddmin` 1-minimality and ordering invariants (unit + `FuzzMinimize`).
- [auto] Manifest parsers for every ecosystem (12 fuzz targets).
- [exp] Randomised property test: for a generated change-set with a known
  interacting culprit subset, assert the reported `minimalSet` is exactly that
  subset and `minimalityProven` is true. (Demos cover fixed fixtures.)
- [exp] Memoisation: no dependency subset is installed/tested twice; the
  bisection path is identical across repeated runs of the same input.

## 3. Safety invariants (highest stakes)

- [gate][exp] **Working tree untouched**: capture `git status` + a content hash
  of the source repo before and after a full run; assert byte-identical.
- [gate][exp] **Destructive git only in temp worktrees**: `reset --hard` /
  `clean -ffdx` never run in the user's checkout (verify via a wrapping git or
  by confirming an untracked scratch file in the checkout survives a run).
- [exp] **Interrupt cleanliness**: SIGINT during install/verify leaves no
  orphaned worktrees (`git worktree list` is clean) and a valid resumable
  checkpoint.
- [auto] Concurrent worktrees under `--jobs` (Windows file-locking path smoke-
  tested by the `windows-smoke` CI job; pip's per-worktree `.venv` lanes by
  `windows-pip-smoke`).

## 4. Verification and the exit-code contract

The exit code is a public contract — one table-driven end-to-end assertion per
code:

| Code | Meaning | Trigger |
| ---- | ------- | ------- |
| 0 | minimal set found / dry-run / informational OK | culprit reproduced |
| 1 | usage or runtime error | bad flags, bad rev, bad `--pm`, missing repo, command not found |
| 2 | failure did not reproduce | command passes with all updates applied |
| 3 | fails even with all updates reverted | breakage predates the range |
| 4 | inconclusive | verification too flaky to bisect |
| 5 | no direct dependency changes | nothing to bisect in the range |

Values are defined in `internal/cli/cli.go` (`Exit*` constants) and mapped by
`exitCodeFor`; the mapping is unit-tested by `TestExitCodeMapping`.

- [gate][exp] Assert codes 0/1/2 end-to-end (done in the adversarial matrix);
  3/4/5 are pinned by the unit test.
- [auto] Command run **verbatim, no shell**: quotes/`$VARS`/globs passed
  literally; `.bat`/`.cmd` rejected on Windows.
- [exp] Verification timeout (`InstallTimeout`/`OverallTimeout`) fires and is
  reported, not hung.

## 5. Ecosystem matrix

Ecosystems × scenarios. Simple/complex/jobs/swarm are [auto] via `make demos`.

| | simple | complex | --jobs | swarm |
| --- | --- | --- | --- | --- |
| npm | auto | auto | auto | auto |
| pnpm | auto | — | — | — |
| yarn | auto | — | — | — |
| cargo | auto | auto | auto | auto |
| go | auto | auto | auto | auto |
| uv | auto | — | — | — |
| composer | auto | — | — | — |
| pip | auto | — | auto* | — |

\* pip's `--jobs` cell is the simple demo re-run with `--jobs 4` (two changes,
so two concurrent lanes), not a dedicated many-package jobs repo: it exists to
exercise concurrent per-worktree `.venv` creation and the venv verify bridge
under parallel lanes, which no other job covers.

- [exp] Auto-detection precedence when multiple manifests coexist; explicit
  `--pm` override wins.
- [exp] Workspace/monorepo manifest → surfaced as an inconclusive diagnostic.

## 6. Flaky handling and determinism

- [auto] `--runs N` counts a candidate as failing only if all N runs fail.
- [exp] A command failing k/N times (0<k<N) is reported as a diagnostic, never
  guessed.
- [exp] Determinism: identical inputs → identical trial log and minimal set.

## 7. Checkpoint / resume

- [auto] Append-only round-trip, crash-truncated tail ignored, corrupt interior
  rejected, permissions 0600, concurrent appends (unit).
- [exp] Interrupt a real run; `--resume` restores completed trials and finishes
  with the same result as an uninterrupted run.
- [exp] Resume with a **mismatched fingerprint** (different base/command/runs) is
  refused with a clear message, not silently resumed.
- [exp] Corrupt/foreign checkpoint file → clear error, no crash.

## 8. Adversarial / edge inputs (the "utmost scrutiny" set) — [exp]

Run each against the built binary; expect a clear diagnostic and a documented
exit code, never a panic or a wrong culprit.

- Empty diff (base == HEAD) → no-dependency-changes.
- No manifest in the repo.
- Manifest present, no dependency section.
- Lockfile-only change (spec identical, resolution differs).
- Single dependency changed.
- Hundreds of dependencies (performance + memory).
- Duplicate / extras / URL / git dependency specs.
- Unicode package names; CRLF line endings in the manifest.
- `--base` that does not exist / is not an ancestor of HEAD.
- Detached HEAD; dirty working tree.
- Checkpoint file corrupted / truncated / from a different repo.
- Read-only checkpoint directory / disk full.
- Verification command that hangs (timeout path); command not found.

## 9. Reports

- [auto] Schema-stable JSON and Markdown.
- [gate][exp] Validate JSON for every outcome type (found / not-reproduced /
  inconclusive / no-changes / dry-run); Markdown renders for zero-culprit and
  multi-culprit sets.

## 10. Distribution — [gate][exp]

Smoke every install path on a clean machine/container before tagging:

- `npm i -g depbisect` / `npx depbisect`
- `brew install skyneticist/tap/depbisect`
- `scoop install depbisect` (Windows)
- `install.sh` (checksum verified)
- `go install .../cmd/depbisect@latest`
- Docker images (`ghcr.io/...`) — default js variant (git/node/npm/pnpm/yarn) plus the
  `go`, `rust`, `python` (uv + pip), and `php` (composer) variants
  [auto in CI: `docker-smoke` + `docker-variants`].

## 11. Release gate

Tag only when all [gate] rows pass **and**:

- `make ci` green locally; CI green on all three OSes incl. `windows-smoke`
  and `windows-pip-smoke`.
- `make demos` green (all ecosystems).
- `govulncheck` clean; coverage floor met.
- README hero renders on GitHub; links resolve.
- `CHANGELOG`/release notes drafted.

## 12. How to run

```sh
make ci        # fmt, vet, lint, test, race, cover, vuln, fuzz smoke
make demos     # end-to-end bisection across every ecosystem
make docker-smoke           # default js image: toolchain + real bisection
make docker-smoke-variants  # go / rust / python / php images: toolchain checks
```

Exploratory [exp] items are run by hand against `make build` output; findings
are recorded in the release checklist / issue tracker.
