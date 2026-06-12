# Security

## Model

DepBisect runs with your privileges, in your repository, and executes two kinds of subprocesses: the package manager (`npm`/`pnpm install`) and the verification command you supply. Installing dependencies executes lifecycle scripts of the candidate dependency versions, exactly as a manual install would — bisecting untrusted dependency ranges therefore executes untrusted code. Run it in the same environment you would use for `npm install` of those versions (e.g. a CI sandbox).

## What DepBisect does

- Executes subprocesses by argument vector. No shell is involved, so repository content, dependency names, and version strings cannot inject shell commands.
- Validates revision arguments so they cannot be mistaken for git flags.
- Confines all writes to temporary directories it created (`$TMPDIR/depbisect-*`); the user worktree is never written to.
- Cleans up only resources it created, with `git worktree remove`/`prune` plus deletion of its own temp directory.

## What DepBisect never does

- It never reads or writes your environment variables into reports or logs.
- It never includes captured command output in reports (your test output can contain tokens; the reports contain exit codes, timings, and dependency names only).
- It never runs destructive git commands (`reset`, `clean`, `checkout` over your tree, …).

## Caveats

- The verification command's own stdout/stderr is streamed to your terminal with `--verbose`; that stream is your responsibility.
- The command line you pass (including any secrets embedded in it) is recorded in reports. Don't put secrets in argv — use environment variables, which are inherited by the child but never recorded.
- `--keep-worktrees` leaves an installed `node_modules` tree on disk until you remove it.

## Reporting vulnerabilities

Open a GitHub security advisory or email the maintainer. Please do not file public issues for exploitable problems.
