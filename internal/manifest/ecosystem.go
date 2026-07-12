package manifest

import "fmt"

// Parsed is a parsed dependency manifest from any supported ecosystem. The
// engine holds manifests through this interface so it never references a
// concrete manifest type.
type Parsed interface {
	// ProjectName returns the manifest's own package name as it would appear
	// among the ecosystem's lockfile entries ("" when the manifest carries
	// none), so lockfile scans can exclude the root project.
	ProjectName() string
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
	// PinChanges synthesizes bisectable changes from lockfile-only drift by
	// pinning each old resolution as an exact version spec (see pins.go).
	// Entries that cannot be pinned faithfully are skipped. Ecosystems where
	// such drift cannot occur (go: MVS specs are the resolution; pip:
	// requirements.txt is its own lockfile) return nil.
	PinChanges(lcs []LockfileChange) []Change
}

// EcosystemFor returns the Ecosystem for a package manager. The manager keys
// match the string values of pm.Manager ("npm", "pnpm", "yarn", "cargo", "go",
// "uv", "composer", "pip"); keying on the bare string keeps the manifest
// package decoupled from package pm.
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
	case "pip":
		return pipEcosystem{}, nil
	default:
		return nil, fmt.Errorf("no manifest handling for package manager %q", manager)
	}
}

// HasWorkspaceLayout implements Parsed.
func (p *PackageJSON) HasWorkspaceLayout() bool { return p.HasWorkspaces }

// ProjectName implements Parsed.
func (p *PackageJSON) ProjectName() string { return p.Name }

// HasWorkspaceLayout implements Parsed.
func (c *CargoToml) HasWorkspaceLayout() bool { return c.HasWorkspace }

// ProjectName implements Parsed.
func (c *CargoToml) ProjectName() string { return c.Name }

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

func (jsEcosystem) PinChanges(lcs []LockfileChange) []Change { return pinChanges(lcs, jsPin) }

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

func (cargoEcosystem) PinChanges(lcs []LockfileChange) []Change { return pinChanges(lcs, cargoPin) }

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

// PinChanges returns nil: under minimal version selection the go.mod spec is
// the resolution, so lockfile-only drift cannot occur (see LockfileOnlyGo).
func (goEcosystem) PinChanges([]LockfileChange) []Change { return nil }

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

func (pyEcosystem) PinChanges(lcs []LockfileChange) []Change { return pinChanges(lcs, pyPin) }

// pipEcosystem handles requirements.txt manifests for the pip package
// manager. requirements.txt is also its own "lockfile": exact pins stand in
// for resolved versions, so ParseLock reads the same bytes as Parse.
type pipEcosystem struct{}

func (pipEcosystem) Parse(data []byte) (Parsed, error) {
	r, err := ParseRequirements(data)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (pipEcosystem) ParseLock(data []byte) (Resolved, error) { return ParseRequirementsPins(data) }

func (pipEcosystem) Diff(old, new Parsed) []Change {
	return DiffRequirements(old.(*Requirements), new.(*Requirements))
}

func (pipEcosystem) Render(to Parsed, changes []Change, applied map[string]bool) ([]byte, error) {
	return RenderRequirements(to.(*Requirements), changes, applied)
}

func (pipEcosystem) LockfileOnly(old, new Parsed, oldR, newR Resolved) []LockfileChange {
	return LockfileOnlyRequirements(old.(*Requirements), new.(*Requirements), oldR, newR)
}

// PinChanges returns nil: requirements.txt is its own lockfile, so
// LockfileOnlyRequirements is provably empty and there is nothing to pin.
func (pipEcosystem) PinChanges([]LockfileChange) []Change { return nil }

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

func (composerEcosystem) PinChanges(lcs []LockfileChange) []Change {
	return pinChanges(lcs, composerPin)
}
