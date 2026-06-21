# 🧶 DepBisect — Yarn Expansion & Polish · Prompt Pack

A copy-paste prompt for every item on the roadmap. Each one is **self-contained**:
hand it to Claude Code on a fresh branch and it has the file pointers, the
gotchas, and a "done-when" bar — no extra context required.

> [!NOTE]
> Prompts live in fenced blocks so you can lift them cleanly. The **callouts
> around** each prompt are for *you* — decisions to make, traps to avoid, and the
> acceptance bar. Line numbers are `≈` hints (symbols are the source of truth;
> code drifts).

### Legend

| Marker | Meaning |
| --- | --- |
| 🎯 **Done when** | Acceptance bar — the prompt isn't finished until this holds |
| 🔀 **Decision** | A real fork in the road; the prompt asks you to choose (recommendation given) |
| ⚠️ **Trap** | A known way to get this wrong |
| 🧭 **Seam** | Where the existing code already gives you a place to plug in |

### Shared invariants (every prompt assumes these)

> [!IMPORTANT]
> - **Strict-fail ddmin predicate** and **lockfile-only diagnostics** are load-bearing — don't loosen them.
> - **Go 1.25** is the floor (go.mod `go 1.25.0`; CI tests `oldstable` + `stable`); no features beyond the floor.
> - **You handle git.** Claude should *not* add/commit/push — it hands off per-commit with a suggested message.
> - The ecosystem seam is `manifest.Ecosystem` / `manifest.Parsed` in [`internal/manifest/ecosystem.go`](internal/manifest/ecosystem.go). Add managers there, never by branching on PM at each step.

### Contents

| # | Item | # | Item |
| --- | --- | --- | --- |
| [1](#1--yarn-package-manager-support) | Yarn PM support | [9](#9--more-make-commands) | More Make commands |
| [2](#2--docs-for-the-yarn-expansion) | Docs for Yarn | [10](#10--nuclear-code-review) | Nuclear code review |
| [3](#3--example-runs-in-the-makefile) | Example runs in Makefile | [11](#11--test--coverage-audit) | Test & coverage audit |
| [4](#4--clone-with-or-without-examples) | Clone with/without examples | [12](#12--default-to-more-than-one-job) | Default >1 job? |
| [5](#5--fetch-examples-after-install) | Fetch examples later | [13](#13--cleanup-recommendations) | Cleanup recommendations |
| [6](#6--reports-across-every-package-manager) | Reports across all PMs | [14](#14--ci-optimization--symmetry) | CI optimization & symmetry |
| [7](#7--tui-padding--margins) | TUI padding & margins | [15](#15--github-pages-landing-site) | GitHub Pages site |
| [8](#8--docker-audit) | Docker audit | | |

---

## 1. 🧶 Yarn package-manager support

> [!TIP]
> Yarn is a **JavaScript** manager — same `package.json` as npm/pnpm. The
> `jsEcosystem` struct already takes a *pluggable* lock parser, so Yarn is
> mostly "one more lock parser + PM plumbing," not a new ecosystem.

```text
Add Yarn as a supported package manager. Yarn uses package.json, so reuse the
existing jsEcosystem — do NOT add a new Ecosystem type.

Wiring:
- internal/manifest/yarnlock.go (new): ParseYarnLock([]byte) (Resolved, error),
  PEP-equivalent to ParsePnpmLock — return resolved name -> version.
- internal/manifest/ecosystem.go: in EcosystemFor add
  case "yarn": return jsEcosystem{parseLock: ParseYarnLock}, nil
- internal/pm/pm.go: add YARN Manager = "yarn"; extend the override switch;
  LockfileName() -> "yarn.lock"; ManifestName() stays package.json; add
  installArgs() for yarn; update every "supported: ..." error string.
- internal/pm/pm.go Detect(): add a hasYarnLock parameter and make the
  "more than one JS lockfile present" ambiguity error cover all three.
- internal/engine/engine.go (≈750, the JS detection branch): probe yarn.lock
  via Git.FileExists and pass hasYarnLock into pm.Detect.

Tests: table tests for ParseYarnLock mirroring the pnpm tests; a FuzzParseYarnLock
target; and extend pm Detect tests for the three-JS-lockfile matrix. Keep the
strict-fail predicate and lockfile-only diagnostics behaviour identical to pnpm.
```

> [!WARNING]
> ⚠️ **Trap — two incompatible `yarn.lock` formats.** Classic (v1) is a bespoke
> `name@range:\n  version "x"` text format; Berry (v2+) is YAML with a
> `__metadata:` block. A naive parser silently mis-reads the other.
>
> 🔀 **Decision:** ship **Classic v1 first** (still the most common in the wild),
> and *detect* Berry (`__metadata:` / `__metadata` key) to either parse the YAML
> or fail with a clear "Yarn Berry lockfiles not yet supported" message — never
> mis-parse it as v1.
>
> ⚠️ **Trap — install flags.** Candidate manifests deliberately disagree with the
> lockfile, so you must **not** freeze: Classic `yarn install` (no
> `--frozen-lockfile`); Berry `yarn install --no-immutable`. Mirror the pnpm
> `--no-frozen-lockfile` comment that explains *why*.

🎯 **Done when** a generated Yarn demo bisects to its single culprit with
`packageManager: "yarn"` in the JSON report, and the yarn path has full parity
with the pnpm path (parse → diff → render → lockfile-only).

---

## 2. 📚 Docs for the Yarn expansion

```text
Propagate Yarn support through every document and user-facing string. Find and
update each of these:

- README.md "Supported package managers" table — add the yarn row, and DELETE
  the "...and yarn are not supported yet" sentence (≈line 215).
- README.md "Configuration" table + the run-flags help: the --pm enum currently
  reads <npm|pnpm|cargo|go> / <npm|pnpm|cargo|go|uv>. Make every occurrence include yarn.
- internal/cli/cli.go help text (the --pm line) and internal/pm/pm.go error
  strings ("supported: npm, pnpm, ...").
- action.yml — the `pm` input description.
- docs/how-it-works.md and docs/limitations.md — any PM enumeration.
- CHANGELOG.md — add an entry under the unreleased section.
- examples/README.md — only if a Yarn demo is added (see item 3/14).

Verify with: `grep -rin "yarn" . --include='*.md' --include='*.go' --include='*.yml'`
returns only intended references, and no stale "<npm|pnpm|cargo|go>" enum remains.
```

> [!NOTE]
> This is a "no stale enum left behind" sweep. The `--pm` value list appears in
> **at least three** places (README config table, README run-flags block, cli.go
> help, pm.go error) — they drift independently, so grep is the acceptance test.

🎯 **Done when** the grep above is clean and the README table lists yarn with its
`yarn.lock` lockfile.

---

## 3. 🛠️ Example runs in the Makefile

> [!TIP]
> The Makefile today has **zero** demo targets — the example repos are driven by
> `examples/make-demo*.sh` and invoked directly in CI. This adds a friendly
> local on-ramp that mirrors what CI already does.

```text
Add `make` targets that build the binary, generate an example repo, and run a
bisection against it — one per bundled demo. Mirror the exact invocations in
.github/workflows/ci.yml (demo / demo-cargo / demo-go jobs) so a green `make demo*`
predicts green CI.

Add (with `##` help annotations and `.PHONY`):
  demo            # build + make-demo.sh + bisect examples/demo/app -> leftpad
  demo-complex    # the 12-package / 5-culprit npm demo
  demo-jobs       # the 18-package / 8-consensus demo, with --jobs
  demo-swarm      # the 28-package / 12-replica demo, with --jobs
  demo-cargo      # cargo simple+complex+jobs+swarm
  demo-go         # go simple+complex+jobs+swarm (set GOPROXY/GOSUMDB/GOFLAGS as CI does)
  demos           # umbrella: run them all

Keep everything offline (the scripts use file: deps / vendored registries / a
file:// GOPROXY). Reuse BINARY/GO variables already defined at the top of the Makefile.
```

🎯 **Done when** `make demo` from a clean tree builds, generates, bisects, and
prints the `leftpad` culprit — no network, no manual steps.

---

## 4. 📦 Clone with or without examples

> [!CAUTION]
> 🔀 **Decision needed — the premise is ambiguous.** Today `examples/` holds
> committed *generator scripts*; the generated repos are **gitignored**. A `git
> clone` therefore always brings the scripts (cheap), and a `go install` / binary
> download brings **nothing**. So "clone with/without examples" most likely means
> *"let binary-install users opt into runnable examples."*

```text
Design (don't build yet — that's item 5) the "examples are optional" experience.
Produce a short decision doc covering:

1. What "examples" means per install path:
   - git clone: already includes examples/*.sh (the generated repos are gitignored).
   - go install / curl install.sh / npm / Docker: no examples ship today.
2. The "with vs without" control. Options:
   a. (Recommended) A `depbisect examples <dir>` subcommand that scaffolds the
      demo generators on demand — nothing to opt out of at clone time, examples
      arrive only when asked.
   b. A documented sparse/degit one-liner for clone-time selection.
3. Default = "with" for `git clone` (status quo, zero change); "on request" for
   binary installs.

Output: a recommendation + the subcommand UX (flags, output dir, offline behaviour),
feeding directly into item 5.
```

> [!NOTE]
> Your roadmap note said *"default to with??? not sure"* — that uncertainty is
> exactly what this decision doc resolves. Recommended resolution: **keep clone
> behaviour unchanged; make examples a pull, not a push, for binary users.**

🎯 **Done when** there's a one-paragraph recommendation and a defined
`depbisect examples` UX ready to implement.

---

## 5. ⬇️ Fetch examples after install

```text
Implement the examples-on-demand path decided in item 4: a `depbisect examples <dir>`
subcommand that materializes the demo generators (and optionally runs one).

Recommended mechanism: `go:embed examples/*.sh examples/README.md` into the binary
so the command is fully offline and version-locked to the binary — matching the
project's offline-by-default ethos. (Alternative: fetch the examples/ tree from the
matching GitHub release tag; only do this if embedding bloats the binary
unacceptably — measure first.)

Behaviour:
- writes the generator scripts into <dir> (default ./depbisect-examples),
- refuses to clobber a non-empty dir without --force,
- prints the exact next command to run a bisection,
- has a test that scaffolds into a temp dir and asserts the files land.
```

> [!TIP]
> `go:embed` keeps this airtight: no network, the examples always match the
> binary's version, and it reuses the scripts you already maintain in `examples/`.

🎯 **Done when** `depbisect examples /tmp/x` writes runnable generators offline and
prints the follow-up `depbisect run …` command.

---

## 6. 📄 Reports across every package manager

> [!TIP]
> `internal/report/report.go` is already PM-agnostic (a `PackageManager` string
> field + `manifest.Change`). The risk isn't the model — it's whether each PM's
> *version syntax* renders cleanly in the Markdown/JSON cells.

```text
Audit report generation for correctness across ALL managers (npm, pnpm, cargo, go,
uv, yarn). Focus on internal/report/report.go and its testdata goldens.

Check that the Markdown table + JSON render correctly for each manager's specifics:
- Go module paths with slashes (example.com/foo) and pseudo-versions,
- Cargo plain semver (1.0.0) across [dependencies] vs [dev-dependencies] sections,
- uv PEP 508 specs (extras, markers) and the dependencies section name,
- yarn/npm ranges (^, ~, file:) in the versionCell(spec, resolved) helper,
- the "Lockfile-only changes (not bisectable)" section for each PM.

Then ensure internal/report/testdata has golden fixtures covering every manager,
not just npm — add per-PM golden tests where missing. Verify the JSON `section`,
`kind`, and version fields are stable and faithful per ecosystem.
```

🎯 **Done when** every supported PM has a golden report fixture and the version
cells render that PM's real spec syntax without truncation or mangling.

---

## 7. 🎨 TUI padding & margins

> [!TIP]
> All STDOUT/STDERR rendering lives in [`internal/cli/output.go`](internal/cli/output.go).
> Key constants: `statusWidth = 12`, `outputWidth = 100`; the modern summary
> indents facts by 2 and draws a 44-char rule.

```text
Improve the breathing room of the terminal output — more consistent left gutter
and vertical spacing in both the live progress and the final summary — in
internal/cli/output.go. Review statusWidth / outputWidth / modernLabelWidth and
the modern summary's indentation and rule width together so the two styles stay
visually aligned (modernLabelWidth is intentionally derived from statusWidth —
keep that link).

Hard constraints:
- The CLASSIC style must stay byte-stable. Non-TTY, NO_COLOR, CLICOLOR=0, and
  verbose runs all fall back to classic (see terminalMode / modernActive), and CI
  logs + golden tests depend on it.
- Don't break the width math: wrapWords, truncateText, and writeStatus wrapping
  must still respect terminalLineWidth on narrow terminals.
- Re-run the cli snapshot/golden tests; update goldens only for the modern style.
```

> [!WARNING]
> ⚠️ **Trap:** the modern renderer only engages on a colored interactive TTY.
> "Padding" changes that touch the classic path will ripple into CI log assertions
> and the `examples/README.md` "Expected output" blocks. Keep classic frozen.

🎯 **Done when** the modern summary/progress has noticeably calmer spacing,
classic output is unchanged byte-for-byte, and narrow-terminal wrapping still works.

---

## 8. 🐳 Docker audit

> [!CAUTION]
> 🧠 **Respect a prior decision (do not relitigate):** the base images
> `golang:1.23-alpine` (build) and `node:20-slim` (runtime) were chosen
> deliberately — the Alpine "fix" breaks glibc for native npm modules, and the
> pnpm `@9.15.4` pin exists because newer pnpm crashes on Node 20. **Do not
> propose switching base images or unpinning pnpm.**

```text
Audit the Dockerfile and .github/workflows/docker.yml for performance,
deprecations, and image hygiene — WITHOUT changing the base images
(golang:1.23-alpine, node:20-slim) or the pnpm@9.15.4 pin, which are settled.

Look at:
- BuildKit cache mounts: `--mount=type=cache` for `go build` and `go mod download`
  to speed rebuilds (syntax=docker/dockerfile:1 is already declared).
- Layer ordering and .dockerignore coverage (is the build context minimal?).
- Action version currency in docker.yml (setup-qemu/buildx/build-push/login).
- Image size and any deprecated Dockerfile patterns.

Then flag — don't silently fix — the real gap: the runtime image bundles only
git + node + npm + pnpm, but depbisect now supports cargo, go, and uv. In-container
bisection of those ecosystems will fail. Recommend a scope decision: keep a
JS-only image (documented), or build a multi-toolchain image (size cost).
```

> [!NOTE]
> 🔀 **Decision:** JS-only image (small, honest, documented limitation) vs.
> multi-toolchain image (works everywhere, much larger). Surface the trade-off;
> don't assume.

🎯 **Done when** the audit lists concrete, prioritized changes (with the
base-image/pnpm decisions explicitly preserved) and the toolchain-coverage gap is
called out with a scope recommendation.

---

## 9. ➕ More Make commands

> [!TIP]
> Current targets: `help build test test-race cover fmt fmt-check vet lint vuln
> fuzz bench tidy check install-tools clean`. Gaps below are the high-value adds.

```text
Propose and add high-value Makefile targets (with `.PHONY` + `##` help text,
matching the existing style). Candidates:

- install        # go install ./cmd/depbisect (GOBIN)
- run            # build + run with ARGS, e.g. make run ARGS="run --repo ... -- node test.js"
- ci             # the full pre-push mirror: check + cover + vuln (+ fuzz smoke)
- cover-html     # go tool cover -html=coverage.out (after `cover`)
- docker-build   # docker build -t depbisect:dev .
- docker-smoke   # the docker.yml behaviour check, locally
- release-snapshot # goreleaser release --snapshot --clean (dry-run a release)
- gifs / vhs     # regenerate the VHS tapes in docs/assets/vhs -> gifs

Also fold in the demo targets from item 3. Keep `make help` readable — group or
order targets so the list stays scannable.
```

🎯 **Done when** the new targets run, are documented in `make help`, and don't
duplicate logic CI already owns (mirror, don't fork).

---

## 10. ☢️ Nuclear code review

> [!IMPORTANT]
> The deepest review is **`/code-review ultra`** (a.k.a. "ultrareview") — a
> multi-agent cloud review of the current branch. It is **user-triggered and
> billed**; Claude cannot launch it for you. Run it yourself; the prompt below is
> the focus brief to paste in, or to drive a local `/code-review high`.
```text
Perform the deepest review of the Yarn + Python expansion and the engine it plugs
into. Prioritize, in order:

1. Correctness of the new manifest parsers (yarn.lock classic vs berry,
   pyproject.toml PEP 508 splitting, normalization) and their diff/render/
   lockfile-only round-trips — especially format-preserving reverts.
2. The ddmin predicate's strict-fail semantics and minimality proof under
   --jobs concurrency (worktree isolation, no shared mutable state, deterministic
   result regardless of job count).
3. Security of executing arbitrary verification commands (untrusted repo, trusted
   batch flag, signal handling).
4. Cross-ecosystem symmetry: anywhere npm/pnpm/cargo/go/uv/yarn paths diverge in a
   way that isn't justified by the ecosystem.

For each finding: file:line, severity, and the smallest correct fix.
```

🎯 **Done when** you've run `/code-review ultra` (or `high`) on the branch with this
brief and triaged the findings.

---

## 11. 🧪 Test & coverage audit

> [!TIP]
> Floor is **80%** via `scripts/check-coverage.sh` (`COVER_MIN`), enforced
> single-OS on purpose (gitx/execx/verify branch on OS, so per-OS coverage
> differs by tenths). Fuzz targets exist for ddmin + several parsers.

```text
Audit test coverage and quality across the repo.

- Per-package coverage: run `make cover`, then `go tool cover -func=coverage.out`
  and list packages furthest below the 80% floor. Identify untested error paths
  (parse failures, install failures, timeout/cancellation, signal handling).
- Parser edge cases: malformed yarn.lock (both formats), pyproject with markers/
  extras/URL refs, empty/duplicate dependency sections, Windows line endings.
- Symmetry: every ecosystem (npm/pnpm/cargo/go/uv/yarn) should have parallel
  table tests for parse/diff/render/lockfile-only — flag asymmetries.
- Fuzz: confirm a fuzz target exists for each manifest+lock parser; the CI fuzz
  job and the Makefile fuzz target currently list DIFFERENT sets — reconcile.
- If there's comfortable headroom above 80%, recommend raising COVER_MIN.

Output a prioritized gap list with the specific tests to add.
```

> [!WARNING]
> ⚠️ The Makefile `fuzz` target and the CI `fuzz` job enumerate **different**
> parser targets (CI adds Cargo; neither runs Go/pyproject fuzz). That drift is a
> finding in itself — see also item 14.

🎯 **Done when** there's a prioritized list of missing tests, the fuzz-target sets
are reconciled across Makefile + CI, and a COVER_MIN recommendation is made.

---

## 12. 🔢 Default to more than one job?

```text
Evaluate whether --jobs should default to >1 (currently 1; internal/cli/cli.go
≈line 160, engine Jobs). Make a recommendation with rationale, weighing:

- Determinism is NOT a concern: the minimal set is identical at any job count
  (this is already proven + documented).
- Concurrency safety IS: the verification command must be safe to run in parallel
  across worktrees. Docs explicitly warn users about non-isolated side effects
  (ports, shared caches, global state). Parallel-by-default could surprise a user
  whose test command isn't concurrency-safe.
- Resource use: NumCPU candidate installs + test runs at once on a laptop / CI box.
- Speed: real wins on the interacting-culprit demos (jobs/swarm).

Recommend one of: keep default 1 (safe, explicit opt-in) + surface a one-line hint
when changes are many; OR default to a small capped value (e.g. min(NumCPU, 4))
with an easy --jobs 1 escape hatch. State your pick and why.
```

> [!NOTE]
> 💡 Leaning recommendation: **keep the default at 1** (running a user's test
> command in parallel without consent is a footgun) but **emit a hint** like
> *"23 changes — try `--jobs 4` to go faster"* when the candidate count is large.
> Make the call explicitly in the deliverable.

🎯 **Done when** there's a clear recommendation with rationale (and, if "keep 1,"
the hint is specced).

---

## 13. 🧹 Cleanup recommendations

```text
Produce a categorized cleanup report (RECOMMEND, don't delete anything without
confirmation — the user handles git and destructive ops). Investigate:

- Root-level artifacts: is the committed `depbisect` binary, coverage.out, or
  depbisect-report.{md,json} tracked in git? (.gitignore lists some — verify with
  `git ls-files`.) Anything tracked that shouldn't be.
- dev_archive/ (gitignored scratch): GIFs there may duplicate docs/assets/gifs —
  flag duplication, don't auto-remove.
- .DS_Store and other OS cruft in the tree.
- ~30 stale `feature/*` branches — list merged-and-deletable vs still-live
  (`git branch --merged main`).
- Dead code: unexported helpers with no callers, commented-out blocks, TODO/FIXME
  that are already resolved.

Group findings by (a) tracked-but-shouldn't-be, (b) duplicated assets,
(c) dead code, (d) stale branches. Each with the exact command to clean it.
```

🎯 **Done when** there's a grouped report with copy-paste cleanup commands, and
nothing was deleted without sign-off.

---

## 14. ⚙️ CI optimization & symmetry

> [!TIP]
> `.github/workflows/ci.yml` has `demo`, `demo-cargo`, `demo-go` jobs that are
> structurally parallel but **asymmetric**: npm asserts via inline `node -e`,
> cargo/go via `jq`; matrices and timeouts differ; the `fuzz` job lists npm+cargo
> parsers but **omits Go and pyproject** fuzz targets.

```text
Optimize and symmetrize .github/workflows/ci.yml.

Symmetry:
- Unify the three demo jobs (demo/demo-cargo/demo-go) — they repeat the same
  generate -> build -> bisect -> assert shape per ecosystem. Consider a matrix
  over ecosystem with a shared assertion helper (jq for all, including npm).
- Reconcile the fuzz target list with the Makefile: neither runs Go/pyproject
  fuzz; align both to cover every parser.
- Add a yarn demo job once item 1 lands.

Optimization:
- Add module + build caching to setup-go (actions/setup-go cache, keyed on go.sum)
  across jobs — currently uncached.
- Review timeouts and fail-fast settings for consistency.

DO NOT "fix" the deliberate Windows gap: the demo jobs skip windows-latest on
purpose (documented). Leave it, or address it as a separate, explicit task.
```

> [!WARNING]
> ⚠️ **Trap:** the demo jobs intentionally run only `ubuntu` + `macos`. The
> Windows e2e gap is a *known, deferred* decision — don't silently add
> `windows-latest` here.

🎯 **Done when** the demo jobs share one symmetric shape, fuzz targets match
between CI and Makefile, caching is in place, and the Windows gap is left
intentional (not accidentally "fixed").

---

## 15. 🌐 GitHub Pages landing site

> [!TIP]
> There's no Pages site today. You already have great assets to reuse:
> `docs/assets/gifs/{go,js,rust}`, the VHS tapes, and a strong README.

```text
Stand up a GitHub Pages landing site for DepBisect.

Decide the approach first (one short paragraph), then build it:
- (Recommended) Lightweight single-page site — hand-rolled index.html or MkDocs
  Material pointed at the existing docs/ — that leads with the hero GIF, the
  one-line pitch, install one-liner, and the "isolating the culprit" demo. Reuse
  docs/assets GIFs; do NOT duplicate them.
- Add a .github/workflows/pages.yml that builds + deploys on push to main
  (actions/deploy-pages, permissions: pages: write, id-token: write).

Keep it honest and fast: real terminal GIFs over marketing fluff, supported-PM
table straight from the README, link to docs/how-it-works.md.
```

> [!NOTE]
> 🔀 **Decision:** static hand-rolled `index.html` (zero deps, total control) vs.
> MkDocs Material (instant docs nav, themed, slightly heavier). For a CLI tool
> with one killer GIF, the single page usually wins — but call it explicitly.

🎯 **Done when** a Pages workflow deploys a site that leads with the demo GIF, the
install one-liner, and the supported-PM table — assets reused, not duplicated.

---

### How to drive this pack

> [!TIP]
> Work them roughly in order — **1 → 2 → 3** unlock the Yarn demo that **6, 11,
> 14** then lean on. **10 (nuclear review)** is best run *last*, after the new
> surface area has settled. **4/5, 8, 12, 15** are independent and can slot in
> anywhere.

| Wave | Items | Theme |
| --- | --- | --- |
| 🌊 Build | 1, 2, 3 | Yarn lands + docs + local demos |
| 🌊 Verify | 6, 11, 14 | Reports, coverage, CI symmetry catch up |
| 🌊 Polish | 7, 8, 9, 12, 13 | TUI, Docker, Make, defaults, cleanup |
| 🌊 Decide | 4, 5, 15 | Examples UX + the public landing page |
| ☢️ Gate | 10 | Nuclear review before tagging |
