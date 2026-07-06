// Package pm abstracts the supported package managers across ecosystems:
// npm, pnpm, and yarn for JavaScript, cargo for Rust, go for Go, uv and pip
// for Python, and composer for PHP.
//
// All eight managers are installed as immutable [Manager] constants. The
// zero-value Manager ("") is intentionally treated as npm in all switch
// defaults; callers that need to guard against an uninitialized Manager should
// call [Manager.Valid] before use.
package pm

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/skyneticist/depbisect/internal/execx"
)

// Manager identifies a supported package manager.
// The zero value ("") falls through to npm defaults in every switch statement;
// use [Manager.Valid] to distinguish an initialized Manager from the zero value.
type Manager string

const (
	NPM      Manager = "npm"
	PNPM     Manager = "pnpm"
	YARN     Manager = "yarn"
	CARGO    Manager = "cargo"
	GO       Manager = "go"
	UV       Manager = "uv"
	COMPOSER Manager = "composer"
	PIP      Manager = "pip"
)

// pipVenvDir is the worktree-relative virtual environment pip trials install
// into. The install step creates it fresh in every trial worktree; the verify
// command runs against it through the verify package's venv bridge.
const pipVenvDir = ".venv"

// Valid reports whether m is one of the known package manager constants.
// The zero value ("") returns false.
func (m Manager) Valid() bool {
	switch m {
	case NPM, PNPM, YARN, CARGO, GO, UV, COMPOSER, PIP:
		return true
	}
	return false
}

// Detect chooses among the JavaScript managers — npm, pnpm, or yarn — from
// lockfile presence at the target revision. Cargo, Go, UV, Composer, and pip
// require a non-empty override, since those ecosystems are detected by the
// engine's manifest-presence logic rather than by this function.
//
// A non-empty override always wins. When override is empty, exactly one of
// the three JS-ecosystem lockfiles must be present.
func Detect(hasPackageLock, hasPnpmLock, hasYarnLock bool, override string) (Manager, error) {
	switch override {
	case "":
		// fall through to detection
	case string(NPM):
		return NPM, nil
	case string(PNPM):
		return PNPM, nil
	case string(YARN):
		return YARN, nil
	case string(CARGO):
		return CARGO, nil
	case string(GO):
		return GO, nil
	case string(UV):
		return UV, nil
	case string(COMPOSER):
		return COMPOSER, nil
	case string(PIP):
		return PIP, nil
	default:
		return "", fmt.Errorf("unsupported package manager %q (supported: npm, pnpm, yarn, cargo, go, uv, composer, pip)", override)
	}
	var found []string
	if hasPackageLock {
		found = append(found, NPM.LockfileName())
	}
	if hasPnpmLock {
		found = append(found, PNPM.LockfileName())
	}
	if hasYarnLock {
		found = append(found, YARN.LockfileName())
	}
	switch {
	case len(found) > 1:
		return "", fmt.Errorf("multiple lockfiles found (%s); choose one with --pm", strings.Join(found, ", "))
	case hasPackageLock:
		return NPM, nil
	case hasPnpmLock:
		return PNPM, nil
	case hasYarnLock:
		return YARN, nil
	default:
		return "", fmt.Errorf("no supported lockfile found (package-lock.json, pnpm-lock.yaml, or yarn.lock); " +
			"a lockfile is required to identify resolved dependency versions")
	}
}

// LockfileName returns the manager's lockfile filename. pip has no separate
// lockfile: exact pins in requirements.txt are the resolution, so the manifest
// name doubles as the lockfile name.
func (m Manager) LockfileName() string {
	switch m {
	case PNPM:
		return "pnpm-lock.yaml"
	case YARN:
		return "yarn.lock"
	case CARGO:
		return "Cargo.lock"
	case GO:
		return "go.sum"
	case UV:
		return "uv.lock"
	case COMPOSER:
		return "composer.lock"
	case PIP:
		return "requirements.txt"
	default:
		return "package-lock.json"
	}
}

// ManifestName returns the dependency manifest filename for the manager's
// ecosystem.
func (m Manager) ManifestName() string {
	switch m {
	case CARGO:
		return "Cargo.toml"
	case GO:
		return "go.mod"
	case UV:
		return "pyproject.toml"
	case COMPOSER:
		return "composer.json"
	case PIP:
		return "requirements.txt"
	default:
		return "package.json"
	}
}

// VenvDir returns the worktree-relative virtual-environment directory the
// manager's install step creates and the verification command must run
// against, or "" for managers that do not install into one. Only pip uses a
// virtual environment; uv projects bridge through `uv run` instead.
func (m Manager) VenvDir() string {
	if m == PIP {
		return pipVenvDir
	}
	return ""
}

// DependencyFiles returns the manifest and lockfile names across every
// supported manager, deduplicated and in a stable order. These are the paths
// whose history marks a dependency change, used to suggest a --base revision.
func DependencyFiles() []string {
	managers := []Manager{NPM, PNPM, YARN, CARGO, GO, UV, COMPOSER, PIP}
	seen := make(map[string]bool)
	var files []string
	for _, m := range managers {
		for _, name := range []string{m.ManifestName(), m.LockfileName()} {
			if !seen[name] {
				seen[name] = true
				files = append(files, name)
			}
		}
	}
	return files
}

// installArgs returns the argument vector for a candidate installation.
func (m Manager) installArgs() []string {
	switch m {
	case PNPM:
		// pnpm enables --frozen-lockfile automatically when CI=true;
		// candidate manifests intentionally disagree with the lockfile,
		// so freezing must be disabled explicitly.
		return []string{"install", "--no-frozen-lockfile"}
	case YARN:
		// `yarn install` is the correct verb for both classic v1 and Berry.
		// Berry enables --immutable automatically when CI=true, which must be
		// disabled because candidate manifests intentionally disagree with the
		// lockfile — but no flag for that is accepted by both major versions
		// (--no-immutable is Berry-only, and Berry rejects unknown flags), so
		// Install sets YARN_ENABLE_IMMUTABLE_INSTALLS=false in the environment
		// instead; classic v1 harmlessly ignores the setting.
		return []string{"install"}
	case CARGO:
		// cargo fetch resolves and downloads dependencies (updating Cargo.lock
		// to match the candidate manifest) without compiling. Build and test
		// failures — the signal DepBisect bisects — are left to the verify
		// command.
		return []string{"fetch"}
	case GO:
		// `go mod download all` resolves and fetches every module in the
		// candidate's build list, recording the full module-zip checksums in
		// go.sum — not just the "<mod>/go.mod" checksums a bare `go mod
		// download` writes. Those zip checksums are what `go build`/`go test`
		// verify against, so the `all` pattern is required: without it a
		// candidate that reverts a dependency installs cleanly yet fails
		// verification with "missing go.sum entry". Compiling and testing are
		// left to the verify command.
		//
		// The required -mod=mod flag is injected into GOFLAGS at install time
		// (see Install) rather than here, so that any other GOFLAGS the user
		// already has set (e.g. -tags=integration) are preserved.
		return []string{"mod", "download", "all"}
	case UV:
		// `uv lock` re-resolves uv.lock to satisfy the candidate pyproject.toml
		// without creating a virtualenv or installing anything; a revert that
		// cannot be satisfied surfaces here as an install failure. Building the
		// environment and running tests is left to the verification command,
		// which should invoke `uv run` (e.g. `uv run -- pytest`) so it executes
		// against the freshly resolved lock.
		return []string{"lock"}
	case COMPOSER:
		// `composer install` reuses composer.lock verbatim — when the manifest
		// disagrees with the lock it only warns and installs the locked (head)
		// versions anyway, silently ignoring the candidate's reverts. `composer
		// update` re-resolves from composer.json, rewrites composer.lock, and
		// installs vendor/, which is what candidates need. --no-progress keeps
		// per-trial output quiet; the post-update security audit is suppressed
		// via COMPOSER_NO_AUDIT (see composerInstallEnv).
		return []string{"update", "--no-interaction", "--no-progress"}
	case PIP:
		// The host pip installs into the trial's freshly created virtual
		// environment via --python (pip >= 22.3), which re-executes pip under
		// that interpreter; the venv itself is created --without-pip, so no
		// ensurepip bootstrap runs per trial (and Debian/Ubuntu systems work
		// without the python3-venv package). --no-input prevents index
		// credential prompts from hanging a trial, --disable-pip-version-check
		// suppresses an irrelevant network check, and the progress bar is
		// noise in captured output.
		return []string{"--python", pipVenvPython(), "install",
			"--requirement", "requirements.txt", "--no-input",
			"--disable-pip-version-check", "--progress-bar", "off"}
	default:
		// --no-audit and --no-fund suppress network calls to the npm advisory
		// and funding registries that are irrelevant during bisection.
		// --loglevel=error suppresses per-trial deprecation and peer-dependency
		// warnings that would drown the bisection progress output.
		return []string{"install", "--no-audit", "--no-fund", "--loglevel=error"}
	}
}

// goInstallEnv builds the GOFLAGS value for a Go candidate install. It reads
// the current GOFLAGS, strips any existing -mod=… token (to avoid conflicts
// with -mod=vendor or -mod=readonly), then appends -mod=mod. All other flags
// the user had in GOFLAGS (e.g. -tags=integration) are preserved.
//
// The result is returned as a single "GOFLAGS=…" entry suitable for ExtraEnv.
// Because ExtraEnv entries are appended after the inherited environment and
// Go's os.Getenv returns the last duplicate, our entry overrides the parent's
// GOFLAGS with the merged value.
func goInstallEnv() []string {
	existing := os.Getenv("GOFLAGS")
	merged := mergeModFlag(existing)
	return []string{"GOFLAGS=" + merged}
}

// yarnInstallEnv disables Berry's (yarn v2+) automatic immutable-installs
// mode, which activates when CI=true and would reject the lockfile updates
// that DepBisect's candidate manifests require. See installArgs for why this
// is an environment setting rather than a flag.
func yarnInstallEnv() []string {
	return []string{"YARN_ENABLE_IMMUTABLE_INSTALLS=false"}
}

// pipVenvPython returns the worktree-relative path of the trial virtual
// environment's interpreter, which differs between the POSIX (bin/) and
// Windows (Scripts/) venv layouts.
func pipVenvPython() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(pipVenvDir, "Scripts", "python.exe")
	}
	return filepath.Join(pipVenvDir, "bin", "python")
}

// pythonCandidates lists interpreter names to try when creating pip's trial
// virtual environments. Windows installers ship python.exe (python3.exe is
// usually only the Microsoft Store alias stub), so python is preferred there;
// elsewhere python3 is the canonical name.
func pythonCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{"python", "python3"}
	}
	return []string{"python3", "python"}
}

// findPython resolves the host Python interpreter used to create pip's trial
// virtual environments.
func (i Installer) findPython() (string, error) {
	lookPath := i.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	for _, name := range pythonCandidates() {
		if path, err := lookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("pip needs a Python interpreter (%s) on PATH to create trial virtual environments",
		strings.Join(pythonCandidates(), " or "))
}

// composerInstallEnv disables Composer's automatic security audit after
// update (Composer >= 2.4), which queries the repository's advisory API and is
// irrelevant during bisection. An environment variable is used rather than the
// --no-audit flag because Composer versions before 2.4 reject the unknown flag
// but harmlessly ignore the unknown variable.
func composerInstallEnv() []string {
	return []string{"COMPOSER_NO_AUDIT=1"}
}

// mergeModFlag removes any existing -mod=… token from flags and appends
// -mod=mod. flags is a space-separated GOFLAGS value (may be empty).
func mergeModFlag(flags string) string {
	var parts []string
	for _, f := range strings.Fields(flags) {
		if !strings.HasPrefix(f, "-mod=") {
			parts = append(parts, f)
		}
	}
	parts = append(parts, "-mod=mod")
	return strings.Join(parts, " ")
}

// Installer installs dependencies in candidate worktrees.
type Installer struct {
	Runner  execx.Runner
	Manager Manager
	// LookPath overrides exec.LookPath; set in tests to avoid PATH lookups.
	LookPath func(string) (string, error)
}

// versionArgs returns the argument vector that prints the manager's version.
// Most managers accept "--version"; the go tool uses the "version" subcommand
// instead (`go --version` is not valid).
func (m Manager) versionArgs() []string {
	if m == GO {
		return []string{"version"}
	}
	return []string{"--version"}
}

// Version verifies the executable is available and returns its reported version
// string (e.g. "npm 10.2.3", "cargo 1.75.0 (…)"). The version string is
// suitable for display and checkpoint fingerprinting; its format is not parsed.
func (i Installer) Version(ctx context.Context) (string, error) {
	lookPath := i.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(string(i.Manager))
	if err != nil {
		return "", fmt.Errorf("package manager %q not found on PATH: %w", i.Manager, err)
	}
	// Version doubles as the pre-flight check, so a missing interpreter for
	// pip's per-trial virtual environments fails here, before any trial runs.
	if i.Manager == PIP {
		if _, err := i.findPython(); err != nil {
			return "", err
		}
	}
	res, err := i.Runner.Run(ctx, execx.Cmd{
		Name:              string(i.Manager),
		Args:              i.Manager.versionArgs(),
		AllowTrustedBatch: true,
	})
	if err != nil {
		return "", fmt.Errorf("inspect package manager %q version: %w", i.Manager, err)
	}
	if res.ExitCode != 0 {
		if stderr, ok := execx.FirstLine(res.Stderr); ok {
			return "", fmt.Errorf("inspect package manager %q version: exit %d: %s",
				i.Manager, res.ExitCode, stderr)
		}
		return "", fmt.Errorf("inspect package manager %q version: exit %d", i.Manager, res.ExitCode)
	}
	version, ok := execx.FirstLine(res.Stdout)
	if !ok {
		return "", fmt.Errorf("inspect package manager %q version: command produced no output", i.Manager)
	}
	// cargo --version already prints "cargo x.y.z" and composer prints
	// "Composer version x.y.z" (capitalized); npm and pnpm print only the bare
	// number. The manager name is prefixed only when not already present,
	// compared case-insensitively for composer's capital C.
	if n := len(i.Manager); len(version) >= n && strings.EqualFold(version[:n], string(i.Manager)) {
		return version, nil
	}
	return fmt.Sprintf("%s %s", i.Manager, version), nil
}

// Install runs the package manager in dir. A nonzero exit is reported via
// the Result, not as an error. stream, when non-nil, receives live output.
//
// Install validates that dir is non-empty and exists before invoking the
// package manager. An empty or missing dir would otherwise produce an opaque
// PM-native error with no indication of which worktree caused the failure.
//
// For Go modules, Install injects a merged GOFLAGS that ensures -mod=mod
// without discarding any other flags the user already has in GOFLAGS. For
// yarn, it disables Berry's automatic immutable-installs mode. For pip, it
// first creates a per-worktree virtual environment that the install targets.
func (i Installer) Install(ctx context.Context, dir string, stream io.Writer) (execx.Result, error) {
	if dir == "" {
		return execx.Result{}, fmt.Errorf("install %s: target directory must not be empty", i.Manager)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return execx.Result{}, fmt.Errorf("install %s: target directory: %w", i.Manager, err)
	}
	if !fi.IsDir() {
		return execx.Result{}, fmt.Errorf("install %s: target directory %q is not a directory", i.Manager, dir)
	}
	var extraEnv []string
	switch i.Manager {
	case GO:
		extraEnv = goInstallEnv()
	case YARN:
		extraEnv = yarnInstallEnv()
	case COMPOSER:
		extraEnv = composerInstallEnv()
	case PIP:
		// pip installs into a per-worktree virtual environment so parallel
		// trial lanes never mutate a shared interpreter. The venv must exist
		// before pip targets it; a creation failure is environmental (broken
		// or missing venv module), not a property of the candidate, so unlike
		// the install itself it is returned as an error to fail the run
		// immediately rather than skipping every trial as unresolved.
		python, err := i.findPython()
		if err != nil {
			return execx.Result{}, fmt.Errorf("install %s: %w", i.Manager, err)
		}
		res, err := i.Runner.Run(ctx, execx.Cmd{
			Dir:               dir,
			Name:              python,
			Args:              []string{"-m", "venv", "--without-pip", pipVenvDir},
			AllowTrustedBatch: true,
			Stream:            stream,
		})
		if err != nil {
			return res, fmt.Errorf("install %s: create trial virtual environment: %w", i.Manager, err)
		}
		if res.ExitCode != 0 {
			// venv reports some failures (e.g. missing ensurepip) on stdout.
			detail := execx.FirstLineOr(res.Stderr, execx.FirstLineOr(res.Stdout, "no output"))
			return res, fmt.Errorf("install %s: create trial virtual environment (%s -m venv %s): %s",
				i.Manager, python, pipVenvDir, detail)
		}
	}
	return i.Runner.Run(ctx, execx.Cmd{
		Dir:               dir,
		Name:              string(i.Manager),
		Args:              i.Manager.installArgs(),
		ExtraEnv:          extraEnv,
		AllowTrustedBatch: true,
		Stream:            stream,
	})
}
