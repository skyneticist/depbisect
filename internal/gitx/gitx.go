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
	"strings"

	"github.com/skyneticist/depbisect/internal/execx"
)

// ErrNotExist reports that a file does not exist at the requested revision.
var ErrNotExist = errors.New("file does not exist at revision")

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
		return "", fmt.Errorf("resolve revision %q: not a valid commit in this repository (%s)",
			rev, firstLine(res.Stderr))
	}
	return strings.TrimSpace(string(res.Stdout)), nil
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
