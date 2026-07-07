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
	failPrune          bool
	onReset            func()
	onRemove           func()
	// failAddWorktreeAfter, when > 0, causes AddWorktree to return an error
	// starting at the (failAddWorktreeAfter+1)th call. Used to test partial
	// worktree-pool setup cleanup.
	failAddWorktreeAfter int
	addWorktreeCalls     int
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
	g.mu.Lock()
	g.addWorktreeCalls++
	calls := g.addWorktreeCalls
	g.mu.Unlock()
	if g.failAddWorktreeAfter > 0 && calls > g.failAddWorktreeAfter {
		return fmt.Errorf("AddWorktree: simulated failure at call %d", calls)
	}
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
	if g.failPrune {
		return errors.New("prune refused by git")
	}
	return nil
}

func (g *fakeGit) IsPathDirty(ctx context.Context, path string) (bool, error) {
	return g.dirty[path], nil
}

var errNotExistForTest = errors.New("file does not exist at revision")

// readDeps parses the flattened dependency specs of the candidate manifest in
// dir. It prefers package.json (the JavaScript fixtures); a repo without one
// falls through to Cargo.toml, then go.mod.
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
	if data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml")); err == nil {
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
	if data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")); err == nil {
		p, err := manifest.ParsePyproject(data)
		if err != nil {
			t.Fatalf("parse candidate pyproject.toml: %v", err)
		}
		deps := map[string]string{}
		for _, sec := range p.Sections {
			for k, v := range sec {
				deps[k] = v
			}
		}
		return deps
	}
	if data, err := os.ReadFile(filepath.Join(dir, "composer.json")); err == nil {
		c, err := manifest.ParseComposerJSON(data)
		if err != nil {
			t.Fatalf("parse candidate composer.json: %v", err)
		}
		deps := map[string]string{}
		for _, sec := range c.Sections {
			for k, v := range sec {
				deps[k] = v
			}
		}
		return deps
	}
	if data, err := os.ReadFile(filepath.Join(dir, "requirements.txt")); err == nil {
		r, err := manifest.ParseRequirements(data)
		if err != nil {
			t.Fatalf("parse candidate requirements.txt: %v", err)
		}
		deps := map[string]string{}
		for _, sec := range r.Sections {
			for k, v := range sec {
				deps[k] = v
			}
		}
		return deps
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("read candidate manifest: %v", err)
	}
	g, err := manifest.ParseGoMod(data)
	if err != nil {
		t.Fatalf("parse candidate go.mod: %v", err)
	}
	deps := map[string]string{}
	for _, sec := range g.Sections {
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
	// failAsTimeout makes failing runs report a per-run timeout (TimedOut set)
	// instead of a non-zero exit code.
	failAsTimeout bool
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
		rr := verify.RunResult{ExitCode: code, Duration: time.Millisecond}
		if code != 0 && f.failAsTimeout {
			rr.ExitCode = 0
			rr.TimedOut = true
		}
		v.Runs = append(v.Runs, rr)
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
	steps   []string
	details []string
	trials  []trialProgressEvent
}

func (p *recordingProgress) Step(label, format string, args ...any) {
	p.steps = append(p.steps, label+" "+fmt.Sprintf(format, args...))
}

func (p *recordingProgress) Detail(format string, args ...any) {
	p.details = append(p.details, fmt.Sprintf(format, args...))
}

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
		NewVerifier:  func(m pm.Manager) Verifier { return env.verifier },
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

const yarnLockOld = `# yarn lockfile v1

alpha@1.0.0:
  version "1.0.0"

beta@3.0.0:
  version "3.0.0"

gamma@5.0.0:
  version "5.0.0"
`
const yarnLockNew = `# yarn lockfile v1

alpha@1.1.0:
  version "1.1.0"

beta@3.2.0:
  version "3.2.0"

gamma@5.5.0:
  version "5.5.0"
`

func threeChangeYarnRepo() *fakeGit {
	git := threeChangeRepo()
	for _, sha := range []string{"sha-base", "sha-head"} {
		delete(git.files[sha], "package-lock.json")
	}
	git.files["sha-base"]["yarn.lock"] = yarnLockOld
	git.files["sha-head"]["yarn.lock"] = yarnLockNew
	return git
}

// TestRunFindsSingleCulpritYarn exercises the whole engine over a yarn repo:
// yarn.lock auto-detection, the shared package.json diff/render, and
// yarn.lock (classic v1) annotation.
func TestRunFindsSingleCulpritYarn(t *testing.T) {
	env := newEnv(t, threeChangeYarnRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if res.PackageManager != "yarn" {
		t.Errorf("pm = %q, want yarn", res.PackageManager)
	}
	if got := minimalNames(res); ids(got) != "beta" {
		t.Errorf("minimal = %v, want [beta]", got)
	}
	if res.Minimal[0].OldResolved != "3.0.0" || res.Minimal[0].NewResolved != "3.2.0" {
		t.Errorf("resolved versions not annotated from yarn.lock: %+v", res.Minimal[0])
	}
}

const pnpmLockOld = `lockfileVersion: '6.0'

dependencies:
  alpha:
    specifier: 1.0.0
    version: 1.0.0
  beta:
    specifier: 3.0.0
    version: 3.0.0
  gamma:
    specifier: 5.0.0
    version: 5.0.0
`
const pnpmLockNew = `lockfileVersion: '6.0'

dependencies:
  alpha:
    specifier: 1.1.0
    version: 1.1.0
  beta:
    specifier: 3.2.0
    version: 3.2.0
  gamma:
    specifier: 5.5.0
    version: 5.5.0
`

func threeChangePnpmRepo() *fakeGit {
	git := threeChangeRepo()
	for _, sha := range []string{"sha-base", "sha-head"} {
		delete(git.files[sha], "package-lock.json")
	}
	git.files["sha-base"]["pnpm-lock.yaml"] = pnpmLockOld
	git.files["sha-head"]["pnpm-lock.yaml"] = pnpmLockNew
	return git
}

// TestRunFindsSingleCulpritPnpm exercises the whole engine over a pnpm repo:
// pnpm-lock.yaml auto-detection, the shared package.json diff/render, and
// pnpm-lock.yaml (v6) annotation. The v5 and v9 lockfile formats are covered
// by the manifest unit tests.
func TestRunFindsSingleCulpritPnpm(t *testing.T) {
	env := newEnv(t, threeChangePnpmRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if res.PackageManager != "pnpm" {
		t.Errorf("pm = %q, want pnpm", res.PackageManager)
	}
	if got := minimalNames(res); ids(got) != "beta" {
		t.Errorf("minimal = %v, want [beta]", got)
	}
	if res.Minimal[0].OldResolved != "3.0.0" || res.Minimal[0].NewResolved != "3.2.0" {
		t.Errorf("resolved versions not annotated from pnpm-lock.yaml: %+v", res.Minimal[0])
	}
}

const goSumOld = `example.com/alpha v1.0.0 h1:aaa
example.com/alpha v1.0.0/go.mod h1:aaamod
example.com/beta v1.3.0 h1:bbb
example.com/beta v1.3.0/go.mod h1:bbbmod
example.com/gamma v1.5.0 h1:ccc
example.com/gamma v1.5.0/go.mod h1:cccmod
`
const goSumNew = `example.com/alpha v1.1.0 h1:aaa
example.com/alpha v1.1.0/go.mod h1:aaamod
example.com/beta v1.4.0 h1:bbb
example.com/beta v1.4.0/go.mod h1:bbbmod
example.com/gamma v1.6.0 h1:ccc
example.com/gamma v1.6.0/go.mod h1:cccmod
`

func threeChangeGoRepo() *fakeGit {
	const baseMod = "module example.com/app\n\ngo 1.20\n\nrequire (\n\texample.com/alpha v1.0.0\n\texample.com/beta v1.3.0\n\texample.com/gamma v1.5.0\n)\n"
	const headMod = "module example.com/app\n\ngo 1.20\n\nrequire (\n\texample.com/alpha v1.1.0\n\texample.com/beta v1.4.0\n\texample.com/gamma v1.6.0\n)\n"
	return &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {"go.mod": baseMod, "go.sum": goSumOld},
			"sha-head": {"go.mod": headMod, "go.sum": goSumNew},
		},
	}
}

// TestRunFindsSingleCulpritGo exercises the whole engine over a Go repo:
// go.mod auto-detection, modfile diff/render, and go.sum annotation.
func TestRunFindsSingleCulpritGo(t *testing.T) {
	env := newEnv(t, threeChangeGoRepo(), func(deps map[string]string) bool {
		return deps["example.com/beta"] == "v1.4.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if res.PackageManager != "go" {
		t.Errorf("pm = %q, want go", res.PackageManager)
	}
	if got := minimalNames(res); ids(got) != "example.com/beta" {
		t.Errorf("minimal = %v, want [example.com/beta]", got)
	}
	if res.Minimal[0].OldResolved != "v1.3.0" || res.Minimal[0].NewResolved != "v1.4.0" {
		t.Errorf("resolved versions not annotated from go.sum: %+v", res.Minimal[0])
	}
}

const uvLockOld = `version = 1
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
const uvLockNew = `version = 1
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

func threeChangePythonRepo() *fakeGit {
	const basePy = "[project]\nname = \"app\"\nrequires-python = \">=3.9\"\ndependencies = [\"alpha>=1.0.0\", \"beta>=3.0.0\", \"gamma>=5.0.0\"]\n"
	const headPy = "[project]\nname = \"app\"\nrequires-python = \">=3.9\"\ndependencies = [\"alpha>=1.1.0\", \"beta>=3.2.0\", \"gamma>=5.5.0\"]\n"
	return &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {"pyproject.toml": basePy, "uv.lock": uvLockOld},
			"sha-head": {"pyproject.toml": headPy, "uv.lock": uvLockNew},
		},
	}
}

// TestRunFindsSingleCulpritPython exercises the whole engine over a Python repo:
// pyproject.toml auto-detection (via uv.lock), [project.dependencies] diff/render,
// and uv.lock annotation.
func TestRunFindsSingleCulpritPython(t *testing.T) {
	env := newEnv(t, threeChangePythonRepo(), func(deps map[string]string) bool {
		return deps["beta"] == ">=3.2.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if res.PackageManager != "uv" {
		t.Errorf("pm = %q, want uv", res.PackageManager)
	}
	if got := minimalNames(res); ids(got) != "beta" {
		t.Errorf("minimal = %v, want [beta]", got)
	}
	if res.Minimal[0].OldResolved != "3.0.0" || res.Minimal[0].NewResolved != "3.2.0" {
		t.Errorf("resolved versions not annotated from uv.lock: %+v", res.Minimal[0])
	}
}

func composerLock(alpha, beta, gamma string) string {
	return `{
  "packages": [
    {"name": "acme/alpha", "version": "` + alpha + `"},
    {"name": "acme/beta", "version": "` + beta + `"},
    {"name": "acme/gamma", "version": "` + gamma + `"}
  ],
  "packages-dev": []
}`
}

func threeChangeComposerRepo() *fakeGit {
	const baseJSON = `{"name":"acme/app","require":{"acme/alpha":"^1.0.0","acme/beta":"^3.0.0","acme/gamma":"^5.0.0"}}`
	const headJSON = `{"name":"acme/app","require":{"acme/alpha":"^1.1.0","acme/beta":"^3.2.0","acme/gamma":"^5.5.0"}}`
	return &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {"composer.json": baseJSON, "composer.lock": composerLock("1.0.0", "3.0.0", "5.0.0")},
			"sha-head": {"composer.json": headJSON, "composer.lock": composerLock("1.1.0", "3.2.0", "5.5.0")},
		},
	}
}

// TestRunFindsSingleCulpritComposer exercises the whole engine over a PHP repo:
// composer.json auto-detection, require diff/render, and composer.lock
// annotation.
func TestRunFindsSingleCulpritComposer(t *testing.T) {
	env := newEnv(t, threeChangeComposerRepo(), func(deps map[string]string) bool {
		return deps["acme/beta"] == "^3.2.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if res.PackageManager != "composer" {
		t.Errorf("pm = %q, want composer", res.PackageManager)
	}
	if got := minimalNames(res); ids(got) != "acme/beta" {
		t.Errorf("minimal = %v, want [acme/beta]", got)
	}
	if res.Minimal[0].OldResolved != "3.0.0" || res.Minimal[0].NewResolved != "3.2.0" {
		t.Errorf("resolved versions not annotated from composer.lock: %+v", res.Minimal[0])
	}
}

func threeChangePipRepo() *fakeGit {
	const baseReq = "# app pins\n--no-index\nalpha==1.0.0\nbeta==3.0.0\ngamma==5.0.0\n"
	const headReq = "# app pins\n--no-index\nalpha==1.1.0\nbeta==3.2.0\ngamma==5.5.0\n"
	return &fakeGit{
		revs: map[string]string{"base": "sha-base", "HEAD": "sha-head"},
		files: map[string]map[string]string{
			"sha-base": {"requirements.txt": baseReq},
			"sha-head": {"requirements.txt": headReq},
		},
	}
}

// TestRunFindsSingleCulpritPip exercises the whole engine over a pip repo:
// requirements.txt auto-detection, line diff/render, and resolved-version
// annotation from the file's own exact pins (pip has no separate lockfile).
func TestRunFindsSingleCulpritPip(t *testing.T) {
	env := newEnv(t, threeChangePipRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "==3.2.0"
	}, 3)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, diagnostics %v", res.Outcome, res.Diagnostics)
	}
	if res.PackageManager != "pip" {
		t.Errorf("pm = %q, want pip", res.PackageManager)
	}
	if got := minimalNames(res); ids(got) != "beta" {
		t.Errorf("minimal = %v, want [beta]", got)
	}
	if res.Minimal[0].OldResolved != "3.0.0" || res.Minimal[0].NewResolved != "3.2.0" {
		t.Errorf("resolved versions not annotated from the requirements pins: %+v", res.Minimal[0])
	}
	// Pins derive from the specs themselves, so pip can never report
	// lockfile-only drift.
	if len(res.LockfileOnly) != 0 {
		t.Errorf("LockfileOnly = %v, want none for pip", res.LockfileOnly)
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

// TestRunFailsAtBaseTimeoutHint verifies that when the baseline fails purely
// because every run timed out, the fails-without-updates detail points the user
// at --run-timeout instead of implying a genuinely broken command.
func TestRunFailsAtBaseTimeoutHint(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(map[string]string) bool { return true }, 2)
	env.verifier.failAsTimeout = true
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeFailsAtBase {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeFailsAtBase)
	}
	if !strings.Contains(res.OutcomeDetail, "--run-timeout") {
		t.Errorf("OutcomeDetail should hint at --run-timeout, got: %q", res.OutcomeDetail)
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

func TestRunDirtyYarnLockWarnsOnNpmRepo(t *testing.T) {
	// warnDirty probes all three JS lockfiles regardless of the detected
	// manager, so a stray dirty yarn.lock surfaces even on an npm repo.
	git := threeChangeRepo()
	git.dirty = map[string]bool{"yarn.lock": true}
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Diagnostics, "\n")
	if !strings.Contains(joined, "yarn.lock") || !strings.Contains(joined, "uncommitted") {
		t.Errorf("diagnostics = %v, want uncommitted yarn.lock warning", res.Diagnostics)
	}
}

func TestRunDirtyRequirementsWarnsOnce(t *testing.T) {
	// requirements.txt is pip's manifest and lockfile at once; a dirty copy
	// must produce a single warning, not one per role.
	git := threeChangePipRepo()
	git.dirty = map[string]bool{"requirements.txt": true}
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "==3.2.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	var warnings int
	for _, d := range res.Diagnostics {
		if strings.Contains(d, "uncommitted") && strings.Contains(d, "requirements.txt") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("dirty requirements.txt warned %d times, want exactly once: %v", warnings, res.Diagnostics)
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
	first.eng.NewVerifier = func(m pm.Manager) Verifier { return first.verifier }
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
	if err == nil || !strings.Contains(err.Error(), "multiple manifests found") {
		t.Fatalf("err = %v, want ambiguous-ecosystem error", err)
	}
	if len(env.tempDirs) != 0 {
		t.Error("no worktree should be created when detection is ambiguous")
	}
}

func TestRunDetectsAmbiguousJSLockfiles(t *testing.T) {
	// package-lock.json and yarn.lock both present at --to is ambiguous
	// without --pm, and the error names the colliding lockfiles.
	git := threeChangeRepo()
	git.files["sha-head"]["yarn.lock"] = yarnLockNew
	env := newEnv(t, git, nil, 1)
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "multiple lockfiles found") ||
		!strings.Contains(err.Error(), "yarn.lock") {
		t.Fatalf("err = %v, want ambiguous-lockfile error naming yarn.lock", err)
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

// ---------------------------------------------------------------------------
// validateCheckpoint — direct unit tests for every rejection condition.
//
// These complement TestRunResumeRejectsMismatchedCheckpoint, which exercises
// one mismatch case through the full engine. Direct tests here pin each
// individual guard so that silently removing or weakening a check will break
// a specific, named test rather than a subtle integration failure.
// ---------------------------------------------------------------------------

// goodFP returns a fingerprint that validateCheckpoint will accept when paired
// with goodCP. Mutate one field at a time to test each rejection condition.
func goodFP() CheckpointFingerprint {
	return CheckpointFingerprint{
		BaseSHA:               "abc123",
		ToSHA:                 "def456",
		PackageManager:        "npm",
		PackageManagerVersion: "npm 10.0.0",
		Command:               []string{"npm", "test"},
		Runs:                  1,
		Changes:               []string{"dependencies:alpha", "dependencies:beta"},
		Context:               "run-timeout=0s",
	}
}

// goodCP returns a checkpoint that passes validateCheckpoint against goodFP().
func goodCP() *Checkpoint {
	fp := goodFP()
	return &Checkpoint{
		SchemaVersion: CheckpointSchemaVersion,
		Fingerprint:   fp,
		StartedAt:     time.Now(),
	}
}

func TestValidateCheckpointNilIsRejected(t *testing.T) {
	if err := validateCheckpoint(nil, goodFP()); err == nil {
		t.Fatal("validateCheckpoint(nil, ...) returned nil; want error")
	}
}

func TestValidateCheckpointSchemaVersionMismatch(t *testing.T) {
	for _, version := range []int{0, CheckpointSchemaVersion - 1, CheckpointSchemaVersion + 1, 99} {
		cp := goodCP()
		cp.SchemaVersion = version
		err := validateCheckpoint(cp, goodFP())
		if err == nil {
			t.Errorf("schema version %d: want error, got nil", version)
		} else if !strings.Contains(err.Error(), "schema version") {
			t.Errorf("schema version %d: error = %q, want 'schema version'", version, err)
		}
	}
}

// TestValidateCheckpointFingerprintMismatch verifies that every field of the
// fingerprint is independently checked. Removing any one comparison from
// checkpointFingerprintsEqual would leave a test failing here rather than
// silently allowing a mismatched resume.
func TestValidateCheckpointFingerprintMismatch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CheckpointFingerprint)
	}{
		{"BaseSHA", func(f *CheckpointFingerprint) { f.BaseSHA = "000000" }},
		{"ToSHA", func(f *CheckpointFingerprint) { f.ToSHA = "000000" }},
		{"PackageManager", func(f *CheckpointFingerprint) { f.PackageManager = "pnpm" }},
		{"PackageManagerVersion", func(f *CheckpointFingerprint) { f.PackageManagerVersion = "npm 9.0.0" }},
		{"Runs", func(f *CheckpointFingerprint) { f.Runs++ }},
		{"Context", func(f *CheckpointFingerprint) { f.Context = "run-timeout=30s" }},
		// Command: extra element (length change)
		{"Command extra element", func(f *CheckpointFingerprint) { f.Command = append(f.Command, "--coverage") }},
		// Command: same length, different content — exercises slices.Equal element comparison
		{"Command different content", func(f *CheckpointFingerprint) { f.Command = []string{"npm", "build"} }},
		// Changes: extra element
		{"Changes extra element", func(f *CheckpointFingerprint) {
			f.Changes = append(f.Changes, "dependencies:gamma")
		}},
		// Changes: same length, different content — exercises slices.Equal element comparison
		{"Changes different content", func(f *CheckpointFingerprint) {
			f.Changes = []string{"dependencies:alpha", "dependencies:DIFFERENT"}
		}},
		// Changes: same elements, different order — fingerprint comparison is order-sensitive
		{"Changes reordered", func(f *CheckpointFingerprint) {
			f.Changes = []string{"dependencies:beta", "dependencies:alpha"}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := goodCP()
			want := goodFP()
			tc.mutate(&cp.Fingerprint) // checkpoint has the "old" fingerprint
			err := validateCheckpoint(cp, want)
			if err == nil {
				t.Fatalf("fingerprint field %q: validateCheckpoint returned nil; want mismatch error", tc.name)
			}
			if !strings.Contains(err.Error(), "does not match") {
				t.Errorf("fingerprint field %q: error = %q, want 'does not match'", tc.name, err)
			}
		})
	}
}

func TestValidateCheckpointZeroStartTime(t *testing.T) {
	cp := goodCP()
	cp.StartedAt = time.Time{} // zero value
	err := validateCheckpoint(cp, goodFP())
	if err == nil {
		t.Fatal("zero StartedAt: want error, got nil")
	}
	if !strings.Contains(err.Error(), "start time") {
		t.Errorf("error = %q, want 'start time'", err)
	}
}

// TestValidateCheckpointInvalidTrialOutcome verifies that only the three
// defined outcomes ("pass", "fail", "unresolved") are accepted. An unknown
// outcome string indicates file corruption or a version mismatch and must
// not be silently skipped; the engine would miscount verdicts.
func TestValidateCheckpointInvalidTrialOutcome(t *testing.T) {
	fp := goodFP()
	invalidOutcomes := []string{"", "error", "skip", "PASS", "FAIL", "unknown"}
	for _, outcome := range invalidOutcomes {
		t.Run(outcome, func(t *testing.T) {
			cp := goodCP()
			cp.Trials = []Trial{
				{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: outcome},
			}
			err := validateCheckpoint(cp, fp)
			if err == nil {
				t.Fatalf("outcome %q: want error, got nil", outcome)
			}
			if !strings.Contains(err.Error(), "invalid outcome") {
				t.Errorf("outcome %q: error = %q, want 'invalid outcome'", outcome, err)
			}
		})
	}

	// Confirm all three valid outcomes are accepted so we don't over-restrict.
	for _, outcome := range []string{"pass", "fail", "unresolved"} {
		t.Run("valid/"+outcome, func(t *testing.T) {
			cp := goodCP()
			cp.Trials = []Trial{
				{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: outcome},
			}
			if err := validateCheckpoint(cp, fp); err != nil {
				t.Errorf("outcome %q should be valid, got error: %v", outcome, err)
			}
		})
	}
}

// TestValidateCheckpointTrialUnknownChange verifies that a trial whose Applied
// list references a change ID not in the fingerprint's Changes is rejected.
// This catches a checkpoint written for a different dependency graph being
// applied to the current run, which would silently skip or double-apply changes.
func TestValidateCheckpointTrialUnknownChange(t *testing.T) {
	fp := goodFP() // Changes = ["dependencies:alpha", "dependencies:beta"]
	cp := goodCP()
	cp.Trials = []Trial{
		{
			Role:    "candidate",
			Applied: []string{"dependencies:alpha", "dependencies:UNKNOWN"},
			Outcome: "fail",
		},
	}
	err := validateCheckpoint(cp, fp)
	if err == nil {
		t.Fatal("unknown change in Applied: want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown change") {
		t.Errorf("error = %q, want 'unknown change'", err)
	}
}

// TestValidateCheckpointDuplicateTrialSubset verifies that two trials with
// identical Applied sets are rejected. Duplicate subsets corrupt the memo
// table: the engine would skip re-evaluating combinations it hasn't actually
// tested yet, potentially returning a wrong minimal set.
func TestValidateCheckpointDuplicateTrialSubset(t *testing.T) {
	fp := goodFP()

	t.Run("same Applied slice", func(t *testing.T) {
		cp := goodCP()
		cp.Trials = []Trial{
			{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: "fail"},
			{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: "pass"},
		}
		err := validateCheckpoint(cp, fp)
		if err == nil {
			t.Fatal("duplicate Applied: want error, got nil")
		}
		if !strings.Contains(err.Error(), "duplicate trial subset") {
			t.Errorf("error = %q, want 'duplicate trial subset'", err)
		}
	})

	// subsetKeyIDs sorts before hashing, so Applied=["b","a"] and Applied=["a","b"]
	// are the same subset and must also be rejected.
	t.Run("same elements different order", func(t *testing.T) {
		cp := goodCP()
		cp.Trials = []Trial{
			{Role: "candidate", Applied: []string{"dependencies:alpha", "dependencies:beta"}, Outcome: "fail"},
			{Role: "candidate", Applied: []string{"dependencies:beta", "dependencies:alpha"}, Outcome: "pass"},
		}
		err := validateCheckpoint(cp, fp)
		if err == nil {
			t.Fatal("reordered Applied that is the same subset: want duplicate error, got nil")
		}
		if !strings.Contains(err.Error(), "duplicate trial subset") {
			t.Errorf("error = %q, want 'duplicate trial subset'", err)
		}
	})
}

// TestValidateCheckpointHappyPath confirms that well-formed checkpoints with
// zero or more trials pass without error. These are the steady-state cases
// that the engine relies on during every --resume run.
func TestValidateCheckpointHappyPath(t *testing.T) {
	fp := goodFP()

	t.Run("no trials", func(t *testing.T) {
		if err := validateCheckpoint(goodCP(), fp); err != nil {
			t.Fatalf("valid checkpoint with no trials: %v", err)
		}
	})

	t.Run("baseline and candidate trials", func(t *testing.T) {
		cp := goodCP()
		cp.Trials = []Trial{
			// baseline-old: no changes applied, represented as nil Applied
			{Role: "baseline-old", Applied: nil, Outcome: "pass"},
			// baseline-new: all changes applied
			{Role: "baseline-new", Applied: []string{"dependencies:alpha", "dependencies:beta"}, Outcome: "fail"},
			// candidate: a proper subset
			{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: "fail"},
		}
		if err := validateCheckpoint(cp, fp); err != nil {
			t.Fatalf("valid checkpoint with trials: %v", err)
		}
	})

	t.Run("unresolved trial is valid", func(t *testing.T) {
		cp := goodCP()
		cp.Trials = []Trial{
			{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: "unresolved"},
		}
		if err := validateCheckpoint(cp, fp); err != nil {
			t.Fatalf("unresolved outcome should be accepted: %v", err)
		}
	})
}

// TestValidateCheckpointTrialIndexInError confirms that the error message
// identifies which trial number failed. Without a clear index the user cannot
// tell which record is corrupt when inspecting a checkpoint file manually.
func TestValidateCheckpointTrialIndexInError(t *testing.T) {
	fp := goodFP()
	cp := goodCP()
	cp.Trials = []Trial{
		{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: "pass"},
		{Role: "candidate", Applied: []string{"dependencies:beta"}, Outcome: "pass"},
		{Role: "candidate", Applied: []string{"dependencies:alpha", "dependencies:beta"}, Outcome: "INVALID"},
	}
	err := validateCheckpoint(cp, fp)
	if err == nil {
		t.Fatal("want error for invalid outcome on trial 3")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("error %q should identify trial 3 by index", err)
	}
}

// ---------------------------------------------------------------------------
// cleanup / removeWorktree — direct unit tests for uncovered branches
// ---------------------------------------------------------------------------

// TestCleanupKeepWorktreesParallelRemovesExtras verifies the parallel
// --keep-worktrees path: when more than one worktree was created (e.g. -j3),
// only dirs[0] is kept for the user to inspect; every additional lane must be
// cleaned up. A stale extra worktree is indistinguishable from the minimal
// set and would silently mislead the user.
func TestCleanupKeepWorktreesParallelRemovesExtras(t *testing.T) {
	git := &fakeGit{}
	progress := &recordingProgress{}
	eng := &Engine{Git: git}

	// Simulate three lanes created under a parent temp directory.
	parent := t.TempDir()
	dirs := []string{
		filepath.Join(parent, "worktree-0"),
		filepath.Join(parent, "worktree-1"),
		filepath.Join(parent, "worktree-2"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	res := &Result{}
	eng.cleanup(parent, dirs, true /* keep */, res, progress)

	// Only the first worktree should be kept.
	if res.KeptWorktree != dirs[0] {
		t.Errorf("KeptWorktree = %q, want %q", res.KeptWorktree, dirs[0])
	}
	if _, err := os.Stat(dirs[0]); err != nil {
		t.Errorf("dirs[0] was removed but should be kept: %v", err)
	}

	// Extra worktrees must be gone.
	for _, extra := range dirs[1:] {
		if _, err := os.Stat(extra); !os.IsNotExist(err) {
			t.Errorf("extra worktree %s still exists; should have been removed", extra)
		}
	}

	// A diagnostic must mention the number of worktrees so the user knows the
	// kept one is not necessarily the final minimal-set worktree.
	found := false
	for _, d := range res.Diagnostics {
		if strings.Contains(d, "3") && strings.Contains(d, "worktree") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("diagnostics %v should mention the 3-worktree count", res.Diagnostics)
	}
}

// TestRemoveWorktreePruneFailureSurfacesViaProgress verifies that when both
// git-worktree-remove and the subsequent git-worktree-prune fail, the engine
// logs the prune failure via progress.Detail rather than swallowing it. This
// is the only feedback the user has that the git administrative state may be
// inconsistent after a run.
func TestRemoveWorktreePruneFailureSurfacesViaProgress(t *testing.T) {
	git := &fakeGit{failWorktreeRemove: true, failPrune: true}
	progress := &recordingProgress{}
	eng := &Engine{Git: git}

	// Create a real directory so os.RemoveAll (the fallback inside
	// removeWorktree) has something to act on.
	dir := t.TempDir()
	eng.removeWorktree(dir, progress)

	// The directory should be gone via the os.RemoveAll fallback even though
	// git refused to remove the worktree.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir %s still exists after removeWorktree fallback", dir)
	}

	// Prune failure must be surfaced so the operator knows git's worktree
	// list may be stale.
	found := false
	for _, d := range progress.details {
		if strings.Contains(d, "worktree prune failed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("progress.details = %v; want a 'worktree prune failed' message", progress.details)
	}
}

// ---------------------------------------------------------------------------
// HarnessVerifier — confirms the adapter correctly wires stopOnPass through
// to the underlying verify.Harness. This is the only adapter-level logic:
// the engine controls early-stop per phase.
// ---------------------------------------------------------------------------

// fixedExitRunner always returns the configured exit code, recording each call.
type fixedExitRunner struct {
	exitCode int
	calls    int
}

func (r *fixedExitRunner) Run(_ context.Context, _ execx.Cmd) (execx.Result, error) {
	r.calls++
	return execx.Result{ExitCode: r.exitCode}, nil
}

func TestHarnessVerifierWiresStopOnPass(t *testing.T) {
	runner := &fixedExitRunner{exitCode: 0} // always passes
	h := HarnessVerifier{
		Harness: verify.Harness{
			Runner:  runner,
			Command: []string{"true"},
			Runs:    5,
		},
	}

	// stopOnPass=true: the harness must stop after the first passing run.
	v, err := h.Verify(context.Background(), t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Runs) != 1 {
		t.Errorf("stopOnPass=true: executed %d runs, want 1", len(v.Runs))
	}
	firstCallCount := runner.calls
	runner.calls = 0

	// stopOnPass=false: all 5 runs must complete regardless of outcome.
	v, err = h.Verify(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Runs) != 5 {
		t.Errorf("stopOnPass=false: executed %d runs, want 5", len(v.Runs))
	}
	_ = firstCallCount
}

// ---------------------------------------------------------------------------
// detectManager — uncovered branches
// ---------------------------------------------------------------------------

func TestRunGoWorkspaceUnsupported(t *testing.T) {
	env := newEnv(t, threeChangeGoRepo(), func(map[string]string) bool { return false }, 1)
	env.git.files["sha-head"]["go.work"] = "go 1.22\n"
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "go.work") {
		t.Fatalf("err = %v, want go.work-unsupported error", err)
	}
	if len(env.tempDirs) != 0 {
		t.Error("no worktree should be created when go.work is present")
	}
}

func TestRunPyprojectWithoutUvLockFails(t *testing.T) {
	env := newEnv(t, threeChangePythonRepo(), func(map[string]string) bool { return false }, 1)
	delete(env.git.files["sha-head"], "uv.lock")
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil {
		t.Fatal("expected error when pyproject.toml has no uv.lock")
	}
	if !strings.Contains(err.Error(), "pyproject.toml") || !strings.Contains(err.Error(), "uv.lock") {
		t.Errorf("err = %v, want mention of pyproject.toml and uv.lock", err)
	}
}

// TestRunPythonFamilyDetection covers the Python detection rules: uv.lock
// marks a uv project even when a requirements.txt export sits next to it, a
// pyproject.toml without uv.lock falls back to pip when requirements.txt
// exists, and a plain requirements.txt selects pip. Dry runs stop after
// detection and diff, which is all these cases assert.
func TestRunPythonFamilyDetection(t *testing.T) {
	const reqs = "alpha==1.0.0\nbeta==3.0.0\ngamma==5.0.0\n"
	cases := []struct {
		name   string
		git    *fakeGit
		wantPM string
	}{
		{name: "requirements.txt alone", git: threeChangePipRepo(), wantPM: "pip"},
		{
			name: "uv.lock wins over requirements.txt",
			git: func() *fakeGit {
				g := threeChangePythonRepo()
				g.files["sha-base"]["requirements.txt"] = reqs
				g.files["sha-head"]["requirements.txt"] = reqs
				return g
			}(),
			wantPM: "uv",
		},
		{
			name: "pyproject.toml without uv.lock falls back to pip",
			git: func() *fakeGit {
				g := threeChangePipRepo()
				const py = "[project]\nname = \"app\"\n"
				g.files["sha-base"]["pyproject.toml"] = py
				g.files["sha-head"]["pyproject.toml"] = py
				return g
			}(),
			wantPM: "pip",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, tc.git, nil, 1)
			opts := baseOpts()
			opts.DryRun = true
			res, err := env.eng.Run(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.PackageManager != tc.wantPM {
				t.Errorf("pm = %q, want %q", res.PackageManager, tc.wantPM)
			}
		})
	}
}

func TestRunPackageJSONAndRequirementsAmbiguous(t *testing.T) {
	// requirements.txt joins the cross-ecosystem ambiguity check like any
	// other manifest; only its coexistence with pyproject.toml is special.
	env := newEnv(t, threeChangePipRepo(), nil, 1)
	env.git.files["sha-head"]["package.json"] = `{"dependencies":{"a":"1.0.0"}}`
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil || !strings.Contains(err.Error(), "--pm") {
		t.Fatalf("err = %v, want ambiguous-manifest error suggesting --pm", err)
	}
	if !strings.Contains(err.Error(), "requirements.txt") || !strings.Contains(err.Error(), "package.json") {
		t.Errorf("err = %v, want both manifests listed", err)
	}
}

// ---------------------------------------------------------------------------
// readManifest — eco.Parse failure (file exists but content is invalid)
// ---------------------------------------------------------------------------

func TestRunMalformedManifestAtBaseFails(t *testing.T) {
	git := threeChangeRepo()
	git.files["sha-base"]["package.json"] = "{not valid json}"
	env := newEnv(t, git, nil, 1)
	_, err := env.eng.Run(context.Background(), baseOpts())
	if err == nil {
		t.Fatal("expected parse error for malformed base manifest")
	}
	// Error message includes the revision label so the user knows which side is broken.
	if !strings.Contains(err.Error(), "base") {
		t.Errorf("err = %v, want mention of 'base' revision", err)
	}
}

// ---------------------------------------------------------------------------
// readLockfile — ShowFile failure (no lockfile at the revision)
// ---------------------------------------------------------------------------

func TestRunLockfileReadFailureIsDiagnosed(t *testing.T) {
	git := threeChangeRepo()
	delete(git.files["sha-base"], "package-lock.json") // no lockfile at base
	env := newEnv(t, git, func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	res, err := env.eng.Run(context.Background(), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	// Missing lockfile is non-fatal: the run continues without resolved-version
	// annotations for that side.
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, want MinimalFound", res.Outcome)
	}
	joined := strings.Join(res.Diagnostics, "\n")
	if !strings.Contains(joined, "unknown") {
		t.Errorf("diagnostics should mention unknown resolved versions: %v", res.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// Worktree pool partial-setup cleanup
// ---------------------------------------------------------------------------

// TestRunWorktreePoolPartialSetupCleansUp verifies that when the second of N
// AddWorktree calls fails during pool setup, the already-created first worktree
// is cleaned up and the parent temp directory is removed. Skipping cleanup here
// would leak worktrees even though the run returns an error.
func TestRunWorktreePoolPartialSetupCleansUp(t *testing.T) {
	git := threeChangeRepo()
	git.failAddWorktreeAfter = 1 // first call succeeds, second fails
	env := newEnv(t, git, func(map[string]string) bool { return false }, 1)
	opts := baseOpts()
	opts.Jobs = 2 // requests a pool of 2 lanes

	_, err := env.eng.Run(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error when AddWorktree fails during pool setup")
	}
	if !strings.Contains(err.Error(), "AddWorktree") {
		t.Errorf("err = %v, want AddWorktree failure message", err)
	}
	// The first lane's worktree must be cleaned up.
	if len(env.git.removed) == 0 {
		t.Error("partial pool setup failure must clean up the first worktree")
	}
	// Parent temp dir must be gone too.
	for _, d := range env.tempDirs {
		if _, statErr := os.Stat(d); !os.IsNotExist(statErr) {
			t.Errorf("temp dir %s still exists after partial pool cleanup", d)
		}
	}
}

// ---------------------------------------------------------------------------
// Checkpoint store error paths
// ---------------------------------------------------------------------------

// appendErrorStore is a CheckpointStore whose Append always returns an error.
type appendErrorStore struct {
	starts  int
	appends int
	err     error
}

func (s *appendErrorStore) Load() (*Checkpoint, error) { return nil, os.ErrNotExist }
func (s *appendErrorStore) Start(Checkpoint) error     { s.starts++; return nil }
func (s *appendErrorStore) Append(Trial) error         { s.appends++; return s.err }
func (s *appendErrorStore) Clear() error               { return nil }

// clearFailStore wraps fakeCheckpointStore and returns an error from Clear.
type clearFailStore struct {
	*fakeCheckpointStore
}

func (s *clearFailStore) Clear() error { return errors.New("fsync failed") }

// TestRunCheckpointAppendFailureDegrades pins the graceful-degradation
// contract: a failed checkpoint write must not abort the bisection. The run
// completes with its normal result, checkpointing is disabled after the
// first failure (no further Append attempts), and the loss is surfaced as a
// diagnostic. This is correctness-safe: a checkpoint merely missing trials
// still resumes (matching fingerprint; unpersisted trials just run again).
func TestRunCheckpointAppendFailureDegrades(t *testing.T) {
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	store := &appendErrorStore{err: errors.New("disk full")}
	opts := baseOpts()
	opts.Checkpoint = store
	res, err := env.eng.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("checkpoint append failure must not abort the run: %v", err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, want MinimalFound", res.Outcome)
	}
	if store.appends != 1 {
		t.Errorf("Append called %d times, want 1 (checkpointing disabled after the first failure)", store.appends)
	}
	joined := strings.Join(res.Diagnostics, "\n")
	if !strings.Contains(joined, "checkpointing disabled") || !strings.Contains(joined, "disk full") {
		t.Errorf("diagnostics missing degradation notice: %q", joined)
	}
}

func TestRunCheckpointClearFailureIsNonFatal(t *testing.T) {
	// A successful run tries to clear the checkpoint on completion. A Clear
	// error must not abort the run — the result is still valid; the user
	// just needs to remove the checkpoint file manually.
	store := &clearFailStore{&fakeCheckpointStore{}}
	env := newEnv(t, threeChangeRepo(), func(deps map[string]string) bool {
		return deps["beta"] == "3.2.0"
	}, 1)
	opts := baseOpts()
	opts.Checkpoint = store
	res, err := env.eng.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("checkpoint clear failure must not abort the run: %v", err)
	}
	if res.Outcome != OutcomeMinimalFound {
		t.Fatalf("outcome = %q, want MinimalFound", res.Outcome)
	}
	joined := strings.Join(res.Diagnostics, "\n")
	if !strings.Contains(joined, "checkpoint") {
		t.Errorf("diagnostics must mention the checkpoint clear error: %v", res.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// Utility functions — direct unit tests for uncovered branches
// ---------------------------------------------------------------------------

func TestLockfileOnlyHint(t *testing.T) {
	if got := lockfileOnlyHint(&Result{}); got != "" {
		t.Errorf("empty LockfileOnly: got %q, want empty string", got)
	}
	res := &Result{
		LockfileOnly: []manifest.LockfileChange{
			{Name: "transit", Spec: "^4.0.0", OldResolved: "4.0.1", NewResolved: "4.9.0"},
		},
	}
	got := lockfileOnlyHint(res)
	if got == "" {
		t.Fatal("non-empty LockfileOnly: got empty string, want hint")
	}
	if !strings.Contains(got, "1") {
		t.Errorf("hint should include count: %q", got)
	}
}

func TestLockfileOnlyNamesLong(t *testing.T) {
	// Exactly maxListed (8) entries — no truncation.
	eight := make([]manifest.LockfileChange, 8)
	for i := range eight {
		eight[i] = manifest.LockfileChange{Name: fmt.Sprintf("dep%d", i), OldResolved: "1.0.0", NewResolved: "1.1.0"}
	}
	got := lockfileOnlyNames(eight)
	for _, lc := range eight {
		if !strings.Contains(got, lc.Name) {
			t.Errorf("8-item list missing %q: %q", lc.Name, got)
		}
	}
	if strings.Contains(got, "more") {
		t.Errorf("8-item list must not truncate: %q", got)
	}

	// 10 entries — first 8 shown, remaining 2 summarized.
	ten := make([]manifest.LockfileChange, 10)
	for i := range ten {
		ten[i] = manifest.LockfileChange{Name: fmt.Sprintf("pkg%d", i), OldResolved: "1.0.0", NewResolved: "1.1.0"}
	}
	got = lockfileOnlyNames(ten)
	if !strings.Contains(got, "and 2 more") {
		t.Errorf("10-item list: want 'and 2 more', got %q", got)
	}
	for _, lc := range ten[:8] {
		if !strings.Contains(got, lc.Name) {
			t.Errorf("10-item list missing %q: %q", lc.Name, got)
		}
	}
	// Entries 8 and 9 must not appear by name (they're in the "and 2 more").
	for _, lc := range ten[8:] {
		if strings.Contains(got, lc.Name) {
			t.Errorf("truncated entry %q must not appear by name: %q", lc.Name, got)
		}
	}
}

func TestPluralize(t *testing.T) {
	cases := []struct {
		n    int
		sing string
		pl   string
		want string
	}{
		{1, "change", "changes", "1 change"},
		{0, "change", "changes", "0 changes"},
		{2, "change", "changes", "2 changes"},
		{42, "trial", "trials", "42 trials"},
	}
	for _, tc := range cases {
		if got := pluralize(tc.n, tc.sing, tc.pl); got != tc.want {
			t.Errorf("pluralize(%d, %q, %q) = %q, want %q", tc.n, tc.sing, tc.pl, got, tc.want)
		}
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("abc123"); got != "abc123" {
		t.Errorf("short SHA (%q): got %q, want unchanged", "abc123", got)
	}
	long := "abcdef123456789012345678901234567890"
	if got := shortSHA(long); got != "abcdef123456" {
		t.Errorf("long SHA: got %q, want first 12 chars", got)
	}
}
