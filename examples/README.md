# Examples

## Demo repository

`make-demo.sh` generates a disposable git repository (`examples/demo/`,
gitignored) you can point DepBisect at. It needs `git`, `node`, and `npm`,
but **no network**: the dependencies are local `file:` packages.

The repo has two commits. The second bumps two dependencies; only the
`leftpad 1.0.0 -> 2.0.0` bump breaks `node test.js` (it starts padding the
wrong side).

```sh
./examples/make-demo.sh
go build ./cmd/depbisect
./depbisect run --repo examples/demo/app --base HEAD~1 --runs 3 -- node test.js
```

Expected output ends with:

```text
      Result Minimal breaking dependency set found

Breaking dependencies
  - leftpad file:.../pkgs/leftpad-1.0.0 -> file:.../pkgs/leftpad-2.0.0

    Evidence 3/3 failing runs
     Outcome minimal-set-found
```

Useful variations:

```sh
./depbisect run --repo examples/demo/app --base HEAD~1 --dry-run -- node test.js
./depbisect run --repo examples/demo/app --base HEAD~1 --keep-worktrees -- node test.js
./depbisect run --repo examples/demo/app --base HEAD~1 -- node -e 'process.exit(0)'  # exit 2: not reproduced
```

Re-run `make-demo.sh` any time to reset the demo (it embeds absolute paths,
so regenerate rather than moving it).

## Complex interdependency repository

`make-demo-complex.sh` generates a larger offline repository at
`examples/demo-complex/`. Its second commit updates twelve packages across
`dependencies`, `devDependencies`, and `optionalDependencies`.

The application has a primary wire path and a cache fallback:

- `wire-encoder`, `wire-transport`, and `wire-decoder` break the primary path
  only when all three updates are present.
- `cache-writer` and `cache-reader` break the fallback only when both updates
  are present.
- The application fails only when both paths are broken.
- Seven other package updates are harmless noise.

This produces a five-package minimal failing set. Reverting any one of those
five packages restores either the primary path or its fallback.

```sh
./examples/make-demo-complex.sh
go build ./cmd/depbisect
./depbisect run --repo examples/demo-complex/app --base HEAD~1 --runs 3 -- node test.js
```

Expected output includes:

```text
     Changes 12 analyzed

Breaking dependencies
  - cache-reader file:.../cache-reader-1.0.0 -> file:.../cache-reader-2.0.0
  - cache-writer file:.../cache-writer-1.0.0 -> file:.../cache-writer-2.0.0
  - wire-decoder file:.../wire-decoder-1.0.0 -> file:.../wire-decoder-2.0.0
  - wire-encoder file:.../wire-encoder-1.0.0 -> file:.../wire-encoder-2.0.0
  - wire-transport file:.../wire-transport-1.0.0 -> file:.../wire-transport-2.0.0
```

Re-run `make-demo-complex.sh` to reset the generated repository.

## Parallel repository (`--jobs`)

`make-demo-jobs.sh` generates a still-larger offline repository at
`examples/demo-jobs/`. Its second commit bumps **eighteen** packages across all
three dependency sections. Eight `consensus-*` packages model a protocol
upgrade that only deadlocks once **all eight** reach the new version at the same
time — while any one participant still speaks the old protocol, compatibility
mode keeps the tests green. The other ten `lib-*` bumps are harmless noise.

Because the eight culprits fail only in combination, ddmin evaluates wide
batches of candidate subsets at each granularity level, and the closing
1-minimality proof tests eight neighbors at once — precisely the work `--jobs`
spreads across worktrees. It is therefore the example where parallelism is most
visible.

```sh
./examples/make-demo-jobs.sh
go build ./cmd/depbisect

# Sequential, then parallel — same answer, less wall time:
./depbisect run --repo examples/demo-jobs/app --base HEAD~1 --runs 3 --jobs 1 -- node test.js
./depbisect run --repo examples/demo-jobs/app --base HEAD~1 --runs 3 --jobs 6 -- node test.js
```

Both runs report the identical eight-package minimal set:

```text
     Changes 18 analyzed

Breaking dependencies
  - consensus-auth     file:.../consensus-auth-1.0.0     -> file:.../consensus-auth-2.0.0
  - consensus-codec    file:.../consensus-codec-1.0.0    -> file:.../consensus-codec-2.0.0
  - consensus-gossip   file:.../consensus-gossip-1.0.0   -> file:.../consensus-gossip-2.0.0
  - consensus-ledger   file:.../consensus-ledger-1.0.0   -> file:.../consensus-ledger-2.0.0
  - consensus-router   file:.../consensus-router-1.0.0   -> file:.../consensus-router-2.0.0
  - consensus-session  file:.../consensus-session-1.0.0  -> file:.../consensus-session-2.0.0
  - consensus-stream   file:.../consensus-stream-1.0.0   -> file:.../consensus-stream-2.0.0
  - consensus-sync     file:.../consensus-sync-1.0.0     -> file:.../consensus-sync-2.0.0
```

`--jobs` only changes *how fast* the search runs, never *what it finds*: the
minimal set is identical at any job count (the verification command must be safe
to run concurrently — these isolated worktrees are). Re-run `make-demo-jobs.sh`
to reset the generated repository.

## Largest parallel repository (`demo-swarm`)

`make-demo-swarm.sh` generates the biggest bundled example at
`examples/demo-swarm/`. Its second commit bumps **twenty-eight** packages across
all three dependency sections. Twelve `replica-*` nodes model a wire-format
migration that only loses quorum once **all twelve** reach the new version at the
same time — while any single replica still speaks the old format, a translation
shim keeps the tests green. The other sixteen `lib-*` bumps are harmless noise.

It is `demo-jobs` taken further: a larger, twelve-package combination-locked
culprit forces ddmin to evaluate the widest candidate batches of any example, so
the wall-time gap between `--jobs 1` and a high job count is the most pronounced
here. It is the example recorded for the README's parallel demo.

```sh
./examples/make-demo-swarm.sh
go build ./cmd/depbisect

# Sequential, then parallel — same twelve-package answer, much less wall time:
./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 1  -- node test.js
./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- node test.js
```

Both runs report the identical twelve-package minimal set (`replica-01` …
`replica-12`). Re-run `make-demo-swarm.sh` to reset the generated repository.
