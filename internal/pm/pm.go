// Package pm abstracts the supported package managers across ecosystems:
// npm and pnpm for JavaScript, cargo for Rust, go for Go, and uv for Python.
//
// All five managers are installed as immutable [Manager] constants. The
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
	"strings"

	"github.com/skyneticist/depbisect/internal/execx"
)

// Manager identifies a supported package manager.
// The zero value ("") falls through to npm defaults in every switch statement;
// use [Manager.Valid] to distinguish an initialized Manager from the zero value.
type Manager string

const (
	NPM   Manager = "npm"
	PNPM  Manager = "pnpm"
	CARGO Manager = "cargo"
	GO    Manager = "go"
	UV    Manager = "uv"
)

// Valid reports whether m is one of the known package manager constants.
// The zero value ("") returns false.
func (m Manager) Valid() bool {
	switch m {
	case NPM, PNPM, CARGO, GO, UV:
		return true
	}
	return false
}

// Detect chooses an npm or pnpm manager from lockfile presence at the target
// revision. For Cargo, Go, and UV the caller must provide a non-empty override,
// since those ecosystems are detected by the engine's manifest-presence logic
// rather than by this function.
//
// A non-empty override always wins. When override is empty, exactly one of the
// two npm-ecosystem lockfiles must be present.
func Detect(hasPackageLock, hasPnpmLock bool, override string) (Manager, error) {
	switch override {
	case "":
		// fall through to detection
	case string(NPM):
		return NPM, nil
	case string(PNPM):
		return PNPM, nil
	case string(CARGO):
		return CARGO, nil
	case string(GO):
		return GO, nil
	case string(UV):
		return UV, nil
	default:
		return "", fmt.Errorf("unsupported package manager %q (supported: npm, pnpm, cargo, go, uv)", override)
	}
	switch {
	case hasPackageLock && hasPnpmLock:
		return "", fmt.Errorf("both package-lock.json and pnpm-lock.yaml exist; choose one with --pm")
	case hasPackageLock:
		return NPM, nil
	case hasPnpmLock:
		return PNPM, nil
	default:
		return "", fmt.Errorf("no supported lockfile found (package-lock.json or pnpm-lock.yaml); " +
			"a lockfile is required to identify resolved dependency versions")
	}
}

// LockfileName returns the manager's lockfile filename.
func (m Manager) LockfileName() string {
	switch m {
	case PNPM:
		return "pnpm-lock.yaml"
	case CARGO:
		return "Cargo.lock"
	case GO:
		return "go.sum"
	case UV:
		return "uv.lock"
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
	default:
		return "package.json"
	}
}

// DependencyFiles returns the manifest and lockfile names across every
// supported manager, deduplicated and in a stable order. These are the paths
// whose history marks a dependency change, used to suggest a --base revision.
func DependencyFiles() []string {
	managers := []Manager{NPM, PNPM, CARGO, GO, UV}
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
	// cargo --version already prints "cargo x.y.z"; npm and pnpm print only the
	// bare number, so the manager name is prefixed only when not already present.
	if strings.HasPrefix(version, string(i.Manager)) {
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
// without discarding any other flags the user already has in GOFLAGS.
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
	if i.Manager == GO {
		extraEnv = goInstallEnv()
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
