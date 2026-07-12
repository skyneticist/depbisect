package manifest

import (
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
)

// GoRequire is the single bisectable "section" of a go.mod: its direct require
// directives. Go has no separate dev/optional dependency tables, so one section
// suffices. Indirect requires (marked "// indirect") and modules redirected by
// a replace directive are not recorded — they carry no directly bisectable
// version spec — mirroring how the Cargo path skips git/path dependencies.
const GoRequire Section = "require"

// goSectionOrder is the deterministic processing order for go.mod sections.
var goSectionOrder = []Section{GoRequire}

// GoMod is a parsed go.mod. Only direct require directives are recorded in
// Sections. Their version specs are already exact versions (Go uses minimal
// version selection), so there is no range/lockfile distinction as there is for
// npm. The original bytes are retained so candidates can be rendered with
// go.mod's own format-preserving editor rather than a full reserialize.
type GoMod struct {
	Name string
	// Sections holds the single GoRequire section: module path -> version.
	Sections map[Section]map[string]string

	// data is the original go.mod, re-parsed per Render so candidate edits
	// never mutate shared state.
	data []byte
}

// ParseGoMod parses go.mod bytes via golang.org/x/mod/modfile. Indirect
// requires and modules targeted by a replace directive are skipped.
func ParseGoMod(data []byte) (*GoMod, error) {
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return nil, fmt.Errorf("parse go.mod: %w", err)
	}
	g := &GoMod{
		Sections: map[Section]map[string]string{},
		data:     data,
	}
	if f.Module != nil {
		g.Name = f.Module.Mod.Path
	}
	replaced := make(map[string]bool, len(f.Replace))
	for _, r := range f.Replace {
		replaced[r.Old.Path] = true
	}
	deps := map[string]string{}
	for _, r := range f.Require {
		if r.Indirect || replaced[r.Mod.Path] {
			continue
		}
		deps[r.Mod.Path] = r.Mod.Version
	}
	if len(deps) > 0 {
		g.Sections[GoRequire] = deps
	}
	return g, nil
}

// HasWorkspaceLayout implements Parsed. Go workspaces are declared in a
// separate go.work file, never inside go.mod, so a parsed manifest cannot see
// one; the engine probes for go.work directly.
func (g *GoMod) HasWorkspaceLayout() bool { return false }

// ProjectName implements Parsed; the module path is what go.sum entries key on.
func (g *GoMod) ProjectName() string { return g.Name }

// DiffGo computes the direct dependency changes from old to new, using the
// shared section diff over go.mod's require directives.
func DiffGo(old, new *GoMod) []Change {
	return diffSections(old.Sections, new.Sections, goSectionOrder)
}

// RenderGoMod produces candidate go.mod bytes based on the new manifest, in
// which every change whose ID is in applied keeps its new state and every other
// change reverts to its old state. Edits go through modfile, preserving the
// file's formatting, comments, and unrelated directives (replace, exclude, the
// go/toolchain lines, and indirect requires).
func RenderGoMod(to *GoMod, changes []Change, applied map[string]bool) ([]byte, error) {
	f, err := modfile.Parse("go.mod", to.data, nil)
	if err != nil {
		return nil, fmt.Errorf("render go.mod: %w", err)
	}
	for _, c := range changes {
		if applied[c.ID()] {
			continue // keep the new state, already present
		}
		switch c.Kind {
		case Updated, Removed:
			// Updated: pin back to the old version. Removed: re-add the
			// direct require the new manifest dropped. AddRequire both sets an
			// existing line and inserts a missing one.
			if err := f.AddRequire(c.Name, c.OldSpec); err != nil {
				return nil, fmt.Errorf("render go.mod: revert %s: %w", c.Name, err)
			}
		case Added:
			// Present only in the new manifest: drop it for this candidate.
			if err := f.DropRequire(c.Name); err != nil {
				return nil, fmt.Errorf("render go.mod: drop %s: %w", c.Name, err)
			}
		}
	}
	f.Cleanup()
	out, err := f.Format()
	if err != nil {
		return nil, fmt.Errorf("render go.mod: %w", err)
	}
	return out, nil
}

// ParseGoSum extracts the resolved (selected) version of each module from
// go.sum. Each selected module version contributes two lines — a module-content
// hash and a go.mod hash (its version suffixed "/go.mod"). Only the content
// line marks a version actually selected into the build, so go.mod-only lines
// are ignored. For direct requires this simply echoes the go.mod version
// (minimal version selection makes the spec exact), so it serves only to
// annotate/confirm, never to resolve a range.
func ParseGoSum(data []byte) (Resolved, error) {
	out := Resolved{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		path, version := fields[0], fields[1]
		if strings.HasSuffix(version, "/go.mod") {
			continue
		}
		out[path] = version
	}
	return out, nil
}

// LockfileOnlyGo returns direct requires declared identically in both manifests
// whose go.sum resolution nonetheless differs. With minimal version selection
// the spec already is the resolved version, so this is expected to be empty; it
// exists for parity with the other ecosystems.
func LockfileOnlyGo(old, new *GoMod, oldR, newR Resolved) []LockfileChange {
	return lockfileOnly(old.Sections, new.Sections, oldR, newR, goSectionOrder)
}
