package manifest

import (
	"fmt"
	"slices"
	"strings"
)

// Requirements is a parsed requirements.txt for the pip package manager.
// requirements.txt has the same single-section shape as pyproject.toml, so it
// reuses the PyDependencies section and pySectionOrder. Each entry is keyed by
// its PEP 503-normalized name; the recorded spec is the requirement text
// following the name (extras, version specifiers, and environment marker).
//
// Unlike the other ecosystems, pip has no separate lockfile: a pinned
// requirements.txt (pip-compile or pip freeze output) is both the manifest and
// the resolution. ParseRequirementsPins extracts the exact "==" pins from the
// same bytes for the lockfile side of the seam.
//
// Lines the parser cannot bisect are preserved for rendering but not recorded:
// blank and comment lines, option lines ("-r", "-e", "--index-url", ...),
// direct references ("name @ url"), bare URL or path requirements, and
// requirement lines carrying per-requirement options. Hash pins ("--hash=...")
// are rejected outright: any hash activates pip's hash-checking mode for the
// whole file, and a candidate that rewrites a version without its hash could
// never install.
type Requirements struct {
	// Sections maps the single PyDependencies section to normalized dependency
	// name -> requirement text. Absent when there are no dependencies.
	Sections map[Section]map[string]string

	// lines holds every logical line for verbatim rendering.
	lines []reqLine
}

// reqLine is one logical line of a requirements file: a physical line plus any
// continuation lines joined to it by trailing backslashes.
type reqLine struct {
	// raw is the original text, without the trailing newline but with any
	// embedded continuation newlines, emitted verbatim for untouched lines.
	raw string
	// name is the PEP 503-normalized package name when the line is a
	// bisectable requirement, "" otherwise.
	name string
	// rawName is the verbatim name token, for format-preserving rewrites.
	rawName string
}

// HasWorkspaceLayout implements Parsed. pip has no workspace concept — a
// requirements file describes a single environment — so this is always false.
func (r *Requirements) HasWorkspaceLayout() bool { return false }

// ParseRequirements parses requirements.txt bytes. The only rejected input is
// a file using "--hash" pins (see Requirements); everything else parses, with
// non-requirement lines preserved verbatim and skipped.
func ParseRequirements(data []byte) (*Requirements, error) {
	r := &Requirements{Sections: map[Section]map[string]string{}}
	deps := map[string]string{}

	physical := strings.Split(string(data), "\n")
	if n := len(physical); n > 0 && physical[n-1] == "" {
		physical = physical[:n-1] // drop the empty element after a final newline
	}
	for i := 0; i < len(physical); i++ {
		raw := physical[i]
		logical := strings.TrimSuffix(raw, "\r")
		// A trailing backslash joins the next physical line, per pip.
		for strings.HasSuffix(logical, `\`) && i+1 < len(physical) {
			i++
			raw += "\n" + physical[i]
			logical = strings.TrimSuffix(logical, `\`) + strings.TrimSuffix(physical[i], "\r")
		}
		line := reqLine{raw: raw}
		trimmed := strings.TrimSpace(stripReqComment(logical))
		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "-"):
			// Blank, comment-only, or option line ("-r", "-c", "-e",
			// "--index-url", ...): preserved verbatim, never bisected.
		case strings.Contains(trimmed, "--hash"):
			return nil, fmt.Errorf("parse requirements.txt: %q uses --hash pins; "+
				"hash-checking mode applies to every requirement in the file, so DepBisect "+
				"cannot rewrite versions in it (bisect from a requirements file without hashes)",
				firstReqToken(trimmed))
		default:
			name, rawName, spec, ok := splitRequirement(trimmed)
			// Per-requirement options other than --hash (e.g.
			// --config-settings) would end up inside the spec; such lines are
			// preserved but not bisected, like direct references.
			if ok && isValidPyName(rawName) && !strings.Contains(spec, "--") {
				line.name, line.rawName = name, rawName
				deps[name] = spec
			}
		}
		r.lines = append(r.lines, line)
	}
	if len(deps) > 0 {
		r.Sections[PyDependencies] = deps
	}
	return r, nil
}

// stripReqComment removes a requirements-file comment: everything from a "#"
// that starts the line or follows whitespace. A "#" embedded in a token (e.g.
// a URL fragment like "#egg=") is kept, matching pip.
func stripReqComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

// firstReqToken returns the first whitespace-delimited token of a logical
// line, for one-line error messages about that line.
func firstReqToken(line string) string {
	if f := strings.Fields(line); len(f) > 0 {
		return f[0]
	}
	return line
}

// isValidPyName reports whether name is a valid PEP 508 project name: ASCII
// letters, digits, ".", "-", and "_", beginning and ending with a letter or
// digit. Bare URL and path requirements fail this check and are skipped.
func isValidPyName(name string) bool {
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
			if i == 0 || i == len(name)-1 {
				return false
			}
		default:
			return false
		}
	}
	return len(name) > 0
}

// DiffRequirements computes the direct dependency changes from old to new,
// using the shared section diff over the requirement lines.
func DiffRequirements(old, new *Requirements) []Change {
	return diffSections(old.Sections, new.Sections, pySectionOrder)
}

// RenderRequirements produces candidate requirements.txt bytes based on the
// new manifest, in which every change whose ID is in applied keeps its new
// state and every other change reverts to its old state. Untouched lines —
// including comments, option lines, and skipped references — are emitted
// verbatim. A reverted update rewrites the first matching requirement line,
// keeping its original name token but dropping any trailing comment or
// continuation layout; a reverted addition drops every matching line; a
// reverted removal appends a normalized "name+spec" line. Candidates are
// ephemeral build inputs, not files the user keeps.
func RenderRequirements(to *Requirements, changes []Change, applied map[string]bool) ([]byte, error) {
	lines := slices.Clone(to.lines)
	var appendix []string
	for _, c := range changes {
		if applied[c.ID()] {
			continue // keep the new state, already present
		}
		switch c.Kind {
		case Updated:
			lines, appendix = rewriteRequirementLine(lines, appendix, c.Name, c.OldSpec)
		case Added:
			lines = dropRequirementLines(lines, c.Name)
		case Removed:
			appendix = append(appendix, c.Name+c.OldSpec)
		}
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.raw)
		b.WriteByte('\n')
	}
	for _, s := range appendix {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// rewriteRequirementLine replaces the spec of the first line matching name,
// keeping its verbatim name token. If no line matches (the target manifest
// dropped it), a normalized requirement is appended instead.
func rewriteRequirementLine(lines []reqLine, appendix []string, name, spec string) ([]reqLine, []string) {
	for i, l := range lines {
		if l.name == name {
			lines[i].raw = l.rawName + spec
			return lines, appendix
		}
	}
	return lines, append(appendix, name+spec)
}

// dropRequirementLines removes every line whose normalized name matches name.
func dropRequirementLines(lines []reqLine, name string) []reqLine {
	out := lines[:0]
	for _, l := range lines {
		if l.name != name {
			out = append(out, l)
		}
	}
	return out
}

// ParseRequirementsPins extracts resolved versions from requirements.txt,
// standing in for a lockfile parser: pip has no separate lockfile, so exact
// "==" (or "===") pins are the resolution. Requirements with any other
// specifier — ranges, wildcards, compound constraints — resolve only at
// install time and stay unresolved here.
func ParseRequirementsPins(data []byte) (Resolved, error) {
	r, err := ParseRequirements(data)
	if err != nil {
		return nil, err
	}
	out := Resolved{}
	for name, spec := range r.Sections[PyDependencies] {
		if v, ok := exactPin(spec); ok {
			out[name] = v
		}
	}
	return out, nil
}

// exactPin extracts the version from a requirement spec that pins exactly one
// version: "==x" or "===x", tolerating an extras prefix and an environment
// marker, and rejecting wildcards and compound constraints.
func exactPin(spec string) (string, bool) {
	s := spec
	if strings.HasPrefix(s, "[") {
		i := strings.IndexByte(s, ']')
		if i < 0 {
			return "", false
		}
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	s, ok := strings.CutPrefix(strings.TrimSpace(s), "==")
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "=")) // "===": arbitrary equality
	if s == "" || strings.ContainsAny(s, "*,<>!~= \t") {
		return "", false
	}
	return s, true
}

// LockfileOnlyRequirements returns dependencies declared identically in both
// manifests whose pinned resolution nonetheless differs. Because the pins are
// derived from the very specs being compared, this is empty in practice; it
// exists so the pip ecosystem satisfies the same seam as the others.
func LockfileOnlyRequirements(old, new *Requirements, oldR, newR Resolved) []LockfileChange {
	return lockfileOnly(old.Sections, new.Sections, oldR, newR, pySectionOrder)
}
