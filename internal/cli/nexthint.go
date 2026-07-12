package cli

import (
	"fmt"
	"strings"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
)

// summaryNextHints returns the most useful next move for the run's outcome:
// pin-back commands after a conviction, and a recovery suggestion for the
// outcomes where the bisection could not convict anything — the moments a
// user is most lost. Failure-path suggestions are static strings chosen from
// evidence the run already gathered (unresolved installs vs. flakiness), so
// they cannot rot the way generated commands could.
func summaryNextHints(res *engine.Result) []string {
	switch res.Outcome {
	case engine.OutcomeMinimalFound:
		return nextHints(res.PackageManager, res.Minimal)
	case engine.OutcomeNotReproduced:
		return []string{"the failure may be environmental — compare env vars, caches, and toolchain versions " +
			"against a clean worktree, or raise --runs if it is intermittent"}
	case engine.OutcomeFailsAtBase:
		return []string{"the failure predates these dependency updates — git bisect the code itself, or widen --base"}
	case engine.OutcomeInconclusive:
		if res.UnresolvedTrials > 0 {
			return []string{"some candidates failed to install — rerun with --verbose to inspect the installer output"}
		}
		return []string{fmt.Sprintf("increase --runs to separate flakiness from the real signal (current: %d)", res.Runs)}
	default:
		return nil
	}
}

// interactionSentence states what the 1-minimality proof established about a
// multi-change minimal set: no single member is guilty alone.
func interactionSentence(n int) string {
	return fmt.Sprintf("these %d changes break only in combination — removing any one of them makes the failure disappear", n)
}

// nextHints returns copy-paste suggestions that hold every culprit in the
// minimal set at its last good version — or remove culprits whose addition
// broke the build. One suggestion is emitted per (action, section) group so
// section flags stay exact; in the common single-culprit case that is one
// line. It returns nil when any culprit's last good version is not exactly
// known (no lockfile on the old side): partial advice would be misleading,
// and so would pinning to a range.
//
// The command syntax per manager is part of the human contract and is pinned
// by TestNextHints; extend both together when adding an ecosystem.
func nextHints(manager string, minimal []manifest.Change) []string {
	if len(minimal) == 0 {
		return nil
	}
	type group struct {
		remove  bool
		section manifest.Section
	}
	groups := map[group][]manifest.Change{}
	order := make([]group, 0, len(minimal))
	for _, c := range minimal {
		remove := c.Kind == manifest.Added
		if !remove && c.OldResolved == "" {
			return nil
		}
		g := group{remove: remove, section: c.Section}
		if _, seen := groups[g]; !seen {
			order = append(order, g)
		}
		groups[g] = append(groups[g], c)
	}
	hints := make([]string, 0, len(order))
	for _, g := range order {
		hint := buildHint(manager, g.remove, g.section, groups[g])
		if hint == "" {
			return nil
		}
		hints = append(hints, hint)
	}
	return hints
}

// buildHint renders one suggestion for culprits sharing an action and section.
func buildHint(manager string, remove bool, section manifest.Section, culprits []manifest.Change) string {
	names := make([]string, len(culprits))
	for i, c := range culprits {
		names[i] = c.Name
	}
	joinedNames := strings.Join(names, " ")

	// pinArgs renders "name<sep>version" per culprit, e.g. "lodash@4.17.21"
	// or "acme/lib:v2.9.1" or "requests==2.31.0".
	pinArgs := func(sep, versionPrefix string) string {
		args := make([]string, len(culprits))
		for i, c := range culprits {
			args[i] = c.Name + sep + versionPrefix + c.OldResolved
		}
		return strings.Join(args, " ")
	}

	switch manager {
	case "npm":
		if remove {
			return "npm uninstall " + joinedNames
		}
		return "npm install --save-exact" + npmSectionFlag(section) + " " + pinArgs("@", "")
	case "pnpm":
		if remove {
			return "pnpm remove " + joinedNames
		}
		return "pnpm add --save-exact" + npmSectionFlag(section) + " " + pinArgs("@", "")
	case "yarn":
		if remove {
			return "yarn remove " + joinedNames
		}
		return "yarn add --exact" + yarnSectionFlag(section) + " " + pinArgs("@", "")
	case "cargo":
		if remove {
			return "cargo remove" + cargoSectionFlag(section) + " " + joinedNames
		}
		return "cargo add" + cargoSectionFlag(section) + " " + pinArgs("@", "=")
	case "go":
		if remove {
			return "go get " + suffixEach(names, "@none")
		}
		return "go get " + pinArgs("@", "")
	case "uv":
		if remove {
			return "uv remove " + joinedNames
		}
		return "uv add " + pinArgs("==", "")
	case "composer":
		if remove {
			return "composer remove" + composerSectionFlag(section) + " " + joinedNames
		}
		return "composer require" + composerSectionFlag(section) + " " + pinArgs(":", "")
	case "pip":
		// pip has no command that persists to requirements.txt; suggest the
		// edit itself.
		if remove {
			return fmt.Sprintf("remove %s from requirements.txt", strings.Join(names, ", "))
		}
		return fmt.Sprintf("pin %s in requirements.txt", pinArgs("==", ""))
	default:
		return ""
	}
}

func npmSectionFlag(section manifest.Section) string {
	switch section {
	case manifest.DevDependencies:
		return " --save-dev"
	case manifest.OptionalDependencies:
		return " --save-optional"
	default:
		return ""
	}
}

func yarnSectionFlag(section manifest.Section) string {
	switch section {
	case manifest.DevDependencies:
		return " --dev"
	case manifest.OptionalDependencies:
		return " --optional"
	default:
		return ""
	}
}

func cargoSectionFlag(section manifest.Section) string {
	switch section {
	case manifest.CargoDevDependencies:
		return " --dev"
	case manifest.CargoBuildDependencies:
		return " --build"
	default:
		return ""
	}
}

func composerSectionFlag(section manifest.Section) string {
	if section == manifest.ComposerRequireDev {
		return " --dev"
	}
	return ""
}

func suffixEach(names []string, suffix string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n + suffix
	}
	return strings.Join(out, " ")
}

// registryLinks returns one registry URL per culprit, pointing at the page of
// the version that broke the build — one click from its changelog. Culprits
// whose new resolution is not a registry version (file:/link:/git targets)
// are skipped rather than linked wrongly. The URL schemes are long-stable
// registry conventions and are pinned by TestRegistryLinks.
func registryLinks(manager string, minimal []manifest.Change) []string {
	var links []string
	for _, c := range minimal {
		v := c.NewResolved
		if !versionish(v) {
			continue
		}
		var url string
		switch manager {
		case "npm", "pnpm", "yarn":
			url = fmt.Sprintf("https://www.npmjs.com/package/%s/v/%s", c.Name, v)
		case "cargo":
			url = fmt.Sprintf("https://crates.io/crates/%s/%s", c.Name, v)
		case "go":
			url = fmt.Sprintf("https://pkg.go.dev/%s@%s", c.Name, v)
		case "uv", "pip":
			url = fmt.Sprintf("https://pypi.org/project/%s/%s/", c.Name, strings.TrimPrefix(v, "v"))
		case "composer":
			url = fmt.Sprintf("https://packagist.org/packages/%s#%s", c.Name, v)
		default:
			return nil
		}
		links = append(links, url)
	}
	return links
}

// versionish reports whether s looks like a registry version: "1.2.3" or
// "v1.2.3", as opposed to a path, URL, or symbolic reference.
func versionish(s string) bool {
	s = strings.TrimPrefix(s, "v")
	return s != "" && s[0] >= '0' && s[0] <= '9' && !strings.ContainsAny(s, ":/ ")
}
