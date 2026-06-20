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
	"github.com/skyneticist/depbisect/internal/manifest"
	"github.com/skyneticist/depbisect/internal/pm"
	"github.com/skyneticist/depbisect/internal/verify"
)

// fakeGit serves file content per revision from memory and materializes
// worktrees as plain directories.
type fakeGit struct {
	mu                 sync.Mutex
	files              map[string]map[string]string // sha -> path -> content
	revs               map[string]string            // rev expr -> sha (identity for shas)
	resets             int
	removed            []string
	pruned             int
	dirty              map[string]bool
	failWorktreeRemove bool
	onReset            func()
	onRemove           func()
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

func (g *fakeGit) ResetWorktree(ctx context.Context, dir, rev string) error {
	g.mu.Lock()
	g.resets++
	g.mu.Unlock()
	if g.onReset != nil {
		g.onReset()
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return g.AddWorktree(ctx, dir, rev)
}

func (g *fakeGit) RemoveWorktree(ctx context.Context, dir string) error {
	if g.onRemove != nil {
		g.onRemove()
	}
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

// readDeps parses the flattened dependency specs of the candidate manifest in
// dir. It prefers package.json (the JavaScript fixtures); a cargo-only repo has
// no package.json and falls through to Cargo.toml.
func readDeps(t testing.TB, dir string) map[string]string {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
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
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		t.Fatalf("read candidate manifest: %v", err)
	}
	c, err := manifest.ParseCargoToml(data)
	if err != nil {
		t.Fatalf("parse candidate Cargo.toml: %v", err)
	}
	deps := map[string]string{}
	for _, sec := range c.Sections {
		for k, v := range sec {
			deps[k] = v
		}
	}
	return deps
}

// fakeInstaller pretends to install; it can be scripted to fail for certain
// manifests and records every install.
type fakeInstaller struct {
	mu            sync.Mutex
	installs      int
	failWhen      func(deps map[string]string) bool
	onInstall     func(dir string)
	waitForCancel bool
	t             testing.TB
}

func (f *fakeInstaller) Version(context.Context) (string, error) {
	return "npm test-version", nil
}

func (f *fakeInstaller) Install(ctx context.Context, dir string, stream io.Writer) (execx.Result, error) {
	f.mu.Lock()
	f.installs++
	f.mu.Unlock()
	if f.waitForCancel {
		<-ctx.Done()
		return execx.Result{}, ctx.Err()
	}
	if f.onInstall != nil {
		f.onInstall(dir)
	}
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
	onVerify func()
	// flakyFailures, when non-nil, supplies Failures for call i (cycling).
	flakyFailures []int
	t             testing.TB
}

func (f *fakeVerifier) Verify(ctx context.Context, dir string, stopOnPass bool) (verify.Verdict, error) {
	if err := ctx.Err(); err != nil {
		return verify.Verdict{}, err
	}
	if f.onVerify != nil {
		f.onVerify()
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

type fakeCheckpointStore struct {
	checkpoint *Checkpoint
	starts     int
	appends    int
	clears     int
}

func (s *fakeCheckpointStore) Load() (*Checkpoint, error) {
	if s.checkpoint == nil {
		return nil, os.ErrNotExist
	}
	cp := *s.checkpoint
	cp.Fingerprint.Command = append([]string(nil), cp.Fingerprint.Command...)
	cp.Fingerprint.Changes = append([]string(nil), cp.Fingerprint.Changes...)
	cp.Trials = append([]Trial(nil), cp.Trials...)
	return &cp, nil
}

func (s *fakeCheckpointStore) Start(cp Checkpoint) error {
	s.starts++
	cp.Fingerprint.Command = append([]string(nil), cp.Fingerprint.Command...)
	cp.Fingerprint.Changes = append([]string(nil), cp.Fingerprint.Changes...)
	cp.Trials = nil
	s.checkpoint = &cp
	return nil
}

func (s *fakeCheckpointStore) Append(trial Trial) error {
	s.appends++
	s.checkpoint.Trials = append(s.checkpoint.Trials, trial)
	return nil
}

func (s *fakeCheckpointStore) Clear() error {
	s.clears++
	s.checkpoint = nil
	return nil
}

type trialProgressEvent struct {
	number  int
	role    string
	applied int
	total   int
	phase   string
	elapsed time.Duration
}

type recordingProgress struct {
	steps  []string
	trials []trialProgressEvent
}

func (p *recordingProgress) Step(label, format string, args ...any) {
	p.steps = append(p.steps, label+" "+fmt.Sprintf(format, args...))
}

func (p *recordingProgress) Detail(string, ...any) {}

func (p *recordingProgress) Trial(number int, role string, applied, total int, phase string, elapsed time.Duration) {
	p.trials = append(p.trials, trialProgressEvent{
		number: number, role: role, applied: applied, total: total, phase: phase, elapsed: elapsed,
	})
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

const cargoLockOld = `version = 3
[[package]]
name = "alpha"
version = "1.0.0"
[[package]]
name = "beta"
version = "3.0.0"
[[package]]
name = "gamma"
version = "5.0.0"
`
const cargoLockNew = `version = 3
[[package]]
name = "alpha"
version = "1.1.0"
[[package]]
name = "beta"
version = "3.2.0"
[[package]]
name = "gamma"
version = "5.5.0"
`

func threeChangeCargoRepo() *fakeGit {
	const baseToml = "[package]\nname = \"app\"\n\n[dependencies]\nalpha = \"1.0.0\"\nbeta = \"3.0.0\"\ngamma = \"5.0.0\"\n"
	const headToml = "[package]\nname = \"app\"\n\n[dependencies]\nalpha = \"1.1.0\"\nbeta = \"3.2.0\"\ngamma = \"5.5.0\"\n"
	return &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {"Cargo.toml": baseToml, "Cargo.lock": cargoLockOld},
			"sha-head": {"Cargo.toml": headToml, "Cargo.lock": cargoLockNew},
		},
	}
}

// TestRunFindsSingleCulpritCargo exercises the whole engine over a Rust repo:
// auto-detection, Cargo.toml diff/render, and Cargo.lock annotation.
func TestRunFindsSingleCulpritCargo(t *testing.T) {
	env := newEnv(t, threeChangeCargoRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if res.PackageManager != "cargo" {
		t.Errorf("pm = %q, want cargo", res.PackageManager)
	}
	if got := minimalNames(res); ids(got) != "beta" {
		t.Errorf("minimal = %v, want [beta]", got)
	}
	if res.Minimal[0].OldResolved != "3.0.0" || res.Minimal[0].NewResolved != "3.2.0" {
		t.Errorf("resolved versions not annotated from Cargo.lock: %+v", res.Minimal[0])
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

func TestRunParallelMatchesSequentialSingleCulprit(t *testing.T) {
	failBeta := func(deps map[string]string) bool { return deps["beta"] == "3.2.0" }

	seqEnv := newEnv(t, threeChangeRepo(), failBeta, 3)
	seqRes, err := seqEnv.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}

	parEnv := newEnv(t, threeChangeRepo(), failBeta, 3)
	parOpts := baseOpts()
	parOpts.Jobs = 3
	parRes, err := parEnv.eng.Run(context.Background(), parOpts)
	if err != nil {
		t.Fatal(err)
	}

	if parRes.Outcome != OutcomeMinimalFound {
		t.Fatalf("parallel outcome = %q, diagnostics %v", parRes.Outcome, parRes.Diagnostics)
	}
	if got := ids(minimalNames(parRes)); got != "beta" {
		t.Errorf("parallel minimal = %v, want [beta]", minimalNames(parRes))
	}
	if got, want := ids(minimalNames(parRes)), ids(minimalNames(seqRes)); got != want {
		t.Errorf("parallel minimal %q != sequential %q", got, want)
	}
	if !parRes.MinimalityProven {
		t.Error("parallel run did not prove minimality")
	}
	if parRes.Confidence != seqRes.Confidence {
		t.Errorf("parallel confidence %+v != sequential %+v", parRes.Confidence, seqRes.Confidence)
	}
	// The pool created and cleaned up one worktree per lane (3 changes -> 3 lanes).
	if len(parEnv.git.removed) != 3 {
		t.Errorf("parallel worktrees removed = %d, want 3", len(parEnv.git.removed))
	}
	for _, d := range parEnv.tempDirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("temp dir %s still exists", d)
		}
	}
}

func TestRunParallelFindsInteractingPair(t *testing.T) {
	failPair := func(deps map[string]string) bool {
		return deps["alpha"] == "1.1.0" && deps["gamma"] == "5.5.0"
	}
	env := newEnv(t, threeChangeRepo(), failPair, 1)
	opts := baseOpts()
	opts.Jobs = 4 // exceeds the 3 changes; lane count clamps to 3
	res, err := env.eng.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if got := ids(minimalNames(res)); got != "alpha,gamma" {
		t.Errorf("minimal = %v, want [alpha gamma]", minimalNames(res))
	}
	if len(env.git.removed) != 3 {
		t.Errorf("worktrees removed = %d, want 3 (jobs clamped to changes)", len(env.git.removed))
	}
}

// TestRunParallelDeterministicResult runs the same parallel bisection several
// times; the minimal set must not depend on lane scheduling.
func TestRunParallelDeterministicResult(t *testing.T) {
	failPair := func(deps map[string]string) bool {
		return deps["alpha"] == "1.1.0" && deps["gamma"] == "5.5.0"
	}
	var want string
	for i := 0; i < 8; i++ {
		env := newEnv(t, threeChangeRepo(), failPair, 1)
		opts := baseOpts()
		opts.Jobs = 3
		res, err := env.eng.Run(context.Background(), opts)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		got := ids(minimalNames(res))
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("run %d minimal = %q, want %q (nondeterministic under parallelism)", i, got, want)
		}
	}
}

func TestRunResetsWorktreeBeforeEveryExecutedTrial(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	var contaminated bool
	env.installer.onInstall = func(dir string) {
		marker := filepath.Join(dir, "node_modules", ".depbisect-previous-trial")
		if _, err := os.Stat(marker); err == nil {
			contaminated = true
		}
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("left by installer"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "generated.txt"), []byte("left by verifier"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if contaminated {
		t.Fatal("a trial observed filesystem state left by an earlier trial")
	}
	if env.git.resets != len(res.Trials) {
		t.Errorf("worktree resets = %d, executed trials = %d", env.git.resets, len(res.Trials))
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
	if !res.MinimalityProven {
		t.Error("unrelated unresolved trials must not invalidate a proven singleton result")
	}
}

func TestRunRequiredUnresolvedNeighborMakesResultInconclusive(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["alpha"] == "1.1.0" && deps["beta"] == "3.2.0"
	}, 1)
	// The pair {alpha,beta} fails, but removing alpha leaves {beta}, which
	// cannot be installed. The pair is therefore only the best-known set,
	// not a proven 1-minimal result.
	env.installer.failWhen = func(deps map[string]string) bool {
		return deps["alpha"] == "1.0.0" &&
			deps["beta"] == "3.2.0" &&
			deps["gamma"] == "5.0.0"
	}

	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeInconclusive {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeInconclusive)
	}
	if got := minimalNames(res); ids(got) != "alpha,beta" {
		t.Fatalf("best-known set = %v, want [alpha beta]", got)
	}
	if res.MinimalityProven {
		t.Fatal("minimality reported as proven despite an unresolved required neighbor")
	}
	if res.UnresolvedTrials == 0 {
		t.Fatal("unresolved trial count was not recorded")
	}
	if !strings.Contains(res.OutcomeDetail, "best-known") {
		t.Errorf("outcome detail = %q, want best-known qualification", res.OutcomeDetail)
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

func TestRunResumesCompletedTrialsFromCheckpoint(t *testing.T) {
	store := &fakeCheckpointStore{}
	ctx, cancel := context.WithCancel(context.Background())
	first := newEnv(t, threeChangeRepo(), nil, 1)
	first.eng.Progress = &recordingProgress{}
	first.eng.Verifier = first.verifier
	first.verifier.failWhen = func(deps map[string]string) bool {
		if first.verifier.calls == 3 {
			cancel()
		}
		return deps["beta"] == "3.2.0"
	}
	opts := baseOpts()
	opts.Checkpoint = store
	opts.CheckpointContext = "run-timeout=0s"

	_, err := first.eng.Run(ctx, opts)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error = %v, want cancellation", err)
	}
	if store.checkpoint == nil || len(store.checkpoint.Trials) < 2 {
		t.Fatalf("checkpoint = %+v, want completed trials", store.checkpoint)
	}
	savedTrials := len(store.checkpoint.Trials)

	second := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	progress := &recordingProgress{}
	second.eng.Progress = progress
	opts.Resume = true
	res, err := second.eng.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if res.ResumedTrials != savedTrials {
		t.Errorf("resumed trials = %d, want %d", res.ResumedTrials, savedTrials)
	}
	if second.verifier.calls >= len(res.Trials) {
		t.Errorf("verifier calls = %d, total trials = %d; checkpoint was not reused",
			second.verifier.calls, len(res.Trials))
	}
	if store.starts != 1 {
		t.Errorf("checkpoint starts = %d, want 1", store.starts)
	}
	if store.clears != 1 || store.checkpoint != nil {
		t.Errorf("completed checkpoint was not cleared: clears=%d checkpoint=%+v", store.clears, store.checkpoint)
	}
	if len(progress.trials) == 0 || progress.trials[0].number != savedTrials+1 {
		t.Errorf("continued trial numbering = %+v, want first new trial %d", progress.trials, savedTrials+1)
	}
	if !containsString(progress.steps, "Resume") {
		t.Errorf("progress steps = %v, want resume notice", progress.steps)
	}
}

func TestRunResumeRejectsMismatchedCheckpoint(t *testing.T) {
	store := &fakeCheckpointStore{
		checkpoint: &Checkpoint{
			SchemaVersion: CheckpointSchemaVersion,
			Fingerprint: CheckpointFingerprint{
				BaseSHA:               "sha-base",
				ToSHA:                 "sha-head",
				PackageManager:        "npm",
				PackageManagerVersion: "npm different-version",
				Command:               []string{"npm", "test"},
				Runs:                  3,
				Changes: []string{
					"dependencies:alpha",
					"dependencies:beta",
					"dependencies:gamma",
				},
				Context: "run-timeout=0s",
			},
			StartedAt: time.Now(),
		},
	}
	env := newEnv(t, threeChangeRepo(), nil, 1)
	opts := baseOpts()
	opts.Checkpoint = store
	opts.CheckpointContext = "run-timeout=0s"
	opts.Resume = true

	_, err := env.eng.Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "checkpoint does not match") {
		t.Fatalf("err = %v, want checkpoint mismatch", err)
	}
	if len(env.tempDirs) != 0 {
		t.Fatal("mismatched checkpoint should be rejected before creating a worktree")
	}
}

func TestRunReportsTrialPhasesAndElapsedTime(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	progress := &recordingProgress{}
	env.eng.Progress = progress

	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(progress.trials) < len(res.Trials)*3 {
		t.Fatalf("trial progress events = %d, want at least %d", len(progress.trials), len(res.Trials)*3)
	}
	phases := map[string]bool{}
	for _, event := range progress.trials {
		phases[event.phase] = true
		if event.number < 1 || event.applied < 0 || event.total != len(res.Changes) {
			t.Errorf("invalid trial progress event: %+v", event)
		}
	}
	for _, phase := range []string{"preparing", "installing", "verifying", "pass", "fail"} {
		if !phases[phase] {
			t.Errorf("missing progress phase %q in %+v", phase, progress.trials)
		}
	}
}

func TestRunRecordsPhaseTimingsAndCleanupInCompletedWallTime(t *testing.T) {
	git := threeChangeRepo()
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)

	current := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) { current = current.Add(d) }
	env.eng.Now = func() time.Time { return current }
	git.onReset = func() { advance(2 * time.Second) }
	env.installer.onInstall = func(string) { advance(3 * time.Second) }
	env.verifier.onVerify = func() { advance(5 * time.Second) }
	git.onRemove = func() { advance(7 * time.Second) }

	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trials) == 0 {
		t.Fatal("expected recorded trials")
	}
	for i, trial := range res.Trials {
		if trial.PrepareDuration != 2*time.Second {
			t.Errorf("trial %d prepare duration = %v, want 2s", i+1, trial.PrepareDuration)
		}
		if trial.InstallDuration != 3*time.Second {
			t.Errorf("trial %d install duration = %v, want 3s", i+1, trial.InstallDuration)
		}
		if trial.VerifyDuration != 5*time.Second {
			t.Errorf("trial %d verify duration = %v, want 5s", i+1, trial.VerifyDuration)
		}
		if trial.Duration != 10*time.Second {
			t.Errorf("trial %d total duration = %v, want 10s", i+1, trial.Duration)
		}
	}
	if res.CleanupDuration != 7*time.Second {
		t.Errorf("cleanup duration = %v, want 7s", res.CleanupDuration)
	}
	wantWall := time.Duration(len(res.Trials))*10*time.Second + 7*time.Second
	if got := res.FinishedAt.Sub(res.StartedAt); got != wantWall {
		t.Errorf("completed wall time = %v, want %v", got, wantWall)
	}
}

func TestRunInstallTimeoutIsDistinctAndCleansUp(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), nil, 1)
	env.installer.waitForCancel = true
	opts := baseOpts()
	opts.InstallTimeout = 10 * time.Millisecond

	_, err := env.eng.Run(context.Background(), opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "dependency installation timed out after 10ms") {
		t.Fatalf("err = %v, want install timeout detail", err)
	}
	if len(env.git.removed) != 1 {
		t.Fatalf("removed worktrees = %d, want 1", len(env.git.removed))
	}
}

func TestRunOverallTimeoutIsDistinctAndCleansUp(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), nil, 1)
	env.installer.waitForCancel = true
	opts := baseOpts()
	opts.OverallTimeout = 10 * time.Millisecond

	_, err := env.eng.Run(context.Background(), opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "bisection timed out after 10ms") {
		t.Fatalf("err = %v, want overall timeout detail", err)
	}
	if len(env.git.removed) != 1 {
		t.Fatalf("removed worktrees = %d, want 1", len(env.git.removed))
	}
}

func TestRunDoesNotMislabelParentDeadlineAsOverallTimeout(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), nil, 1)
	env.installer.waitForCancel = true
	opts := baseOpts()
	opts.OverallTimeout = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := env.eng.Run(ctx, opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if strings.Contains(err.Error(), "bisection timed out after 1h0m0s") {
		t.Fatalf("parent deadline mislabeled as configured overall timeout: %v", err)
	}
}

func containsString(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
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

func TestRunDetectsAmbiguousEcosystems(t *testing.T) {
	git := threeChangeRepo()
	// A package.json (with its lockfile) and a Cargo.toml both present at --to
	// is ambiguous without --pm.
	git.files["sha-head"]["Cargo.toml"] = "[package]\nname = \"x\"\nversion = \"0.1.0\"\n"
	env := newEnv(t, git, nil, 1)
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "both package.json and Cargo.toml") {
		t.Fatalf("err = %v, want ambiguous-ecosystem error", err)
	}
	if len(env.tempDirs) != 0 {
		t.Error("no worktree should be created when detection is ambiguous")
	}
}

func TestRunNoManifestFound(t *testing.T) {
	git := &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {"README.md": "a"},
			"sha-head": {"README.md": "b"},
		},
	}
	env := newEnv(t, git, nil, 1)
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "no supported manifest") {
		t.Fatalf("err = %v, want no-manifest error", err)
	}
}

func TestRunPMOverrideWinsOverDetection(t *testing.T) {
	git := threeChangeRepo()
	// A stray Cargo.toml would make auto-detection ambiguous; --pm npm forces
	// the JavaScript ecosystem regardless.
	git.files["sha-base"]["Cargo.toml"] = "[package]\nname = \"x\"\nversion = \"0.1.0\"\n"
	git.files["sha-head"]["Cargo.toml"] = "[package]\nname = \"x\"\nversion = \"0.1.0\"\n"
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	opts := baseOpts()
	opts.PMOverride = "npm"
	res, err := env.eng.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.PackageManager != "npm" {
		t.Errorf("pm = %q, want npm from override", res.PackageManager)
	}
	if ids(minimalNames(res)) != "beta" {
		t.Errorf("minimal = %v, want [beta]", minimalNames(res))
	}
}
