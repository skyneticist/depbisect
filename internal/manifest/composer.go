package manifest

import (
	"encoding/json"
	"fmt"
)

// Composer dependency sections of a composer.json.
const (
	ComposerRequire    Section = "require"
	ComposerRequireDev Section = "require-dev"
)

// composerSectionOrder is the deterministic processing order for composer.json
// sections.
var composerSectionOrder = []Section{ComposerRequire, ComposerRequireDev}

// ComposerJSON is a parsed composer.json for the composer package manager,
// retaining unknown top-level fields so candidate manifests can be rendered
// without losing information. Platform packages (php, ext-*, lib-*) are kept
// like any other requirement: a changed platform constraint is a bisectable
// manifest change, it just never gets a resolved version from composer.lock.
type ComposerJSON struct {
	Name string
	// Sections maps each dependency section to dependency name -> version
	// constraint. Absent sections have no entry.
	Sections map[Section]map[string]string

	// top holds every top-level field as raw JSON, for rendering.
	top map[string]json.RawMessage
}

// HasWorkspaceLayout implements Parsed. Composer has no workspace concept —
// each composer.json is a standalone package — so this is always false.
func (c *ComposerJSON) HasWorkspaceLayout() bool { return false }

// ProjectName implements Parsed. The root package never appears among
// composer.lock's packages, but the name is returned for interface parity.
func (c *ComposerJSON) ProjectName() string { return c.Name }

// ParseComposerJSON parses composer.json bytes. It fails on malformed JSON, on
// non-object documents, and on require/require-dev sections whose values are
// not name -> string maps.
func ParseComposerJSON(data []byte) (*ComposerJSON, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse composer.json: %w", err)
	}
	c := &ComposerJSON{
		Sections: map[Section]map[string]string{},
		top:      top,
	}
	if raw, ok := top["name"]; ok {
		// A non-string name is tolerated; the name is informational only.
		_ = json.Unmarshal(raw, &c.Name)
	}
	for _, sec := range composerSectionOrder {
		raw, ok := top[string(sec)]
		if !ok || string(raw) == "null" {
			continue
		}
		var deps map[string]json.RawMessage
		if err := json.Unmarshal(raw, &deps); err != nil {
			return nil, fmt.Errorf("parse composer.json: section %q is not an object: %w", sec, err)
		}
		m := make(map[string]string, len(deps))
		for name, rawSpec := range deps {
			var spec string
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return nil, fmt.Errorf("parse composer.json: %s entry %q has a non-string version constraint", sec, name)
			}
			m[name] = spec
		}
		c.Sections[sec] = m
	}
	return c, nil
}

// DiffComposer computes the direct dependency changes from old to new, using
// the shared section diff over require and require-dev.
func DiffComposer(old, new *ComposerJSON) []Change {
	return diffSections(old.Sections, new.Sections, composerSectionOrder)
}

// RenderComposer produces candidate composer.json bytes based on the new
// manifest, in which every change whose ID is in applied keeps its new state
// and every other change is reverted to its old state. Fields other than the
// dependency sections are copied from the new manifest verbatim. Top-level
// keys are emitted in sorted order; the output is deterministic.
func RenderComposer(to *ComposerJSON, changes []Change, applied map[string]bool) ([]byte, error) {
	sections := map[Section]map[string]string{}
	for sec, deps := range to.Sections {
		m := make(map[string]string, len(deps))
		for k, v := range deps {
			m[k] = v
		}
		sections[sec] = m
	}

	for _, c := range changes {
		if applied[c.ID()] {
			continue // keep the new state, already present
		}
		sec := sections[c.Section]
		if sec == nil {
			sec = map[string]string{}
			sections[c.Section] = sec
		}
		switch c.Kind {
		case Updated:
			sec[c.Name] = c.OldSpec
		case Added:
			delete(sec, c.Name)
		case Removed:
			sec[c.Name] = c.OldSpec
		}
	}

	out := make(map[string]json.RawMessage, len(to.top)+2)
	for k, v := range to.top {
		out[k] = v
	}
	for _, sec := range composerSectionOrder {
		deps := sections[sec]
		if len(deps) == 0 {
			delete(out, string(sec))
			continue
		}
		raw, err := json.Marshal(deps)
		if err != nil {
			return nil, fmt.Errorf("render composer.json: %w", err)
		}
		out[string(sec)] = raw
	}

	rendered, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render composer.json: %w", err)
	}
	return append(rendered, '\n'), nil
}

// ParseComposerLock extracts resolved versions from composer.lock: the
// packages array resolves require, packages-dev resolves require-dev.
// Versions are kept verbatim ("v2.9.1", "2.9.1", "dev-main", ...); platform
// requirements have no lock entry and stay unresolved.
func ParseComposerLock(data []byte) (Resolved, error) {
	var lock struct {
		Packages    []composerLockPackage `json:"packages"`
		PackagesDev []composerLockPackage `json:"packages-dev"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse composer.lock: %w", err)
	}
	out := Resolved{}
	for _, p := range lock.Packages {
		if p.Name != "" && p.Version != "" {
			out[p.Name] = p.Version
		}
	}
	for _, p := range lock.PackagesDev {
		if p.Name != "" && p.Version != "" {
			// require wins if a name improbably appears in both arrays.
			if _, exists := out[p.Name]; !exists {
				out[p.Name] = p.Version
			}
		}
	}
	return out, nil
}

// composerLockPackage is one entry of composer.lock's packages/packages-dev.
type composerLockPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// LockfileOnlyComposer returns dependencies declared identically in both
// manifests whose composer.lock resolution nonetheless differs.
func LockfileOnlyComposer(old, new *ComposerJSON, oldR, newR Resolved) []LockfileChange {
	return lockfileOnly(old.Sections, new.Sections, oldR, newR, composerSectionOrder)
}
