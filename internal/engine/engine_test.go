package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyneticist/depbisect/internal/execx"
	"github.com/skyneticist/depbisect/internal/pm"
	"github.com/skyneticist/depbisect/internal/verify"
)

// fakeGit serves file content per revision from memory and materializes
// worktrees as plain directories.
type fakeGit struct {
	mu                 sync.Mutex
	files              map[string]map[string]string // sha -> path -> content
	revs               map[string]string            // rev expr -> sha (identity for shas)
	removed            []string
	pruned             int
	dirty              map[string]bool
	failWorktreeRemove bool
}

func (g *fakeGit) resolve(rev string) (string, bool) {
	if sha, ok := g.revs[rev]; ok {
		return sha, true
	}
	if _, ok := g.files[rev]; ok {
		return rev, true
	}
	return "", false
}

func (g *fakeGit) ResolveRev(ctx context.Context, rev string) (string, error) {
	if sha, ok := g.resolve(rev); ok {
		return sha, nil
	}
	return "", fmt.Errorf("resolve revision %q: unknown", rev)
}

func (g *fakeGit) ShowFile(ctx context.Context, rev, path string) ([]byte, error) {
	sha, ok := g.resolve(rev)
	if !ok {
		return nil, fmt.Errorf("unknown rev %q", rev)
	}
	content, ok := g.files[sha][path]
	if !ok {
		return nil, fmt.Errorf("%s at %s: %w", path, rev, errNotExistForTest)
	}
	return []byte(content), nil
}

func (g *fakeGit) FileExists(ctx context.Context, rev, path string) (bool, error) {
	sha, ok := g.resolve(rev)
	if !ok {
		return false, fmt.Errorf("unknown rev %q", rev)
	}
	_, ok = g.files[sha][path]
	return ok, nil
}

func (g *fakeGit) AddWorktree(ctx context.Context, dir, rev string) error {
	sha, ok := g.resolve(rev)
	if !ok {
		return fmt.Errorf("unknown rev %q", rev)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for path, content := range g.files[sha] {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (g *fakeGit) RemoveWorktree(ctx context.Context, dir string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failWorktreeRemove {
		return errors.New("worktree remove refused")
	}
	g.removed = append(g.removed, dir)
	return os.RemoveAll(dir)
}

func (g *fakeGit) PruneWorktrees(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruned++
	return nil
}

func (g *fakeGit) IsPathDirty(ctx context.Context, path string) (bool, error) {
	return g.dirty[path], nil
}

var errNotExistForTest = errors.New("file does not exist at revision")

// readDeps parses the dependencies section of the package.json in dir.
func readDeps(t testing.TB, dir string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatalf("read candidate manifest: %v", err)
	}
	var doc struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse candidate manifest: %v", err)
	}
	deps := map[string]string{}
	for k, v := range doc.Dependencies {
		deps[k] = v
	}
	for k, v := range doc.DevDependencies {
		deps[k] = v
	}
	return deps
}

// fakeInstaller pretends to install; it can be scripted to fail for certain
// manifests and records every install.
type fakeInstaller struct {
	mu       sync.Mutex
	installs int
	failWhen func(deps map[string]string) bool
	t        testing.TB
}

func (f *fakeInstaller) CheckAvailable() error { return nil }

func (f *fakeInstaller) Install(ctx context.Context, dir string, stream io.Writer) (execx.Result, error) {
	f.mu.Lock()
	f.installs++
	f.mu.Unlock()
	if f.failWhen != nil && f.failWhen(readDeps(f.t, dir)) {
		return execx.Result{ExitCode: 1, Stderr: []byte("ERESOLVE")}, nil
	}
	return execx.Result{ExitCode: 0}, nil
}

// fakeVerifier fails when failWhen(deps) is true; flakySeq can override with
// scripted per-call fail counts.
type fakeVerifier struct {
	mu       sync.Mutex
	calls    int
	runs     int
	failWhen func(deps map[string]string) bool
	// flakyFailures, when non-nil, supplies Failures for call i (cycling).
	flakyFailures []int
	t             testing.TB
}

func (f *fakeVerifier) Verify(ctx context.Context, dir string, stopOnPass bool) (verify.Verdict, error) {
	if err := ctx.Err(); err != nil {
		return verify.Verdict{}, err
	}
	f.mu.Lock()
	call := f.calls
	f.calls++
	f.mu.Unlock()

	runs := f.runs
	if runs < 1 {
		runs = 1
	}
	v := verify.Verdict{Planned: runs}
	failures := 0
	if f.flakyFailures != nil {
		failures = f.flakyFailures[call%len(f.flakyFailures)]
	} else if f.failWhen(readDeps(f.t, dir)) {
		failures = runs
	}
	for i := 0; i < runs; i++ {
		code := 0
		if i < failures {
			code = 1
		}
		v.Runs = append(v.Runs, verify.RunResult{ExitCode: code, Duration: time.Millisecond})
		if code != 0 {
			v.Failures++
		} else if stopOnPass {
			break
		}
	}
	return v, nil
}

const lockOld = `{"lockfileVersion":3,"packages":{"":{} ,"node_modules/alpha":{"version":"1.0.0"},"node_modules/beta":{"version":"3.0.0"},"node_modules/gamma":{"version":"5.0.0"}}}`
const lockNew = `{"lockfileVersion":3,"packages":{"":{} ,"node_modules/alpha":{"version":"1.1.0"},"node_modules/beta":{"version":"3.2.0"},"node_modules/gamma":{"version":"5.5.0"}}}`

func threeChangeRepo() *fakeGit {
	return &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {
				"package.json":      `{"name":"app","dependencies":{"alpha":"1.0.0","beta":"3.0.0","gamma":"5.0.0"}}`,
				"package-lock.json": lockOld,
			},
			"sha-head": {
				"package.json":      `{"name":"app","dependencies":{"alpha":"1.1.0","beta":"3.2.0","gamma":"5.5.0"}}`,
				"package-lock.json": lockNew,
			},
		},
	}
}

type testEnv struct {
	git       *fakeGit
	installer *fakeInstaller
	verifier  *fakeVerifier
	tempDirs  []string
	eng       *Engine
}

func newEnv(t *testing.T, git *fakeGit, failWhen func(map[string]string) bool, runs int) *testEnv {
	t.Helper()
	env := &testEnv{
		git:       git,
		installer: &fakeInstaller{t: t},
		verifier:  &fakeVerifier{failWhen: failWhen, runs: runs, t: t},
	}
	env.eng = &Engine{
		Git:          git,
		NewInstaller: func(m pm.Manager) Installer { return env.installer },
		Verifier:     env.verifier,
		Now:          func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		MkdirTemp: func() (string, error) {
			d := filepath.Join(t.TempDir(), fmt.Sprintf("depbisect-%d", len(env.tempDirs)))
			if err := os.MkdirAll(d, 0o755); err != nil {
				return "", err
			}
			env.tempDirs = append(env.tempDirs, d)
			return d, nil
		},
	}
	return env
}

func baseOpts() Options {
	return Options{
		BaseRev: "base",
		ToRev:   "HEAD",
		Command: []string{"npm", "test"},
		Runs:    3,
	}
}

func ids(changes []string) string { return strings.Join(changes, ",") }

func minimalNames(res *Result) []string {
	var out []string
	for _, c := range res.Minimal {
		out = append(out, c.Name)
	}
	return out
}

func TestRunFindsSingleCulprit(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if got := minimalNames(res); ids(got) != "beta" {
		t.Errorf("minimal = %v, want [beta]", got)
	}
	if len(res.Changes) != 3 {
		t.Errorf("changes = %d, want 3", len(res.Changes))
	}
	if res.Minimal[0].OldResolved != "3.0.0" || res.Minimal[0].NewResolved != "3.2.0" {
		t.Errorf("resolved versions not annotated: %+v", res.Minimal[0])
	}
	if res.BaseSHA != "sha-base" || res.ToSHA != "sha-head" {
		t.Errorf("SHAs = %q %q", res.BaseSHA, res.ToSHA)
	}
	if res.PackageManager != "npm" {
		t.Errorf("pm = %q", res.PackageManager)
	}
	// Confidence: the minimal set's final verification failed all runs.
	if res.Confidence.Failures != 3 || res.Confidence.Runs != 3 {
		t.Errorf("confidence = %+v", res.Confidence)
	}
	// Worktree cleaned up.
	if len(env.git.removed) != 1 {
		t.Errorf("worktrees removed = %v", env.git.removed)
	}
	for _, d := range env.tempDirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("temp dir %s still exists", d)
		}
	}
	// Trials must include both baselines and at least one candidate.
	roles := map[string]int{}
	for _, tr := range res.Trials {
		roles[tr.Role]++
	}
	if roles["baseline-old"] != 1 || roles["baseline-new"] != 1 || roles["candidate"] == 0 {
		t.Errorf("trial roles = %v", roles)
	}
}

func TestRunFindsInteractingPair(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["alpha"] == "1.1.0" && deps["gamma"] == "5.5.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if got := minimalNames(res); ids(got) != "alpha,gamma" {
		t.Errorf("minimal = %v, want [alpha gamma]", got)
	}
}

func TestRunNotReproduced(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(map[string]string) bool { return false }, 2)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeNotReproduced {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if len(env.git.removed) != 1 {
		t.Errorf("worktree not cleaned up")
	}
}

func TestRunFailsAtBase(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(map[string]string) bool { return true }, 2)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeFailsAtBase {
		t.Fatalf("outcome = %q", res.Outcome)
	}
}

func TestRunFlakyBaselineInconclusive(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), nil, 3)
	// Baseline-old passes cleanly, baseline-new is flaky (1 of 3 failed).
	env.verifier.flakyFailures = []int{0, 1}
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeInconclusive {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if !strings.Contains(strings.Join(res.Diagnostics, "\n"), "flaky") {
		t.Errorf("diagnostics missing flakiness explanation: %v", res.Diagnostics)
	}
}

func TestRunZeroChanges(t *testing.T) {
	git := threeChangeRepo()
	git.files["sha-head"]["package.json"] = git.files["sha-base"]["package.json"]
	git.files["sha-head"]["package-lock.json"] = git.files["sha-base"]["package-lock.json"]
	env := newEnv(t, git, func(map[string]string) bool { return true }, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeNoChanges {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if len(env.tempDirs) != 0 {
		t.Errorf("no worktree should be created for zero changes")
	}
	if env.installer.installs != 0 || env.verifier.calls != 0 {
		t.Errorf("no installs or verifications expected")
	}
}

func TestRunLockfileOnlyChangesDiagnosed(t *testing.T) {
	git := threeChangeRepo()
	// Same specs, different resolution for alpha.
	git.files["sha-head"]["package.json"] = git.files["sha-base"]["package.json"]
	env := newEnv(t, git, func(map[string]string) bool { return true }, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeNoChanges {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if len(res.LockfileOnly) != 3 {
		t.Fatalf("lockfile-only = %+v", res.LockfileOnly)
	}
	joined := strings.Join(res.Diagnostics, "\n")
	if !strings.Contains(joined, "lockfile") {
		t.Errorf("diagnostics = %v, want lockfile-only explanation", res.Diagnostics)
	}
}

func TestRunDryRun(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(map[string]string) bool { return true }, 1)
	opts := baseOpts()
	opts.DryRun = true
	res, err := env.eng.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDryRun {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if len(res.Changes) != 3 {
		t.Errorf("changes = %d", len(res.Changes))
	}
	if len(env.tempDirs) != 0 || env.installer.installs != 0 || env.verifier.calls != 0 {
		t.Error("dry run must not create worktrees, install, or verify")
	}
}

func TestRunKeepWorktrees(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	opts := baseOpts()
	opts.KeepWorktrees = true
	res, err := env.eng.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.KeptWorktree == "" {
		t.Fatal("KeptWorktree empty")
	}
	if _, err := os.Stat(res.KeptWorktree); err != nil {
		t.Errorf("kept worktree missing: %v", err)
	}
	if len(env.git.removed) != 0 {
		t.Errorf("worktree was removed despite KeepWorktrees")
	}
}

func TestRunInstallFailureIsUnresolved(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	// Installs fail whenever alpha is new AND beta is old: those candidates
	// become unresolved, but bisection must still find beta.
	env.installer.failWhen = func(deps map[string]string) bool {
		return deps["alpha"] == "1.1.0" && deps["beta"] == "3.0.0"
	}
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if got := minimalNames(res); ids(got) != "beta" {
		t.Errorf("minimal = %v", got)
	}
	unresolved := 0
	for _, tr := range res.Trials {
		if tr.Outcome == "unresolved" {
			unresolved++
		}
	}
	if unresolved == 0 {
		t.Error("expected at least one unresolved trial recorded")
	}
}

func TestRunBaselineInstallFailureIsFatal(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(map[string]string) bool { return true }, 1)
	env.installer.failWhen = func(map[string]string) bool { return true }
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil {
		t.Fatal("expected error when baseline install fails")
	}
	if len(env.git.removed) != 1 {
		t.Error("worktree must be cleaned up after fatal error")
	}
}

func TestRunCancellationCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	env := newEnv(t, threeChangeRepo(), nil, 1)
	calls := 0
	env.verifier.failWhen = func(deps map[string]string) bool {
		calls++
		if calls == 2 {
			cancel() // cancel during the second verification
		}
		return deps["beta"] == "3.2.0"
	}
	_, err := env.eng.Run(ctx, baseOpts())
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(env.git.removed) != 1 {
		t.Error("worktree must be cleaned up after cancellation")
	}
	for _, d := range env.tempDirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("temp dir %s still exists", d)
		}
	}
}

func TestRunWorktreeRemoveFallback(t *testing.T) {
	git := threeChangeRepo()
	git.failWorktreeRemove = true
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if git.pruned == 0 {
		t.Error("expected worktree prune after failed removal")
	}
	for _, d := range env.tempDirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("temp dir %s still exists after fallback cleanup", d)
		}
	}
}

func TestRunSameCommitRejected(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), nil, 1)
	opts := baseOpts()
	opts.BaseRev = "HEAD"
	_, err := env.eng.Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "same commit") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunWorkspacesUnsupported(t *testing.T) {
	git := threeChangeRepo()
	git.files["sha-head"]["package.json"] = `{"name":"app","workspaces":["packages/*"],"dependencies":{"alpha":"1.1.0"}}`
	env := newEnv(t, git, nil, 1)
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunPnpmWorkspaceFileUnsupported(t *testing.T) {
	git := threeChangeRepo()
	delete(git.files["sha-head"], "package-lock.json")
	delete(git.files["sha-base"], "package-lock.json")
	git.files["sha-head"]["pnpm-lock.yaml"] = "lockfileVersion: '6.0'\n"
	git.files["sha-base"]["pnpm-lock.yaml"] = "lockfileVersion: '6.0'\n"
	git.files["sha-head"]["pnpm-workspace.yaml"] = "packages:\n  - packages/*\n"
	env := newEnv(t, git, nil, 1)
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunMissingManifestAtBase(t *testing.T) {
	git := threeChangeRepo()
	delete(git.files["sha-base"], "package.json")
	env := newEnv(t, git, nil, 1)
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "package.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunMalformedLockfileWarnsButProceeds(t *testing.T) {
	git := threeChangeRepo()
	git.files["sha-head"]["package-lock.json"] = "{not json"
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if !strings.Contains(strings.Join(res.Diagnostics, "\n"), "lockfile") {
		t.Errorf("expected lockfile parse warning, got %v", res.Diagnostics)
	}
}

func TestRunDirtyManifestWarning(t *testing.T) {
	git := threeChangeRepo()
	git.dirty = map[string]bool{"package.json": true}
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Diagnostics, "\n"), "uncommitted") {
		t.Errorf("expected uncommitted-changes warning, got %v", res.Diagnostics)
	}
}

func TestRunZeroChangesWithDirtyManifestStillWarns(t *testing.T) {
	// A user whose uncommitted package.json edits are the only difference
	// gets "no changes" — the dirty warning is essential context there.
	git := threeChangeRepo()
	git.files["sha-head"]["package.json"] = git.files["sha-base"]["package.json"]
	git.files["sha-head"]["package-lock.json"] = git.files["sha-base"]["package-lock.json"]
	git.dirty = map[string]bool{"package.json": true}
	env := newEnv(t, git, nil, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeNoChanges {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if !strings.Contains(strings.Join(res.Diagnostics, "\n"), "uncommitted") {
		t.Errorf("diagnostics = %v, want uncommitted-changes warning", res.Diagnostics)
	}
}

func TestRunMemoizesRepeatedSubsets(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, tr := range res.Trials {
		seen[ids(tr.Applied)]++
	}
	for subset, n := range seen {
		if n > 1 {
			t.Errorf("subset %q executed %d times; memoization broken", subset, n)
		}
	}
	if env.verifier.calls != len(res.Trials)-countUnresolved(res) {
		// every executed, resolvable trial corresponds to exactly one verify
		t.Errorf("verifier calls = %d, trials = %d", env.verifier.calls, len(res.Trials))
	}
}

func countUnresolved(res *Result) int {
	n := 0
	for _, tr := range res.Trials {
		if tr.Outcome == "unresolved" {
			n++
		}
	}
	return n
}

func TestRunManyChangesScales(t *testing.T) {
	// 40 dependencies, one culprit: ensure trial count stays modest and the
	// result is exact.
	basePkg := map[string]string{}
	newPkg := map[string]string{}
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("dep%02d", i)
		basePkg[name] = "1.0.0"
		newPkg[name] = "1.1.0"
	}
	mk := func(deps map[string]string) string {
		b, _ := json.Marshal(map[string]any{"name": "big", "dependencies": deps})
		return string(b)
	}
	git := &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {"package.json": mk(basePkg), "package-lock.json": `{"lockfileVersion":3,"packages":{"":{}}}`},
			"sha-head": {"package.json": mk(newPkg), "package-lock.json": `{"lockfileVersion":3,"packages":{"":{}}}`},
		},
	}
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["dep23"] == "1.1.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if got := minimalNames(res); ids(got) != "dep23" {
		t.Errorf("minimal = %v", got)
	}
	if len(res.Trials) > 60 {
		t.Errorf("trials = %d, want O(log n)-ish for single culprit", len(res.Trials))
	}
}
