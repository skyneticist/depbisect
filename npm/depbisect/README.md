# depbisect

> **git bisect, but for dependency updates.**

Find the smallest set of `package.json` dependency changes between two Git
revisions that makes a command fail — and prove it's minimal.

## Install

```sh
npm  i -g depbisect      # or: pnpm add -g depbisect   /   yarn global add depbisect

# …or run it without installing:
npx depbisect run --base origin/main -- npm test
```

This package ships a **prebuilt binary** for your platform as an optional
dependency. There is **no Go toolchain to install, no post-install script, and
no network fetch at install time** — just the binary for your `os`/`cpu`.

## Usage

```sh
depbisect run --base origin/main --runs 3 -- npm test
```

`depbisect` runs entirely in a throwaway git worktree, so your checkout is never
modified. It works with **npm** and **pnpm**, and the same binary also bisects
**cargo** (Rust), **go** modules, and **uv** (Python) projects.

Full documentation — flags, exit codes, the algorithm, and the GitHub Action:

**https://github.com/skyneticist/depbisect**

Licensed under the MIT License.
