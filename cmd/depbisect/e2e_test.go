package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	if os.Getenv("DEPBISECT_PM_HELPER") != "" {
		runPMHelper()
		return
	}
	dir, err := os.MkdirTemp("", "depbisect-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, executableName("depbisect"))
	out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func runPMHelper() {
	logPath := os.Getenv("PM_LOG")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0]))
	fmt.Fprintf(f, "%s %s\n", name, strings.Join(os.Args[1:], " "))
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Fprintln(os.Stdout, "9.9.9-test")
	}
}

func requireTools(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	return node
}

// repo is a real temporary git repository with two commits that differ in
// three direct dependencies; "leftpad 1.0.0 -> 2.0.0" is the culprit
// candidates will test for.
type repo struct {
	dir   string
	first string
}

const basePkgJSON = `{
  "name": "fixture",
  "version": "1.0.0",
  "dependencies": {"alpha": "1.0.0", "beta": "1.0.0", "leftpad": "1.0.0"}
}
`

const newPkgJSON = `{
  "name": "fixture",
  "version": "1.0.0",
  "dependencies": {"alpha": "1.1.0", "beta": "1.2.0", "leftpad": "2.0.0"}
}
`

func npmLock(alpha, beta, leftpad string) string {
	return fmt.Sprintf(`{"name":"fixture","lockfileVersion":3,"packages":{"":{},"node_modules/alpha":{"version":%q},"node_modules/beta":{"version":%q},"node_modules/leftpad":{"version":%q}}}`,
		alpha, beta, leftpad)
}

func pnpmLock(alpha, beta, leftpad string) string {
	return fmt.Sprintf(`lockfileVersion: '6.0'
dependencies:
  alpha:
    specifier: %s
    version: %s
  beta:
    specifier: %s
    version: %s
  leftpad:
    specifier: %s
    version: %s
`, alpha, alpha, beta, beta, leftpad, leftpad)
}

func gitIn(t *testing.T, dir string, args ...string) string {
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

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initRepo builds the fixture repo. pmKind is "npm" or "pnpm".
func initRepo(t *testing.T, pmKind string) *repo {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "core.autocrlf", "false")
	gitIn(t, dir, "config", "core.filemode", "false")
	write(t, filepath.Join(dir, "package.json"), basePkgJSON)
	if pmKind == "pnpm" {
		write(t, filepath.Join(dir, "pnpm-lock.yaml"), pnpmLock("1.0.0", "1.0.0", "1.0.0"))
	} else {
		write(t, filepath.Join(dir, "package-lock.json"), npmLock("1.0.0", "1.0.0", "1.0.0"))
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	first := gitIn(t, dir, "rev-parse", "HEAD")

	write(t, filepath.Join(dir, "package.json"), newPkgJSON)
	if pmKind == "pnpm" {
		write(t, filepath.Join(dir, "pnpm-lock.yaml"), pnpmLock("1.1.0", "1.2.0", "2.0.0"))
	} else {
		write(t, filepath.Join(dir, "package-lock.json"), npmLock("1.1.0", "1.2.0", "2.0.0"))
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "bump deps")
	return &repo{dir: dir, first: first}
}

// stubPM creates fake npm and pnpm commands that log their argv and succeed.
func stubPM(t *testing.T) (stubDir, logPath string) {
	t.Helper()
	stubDir = filepath.Join(t.TempDir(), "package manager stubs")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(t.TempDir(), "pm.log")
	for _, name := range []string{"npm", "pnpm"} {
		if runtime.GOOS == "windows" {
			script := "@echo off\r\n>>\"%PM_LOG%\" echo " + name + " %*\r\n" +
				"if \"%1\"==\"--version\" echo 9.9.9-test\r\nexit /b 0\r\n"
			if err := os.WriteFile(filepath.Join(stubDir, name+".cmd"), []byte(script), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		testExe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(testExe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stubDir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return stubDir, logPath
}

type runResult struct {
	code   int
	stdout string
	stderr string
}

// runBin executes the compiled depbisect binary in workDir with a stubbed
// package-manager PATH and a private TMPDIR, and returns the outcome.
func runBin(t *testing.T, r *repo, workDir, tmpDir, stubDir, logPath string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DEPBISECT_PM_HELPER=1",
		"PM_LOG="+logPath,
		"TMPDIR="+tmpDir,
		"TMP="+tmpDir,
		"TEMP="+tmpDir,
	)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run binary: %v", err)
		}
	}
	return runResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// snapshot captures every file in the repo (outside .git) for before/after
// comparison.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func assertRepoUntouched(t *testing.T, r *repo, before map[string]string) {
	t.Helper()
	after := snapshot(t, r.dir)
	if len(after) != len(before) {
		t.Errorf("repo file count changed: %d -> %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("repo file %s was modified", path)
		}
	}
	if status := gitIn(t, r.dir, "status", "--porcelain"); status != "" {
		t.Errorf("repo dirty after run:\n%s", status)
	}
	worktrees := gitIn(t, r.dir, "worktree", "list", "--porcelain")
	if n := strings.Count(worktrees, "worktree "); n != 1 {
		t.Errorf("unexpected worktrees registered:\n%s", worktrees)
	}
}

func assertTmpEmpty(t *testing.T, tmpDir string) {
	t.Helper()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "depbisect-") {
			t.Errorf("leftover temp dir %s", e.Name())
		}
	}
}

// failIfLeftpad2 exits 1 when the worktree's package.json pins leftpad 2.0.0.
const failIfLeftpad2 = `const p=require(process.cwd()+"/package.json");process.exit((p.dependencies||{}).leftpad==="2.0.0"?1:0);`

func TestE2EFindsCulpritNpm(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	before := snapshot(t, r.dir)
	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--runs", "3", "--",
		node, "-e", failIfLeftpad2)

	if res.code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	for _, want := range []string{"Changes 3 analyzed", "Breaking dependencies", "leftpad 1.0.0 -> 2.0.0", "Evidence 3/3 failing runs"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, res.stdout)
		}
	}
	if strings.ContainsAny(res.stdout+res.stderr, "\r\x1b") {
		t.Errorf("redirected output contains terminal control sequences:\nstdout:\n%s\nstderr:\n%s",
			res.stdout, res.stderr)
	}

	// JSON report is valid and identifies the culprit.
	data, err := os.ReadFile(filepath.Join(outDir, "depbisect-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion         int    `json:"schemaVersion"`
		Outcome               string `json:"outcome"`
		PackageManagerVersion string `json:"packageManagerVersion"`
		MinimalSet            []struct {
			Name string `json:"name"`
		} `json:"minimalSet"`
		Confidence struct {
			Failures, Runs int
		} `json:"confidence"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("report JSON invalid: %v", err)
	}
	if doc.SchemaVersion != 1 || doc.Outcome != "minimal-set-found" ||
		doc.PackageManagerVersion != "npm 9.9.9-test" ||
		len(doc.MinimalSet) != 1 || doc.MinimalSet[0].Name != "leftpad" ||
		doc.Confidence.Failures != 3 {
		t.Errorf("report = %+v", doc)
	}
	md, err := os.ReadFile(filepath.Join(outDir, "depbisect-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "## Minimal failing set") || !strings.Contains(string(md), "leftpad") {
		t.Errorf("markdown report:\n%s", md)
	}

	// npm stub was used (never pnpm), and the repo is pristine.
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "npm install") || strings.Contains(string(log), "pnpm") {
		t.Errorf("pm log:\n%s", log)
	}
	assertRepoUntouched(t, r, before)
	assertTmpEmpty(t, tmpDir)
}

// TestE2EParallelFindsCulpritNpm runs the bisection across three real git
// worktrees concurrently (-j3), proving the worktree pool, concurrent installs,
// and concurrent verification all work end-to-end and clean up afterward.
func TestE2EParallelFindsCulpritNpm(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	before := snapshot(t, r.dir)
	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--jobs", "3", "--runs", "3", "--",
		node, "-e", failIfLeftpad2)

	if res.code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	for _, want := range []string{"Breaking dependencies", "leftpad 1.0.0 -> 2.0.0", "Evidence 3/3 failing runs"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, res.stdout)
		}
	}
	// All lanes installed via npm; the repo and its worktree list are pristine.
	if log, _ := os.ReadFile(logPath); !strings.Contains(string(log), "npm install") {
		t.Errorf("pm log:\n%s", log)
	}
	assertRepoUntouched(t, r, before)
	assertTmpEmpty(t, tmpDir)
}

func TestE2EFindsCulpritPnpm(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "pnpm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--",
		node, "-e", failIfLeftpad2)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "leftpad 1.0.0 -> 2.0.0") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "pnpm install --no-frozen-lockfile") {
		t.Errorf("pnpm must be invoked with --no-frozen-lockfile:\n%s", log)
	}
	assertTmpEmpty(t, tmpDir)
}

func TestE2ENotReproduced(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--",
		node, "-e", "process.exit(0);")
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	for _, want := range []string{
		"Result No breaking dependency update reproduced the failure",
		"Outcome not-reproduced",
		"Changes 3 analyzed",
		"Trials 2",
		"passed 1/1 runs with all dependency updates applied",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, res.stdout)
		}
	}
	if compact := strings.Join(strings.Fields(res.stdout), " "); !strings.Contains(compact, "no bisection was needed") {
		t.Errorf("stdout should explain that no bisection ran:\n%s", res.stdout)
	}
	assertTmpEmpty(t, tmpDir)
}

func TestE2EFailsAtBase(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--",
		node, "-e", "process.exit(1);")
	if res.code != 3 {
		t.Fatalf("exit = %d, want 3\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	assertTmpEmpty(t, tmpDir)
}

func TestE2ENoChanges(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "npm")
	// Add a commit that does not touch dependencies.
	write(t, filepath.Join(r.dir, "README.md"), "hello\n")
	gitIn(t, r.dir, "add", ".")
	gitIn(t, r.dir, "commit", "-q", "-m", "docs")
	head1 := gitIn(t, r.dir, "rev-parse", "HEAD~1")

	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()
	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", head1, "--",
		node, "-e", "process.exit(1);")
	if res.code != 5 {
		t.Fatalf("exit = %d, want 5\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "Result No dependency changes to bisect") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
	// No installs should have happened.
	if log, _ := os.ReadFile(logPath); len(log) != 0 {
		t.Errorf("unexpected package manager calls:\n%s", log)
	}
}

func TestE2EQuietSuppressesProgress(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--quiet", "--no-reports", "--",
		node, "-e", failIfLeftpad2)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	if res.stderr != "" {
		t.Fatalf("--quiet stderr should be empty:\n%s", res.stderr)
	}
	if !strings.Contains(res.stdout, "Result Minimal breaking dependency set found") {
		t.Errorf("quiet output must retain the final result:\n%s", res.stdout)
	}
}

func TestE2EDryRun(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--dry-run", "--",
		node, "-e", failIfLeftpad2)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", res.code, res.stderr)
	}
	for _, want := range []string{"Dependency changes", "alpha", "leftpad", "Outcome dry-run"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("dry-run stdout missing %q:\n%s", want, res.stdout)
		}
	}
	if log, _ := os.ReadFile(logPath); len(log) != 0 {
		t.Errorf("dry run must not install:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(outDir, "depbisect-report.md")); !os.IsNotExist(err) {
		t.Error("dry run must not write reports")
	}
	assertTmpEmpty(t, tmpDir)
}

func TestE2EFlakyInconclusive(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()
	counter := filepath.Join(t.TempDir(), "counter")

	// Fails on every second run when leftpad is 2.0.0: flaky baseline-new.
	flaky := fmt.Sprintf(`const fs=require("fs");const p=require(process.cwd()+"/package.json");
if((p.dependencies||{}).leftpad!=="2.0.0"){process.exit(0)}
let n=0;try{n=parseInt(fs.readFileSync(%q,"utf8"))}catch(e){}
fs.writeFileSync(%q,String(n+1));process.exit(n%%2===0?1:0);`, counter, counter)

	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--runs", "2", "--",
		node, "-e", flaky)
	if res.code != 4 {
		t.Fatalf("exit = %d, want 4\nstdout:\n%s\nstderr:\n%s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "flaky") {
		t.Errorf("stdout should explain flakiness:\n%s", res.stdout)
	}
	assertTmpEmpty(t, tmpDir)
}

func TestE2EKeepWorktrees(t *testing.T) {
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()

	res := runBin(t, r, outDir, tmpDir, stubDir, logPath,
		"run", "--repo", r.dir, "--base", r.first, "--keep-worktrees", "--",
		node, "-e", failIfLeftpad2)
	if res.code != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "Worktree ") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
	// The kept worktree must exist under our TMPDIR.
	entries, _ := os.ReadDir(tmpDir)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "depbisect-") {
			found = true
		}
	}
	if !found {
		t.Error("kept worktree not found in TMPDIR")
	}
	// Clean up the registration so the repo is reusable.
	gitIn(t, r.dir, "worktree", "prune")
}

func TestE2EInterruptCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go cannot portably generate a Windows console interrupt for another process")
	}
	node := requireTools(t)
	r := initRepo(t, "npm")
	stubDir, logPath := stubPM(t)
	tmpDir, outDir := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")

	hang := fmt.Sprintf(`require("fs").writeFileSync(%q,"");setTimeout(()=>{},120000);`, marker)
	cmd := exec.Command(binPath,
		"run", "--repo", r.dir, "--base", r.first, "--",
		node, "-e", hang)
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DEPBISECT_PM_HELPER=1",
		"PM_LOG="+logPath,
		"TMPDIR="+tmpDir,
		"TMP="+tmpDir,
		"TEMP="+tmpDir,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the verification command to start, then interrupt.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatalf("verification never started; stderr:\n%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected nonzero exit after interrupt")
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatal("binary did not exit after interrupt")
	}

	if !strings.Contains(stderr.String(), "interrupted") {
		t.Errorf("stderr should mention interruption:\n%s", stderr.String())
	}
	assertTmpEmpty(t, tmpDir)
	worktrees := gitIn(t, r.dir, "worktree", "list", "--porcelain")
	if n := strings.Count(worktrees, "worktree "); n != 1 {
		t.Errorf("worktree leaked after interrupt:\n%s", worktrees)
	}
}
