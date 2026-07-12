package manifest

import "strings"

// This file turns lockfile-only drift into bisectable changes. A dependency
// whose manifest spec is unchanged but whose lockfile resolution moved cannot
// be bisected by rewriting specs the manifests already disagree on — so a
// synthetic Change is built instead, whose old state pins the old resolution
// as an exact version spec and whose new state keeps the manifest's spec
// untouched. Reverting the change forces the package manager to re-resolve
// that one dependency to its old version; applying it leaves the target
// manifest byte-identical, so the checked-out lockfile supplies the new
// resolution. Everything downstream (Render, ddmin, memoization, reporting)
// treats these like ordinary changes.

// pinChanges builds one synthetic Change per pinnable lockfile-only entry.
// pin returns the exact-pin spec for the entry's old resolution, or false for
// entries that cannot be pinned faithfully (non-registry resolutions, protocol
// specs, ...); those are skipped and remain diagnostics.
func pinChanges(lcs []LockfileChange, pin func(LockfileChange) (string, bool)) []Change {
	var out []Change
	for _, lc := range lcs {
		oldSpec, ok := pin(lc)
		if !ok {
			continue
		}
		out = append(out, Change{
			Name:         lc.Name,
			Section:      lc.Section,
			Kind:         Updated,
			OldSpec:      oldSpec,
			NewSpec:      lc.Spec,
			LockfileOnly: true,
		})
	}
	return out
}

// plainVersion reports whether s looks like a bare registry version ("1.2.3"),
// as opposed to a path, URL, git reference, or symbolic version.
func plainVersion(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// jsPin pins a package.json dependency to its old resolved version. Protocol
// and aliasing specs (npm:, file:, link:, workspace:, git URLs, GitHub
// user/repo shorthands) cannot be replaced by a bare version without changing
// what is installed, so they are unpinnable.
func jsPin(lc LockfileChange) (string, bool) {
	if !plainVersion(lc.OldResolved) || strings.ContainsAny(lc.Spec, ":/") {
		return "", false
	}
	return lc.OldResolved, true
}

// cargoPin pins a Cargo.toml dependency with an exact requirement ("=1.2.3").
// Bare versions default to caret semantics, so the "=" prefix is required.
func cargoPin(lc LockfileChange) (string, bool) {
	if !plainVersion(lc.OldResolved) {
		return "", false
	}
	return "=" + lc.OldResolved, true
}

// pyPin pins a PEP 508 requirement to its old resolved version, preserving the
// extras and environment marker that travel in the spec text: only the version
// specifier between them is replaced.
func pyPin(lc LockfileChange) (string, bool) {
	if !plainVersion(lc.OldResolved) {
		return "", false
	}
	extras, _, marker := splitPySpec(lc.Spec)
	return extras + "==" + lc.OldResolved + marker, true
}

// splitPySpec splits PEP 508 requirement text (everything after the name) into
// its leading extras ("[socks]"), the version specifier, and the environment
// marker (from ";" on, kept verbatim including the separator). Absent parts
// are empty strings.
func splitPySpec(spec string) (extras, specifier, marker string) {
	rest := spec
	if strings.HasPrefix(rest, "[") {
		if i := strings.IndexByte(rest, ']'); i >= 0 {
			extras, rest = rest[:i+1], rest[i+1:]
		}
	}
	if i := strings.IndexByte(rest, ';'); i >= 0 {
		rest, marker = rest[:i], rest[i:]
	}
	return extras, strings.TrimSpace(rest), marker
}

// composerPin pins a composer.json dependency to its old resolved version,
// verbatim as recorded in composer.lock (Composer treats a bare version, with
// or without its "v" prefix, as an exact constraint). Branch versions
// ("dev-main") and inline aliases are not exact releases and are unpinnable.
func composerPin(lc LockfileChange) (string, bool) {
	v := strings.TrimPrefix(lc.OldResolved, "v")
	if !plainVersion(v) || strings.Contains(lc.Spec, " as ") {
		return "", false
	}
	return lc.OldResolved, true
}
