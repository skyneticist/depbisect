package manifest

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// PyDependencies is the single bisectable "section" of a pyproject.toml: its
// PEP 621 [project.dependencies] array. uv's optional-dependencies (extras) and
// dependency-groups are not bisected yet — like the Cargo path skipping
// git/path dependencies, they are simply not recorded — so one section
// suffices, mirroring Go's single require section.
const PyDependencies Section = "dependencies"

// pySectionOrder is the deterministic processing order for pyproject sections.
var pySectionOrder = []Section{PyDependencies}

// PyProject is a parsed pyproject.toml (PEP 621) for the uv package manager.
// Only [project.dependencies] entries are recorded in Sections, keyed by their
// PEP 503-normalized name; the recorded spec is the requirement text following
// the name (extras, version specifiers, and environment marker). Bare
// requirements ("requests") keep an empty spec; direct-reference requirements
// ("name @ url") carry no bisectable version and are skipped. Like CargoToml it
// retains the full decoded document so candidate manifests can be rendered
// without losing unrelated fields.
type PyProject struct {
	Name string
	// Sections maps the single PyDependencies section to normalized dependency
	// name -> requirement text. Absent when there are no dependencies.
	Sections map[Section]map[string]string
	// HasWorkspace reports whether the manifest declares a uv workspace
	// ([tool.uv.workspace]).
	HasWorkspace bool

	// doc holds the full decoded document, for rendering.
	doc map[string]any
}

// ParsePyproject parses pyproject.toml bytes. It fails on malformed TOML and on
// a [project].dependencies value that is not an array of strings.
func ParsePyproject(data []byte) (*PyProject, error) {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse pyproject.toml: %w", err)
	}
	p := &PyProject{
		Sections: map[Section]map[string]string{},
		doc:      doc,
	}
	if uvWorkspacePresent(doc) {
		p.HasWorkspace = true
	}
	project, ok := doc["project"].(map[string]any)
	if !ok {
		// No [project] table (e.g. a build-backend-only or workspace-root
		// manifest); nothing to bisect.
		return p, nil
	}
	// A non-string name is tolerated; the name is informational only.
	if name, ok := project["name"].(string); ok {
		p.Name = name
	}
	raw, ok := project["dependencies"]
	if !ok || raw == nil {
		return p, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("parse pyproject.toml: [project].dependencies is not an array")
	}
	m := make(map[string]string, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("parse pyproject.toml: [project].dependencies entry is not a string")
		}
		name, _, spec, ok := splitRequirement(s)
		if !ok {
			continue // direct URL/path reference: no bisectable version
		}
		m[name] = spec
	}
	if len(m) > 0 {
		p.Sections[PyDependencies] = m
	}
	return p, nil
}

// HasWorkspaceLayout implements Parsed.
func (p *PyProject) HasWorkspaceLayout() bool { return p.HasWorkspace }

// ProjectName implements Parsed. uv.lock records the root project as a
// [[package] entry under its PEP 503-normalized name, so the name is
// normalized to match.
func (p *PyProject) ProjectName() string { return normalizePyName(p.Name) }

// uvWorkspacePresent reports whether the document declares a [tool.uv.workspace]
// table, which marks a uv workspace root.
func uvWorkspacePresent(doc map[string]any) bool {
	tool, _ := doc["tool"].(map[string]any)
	uv, _ := tool["uv"].(map[string]any)
	_, ok := uv["workspace"]
	return ok
}

// isReqDelim reports whether r terminates the package-name token of a PEP 508
// requirement (the start of extras, a version specifier, a marker, a URL
// reference, or whitespace). Hyphens, underscores, and dots are part of the
// name and are deliberately excluded.
func isReqDelim(r rune) bool {
	switch r {
	case '[', '(', ';', '@', '<', '>', '=', '!', '~', ' ', '\t':
		return true
	}
	return false
}

// splitRequirement splits a PEP 508 requirement string into its normalized
// package name, the verbatim name token (for format-preserving rewrites), and
// the requirement text that follows it (extras, version specifiers, marker). It
// reports false for direct-reference requirements ("name @ url"), which carry no
// bisectable version, and for an empty name.
func splitRequirement(req string) (name, rawName, spec string, ok bool) {
	req = strings.TrimSpace(req)
	rawName = req
	if i := strings.IndexFunc(req, isReqDelim); i >= 0 {
		rawName = req[:i]
		spec = strings.TrimSpace(req[i:])
	}
	if rawName == "" || strings.HasPrefix(spec, "@") {
		return "", "", "", false
	}
	return normalizePyName(rawName), rawName, spec, true
}

// normalizePyName applies PEP 503 normalization: lower-case and collapse any
// run of "-", "_", or "." into a single "-". Package names are ASCII, so a
// byte-wise lower-case is sufficient.
func normalizePyName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	prevSep := false
	for _, r := range name {
		switch r {
		case '-', '_', '.':
			if !prevSep {
				b.WriteByte('-')
				prevSep = true
			}
		default:
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
			prevSep = false
		}
	}
	return b.String()
}

// DiffPyproject computes the direct dependency changes from old to new, using
// the shared section diff over the [project.dependencies] array.
func DiffPyproject(old, new *PyProject) []Change {
	return diffSections(old.Sections, new.Sections, pySectionOrder)
}

// RenderPyproject produces candidate pyproject.toml bytes based on the new
// manifest, in which every change whose ID is in applied keeps its new state and
// every other change reverts to its old state. A reverted update rewrites the
// requirement in place, keeping its original name token (and thus extras and
// marker that traveled with the old spec); a reverted addition is dropped; a
// reverted removal is re-added in normalized "name+spec" form. Requirements not
// involved in any change — including skipped URL references — are preserved.
//
// The candidate is re-serialized from the parsed document, so comments and
// original formatting are not preserved; candidates are ephemeral build inputs,
// not files the user keeps.
func RenderPyproject(to *PyProject, changes []Change, applied map[string]bool) ([]byte, error) {
	doc := deepCopyMap(to.doc)
	project, ok := doc["project"].(map[string]any)
	if !ok {
		project = map[string]any{}
		doc["project"] = project
	}
	deps := pyDepList(project)
	for _, c := range changes {
		if applied[c.ID()] {
			continue // keep the new state, already present
		}
		switch c.Kind {
		case Updated:
			deps = setPyRequirement(deps, c.Name, c.OldSpec)
		case Added:
			deps = removePyRequirement(deps, c.Name)
		case Removed:
			deps = append(deps, c.Name+c.OldSpec)
		}
	}
	if len(deps) == 0 {
		delete(project, "dependencies")
	} else {
		project["dependencies"] = deps
	}
	out, err := toml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("render pyproject.toml: %w", err)
	}
	return out, nil
}

// pyDepList extracts [project.dependencies] as a string slice, ignoring any
// non-string entries (which Parse would already have rejected).
func pyDepList(project map[string]any) []string {
	raw, _ := project["dependencies"].([]any)
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// setPyRequirement rewrites the requirement whose normalized name matches name,
// keeping its original name token and replacing the trailing spec. If no entry
// matches (the target manifest dropped it), the requirement is appended.
func setPyRequirement(deps []string, name, spec string) []string {
	for i, req := range deps {
		if n, raw, _, ok := splitRequirement(req); ok && n == name {
			deps[i] = raw + spec
			return deps
		}
	}
	return append(deps, name+spec)
}

// removePyRequirement drops the requirement whose normalized name matches name.
func removePyRequirement(deps []string, name string) []string {
	out := make([]string, 0, len(deps))
	for _, req := range deps {
		if n, _, _, ok := splitRequirement(req); ok && n == name {
			continue
		}
		out = append(out, req)
	}
	return out
}

// ParseUvLock extracts resolved versions from uv.lock. Like Cargo.lock it is a
// TOML document carrying a [[package]] array; the root project appears as one of
// the entries and is harmlessly included. Names are PEP 503-normalized to match
// the keys produced from the manifest.
func ParseUvLock(data []byte) (Resolved, error) {
	var lock struct {
		Package []struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parse uv.lock: %w", err)
	}
	out := Resolved{}
	for _, p := range lock.Package {
		if p.Name != "" && p.Version != "" {
			out[normalizePyName(p.Name)] = p.Version
		}
	}
	return out, nil
}

// LockfileOnlyPyproject returns dependencies declared identically in both
// manifests whose uv.lock resolution nonetheless differs.
func LockfileOnlyPyproject(old, new *PyProject, oldR, newR Resolved) []LockfileChange {
	return lockfileOnly(old.Sections, new.Sections, oldR, newR, pySectionOrder)
}
