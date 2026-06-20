// Package pm abstracts the supported package managers across ecosystems:
// npm and pnpm for JavaScript, and cargo for Rust.
package pm

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/skyneticist/depbisect/internal/execx"
)

// Manager identifies a supported package manager.
type Manager string

const (
	NPM   Manager = "npm"
	PNPM  Manager = "pnpm"
	CARGO Manager = "cargo"
)

// Detect chooses the package manager from lockfile presence at the target
// revision. A non-empty override wins; otherwise exactly one lockfile must
// be present.
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
	default:
		return "", fmt.Errorf("unsupported package manager %q (supported: npm, pnpm, cargo)", override)
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
	default:
		return "package-lock.json"
	}
}

// ManifestName returns the dependency manifest filename for the manager's
// ecosystem.
func (m Manager) ManifestName() string {
	if m == CARGO {
		return "Cargo.toml"
	}
	return "package.json"
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
	default:
		return []string{"install", "--no-audit", "--no-fund", "--loglevel=error"}
	}
}

// Installer installs dependencies in candidate worktrees.
type Installer struct {
	Runner  execx.Runner
	Manager Manager
	// LookPath overrides exec.LookPath in tests.
	LookPath func(string) (string, error)
}

// Version verifies the executable is available and returns its reported version.
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
		Args:              []string{"--version"},
		AllowTrustedBatch: true,
	})
	if err != nil {
		return "", fmt.Errorf("inspect package manager %q version: %w", i.Manager, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("inspect package manager %q version: exit %d: %s",
			i.Manager, res.ExitCode, firstNonEmptyLine(res.Stderr))
	}
	version := firstNonEmptyLine(res.Stdout)
	if version == "no output" {
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
func (i Installer) Install(ctx context.Context, dir string, stream io.Writer) (execx.Result, error) {
	return i.Runner.Run(ctx, execx.Cmd{
		Dir:               dir,
		Name:              string(i.Manager),
		Args:              i.Manager.installArgs(),
		AllowTrustedBatch: true,
		Stream:            stream,
	})
}

func firstNonEmptyLine(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			return value
		}
	}
	return "no output"
}
