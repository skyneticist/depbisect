// Package manifest understands package.json, Cargo.toml, and go.mod manifests and their lockfiles.
//
// It provides structured parsing, diffing of direct dependency declarations
// between two manifests, and rendering of candidate manifests in which an
// arbitrary subset of those changes is applied. It never touches the
// filesystem; callers pass bytes in and get bytes out.
package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Section is a package.json dependency section.
type Section string

const (
	Dependencies         Section = "dependencies"
	DevDependencies      Section = "devDependencies"
	OptionalDependencies Section = "optionalDependencies"
)

// sectionOrder is the deterministic processing order for sections.
var sectionOrder = []Section{Dependencies, DevDependencies, OptionalDependencies}

// PackageJSON is a parsed package.json, retaining unknown top-level fields so
// candidate manifests can be rendered without losing information.
type PackageJSON struct {
	Name string
	// Sections maps each dependency section to dependency name -> version spec.
	// Absent sections have no entry.
	Sections map[Section]map[string]string
	// HasWorkspaces reports whether the manifest declares npm/yarn workspaces.
	HasWorkspaces bool

	// top holds every top-level field as raw JSON, for rendering.
	top map[string]json.RawMessage
}

// ParsePackageJSON parses package.json bytes. It fails on malformed JSON, on
// non-object documents, and on dependency sections whose values are not
// name -> string maps.
func ParsePackageJSON(data []byte) (*PackageJSON, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	p := &PackageJSON{
		Sections: map[Section]map[string]string{},
		top:      top,
	}
	if raw, ok := top["name"]; ok {
		// A non-string name is tolerated; the name is informational only.
		_ = json.Unmarshal(raw, &p.Name)
	}
	if raw, ok := top["workspaces"]; ok && string(raw) != "null" {
		p.HasWorkspaces = true
	}
	for _, sec := range sectionOrder {
		raw, ok := top[string(sec)]
		if !ok || string(raw) == "null" {
			continue
		}
		var deps map[string]json.RawMessage
		if err := json.Unmarshal(raw, &deps); err != nil {
			return nil, fmt.Errorf("parse package.json: section %q is not an object: %w", sec, err)
		}
		m := make(map[string]string, len(deps))
		for name, rawSpec := range deps {
			var spec string
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return nil, fmt.Errorf("parse package.json: %s entry %q has a non-string version spec", sec, name)
			}
			m[name] = spec
		}
		p.Sections[sec] = m
	}
	return p, nil
}

// ChangeKind classifies a direct dependency change.
type ChangeKind int

const (
	// Updated means the version spec changed.
	Updated ChangeKind = iota
	// Added means the dependency exists only in the new manifest.
	Added
	// Removed means the dependency exists only in the old manifest.
	Removed
)

// String returns a stable lower-case name for the kind.
func (k ChangeKind) String() string {
	switch k {
	case Updated:
		return "updated"
	case Added:
		return "added"
	case Removed:
		return "removed"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// Change is one direct dependency difference between two manifests.
type Change struct {
	Name    string
	Section Section
	Kind    ChangeKind
	// OldSpec is the version spec in the old manifest ("" for Added).
	OldSpec string
	// NewSpec is the version spec in the new manifest ("" for Removed).
	NewSpec string
	// OldResolved/NewResolved are exact installed versions taken from
	// lockfiles, when known ("" otherwise).
	OldResolved string
	NewResolved string
}

// ID returns a stable identifier unique among the changes of one diff.
func (c Change) ID() string {
	return string(c.Section) + ":" + c.Name
}

// String renders the change for terminal output, preferring exact resolved
// versions over specs when both lockfiles provided them.
func (c Change) String() string {
	old, new := c.OldSpec, c.NewSpec
	if c.OldResolved != "" && c.NewResolved != "" {
		old, new = c.OldResolved, c.NewResolved
	}
	suffix := ""
	if c.Section != Dependencies {
		suffix = " (" + string(c.Section) + ")"
	}
	switch c.Kind {
	case Added:
		if c.NewResolved != "" {
			new = c.NewResolved
		}
		return fmt.Sprintf("%s %s (added)%s", c.Name, new, suffix)
	case Removed:
		if c.OldResolved != "" {
			old = c.OldResolved
		}
		return fmt.Sprintf("%s %s (removed)%s", c.Name, old, suffix)
	default:
		return fmt.Sprintf("%s %s -> %s%s", c.Name, old, new, suffix)
	}
}

// Diff computes the direct dependency changes from old to new. The result is
// sorted by dependency name, then section, and is deterministic.
func Diff(old, new *PackageJSON) []Change {
	return diffSections(old.Sections, new.Sections, sectionOrder)
}

// diffSections computes the direct dependency changes between two sets of
// dependency sections, processed in the given order. The result is sorted by
// dependency name, then section, and is deterministic. It is shared by every
// ecosystem's manifest diff.
func diffSections(old, new map[Section]map[string]string, order []Section) []Change {
	var changes []Change
	for _, sec := range order {
		oldDeps := old[sec]
		newDeps := new[sec]
		for name, oldSpec := range oldDeps {
			newSpec, ok := newDeps[name]
			switch {
			case !ok:
				changes = append(changes, Change{Name: name, Section: sec, Kind: Removed, OldSpec: oldSpec})
			case newSpec != oldSpec:
				changes = append(changes, Change{Name: name, Section: sec, Kind: Updated, OldSpec: oldSpec, NewSpec: newSpec})
			}
		}
		for name, newSpec := range newDeps {
			if _, ok := oldDeps[name]; !ok {
				changes = append(changes, Change{Name: name, Section: sec, Kind: Added, NewSpec: newSpec})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Name != changes[j].Name {
			return changes[i].Name < changes[j].Name
		}
		return changes[i].Section < changes[j].Section
	})
	return changes
}

// Render produces candidate package.json bytes based on the new manifest, in
// which every change whose ID is in applied keeps its new state and every
// other change is reverted to its old state. Fields other than the dependency
// sections are copied from the new manifest verbatim. Top-level keys are
// emitted in sorted order; the output is deterministic.
func Render(to *PackageJSON, changes []Change, applied map[string]bool) ([]byte, error) {
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

	out := make(map[string]json.RawMessage, len(to.top)+3)
	for k, v := range to.top {
		out[k] = v
	}
	for _, sec := range sectionOrder {
		deps := sections[sec]
		if len(deps) == 0 {
			delete(out, string(sec))
			continue
		}
		raw, err := json.Marshal(deps)
		if err != nil {
			return nil, fmt.Errorf("render package.json: %w", err)
		}
		out[string(sec)] = raw
	}

	rendered, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render package.json: %w", err)
	}
	return append(rendered, '\n'), nil
}
