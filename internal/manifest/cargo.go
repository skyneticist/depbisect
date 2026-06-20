package manifest

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// Cargo dependency sections, mirroring Cargo.toml's tables. They reuse the
// shared Section/Change model so the diff, annotation, and reporting logic is
// shared with the JavaScript path.
const (
	CargoDependencies      Section = "dependencies"
	CargoDevDependencies   Section = "dev-dependencies"
	CargoBuildDependencies Section = "build-dependencies"
)

// cargoSectionOrder is the deterministic processing order for Cargo sections.
var cargoSectionOrder = []Section{CargoDependencies, CargoDevDependencies, CargoBuildDependencies}

// CargoToml is a parsed Cargo.toml. Like PackageJSON it retains the full
// decoded document so candidate manifests can be rendered without losing
// unrelated fields. Only dependencies carrying a concrete version requirement
// are recorded in Sections; git, path, and workspace-inherited dependencies
// have no bisectable version and are skipped.
type CargoToml struct {
	Name string
	// Sections maps each dependency section to dependency name -> version
	// requirement. Absent sections have no entry.
	Sections map[Section]map[string]string
	// HasWorkspace reports whether the manifest declares a [workspace] table
	// (including virtual workspace manifests).
	HasWorkspace bool

	// doc holds the full decoded document, for rendering.
	doc map[string]any
}

// ParseCargoToml parses Cargo.toml bytes. It fails on malformed TOML and on
// dependency sections that are not tables.
func ParseCargoToml(data []byte) (*CargoToml, error) {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse Cargo.toml: %w", err)
	}
	c := &CargoToml{
		Sections: map[Section]map[string]string{},
		doc:      doc,
	}
	if pkg, ok := doc["package"].(map[string]any); ok {
		// A non-string name is tolerated; the name is informational only.
		if name, ok := pkg["name"].(string); ok {
			c.Name = name
		}
	}
	if _, ok := doc["workspace"]; ok {
		c.HasWorkspace = true
	}
	for _, sec := range cargoSectionOrder {
		raw, ok := doc[string(sec)]
		if !ok || raw == nil {
			continue
		}
		deps, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parse Cargo.toml: section %q is not a table", sec)
		}
		m := make(map[string]string, len(deps))
		for name, v := range deps {
			if spec, ok := cargoDepVersion(v); ok {
				m[name] = spec
			}
		}
		if len(m) > 0 {
			c.Sections[sec] = m
		}
	}
	return c, nil
}

// cargoDepVersion extracts the version requirement from one Cargo dependency
// entry: a bare string ("1.0"), or a table carrying a version key. It reports
// false for entries with no concrete version — git or path dependencies, and
// dependencies inherited from a workspace — which cannot be bisected.
func cargoDepVersion(v any) (string, bool) {
	switch entry := v.(type) {
	case string:
		return entry, true
	case map[string]any:
		if ws, _ := entry["workspace"].(bool); ws {
			return "", false
		}
		if ver, ok := entry["version"].(string); ok && ver != "" {
			return ver, true
		}
		return "", false
	default:
		return "", false
	}
}

// DiffCargo computes the direct dependency changes from old to new, using the
// shared section diff over Cargo's dependency tables.
func DiffCargo(old, new *CargoToml) []Change {
	return diffSections(old.Sections, new.Sections, cargoSectionOrder)
}

// RenderCargo produces candidate Cargo.toml bytes based on the new manifest, in
// which every change whose ID is in applied keeps its new state and every other
// change is reverted to its old state. A version update is rewritten in place,
// preserving sibling keys (features, optional, ...). Reverting a removed
// dependency re-adds it in bare-string form, which does not restore non-version
// keys it may originally have carried.
//
// The candidate is re-serialized from the parsed document, so comments and
// original formatting are not preserved; candidates are ephemeral build inputs,
// not files the user keeps.
func RenderCargo(to *CargoToml, changes []Change, applied map[string]bool) ([]byte, error) {
	doc := deepCopyMap(to.doc)
	for _, c := range changes {
		if applied[c.ID()] {
			continue // keep the new state, already present
		}
		sec, _ := doc[string(c.Section)].(map[string]any)
		switch c.Kind {
		case Updated:
			setCargoVersion(sec, c.Name, c.OldSpec)
		case Added:
			if sec != nil {
				delete(sec, c.Name)
			}
		case Removed:
			if sec == nil {
				sec = map[string]any{}
				doc[string(c.Section)] = sec
			}
			sec[c.Name] = c.OldSpec
		}
	}
	// Drop sections left empty by reverts so the candidate carries no stray
	// empty tables.
	for _, sec := range cargoSectionOrder {
		if m, ok := doc[string(sec)].(map[string]any); ok && len(m) == 0 {
			delete(doc, string(sec))
		}
	}
	out, err := toml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("render Cargo.toml: %w", err)
	}
	return out, nil
}

// setCargoVersion rewrites the version requirement of one dependency, keeping
// table-form entries (and their sibling keys) intact.
func setCargoVersion(sec map[string]any, name, version string) {
	if sec == nil {
		return
	}
	if entry, ok := sec[name].(map[string]any); ok {
		entry["version"] = version
		return
	}
	sec[name] = version
}

// ParseCargoLock extracts resolved versions from Cargo.lock. When a crate
// appears at multiple versions (allowed across semver-incompatible majors),
// the last wins; resolution data is used only for diagnostics.
func ParseCargoLock(data []byte) (Resolved, error) {
	var lock struct {
		Package []struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse Cargo.lock: %w", err)
	}
	out := Resolved{}
	for _, p := range lock.Package {
		if p.Name != "" && p.Version != "" {
			out[p.Name] = p.Version
		}
	}
	return out, nil
}

// deepCopyMap returns a recursively independent copy of a decoded TOML document
// so rendering never mutates the shared parse result.
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return deepCopyMap(x)
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = deepCopyValue(e)
		}
		return s
	default:
		return x
	}
}
