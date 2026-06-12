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
Minimal failing set:
  leftpad file:.../pkgs/leftpad-1.0.0 -> file:.../pkgs/leftpad-2.0.0

Reproduced 3/3 times
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
Analyzed 12 dependency changes

Minimal failing set:
  cache-reader file:.../cache-reader-1.0.0 -> file:.../cache-reader-2.0.0
  cache-writer file:.../cache-writer-1.0.0 -> file:.../cache-writer-2.0.0
  wire-decoder file:.../wire-decoder-1.0.0 -> file:.../wire-decoder-2.0.0
  wire-encoder file:.../wire-encoder-1.0.0 -> file:.../wire-encoder-2.0.0
  wire-transport file:.../wire-transport-1.0.0 -> file:.../wire-transport-2.0.0
```

Re-run `make-demo-complex.sh` to reset the generated repository.
