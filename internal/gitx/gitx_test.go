package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyneticist/depbisect/internal/execx"
)

// initRepo creates a real git repository with two commits and returns the
// repo path plus the two commit SHAs. The path deliberately contains spaces.
func initRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir = filepath.Join(t.TempDir(), "repo with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "core.autocrlf", "false")
	run("config", "core.filemode", "false")
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"a","dependencies":{"x":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "one")
	first = run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"a","dependencies":{"x":"2.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra file.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "two")
	second = run("rev-parse", "HEAD")
	return dir, first, second
}

func newGit(t *testing.T, dir string) *Git {
	t.Helper()
	return New(execx.Local{}, dir)
}

func TestResolveRev(t *testing.T) {
	dir, first, second := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	sha, err := g.ResolveRev(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if sha != second {
		t.Errorf("HEAD = %q, want %q", sha, second)
	}
	sha, err = g.ResolveRev(ctx, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if sha != first {
		t.Errorf("HEAD~1 = %q, want %q", sha, first)
	}
}

func TestResolveRevInvalid(t *testing.T) {
	dir, _, _ := initRepo(t)
	g := newGit(t, dir)
	_, err := g.ResolveRev(context.Background(), "no-such-rev")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no-such-rev") {
		t.Errorf("error %v does not name the revision", err)
	}
}

func TestShowFile(t *testing.T) {
	dir, first, _ := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	data, err := g.ShowFile(ctx, first, "package.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"1.0.0"`) {
		t.Errorf("content = %q", data)
	}
	data, err = g.ShowFile(ctx, "HEAD", "package.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"2.0.0"`) {
		t.Errorf("content = %q", data)
	}
	// File name with spaces.
	data, err = g.ShowFile(ctx, "HEAD", "extra file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hi\n" {
		t.Errorf("content = %q", data)
	}
}

func TestShowFileNotExist(t *testing.T) {
	dir, first, _ := initRepo(t)
	g := newGit(t, dir)
	_, err := g.ShowFile(context.Background(), first, "missing.json")
	if !errors.Is(err, ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestFileExistsAtRev(t *testing.T) {
	dir, first, second := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	ok, err := g.FileExists(ctx, first, "extra file.txt")
	if err != nil || ok {
		t.Errorf("first: exists=%v err=%v, want false nil", ok, err)
	}
	ok, err = g.FileExists(ctx, second, "extra file.txt")
	if err != nil || !ok {
		t.Errorf("second: exists=%v err=%v, want true nil", ok, err)
	}
}

func TestWorktreeAddRemove(t *testing.T) {
	dir, first, _ := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	wt := filepath.Join(t.TempDir(), "wt with spaces")
	if err := g.AddWorktree(ctx, wt, first); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(wt, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"1.0.0"`) {
		t.Errorf("worktree has wrong content: %q", data)
	}

	// Writing inside the worktree must not affect the main repo.
	if err := os.WriteFile(filepath.Join(wt, "package.json"), []byte("scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainData, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainData), `"2.0.0"`) {
		t.Errorf("main repo content changed: %q", mainData)
	}

	if err := g.RemoveWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after removal: %v", err)
	}

	// The user's repository must be clean after add+remove.
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("main repo dirty after worktree ops:\n%s", out)
	}
}

func TestResetWorktreeRestoresTrackedAndRemovesUntrackedAndIgnored(t *testing.T) {
	dir, _, second := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	wt := filepath.Join(t.TempDir(), "wt with spaces")
	if err := g.AddWorktree(ctx, wt, second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.RemoveWorktree(context.Background(), wt) })

	if err := os.WriteFile(filepath.Join(wt, "package.json"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "untracked.txt"), []byte("untracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "node_modules", "pkg", "index.js"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := g.ResetWorktree(ctx, wt, second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(wt, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"2.0.0"`) {
		t.Errorf("tracked file was not restored: %q", data)
	}
	for _, path := range []string{"untracked.txt", "node_modules"} {
		if _, err := os.Stat(filepath.Join(wt, path)); !os.IsNotExist(err) {
			t.Errorf("%s survived worktree reset: %v", path, err)
		}
	}
}

func TestPruneWorktrees(t *testing.T) {
	dir, first, _ := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	wt := filepath.Join(t.TempDir(), "wt")
	if err := g.AddWorktree(ctx, wt, first); err != nil {
		t.Fatal(err)
	}
	// Simulate a worktree dir that vanished without `git worktree remove`.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := g.PruneWorktrees(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "wt") {
		t.Errorf("stale worktree entry survived prune:\n%s", out)
	}
}

func TestAddWorktreeInvalidRev(t *testing.T) {
	dir, _, _ := initRepo(t)
	g := newGit(t, dir)
	err := g.AddWorktree(context.Background(), filepath.Join(t.TempDir(), "wt"), "deadbeef")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIsPathDirty(t *testing.T) {
	dir, _, _ := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	dirty, err := g.IsPathDirty(ctx, "package.json")
	if err != nil || dirty {
		t.Errorf("clean repo: dirty=%v err=%v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = g.IsPathDirty(ctx, "package.json")
	if err != nil || !dirty {
		t.Errorf("modified file: dirty=%v err=%v", dirty, err)
	}
}

func TestRevisionFlagConfusionRejected(t *testing.T) {
	dir, _, _ := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	for _, rev := range []string{"", "-x", "--all"} {
		if _, err := g.ResolveRev(ctx, rev); err == nil {
			t.Errorf("ResolveRev(%q): expected error", rev)
		}
		if _, err := g.ShowFile(ctx, rev, "package.json"); err == nil {
			t.Errorf("ShowFile(%q): expected error", rev)
		}
		if err := g.AddWorktree(ctx, filepath.Join(t.TempDir(), "wt"), rev); err == nil {
			t.Errorf("AddWorktree(%q): expected error", rev)
		}
		if err := g.ResetWorktree(ctx, filepath.Join(t.TempDir(), "wt"), rev); err == nil {
			t.Errorf("ResetWorktree(%q): expected error", rev)
		}
	}
}

func TestNotARepo(t *testing.T) {
	g := newGit(t, t.TempDir())
	_, err := g.ResolveRev(context.Background(), "HEAD")
	if err == nil {
		t.Fatal("expected error outside a repository")
	}
}
