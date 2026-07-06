package manifest

import "fmt"

// Parsed is a parsed dependency manifest from any supported ecosystem. The
// engine holds manifests through this interface so it never references a
// concrete manifest type.
type Parsed interface {
	// HasWorkspaceLayout reports a multi-package workspace, which DepBisect
	// does not yet support.
	HasWorkspaceLayout() bool
}

// Ecosystem encapsulates manifest and lockfile handling for one supported
// dependency ecosystem. The engine selects one via EcosystemFor and drives the
// whole run through it, never branching on ecosystem at each step.
//
// Diff, Render, and LockfileOnly assume their Parsed arguments came from this
// same Ecosystem's Parse; mixing ecosystems is a programmer error.
type Ecosystem interface {
	// Parse parses manifest bytes (package.json, Cargo.toml, ...).
	Parse(data []byte) (Parsed, error)
	// ParseLock parses lockfile bytes into resolved versions.
	ParseLock(data []byte) (Resolved, error)
	// Diff computes the direct dependency changes from old to new.
	Diff(old, new Parsed) []Change
	// Render produces candidate manifest bytes applying the changes whose IDs
	// are in applied; the rest revert to their old state.
	Render(to Parsed, changes []Change, applied map[string]bool) ([]byte, error)
	// LockfileOnly returns dependencies whose spec is unchanged but whose
	// resolved version differs between revisions.
	LockfileOnly(old, new Parsed, oldR, newR Resolved) []LockfileChange
}

// EcosystemFor returns the Ecosystem for a package manager. The manager keys
// match the string values of pm.Manager ("npm", "pnpm", "yarn", "cargo", "go",
// "uv", "composer"); keying on the bare string keeps the manifest package
// decoupled from package pm.
func EcosystemFor(manager string) (Ecosystem, error) {
	switch manager {
	case "npm":
		return jsEcosystem{parseLock: ParsePackageLock}, nil
	case "pnpm":
		return jsEcosystem{parseLock: ParsePnpmLock}, nil
	case "yarn":
		return jsEcosystem{parseLock: ParseYarnLock}, nil
	case "cargo":
		return cargoEcosystem{}, nil
	case "go":
		return goEcosystem{}, nil
	case "uv":
		return pyEcosystem{}, nil
	case "composer":
		return composerEcosystem{}, nil
	default:
		return nil, fmt.Errorf("no manifest handling for package manager %q", manager)
	}
}

// HasWorkspaceLayout implements Parsed.
func (p *PackageJSON) HasWorkspaceLayout() bool { return p.HasWorkspaces }

// HasWorkspaceLayout implements Parsed.
func (c *CargoToml) HasWorkspaceLayout() bool { return c.HasWorkspace }

// jsEcosystem handles package.json manifests for npm, pnpm, and yarn; only
// the lockfile parser differs among them.
type jsEcosystem struct {
	parseLock func([]byte) (Resolved, error)
}

func (jsEcosystem) Parse(data []byte) (Parsed, error) {
	p, err := ParsePackageJSON(data)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (e jsEcosystem) ParseLock(data []byte) (Resolved, error) { return e.parseLock(data) }

func (jsEcosystem) Diff(old, new Parsed) []Change {
	return Diff(old.(*PackageJSON), new.(*PackageJSON))
}

func (jsEcosystem) Render(to Parsed, changes []Change, applied map[string]bool) ([]byte, error) {
	return Render(to.(*PackageJSON), changes, applied)
}

func (jsEcosystem) LockfileOnly(old, new Parsed, oldR, newR Resolved) []LockfileChange {
	return LockfileOnly(old.(*PackageJSON), new.(*PackageJSON), oldR, newR)
}

// cargoEcosystem handles Cargo.toml manifests for Rust.
type cargoEcosystem struct{}

func (cargoEcosystem) Parse(data []byte) (Parsed, error) {
	c, err := ParseCargoToml(data)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (cargoEcosystem) ParseLock(data []byte) (Resolved, error) { return ParseCargoLock(data) }

func (cargoEcosystem) Diff(old, new Parsed) []Change {
	return DiffCargo(old.(*CargoToml), new.(*CargoToml))
}

func (cargoEcosystem) Render(to Parsed, changes []Change, applied map[string]bool) ([]byte, error) {
	return RenderCargo(to.(*CargoToml), changes, applied)
}

func (cargoEcosystem) LockfileOnly(old, new Parsed, oldR, newR Resolved) []LockfileChange {
	return LockfileOnlyCargo(old.(*CargoToml), new.(*CargoToml), oldR, newR)
}

// goEcosystem handles go.mod manifests for Go modules.
type goEcosystem struct{}

func (goEcosystem) Parse(data []byte) (Parsed, error) {
	g, err := ParseGoMod(data)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (goEcosystem) ParseLock(data []byte) (Resolved, error) { return ParseGoSum(data) }

func (goEcosystem) Diff(old, new Parsed) []Change {
	return DiffGo(old.(*GoMod), new.(*GoMod))
}

func (goEcosystem) Render(to Parsed, changes []Change, applied map[string]bool) ([]byte, error) {
	return RenderGoMod(to.(*GoMod), changes, applied)
}

func (goEcosystem) LockfileOnly(old, new Parsed, oldR, newR Resolved) []LockfileChange {
	return LockfileOnlyGo(old.(*GoMod), new.(*GoMod), oldR, newR)
}

// pyEcosystem handles pyproject.toml manifests for the uv package manager.
type pyEcosystem struct{}

func (pyEcosystem) Parse(data []byte) (Parsed, error) {
	p, err := ParsePyproject(data)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (pyEcosystem) ParseLock(data []byte) (Resolved, error) { return ParseUvLock(data) }

func (pyEcosystem) Diff(old, new Parsed) []Change {
	return DiffPyproject(old.(*PyProject), new.(*PyProject))
}

func (pyEcosystem) Render(to Parsed, changes []Change, applied map[string]bool) ([]byte, error) {
	return RenderPyproject(to.(*PyProject), changes, applied)
}

func (pyEcosystem) LockfileOnly(old, new Parsed, oldR, newR Resolved) []LockfileChange {
	return LockfileOnlyPyproject(old.(*PyProject), new.(*PyProject), oldR, newR)
}

// composerEcosystem handles composer.json manifests for PHP.
type composerEcosystem struct{}

func (composerEcosystem) Parse(data []byte) (Parsed, error) {
	c, err := ParseComposerJSON(data)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (composerEcosystem) ParseLock(data []byte) (Resolved, error) { return ParseComposerLock(data) }

func (composerEcosystem) Diff(old, new Parsed) []Change {
	return DiffComposer(old.(*ComposerJSON), new.(*ComposerJSON))
}

func (composerEcosystem) Render(to Parsed, changes []Change, applied map[string]bool) ([]byte, error) {
	return RenderComposer(to.(*ComposerJSON), changes, applied)
}

func (composerEcosystem) LockfileOnly(old, new Parsed, oldR, newR Resolved) []LockfileChange {
	return LockfileOnlyComposer(old.(*ComposerJSON), new.(*ComposerJSON), oldR, newR)
}
