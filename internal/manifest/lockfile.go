package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Resolved maps a dependency name to the exact version a lockfile resolved it
// to. It may contain entries beyond the manifest's direct dependencies
// (npm lockfile v1 flattens transitive dependencies into the same namespace);
// callers must only look up names they know are direct dependencies.
type Resolved map[string]string

// ParsePackageLock extracts resolved versions from package-lock.json
// (lockfile versions 1 through 3).
func ParsePackageLock(data []byte) (Resolved, error) {
	var lock struct {
		LockfileVersion int                        `json:"lockfileVersion"`
		Packages        map[string]json.RawMessage `json:"packages"`
		Dependencies    map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse package-lock.json: %w", err)
	}

	out := Resolved{}
	switch {
	case lock.Packages != nil: // lockfile v2/v3
		for path, raw := range lock.Packages {
			name, ok := topLevelModuleName(path)
			if !ok {
				continue
			}
			var entry struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				return nil, fmt.Errorf("parse package-lock.json: entry %q: %w", path, err)
			}
			if entry.Version != "" {
				out[name] = entry.Version
			}
		}
	case lock.Dependencies != nil: // lockfile v1
		for name, entry := range lock.Dependencies {
			if entry.Version != "" {
				out[name] = entry.Version
			}
		}
	default:
		return nil, fmt.Errorf("parse package-lock.json: no %q or %q field (lockfileVersion %d)",
			"packages", "dependencies", lock.LockfileVersion)
	}
	return out, nil
}

// topLevelModuleName converts a lockfile package path like
// "node_modules/@scope/name" to the module name, rejecting the root entry ""
// and nested paths like "node_modules/a/node_modules/b".
func topLevelModuleName(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, "node_modules/")
	if !ok || name == "" || strings.Contains(name, "/node_modules/") {
		return "", false
	}
	return name, true
}

// ParsePnpmLock extracts the root project's resolved versions from
// pnpm-lock.yaml. Lockfile versions 5.x (string values), 6.x (specifier and
// version objects) and 9.x (importers) are supported.
func ParsePnpmLock(data []byte) (Resolved, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse pnpm-lock.yaml: %w", err)
	}

	root := doc
	if importers, ok := doc["importers"].(map[string]any); ok {
		rootImporter, ok := importers["."].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parse pnpm-lock.yaml: importers section has no %q entry (workspace layout?)", ".")
		}
		root = rootImporter
	}

	out := Resolved{}
	for _, sec := range sectionOrder {
		raw, ok := root[string(sec)]
		if !ok || raw == nil {
			continue
		}
		deps, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parse pnpm-lock.yaml: section %q is not a mapping", sec)
		}
		for name, v := range deps {
			version, err := pnpmEntryVersion(v)
			if err != nil {
				return nil, fmt.Errorf("parse pnpm-lock.yaml: dependency %q: %w", name, err)
			}
			if version != "" {
				out[name] = version
			}
		}
	}
	return out, nil
}

// pnpmEntryVersion extracts the version from one pnpm lockfile dependency
// entry: either a plain string (v5) or a {specifier, version} mapping (v6+).
func pnpmEntryVersion(v any) (string, error) {
	switch entry := v.(type) {
	case string:
		return stripPnpmPeerSuffix(entry), nil
	case map[string]any:
		version, _ := entry["version"].(string)
		return stripPnpmPeerSuffix(version), nil
	default:
		return "", fmt.Errorf("unexpected entry type %T", v)
	}
}

// stripPnpmPeerSuffix removes pnpm peer-dependency suffixes such as
// "1.2.3(react@18.2.0)" or "1.2.3_react@18.2.0". Non-registry versions
// (link:, file:, git URLs, ...) are returned unchanged.
func stripPnpmPeerSuffix(version string) string {
	if version == "" || version[0] < '0' || version[0] > '9' {
		return version
	}
	if i := strings.IndexByte(version, '('); i >= 0 {
		version = version[:i]
	}
	if i := strings.IndexByte(version, '_'); i >= 0 {
		version = version[:i]
	}
	return version
}

// AnnotateResolved fills each change's OldResolved/NewResolved from the given
// lockfile resolutions. Unknown names are left blank.
func AnnotateResolved(changes []Change, old, new Resolved) {
	for i := range changes {
		changes[i].OldResolved = old[changes[i].Name]
		changes[i].NewResolved = new[changes[i].Name]
	}
}

// LockfileChange describes a dependency whose manifest spec is unchanged but
// whose lockfile resolution differs between revisions. DepBisect cannot
// bisect these (it materializes candidates by editing version specs), so they
// are surfaced as diagnostics instead.
type LockfileChange struct {
	Name        string
	Section     Section
	Spec        string
	OldResolved string
	NewResolved string
}

// LockfileOnly returns dependencies declared identically in both manifests
// whose resolved versions nonetheless differ. Dependencies with an unknown
// resolution on either side are skipped.
func LockfileOnly(old, new *PackageJSON, oldR, newR Resolved) []LockfileChange {
	var out []LockfileChange
	for _, sec := range sectionOrder {
		for name, spec := range new.Sections[sec] {
			oldSpec, ok := old.Sections[sec][name]
			if !ok || oldSpec != spec {
				continue // added or spec-changed: handled by Diff
			}
			ov, nv := oldR[name], newR[name]
			if ov == "" || nv == "" || ov == nv {
				continue
			}
			out = append(out, LockfileChange{Name: name, Section: sec, Spec: spec, OldResolved: ov, NewResolved: nv})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Section < out[j].Section
	})
	return out
}
