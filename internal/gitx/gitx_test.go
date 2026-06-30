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
	// The repository has commits, so the revision is simply missing; the
	// message must say so rather than blaming an empty repository.
	if !errors.Is(err, ErrNoSuchCommit) {
		t.Errorf("error %v should be ErrNoSuchCommit", err)
	}
}

// TestResolveRevNoCommits covers the unborn-HEAD path: git refuses with a
// non-zero exit and empty stderr, which must translate into an actionable
// "no commits yet" message instead of a cryptic "(no output)".
func TestResolveRevNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "-q", "-b", "main")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	g := newGit(t, dir)
	_, err := g.ResolveRev(context.Background(), "HEAD")
	if err == nil {
		t.Fatal("expected error in a repository with no commits")
	}
	if !errors.Is(err, ErrNoCommits) {
		t.Errorf("error %v should be ErrNoCommits", err)
	}
}

func TestRecentCommitsTouching(t *testing.T) {
	dir, _, _ := initRepo(t)
	g := newGit(t, dir)
	ctx := context.Background()

	// Both commits in initRepo modify package.json; newest ("two") first.
	commits, err := g.RecentCommitsTouching(ctx, "HEAD", []string{"package.json"}, 10)
	if err != nil {
		t.Fatalf("RecentCommitsTouching: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2: %+v", len(commits), commits)
	}
	if commits[0].Subject != "two" || commits[1].Subject != "one" {
		t.Errorf("subjects = [%q %q], want [two one]", commits[0].Subject, commits[1].Subject)
	}
	for _, c := range commits {
		if c.SHA == "" || c.ShortSHA == "" || c.Date == "" {
			t.Errorf("commit has empty field: %+v", c)
		}
	}

	// The limit caps the result count.
	if got, _ := g.RecentCommitsTouching(ctx, "HEAD", []string{"package.json"}, 1); len(got) != 1 {
		t.Errorf("with limit 1 got %d commits, want 1", len(got))
	}

	// Only the second commit added "extra file.txt".
	if got, _ := g.RecentCommitsTouching(ctx, "HEAD", []string{"extra file.txt"}, 10); len(got) != 1 {
		t.Errorf("path filter: got %d commits, want 1", len(got))
	}

	// A path nothing ever touched yields no commits.
	if got, _ := g.RecentCommitsTouching(ctx, "HEAD", []string{"never-existed.lock"}, 10); len(got) != 0 {
		t.Errorf("unknown path: got %d commits, want 0", len(got))
	}
}

func TestParseCommitLog(t *testing.T) {
	// A well-formed record, a blank line, and a malformed line with too few
	// fields — only the well-formed record should survive.
	out := []byte("abc\x1fab\x1f2024-01-02\x1fhello\n\nmalformed-line\n")
	got := parseCommitLog(out)
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1: %+v", len(got), got)
	}
	want := Commit{SHA: "abc", ShortSHA: "ab", Date: "2024-01-02", Subject: "hello"}
	if got[0] != want {
		t.Errorf("parseCommitLog = %+v, want %+v", got[0], want)
	}
}

func TestRecentCommitsTouchingErrors(t *testing.T) {
	ctx := context.Background()

	// An invalid revision is rejected before git runs.
	if _, err := fakeGit(execx.NewFake()).RecentCommitsTouching(ctx, "-bad", nil, 5); err == nil {
		t.Error("expected error for option-like revision")
	}
	// A runner error is wrapped and surfaced.
	wantErr := errors.New("exec failed")
	if _, err := fakeGit(errRunner(wantErr)).RecentCommitsTouching(ctx, "HEAD", nil, 5); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped %v", err, wantErr)
	}
	// A non-zero exit becomes an error naming the operation.
	_, err := fakeGit(exitRunner("bad revision")).RecentCommitsTouching(ctx, "HEAD", nil, 5)
	if err == nil || !strings.Contains(err.Error(), "list recent commits") {
		t.Errorf("err = %v, want 'list recent commits'", err)
	}
}

// TestResolveRevForeignStderr covers the branch where git fails with a
// non-empty stderr that is not a "not a git repository" error: the raw stderr
// must be surfaced verbatim rather than mapped to a sentinel.
func TestResolveRevForeignStderr(t *testing.T) {
	g := fakeGit(exitRunner("error: object file is empty"))
	_, err := g.ResolveRev(context.Background(), "HEAD")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "object file is empty") {
		t.Errorf("error %v should surface the raw git stderr", err)
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
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	g := newGit(t, t.TempDir())
	_, err := g.ResolveRev(context.Background(), "HEAD")
	if err == nil {
		t.Fatal("expected error outside a repository")
	}
	if !errors.Is(err, ErrNotARepo) {
		t.Errorf("error %v should be ErrNotARepo", err)
	}
}

// ---------------------------------------------------------------------------
// Error-path coverage using execx.Fake — no real git repo required.
//
// Every git.Run call has two error branches: (1) the runner itself returns a
// non-nil error (binary not found, context canceled, etc.) and (2) the
// command exits with a non-zero code. The integration tests above exercise
// the happy paths against a real repository; the tests below pin the error
// paths so that removing or mis-routing an error return is caught immediately.
// ---------------------------------------------------------------------------

// fakeGit constructs a Git bound to a scriptable fake runner. dir is
// arbitrary; it is only embedded in error messages, not used for real I/O.
func fakeGit(runner *execx.Fake) *Git {
	return New(runner, "/fake/repo")
}

// errRunner returns a Fake whose every call fails with the given error.
func errRunner(err error) *execx.Fake {
	f := execx.NewFake()
	f.Default = execx.Response{Err: err}
	return f
}

// exitRunner returns a Fake whose every call exits with code 1 and the
// given stderr text.
func exitRunner(stderr string) *execx.Fake {
	f := execx.NewFake()
	f.Default = execx.Response{Result: execx.Result{ExitCode: 1, Stderr: []byte(stderr)}}
	return f
}

// stubArgs stubs any command whose first arg matches first with resp.
func stubArgs(f *execx.Fake, first string, resp execx.Response) {
	f.Stub(func(c execx.Cmd) bool {
		return len(c.Args) > 0 && c.Args[0] == first
	}, resp)
}

// TestRunnerErrorPropagates verifies that when the underlying runner returns a
// non-nil error (e.g. context cancellation, binary not found), each Git method
// wraps and surfaces it rather than returning nil or swallowing the message.
func TestRunnerErrorPropagates(t *testing.T) {
	ctx := context.Background()
	execErr := errors.New("runner: exec failed")
	g := fakeGit(errRunner(execErr))

	cases := []struct {
		name string
		call func() error
	}{
		{"ResolveRev", func() error {
			_, err := g.ResolveRev(ctx, "HEAD")
			return err
		}},
		{"FileExists via blobRef", func() error {
			_, err := g.FileExists(ctx, "HEAD", "package.json")
			return err
		}},
		{"ShowFile via blobRef", func() error {
			_, err := g.ShowFile(ctx, "HEAD", "package.json")
			return err
		}},
		{"AddWorktree", func() error {
			return g.AddWorktree(ctx, "/wt", "HEAD")
		}},
		{"RemoveWorktree", func() error {
			return g.RemoveWorktree(ctx, "/wt")
		}},
		{"PruneWorktrees", func() error {
			return g.PruneWorktrees(ctx)
		}},
		{"IsPathDirty", func() error {
			_, err := g.IsPathDirty(ctx, "package.json")
			return err
		}},
		{"ResetWorktree", func() error {
			return g.ResetWorktree(ctx, "/wt", "HEAD")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("%s: got nil error on runner failure, want non-nil", tc.name)
			}
			if !errors.Is(err, execErr) {
				t.Errorf("%s: err = %v; want errors.Is(err, execErr) to be true (error must be wrapped, not swallowed)", tc.name, err)
			}
		})
	}
}

// TestNonZeroExitCodePropagates verifies that a git command that exits with a
// non-zero code produces an error whose message names the operation and
// includes the stderr output. Without the stderr, the user sees no hint about
// why git refused.
func TestNonZeroExitCodePropagates(t *testing.T) {
	ctx := context.Background()

	// Each case asserts both the operation name and the stderr text in one test
	// so that the table stays tidy and failures are self-contained.
	cases := []struct {
		name     string
		runner   *execx.Fake
		call     func(*Git) error
		wantMsgs []string // ALL substrings must appear in the error
	}{
		{
			name:     "RemoveWorktree",
			runner:   exitRunner("worktree is locked"),
			call:     func(g *Git) error { return g.RemoveWorktree(ctx, "/wt") },
			wantMsgs: []string{"remove worktree", "worktree is locked"},
		},
		{
			name:     "PruneWorktrees",
			runner:   exitRunner("cannot prune"),
			call:     func(g *Git) error { return g.PruneWorktrees(ctx) },
			wantMsgs: []string{"prune worktrees", "cannot prune"},
		},
		{
			name:     "IsPathDirty",
			runner:   exitRunner("not a git repo"),
			call:     func(g *Git) error { _, err := g.IsPathDirty(ctx, "package.json"); return err },
			wantMsgs: []string{"check status", "not a git repo"},
		},
		{
			// Empty stderr exercises firstLine's "no output" fallback.
			name:     "IsPathDirty without stderr",
			runner:   exitRunner(""),
			call:     func(g *Git) error { _, err := g.IsPathDirty(ctx, "package.json"); return err },
			wantMsgs: []string{"check status", "no output"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := fakeGit(tc.runner)
			err := tc.call(g)
			if err == nil {
				t.Fatalf("%s: got nil error on exit code 1, want non-nil", tc.name)
			}
			for _, want := range tc.wantMsgs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s: err = %q, want substring %q", tc.name, err, want)
				}
			}
		})
	}
}

// TestResetWorktreeErrorPaths covers all four error branches in ResetWorktree:
// the "git reset --hard" runner error, its non-zero exit, the "git clean"
// runner error, and its non-zero exit. The two reset branches and two clean
// branches are staged independently so each is isolated.
func TestResetWorktreeErrorPaths(t *testing.T) {
	ctx := context.Background()
	resetErr := errors.New("reset: exec failed")
	cleanErr := errors.New("clean: exec failed")

	successReset := execx.Response{Result: execx.Result{ExitCode: 0}}

	t.Run("reset runner error", func(t *testing.T) {
		f := errRunner(resetErr)
		err := fakeGit(f).ResetWorktree(ctx, "/wt", "HEAD")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !errors.Is(err, resetErr) {
			t.Errorf("err = %v; want errors.Is(err, resetErr)", err)
		}
		if !strings.Contains(err.Error(), "reset worktree") {
			t.Errorf("err = %q, want 'reset worktree'", err)
		}
	})

	t.Run("reset non-zero exit includes stderr", func(t *testing.T) {
		f := exitRunner("not a git repository")
		err := fakeGit(f).ResetWorktree(ctx, "/wt", "HEAD")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "reset worktree") {
			t.Errorf("err = %q, want 'reset worktree'", err)
		}
		if !strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("err = %q, want stderr 'not a git repository'", err)
		}
	})

	t.Run("clean runner error", func(t *testing.T) {
		f := errRunner(cleanErr)
		stubArgs(f, "reset", successReset) // reset succeeds; clean fails
		err := fakeGit(f).ResetWorktree(ctx, "/wt", "HEAD")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !errors.Is(err, cleanErr) {
			t.Errorf("err = %v; want errors.Is(err, cleanErr)", err)
		}
		if !strings.Contains(err.Error(), "clean worktree") {
			t.Errorf("err = %q, want 'clean worktree'", err)
		}
	})

	t.Run("clean non-zero exit includes stderr", func(t *testing.T) {
		f := exitRunner("clean refused")
		stubArgs(f, "reset", successReset) // reset succeeds; clean exits 1
		err := fakeGit(f).ResetWorktree(ctx, "/wt", "HEAD")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "clean worktree") {
			t.Errorf("err = %q, want 'clean worktree'", err)
		}
		if !strings.Contains(err.Error(), "clean refused") {
			t.Errorf("err = %q, want stderr 'clean refused'", err)
		}
	})
}

// TestShowFileCatFileErrors covers the two error branches that only fire once
// blobRef has successfully resolved the path to a blob hash. Both require
// staging the rev-parse call to succeed while cat-file then fails.
func TestShowFileCatFileErrors(t *testing.T) {
	ctx := context.Background()
	blobHash := "deadbeef1234deadbeef1234deadbeef12345678"

	// revParseSucceeds returns a fake that makes the rev-parse call return a
	// real-looking blob hash so blobRef succeeds and ShowFile reaches cat-file.
	revParseSucceeds := func() *execx.Fake {
		f := execx.NewFake()
		stubArgs(f, "rev-parse", execx.Response{
			Result: execx.Result{Stdout: []byte(blobHash + "\n")},
		})
		return f
	}

	t.Run("cat-file runner error", func(t *testing.T) {
		catErr := errors.New("cat-file: exec failed")
		f := revParseSucceeds()
		f.Default = execx.Response{Err: catErr}

		_, err := fakeGit(f).ShowFile(ctx, "HEAD", "package.json")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !errors.Is(err, catErr) {
			t.Errorf("err = %v; want errors.Is(err, catErr)", err)
		}
		if !strings.Contains(err.Error(), "read package.json") {
			t.Errorf("err = %q, want 'read package.json'", err)
		}
	})

	t.Run("cat-file non-zero exit includes stderr", func(t *testing.T) {
		f := revParseSucceeds()
		f.Default = execx.Response{
			Result: execx.Result{ExitCode: 128, Stderr: []byte("not a valid object\n")},
		}

		_, err := fakeGit(f).ShowFile(ctx, "HEAD", "package.json")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "read package.json") {
			t.Errorf("err = %q, want 'read package.json'", err)
		}
		if !strings.Contains(err.Error(), "not a valid object") {
			t.Errorf("err = %q, want stderr in message", err)
		}
	})
}

// TestContextCancellationPropagates verifies that a canceled context causes
// each Git method to return a context.Canceled-wrapped error rather than
// blocking or returning nil. Context cancellation is the primary real-world
// trigger for the runner-error branches exercised in TestRunnerErrorPropagates.
//
// AddWorktree, FileExists, and ShowFile are omitted here: execx.Fake checks
// ctx.Err() before every call, so all three are guaranteed to propagate
// cancellation identically to the methods below. The omission is intentional,
// not an oversight.
func TestContextCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before any call

	g := fakeGit(execx.NewFake()) // Fake checks ctx.Err() before executing

	calls := []struct {
		name string
		call func() error
	}{
		{"ResolveRev", func() error { _, err := g.ResolveRev(ctx, "HEAD"); return err }},
		{"RemoveWorktree", func() error { return g.RemoveWorktree(ctx, "/wt") }},
		{"PruneWorktrees", func() error { return g.PruneWorktrees(ctx) }},
		{"IsPathDirty", func() error { _, err := g.IsPathDirty(ctx, "pkg"); return err }},
		{"ResetWorktree", func() error { return g.ResetWorktree(ctx, "/wt", "HEAD") }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: err = %v; want errors.Is(err, context.Canceled)", tc.name, err)
			}
		})
	}
}
