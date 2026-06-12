// Package pm abstracts the supported JavaScript package managers.
package pm

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/skyneticist/depbisect/internal/execx"
)

// Manager identifies a supported package manager.
type Manager string

const (
	NPM  Manager = "npm"
	PNPM Manager = "pnpm"
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
	default:
		return "", fmt.Errorf("unsupported package manager %q (supported: npm, pnpm)", override)
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
	if m == PNPM {
		return "pnpm-lock.yaml"
	}
	return "package-lock.json"
}

// installArgs returns the argument vector for a candidate installation.
func (m Manager) installArgs() []string {
	if m == PNPM {
		// pnpm enables --frozen-lockfile automatically when CI=true;
		// candidate manifests intentionally disagree with the lockfile,
		// so freezing must be disabled explicitly.
		return []string{"install", "--no-frozen-lockfile"}
	}
	return []string{"install", "--no-audit", "--no-fund", "--loglevel=error"}
}

// Installer installs dependencies in candidate worktrees.
type Installer struct {
	Runner  execx.Runner
	Manager Manager
	// LookPath overrides exec.LookPath in tests.
	LookPath func(string) (string, error)
}

// CheckAvailable verifies the package manager binary is on PATH.
func (i Installer) CheckAvailable() error {
	lookPath := i.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(string(i.Manager)); err != nil {
		return fmt.Errorf("package manager %q not found on PATH: %w", i.Manager, err)
	}
	return nil
}

// Install runs the package manager in dir. A nonzero exit is reported via
// the Result, not as an error. stream, when non-nil, receives live output.
func (i Installer) Install(ctx context.Context, dir string, stream io.Writer) (execx.Result, error) {
	return i.Runner.Run(ctx, execx.Cmd{
		Dir:    dir,
		Name:   string(i.Manager),
		Args:   i.Manager.installArgs(),
		Stream: stream,
	})
}
