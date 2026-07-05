package manifest

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseYarnLock extracts resolved versions from yarn.lock. Both the classic
// v1 format (an indented custom syntax) and the Berry v2+ format (YAML with a
// top-level __metadata block) are supported; the format is detected from the
// content.
//
// yarn.lock is keyed by descriptor (name@range), so one name can resolve to
// several versions when transitive ranges conflict. Resolved is keyed by bare
// name, so conflicted names are omitted entirely rather than reported with an
// arbitrary winner; callers then see a blank resolution for them, which the
// lockfile-only diagnostics already skip.
func ParseYarnLock(data []byte) (Resolved, error) {
	if yarnLockIsBerry(data) {
		return parseYarnBerry(data)
	}
	return parseYarnClassic(data)
}

// yarnLockIsBerry reports whether the lockfile is in the Berry (v2+) YAML
// format, identified by its mandatory top-level __metadata block. Classic v1
// files are not reliably valid YAML, so content sniffing beats parse-and-retry.
func yarnLockIsBerry(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "__metadata:") {
			return true
		}
	}
	return false
}

// parseYarnBerry extracts versions from a Berry yarn.lock. Only descriptors
// using the npm: protocol are recorded; workspace:, patch:, link: and other
// non-registry entries have no bisectable upstream version, mirroring how the
// Cargo parser skips git and path dependencies.
func parseYarnBerry(data []byte) (Resolved, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse yarn.lock: %w", err)
	}

	out := Resolved{}
	conflicted := map[string]bool{}
	for key, raw := range doc {
		if key == "__metadata" {
			continue
		}
		names := yarnNpmDescriptorNames(key)
		if len(names) == 0 {
			continue
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parse yarn.lock: entry %q is not a mapping", key)
		}
		version, _ := entry["version"].(string)
		if version == "" {
			return nil, fmt.Errorf("parse yarn.lock: entry %q has no version", key)
		}
		for _, name := range names {
			addYarnResolved(out, conflicted, name, version)
		}
	}
	return out, nil
}

// yarnNpmDescriptorNames returns the dependency names from a Berry lock key —
// a comma-separated descriptor list like "chalk@npm:^4.0.0, chalk@npm:^4.1.2"
// — keeping only npm:-protocol descriptors. Descriptors cannot contain commas
// (npm version ranges never use them), so a plain comma split is exact.
func yarnNpmDescriptorNames(key string) []string {
	var names []string
	for _, desc := range strings.Split(key, ",") {
		name, rng, ok := splitYarnDescriptor(strings.TrimSpace(desc))
		if ok && strings.HasPrefix(rng, "npm:") {
			names = append(names, name)
		}
	}
	return names
}

// parseYarnClassic extracts versions from a classic (v1) yarn.lock. Entries
// look like:
//
//	"@scope/pkg@^1.0.0", "@scope/pkg@^1.2.0":
//	  version "1.2.3"
//	  resolved "https://registry.yarnpkg.com/..."
//	  dependencies:
//	    other-pkg "^2.0.0"
//
// Only an entry's own version field — at exactly two spaces of indentation —
// is read. Deeper lines such as the dependencies block are ignored, so a
// transitive dependency literally named "version" (which exists on npm)
// cannot be mistaken for the version field. Classic entries are recorded for
// every descriptor protocol, matching ParsePackageLock's
// record-everything behavior.
func parseYarnClassic(data []byte) (Resolved, error) {
	out := Resolved{}
	conflicted := map[string]bool{}
	inEntry := false
	var names []string // current entry's descriptor names
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") { // unindented: an entry header
			header, ok := strings.CutSuffix(line, ":")
			if !ok {
				return nil, fmt.Errorf("parse yarn.lock: line %d: expected an entry header ending in %q", n+1, ":")
			}
			names, inEntry = yarnClassicDescriptorNames(header), true
			continue
		}
		version, ok := yarnClassicVersion(line)
		if !ok {
			continue
		}
		if !inEntry {
			return nil, fmt.Errorf("parse yarn.lock: line %d: version field outside an entry", n+1)
		}
		for _, name := range names {
			addYarnResolved(out, conflicted, name, version)
		}
		names, inEntry = nil, false // one version field per entry
	}
	return out, nil
}

// yarnClassicDescriptorNames parses a classic entry header — one or more
// comma-separated, individually quoted-or-bare descriptors — into dependency
// names. Unparseable descriptors are skipped rather than failing the whole
// file.
func yarnClassicDescriptorNames(header string) []string {
	var names []string
	for _, desc := range strings.Split(header, ",") {
		if name, _, ok := splitYarnDescriptor(strings.TrimSpace(desc)); ok {
			names = append(names, name)
		}
	}
	return names
}

// yarnClassicVersion matches an entry's own version field, which classic yarn
// always writes at exactly two spaces of indentation: `  version "1.2.3"`.
// Deeper-indented lines fail the prefix cut, so dependency blocks never match.
func yarnClassicVersion(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "  version ")
	if !ok {
		return "", false
	}
	version := strings.Trim(strings.TrimSpace(rest), `"`)
	return version, version != ""
}

// splitYarnDescriptor splits a "name@range" descriptor at the range
// separator: the first "@" after the name's first character, which honors
// leading @scope names ("@babel/core@^7.0.0" → "@babel/core", "^7.0.0") and
// lets ranges contain "@" themselves (aliases like "alias@npm:left-pad@^1").
// Surrounding quotes, as written by classic yarn, are stripped first.
func splitYarnDescriptor(desc string) (name, rng string, ok bool) {
	desc = strings.Trim(desc, `"`)
	if desc == "" {
		return "", "", false
	}
	i := strings.Index(desc[1:], "@")
	if i < 0 || desc[i+2:] == "" {
		return "", "", false
	}
	return desc[:i+1], desc[i+2:], true
}

// addYarnResolved records name → version, dropping names that resolve to more
// than one distinct version across lock entries (see ParseYarnLock).
func addYarnResolved(out Resolved, conflicted map[string]bool, name, version string) {
	if conflicted[name] {
		return
	}
	if prev, ok := out[name]; ok && prev != version {
		delete(out, name)
		conflicted[name] = true
		return
	}
	out[name] = version
}
