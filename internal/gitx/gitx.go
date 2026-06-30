// Package gitx provides the narrow slice of Git that DepBisect needs:
// resolving revisions, reading files at a revision, and managing detached
// worktrees. Every operation is read-only with respect to the user's working
// tree; worktree commands only create and remove directories that DepBisect
// itself chose.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/skyneticist/depbisect/internal/execx"
)

// ErrNotExist reports that a file does not exist at the requested revision.
var ErrNotExist = errors.New("file does not exist at revision")

// Revision-resolution failures are exported as sentinels so callers can
// recognize the common first-run mistakes and attach actionable guidance.
var (
	// ErrNoCommits reports that the repository exists but has no commits yet,
	// so no revision can resolve.
	ErrNoCommits = errors.New("repository has no commits yet")
	// ErrNoSuchCommit reports that a revision expression names no commit in the
	// repository (a typo, or history that does not reach back that far).
	ErrNoSuchCommit = errors.New("no such commit in this repository")
	// ErrNotARepo reports that the working directory is not inside a git
	// repository.
	ErrNotARepo = errors.New("not a git repository")
)

// Git runs git commands against one repository.
type Git struct {
	runner  execx.Runner
	repoDir string
}

// New returns a Git bound to the repository at repoDir.
func New(runner execx.Runner, repoDir string) *Git {
	return &Git{runner: runner, repoDir: repoDir}
}

// run executes git with the given arguments in the repository.
func (g *Git) run(ctx context.Context, args ...string) (execx.Result, error) {
	return g.runner.Run(ctx, execx.Cmd{
		Dir:  g.repoDir,
		Name: "git",
		Args: args,
	})
}

// validRev rejects revision expressions that argv-based git commands could
// mistake for options. There is no shell involved anywhere, so this is about
// flag confusion, not injection.
func validRev(rev string) error {
	if rev == "" {
		return errors.New("revision is empty")
	}
	if strings.HasPrefix(rev, "-") {
		return fmt.Errorf("revision %q must not start with %q", rev, "-")
	}
	return nil
}

// ResolveRev resolves a revision expression to a full commit SHA.
func (g *Git) ResolveRev(ctx context.Context, rev string) (string, error) {
	if err := validRev(rev); err != nil {
		return "", err
	}
	res, err := g.run(ctx, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve revision %q: %w", rev, err)
	}
	if res.ExitCode != 0 {
		// With --verify --quiet, git signals an unknown revision by exit code
		// alone and writes nothing to stderr. A non-empty stderr means a
		// different failure worth surfacing; the most common is operating
		// outside a repository at all.
		if stderr := strings.TrimSpace(string(res.Stderr)); stderr != "" {
			if strings.Contains(stderr, "not a git repository") {
				return "", fmt.Errorf("resolve revision %q: %w", rev, ErrNotARepo)
			}
			return "", fmt.Errorf("resolve revision %q: %s", rev, stderr)
		}
		// Distinguish the two common silent causes so the message is actionable:
		// a repository with no commits yet versus a revision that simply does
		// not exist. If even HEAD will not resolve, there is no history at all.
		if head, herr := g.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD"); herr == nil && head.ExitCode != 0 {
			return "", fmt.Errorf("resolve revision %q: %w", rev, ErrNoCommits)
		}
		return "", fmt.Errorf("resolve revision %q: %w", rev, ErrNoSuchCommit)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// Commit is a terse description of a commit, used to suggest a --base revision.
type Commit struct {
	SHA      string
	ShortSHA string
	Date     string // committer date, YYYY-MM-DD
	Subject  string
}

// logFieldSep separates the fields of one log record. ASCII unit separator
// cannot appear in a commit subject, so parsing is unambiguous.
const logFieldSep = "\x1f"

// RecentCommitsTouching lists up to limit commits, newest first, reachable from
// rev that modified any of paths. It underpins the --base suggestion: the paths
// are dependency manifests and lockfiles, so each result is a point where
// dependencies could have changed.
func (g *Git) RecentCommitsTouching(ctx context.Context, rev string, paths []string, limit int) ([]Commit, error) {
	if err := validRev(rev); err != nil {
		return nil, err
	}
	args := []string{
		"log", rev,
		"--max-count=" + strconv.Itoa(limit),
		"--format=%H" + logFieldSep + "%h" + logFieldSep + "%cs" + logFieldSep + "%s",
		"--",
	}
	args = append(args, paths...)
	res, err := g.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("list recent commits: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("list recent commits: %s", firstLine(res.Stderr))
	}
	return parseCommitLog(res.Stdout), nil
}

// parseCommitLog decodes the unit-separated records emitted by
// RecentCommitsTouching. Malformed lines are skipped rather than failing the
// whole suggestion.
func parseCommitLog(out []byte) []Commit {
	var commits []Commit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, logFieldSep, 4)
		if len(f) != 4 {
			continue
		}
		commits = append(commits, Commit{SHA: f[0], ShortSHA: f[1], Date: f[2], Subject: f[3]})
	}
	return commits
}

// ShowFile returns the content of path at the given revision. If the file
// does not exist there, the error wraps ErrNotExist.
func (g *Git) ShowFile(ctx context.Context, rev, path string) ([]byte, error) {
	blob, err := g.blobRef(ctx, rev, path)
	if err != nil {
		return nil, err
	}
	if blob == "" {
		return nil, fmt.Errorf("%s at %s: %w", path, rev, ErrNotExist)
	}
	res, err := g.run(ctx, "cat-file", "blob", blob)
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", path, rev, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("read %s at %s: %s", path, rev, firstLine(res.Stderr))
	}
	return res.Stdout, nil
}

// FileExists reports whether path exists at the given revision.
func (g *Git) FileExists(ctx context.Context, rev, path string) (bool, error) {
	blob, err := g.blobRef(ctx, rev, path)
	if err != nil {
		return false, err
	}
	return blob != "", nil
}

// blobRef returns the blob SHA for rev:path, or "" if the path does not
// exist at that revision.
func (g *Git) blobRef(ctx context.Context, rev, path string) (string, error) {
	if err := validRev(rev); err != nil {
		return "", err
	}
	res, err := g.run(ctx, "rev-parse", "--verify", "--quiet", rev+":"+path)
	if err != nil {
		return "", fmt.Errorf("inspect %s at %s: %w", path, rev, err)
	}
	if res.ExitCode != 0 {
		return "", nil
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// AddWorktree creates a detached worktree for rev at dir. dir must not exist
// yet or must be empty; it becomes wholly owned by the caller.
func (g *Git) AddWorktree(ctx context.Context, dir, rev string) error {
	if err := validRev(rev); err != nil {
		return err
	}
	res, err := g.run(ctx, "worktree", "add", "--detach", dir, rev)
	if err != nil {
		return fmt.Errorf("add worktree at %s: %w", dir, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("add worktree at %s for %s: %s", dir, rev, firstLine(res.Stderr))
	}
	return nil
}

// ResetWorktree restores an owned worktree to rev and removes every
// untracked and ignored path. This prevents package-manager and verification
// side effects from one candidate affecting the next.
func (g *Git) ResetWorktree(ctx context.Context, dir, rev string) error {
	if err := validRev(rev); err != nil {
		return err
	}
	reset, err := g.runner.Run(ctx, execx.Cmd{
		Dir:  dir,
		Name: "git",
		Args: []string{"reset", "--hard", rev},
	})
	if err != nil {
		return fmt.Errorf("reset worktree %s: %w", dir, err)
	}
	if reset.ExitCode != 0 {
		return fmt.Errorf("reset worktree %s to %s: %s", dir, rev, firstLine(reset.Stderr))
	}
	clean, err := g.runner.Run(ctx, execx.Cmd{
		Dir:  dir,
		Name: "git",
		Args: []string{"clean", "-ffdx"},
	})
	if err != nil {
		return fmt.Errorf("clean worktree %s: %w", dir, err)
	}
	if clean.ExitCode != 0 {
		return fmt.Errorf("clean worktree %s: %s", dir, firstLine(clean.Stderr))
	}
	return nil
}

// RemoveWorktree removes a worktree previously created by AddWorktree,
// including its administrative entry in the main repository.
func (g *Git) RemoveWorktree(ctx context.Context, dir string) error {
	res, err := g.run(ctx, "worktree", "remove", "--force", dir)
	if err != nil {
		return fmt.Errorf("remove worktree %s: %w", dir, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("remove worktree %s: %s", dir, firstLine(res.Stderr))
	}
	return nil
}

// PruneWorktrees removes stale worktree administrative entries, e.g. after a
// worktree directory had to be deleted directly.
func (g *Git) PruneWorktrees(ctx context.Context) error {
	res, err := g.run(ctx, "worktree", "prune")
	if err != nil {
		return fmt.Errorf("prune worktrees: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("prune worktrees: %s", firstLine(res.Stderr))
	}
	return nil
}

// IsPathDirty reports whether path has uncommitted changes (staged or not)
// in the user's working tree.
func (g *Git) IsPathDirty(ctx context.Context, path string) (bool, error) {
	res, err := g.run(ctx, "status", "--porcelain", "--", path)
	if err != nil {
		return false, fmt.Errorf("check status of %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("check status of %s: %s", path, firstLine(res.Stderr))
	}
	return len(strings.TrimSpace(string(res.Stdout))) > 0, nil
}

// firstLine extracts the first non-empty line for terse error messages.
func firstLine(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return "no output"
}
