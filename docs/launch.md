# DepBisect launch kit (LOCAL SCRATCH — do not publish)

> This file is gitignored. It's your playbook, not repo content. Delete it whenever.
> Replace `LINK` with `https://github.com/skyneticist/depbisect` (or the HN thread URL where noted).

---

## Pre-flight — do NOT post until all of these are true

- [ ] Repo is **public**
- [ ] **Social preview** PNG uploaded (Settings → General → Social preview)
- [ ] **Demo GIF** rendered (`vhs docs/demo.tape`) and the README embed line uncommented
- [ ] **Description + topics** set on the repo
- [ ] **v0.1.1 release** has attached binaries + `checksums.txt` (your README points to them)
- [ ] You have a **~3-hour block** free to answer comments (responsiveness is most of the game)

## Posting order — all on ONE morning (Tue–Thu, ~8–10am US Eastern)

Concentrating stars into a single day is what gets you onto GitHub **Trending** and **Changelog Nightly**, which then snowballs.

1. **Show HN** (below) — post first, add the backstory comment immediately.
2. **r/golang** + **r/node** + **Echo JS** (echojs.com submit).
3. **X** + **LinkedIn** — and quote-link your HN thread.
4. Submit to **newsletters** (links at the bottom).
5. Open **awesome-list** PRs.

---

## 1. Hacker News — Show HN

**Title** (≤ 80 chars, no emoji):
```
Show HN: DepBisect – git bisect, but for dependency updates
```

**First comment** (post this yourself, immediately):
```
Hi HN. I built DepBisect to answer a question I kept hitting: a PR bumps 30–40
npm/pnpm dependencies, CI goes red — which bump actually broke it?

git bisect doesn't help here, because the breakage is inside a single
dependency-update commit, not spread across commits. So DepBisect bisects the
dependency *changes* themselves. It diffs the direct deps in package.json
between two revisions and runs Zeller's ddmin delta-debugging to find the
minimal subset that reproduces the failure, then proves the set is 1-minimal by
checking that removing any single dependency makes the failure stop.

The part I found most interesting: the naive "revert one bump at a time"
approach breaks down when the failure only appears with two bumps present
together. ddmin handles those interacting cases — one of the demo repos has a
5-package minimal set spanning two independent failure paths.

A few details:
- Everything runs in a throwaway `git worktree`; your checkout is never modified
  (no reset --hard / clean in your tree).
- Flaky tests: `--runs N` repeats each check; a candidate only "fails" if every
  run fails, and mixed results are reported instead of guessed.
- Deterministic + memoized (no subset tested twice), resumable from a checkpoint
  file, emits a schema-stable JSON report, and ships as a composite GitHub Action.
- Written in Go (single static binary); works on any npm or pnpm repo. Yarn and
  workspaces aren't supported yet and it errors clearly rather than guessing.

It does run dependency lifecycle scripts during install (same as a manual
`npm install`), so the threat model is "run it where you'd run npm install of
those versions."

Repo + README with a demo: LINK

Happy to go deep on the algorithm, the worktree isolation, or the flaky-test
handling.
```

> HN rules: don't ask for upvotes or stars anywhere. The first comment is what converts — keep it technical and present.

---

## 2a. Reddit — r/golang  (flair: "show & tell")

**Title:**
```
DepBisect – git bisect, but for dependency updates (Go, with a pure ddmin core)
```
**Body:**
```
I wrote a CLI in Go that finds which dependency bump in an npm/pnpm PR broke your
build — by delta-debugging the package.json changes between two git revisions.

The bits that might interest this sub:
- The ddmin core (internal/ddmin) is pure and I/O-free, so the algorithm is unit
  tested without processes or clocks. Every external effect (git, installs,
  verification) goes through a small interface with a fake.
- Candidate installs happen in isolated `git worktree`s — the user's checkout is
  never written to.
- Deterministic + memoized; resumable via an append-only JSON-lines checkpoint.
- Targets Go 1.20; CI runs Linux/macOS/Windows + the race detector + staticcheck.

It's a Go tool whose users are JS devs, which was a fun constraint. MIT licensed,
feedback on the design very welcome: LINK
```

## 2b. Reddit — r/node  (primary JS-side audience)

**Title:**
```
I built a tool that finds which dependency bump broke your build
```
**Body:**
```
You merge a PR that bumps a pile of deps (Dependabot/Renovate/manual), tests go
red, and you're stuck reverting bumps one at a time to find the culprit.

DepBisect automates exactly that: give it two git revisions and your test
command, and it finds the *minimal* set of package.json changes that makes the
command fail — including the nasty case where two bumps only break in combination.

- Works with npm and pnpm
- Runs everything in a throwaway git worktree (never touches your checkout)
- Handles flaky tests (`--runs N`)
- Emits a JSON report and ships as a GitHub Action for CI

Free / MIT, single binary. Demo + README: LINK
Would love feedback from anyone who's fought a noisy dependency bump.
```

> r/javascript has stricter self-promo rules — read them first, or use their weekly showoff thread. r/webdev and r/devops are good secondary options.

---

## 3. X / Bluesky thread (attach docs/demo.gif to tweet 1)

```
1/ You merge a PR that bumps 40 npm deps. CI goes red. Which bump broke it?

git bisect can't help — it's all one commit.

So I built DepBisect: git bisect, but for dependency updates. 🧵
[attach demo.gif]

2/ It diffs the deps in package.json between two commits and runs delta-debugging
(ddmin) to find the *minimal* breaking set — then proves removing any single dep
makes the failure stop. Even when two bumps only break together.

3/ Safe by design: every install runs in a throwaway git worktree, so your
checkout is never touched. Flaky tests? --runs N. Interrupted? --resume.
CI? JSON report + a GitHub Action.

4/ Written in Go (single binary), works on any npm/pnpm repo. MIT licensed.

⭐ Repo + demo: LINK
```

## 4. LinkedIn (good for interview-time visibility)

```
Shipping a small open-source tool I'm proud of: DepBisect — "git bisect, but for
dependency updates."

The problem: a PR bumps dozens of npm/pnpm dependencies, CI fails, and finding
the culprit means reverting bumps one at a time — which doesn't even work when
two updates only break in combination.

DepBisect automates it with delta-debugging (Zeller's ddmin): it isolates the
minimal set of package.json changes that reproduces the failure and proves it's
minimal, all inside a throwaway git worktree so your checkout is never touched.
Flaky-test handling, resumable runs, a JSON report, and a GitHub Action included.

Built in Go, MIT licensed. Code + demo: LINK
Feedback and stars welcome 🙏

#golang #javascript #nodejs #opensource #devtools
```

## 5. Echo JS / dev.to (optional)

- **Echo JS** (echojs.com): submit the link, title `DepBisect – git bisect, but for dependency updates`.
- **dev.to / Hashnode**: a short post titled "git bisect, but for dependency updates." Walk through the problem → ddmin → the worktree isolation, embed the GIF, and **set the canonical URL to your repo or blog** so you don't split SEO. Cross-link from the HN thread.

---

## Newsletters (highest-leverage, evergreen — submit the day after)

- **Node Weekly** & **JavaScript Weekly** (cooperpress) — "suggest a link" in the footer. Best fit.
- **Golang Weekly** (cooperpress) — same.
- **Console.dev** — submit via their site; they curate dev tools.
- **Changelog Nightly** — automatic; gaining stars on launch day is what triggers a feature.

## Awesome lists (open a one-line PR each)

- **awesome-nodejs** — strongest fit (a tool for Node projects). Build-tools / dev-tools section.
- Search **awesome-npm**, **awesome-pnpm**, **awesome-ci**, **awesome-devtools** and submit where it fits.
- **awesome-go** — weaker fit (it curates tools *for* Go devs, and yours targets JS projects); try it but expect possible "off-topic".

---

## Ready answers for the obvious questions (paste/adapt fast)

- **"Why Go, not JS?"** Single static binary, no Node-version juggling while you're
  debugging a Node project, fast, trivial to install in CI — and the algorithm core
  stays clean and pure in Go.
- **"Doesn't installing run arbitrary scripts? Security?"** Yes — same as any
  `npm install`. Run it in a sandbox/CI. Reports never capture env vars or command
  output, only revisions, dep names, outcomes, and timings.
- **"How is this different from `npm why` / `npm ls`?"** Those explain the dependency
  *tree*. DepBisect ties the *failure* to specific version bumps.
- **"Yarn / workspaces?"** Not yet — it errors clearly instead of guessing. PRs welcome.
- **"What if it's a transitive or lockfile-only change?"** Detected and reported as
  not-bisectable, with diagnostics, rather than silently ignored.
- **"Monorepo with hundreds of deps — how slow?"** It's O(log n) installs in the good
  case (ddmin), memoizes so no subset is tested twice, and `--runs`/timeouts are
  tunable. Each trial is dominated by your install + test time.
```
