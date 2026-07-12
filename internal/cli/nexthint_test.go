package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
)

func updated(name string, section manifest.Section, oldResolved string) manifest.Change {
	return manifest.Change{Name: name, Section: section, Kind: manifest.Updated, OldResolved: oldResolved}
}

func added(name string, section manifest.Section) manifest.Change {
	return manifest.Change{Name: name, Section: section, Kind: manifest.Added}
}

// TestNextHints pins the suggested-command syntax per package manager. These
// strings are typed into terminals verbatim; treat any diff here as a
// user-facing contract change.
func TestNextHints(t *testing.T) {
	cases := []struct {
		name    string
		manager string
		minimal []manifest.Change
		want    []string
	}{
		{"npm pin", "npm",
			[]manifest.Change{updated("lodash", manifest.Dependencies, "4.17.21")},
			[]string{"npm install --save-exact lodash@4.17.21"}},
		{"npm dev pin", "npm",
			[]manifest.Change{updated("jest", manifest.DevDependencies, "29.0.0")},
			[]string{"npm install --save-exact --save-dev jest@29.0.0"}},
		{"npm optional pin", "npm",
			[]manifest.Change{updated("fsevents", manifest.OptionalDependencies, "2.3.2")},
			[]string{"npm install --save-exact --save-optional fsevents@2.3.2"}},
		{"npm added culprit removed", "npm",
			[]manifest.Change{added("left-pad", manifest.Dependencies)},
			[]string{"npm uninstall left-pad"}},
		{"npm multiple one command", "npm",
			[]manifest.Change{
				updated("a", manifest.Dependencies, "1.0.0"),
				updated("b", manifest.Dependencies, "2.0.0"),
			},
			[]string{"npm install --save-exact a@1.0.0 b@2.0.0"}},
		{"npm mixed sections split", "npm",
			[]manifest.Change{
				updated("a", manifest.Dependencies, "1.0.0"),
				updated("b", manifest.DevDependencies, "2.0.0"),
			},
			[]string{
				"npm install --save-exact a@1.0.0",
				"npm install --save-exact --save-dev b@2.0.0",
			}},
		{"pnpm pin", "pnpm",
			[]manifest.Change{updated("lodash", manifest.Dependencies, "4.17.21")},
			[]string{"pnpm add --save-exact lodash@4.17.21"}},
		{"pnpm remove", "pnpm",
			[]manifest.Change{added("left-pad", manifest.Dependencies)},
			[]string{"pnpm remove left-pad"}},
		{"yarn dev pin", "yarn",
			[]manifest.Change{updated("jest", manifest.DevDependencies, "29.0.0")},
			[]string{"yarn add --exact --dev jest@29.0.0"}},
		{"cargo pin exact requirement", "cargo",
			[]manifest.Change{updated("serde", manifest.CargoDependencies, "1.0.100")},
			[]string{"cargo add serde@=1.0.100"}},
		{"cargo dev pin", "cargo",
			[]manifest.Change{updated("insta", manifest.CargoDevDependencies, "1.34.0")},
			[]string{"cargo add --dev insta@=1.34.0"}},
		{"cargo build remove", "cargo",
			[]manifest.Change{added("cc", manifest.CargoBuildDependencies)},
			[]string{"cargo remove --build cc"}},
		{"go pin keeps v prefix", "go",
			[]manifest.Change{updated("gopkg.in/yaml.v2", manifest.GoRequire, "v2.2.8")},
			[]string{"go get gopkg.in/yaml.v2@v2.2.8"}},
		{"go added culprit dropped", "go",
			[]manifest.Change{added("example.com/mod", manifest.GoRequire)},
			[]string{"go get example.com/mod@none"}},
		{"uv pin", "uv",
			[]manifest.Change{updated("numpy", manifest.PyDependencies, "1.26.4")},
			[]string{"uv add numpy==1.26.4"}},
		{"uv remove", "uv",
			[]manifest.Change{added("numpy", manifest.PyDependencies)},
			[]string{"uv remove numpy"}},
		{"composer pin verbatim lock version", "composer",
			[]manifest.Change{updated("acme/lib", manifest.ComposerRequire, "v2.9.1")},
			[]string{"composer require acme/lib:v2.9.1"}},
		{"composer dev pin", "composer",
			[]manifest.Change{updated("phpunit/phpunit", manifest.ComposerRequireDev, "10.5.0")},
			[]string{"composer require --dev phpunit/phpunit:10.5.0"}},
		{"pip edit hint", "pip",
			[]manifest.Change{updated("requests", manifest.PyDependencies, "2.31.0")},
			[]string{"pin requests==2.31.0 in requirements.txt"}},
		{"pip remove hint", "pip",
			[]manifest.Change{added("requests", manifest.PyDependencies)},
			[]string{"remove requests from requirements.txt"}},
		{"no old resolution suppresses all advice", "npm",
			[]manifest.Change{
				updated("a", manifest.Dependencies, "1.0.0"),
				updated("b", manifest.Dependencies, ""),
			},
			nil},
		{"unknown manager", "brew", []manifest.Change{updated("a", manifest.Dependencies, "1.0.0")}, nil},
		{"empty set", "npm", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextHints(tc.manager, tc.minimal)
			if len(got) != len(tc.want) {
				t.Fatalf("nextHints = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("hint[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNextHintsCoverAllManagers forces a deliberate decision when a new
// package manager is added: either give it a suggestion or extend this list
// of managers that intentionally have none.
func TestNextHintsCoverAllManagers(t *testing.T) {
	managers := []string{"npm", "pnpm", "yarn", "cargo", "go", "uv", "composer", "pip"}
	for _, m := range managers {
		section := manifest.Dependencies
		switch m {
		case "go":
			section = manifest.GoRequire
		case "uv", "pip":
			section = manifest.PyDependencies
		case "composer":
			section = manifest.ComposerRequire
		}
		if hints := nextHints(m, []manifest.Change{updated("dep", section, "1.0.0")}); len(hints) == 0 {
			t.Errorf("manager %q has no next-hint; add one or record the exemption here", m)
		}
	}
}

func TestModernSummaryNextAndDiagnostics(t *testing.T) {
	res := makeResult(engine.OutcomeMinimalFound,
		manifest.Change{Name: "breakage", Section: manifest.PyDependencies, Kind: manifest.Updated,
			OldSpec: "==1.0.0", NewSpec: ">=1", OldResolved: "1.0.0", NewResolved: "2.0.0", LockfileOnly: true})
	res.PackageManager = "uv"
	res.Diagnostics = []string{"2 transitive dependencies resolved differently: x (1.0.0->1.2.0), y (2.0.0->2.1.0)"}
	var buf bytes.Buffer
	printModernSummary(&buf, res, "", "")
	out := buf.String()

	if !strings.Contains(out, "next") || !strings.Contains(out, "uv add breakage==1.0.0") {
		t.Errorf("modern summary missing next hint:\n%s", out)
	}
	if !strings.Contains(out, "⚠ diagnostics") {
		t.Errorf("modern summary missing diagnostics block heading:\n%s", out)
	}
	if !strings.Contains(out, "·") || !strings.Contains(out, "2 transitive dependencies") {
		t.Errorf("modern summary missing diagnostics bullet:\n%s", out)
	}
	if !strings.Contains(out, "x (1.0.0→1.2.0)") {
		t.Errorf("modern diagnostics should swap the ASCII arrow for the glyph:\n%s", out)
	}
	if strings.Contains(out, "note") {
		t.Errorf("diagnostics must not render as dim note facts anymore:\n%s", out)
	}
}

func TestSummaryNextHintsFailureOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*engine.Result)
		want    string
		classic string // substring expected in the classic summary
	}{
		{"not reproduced", func(r *engine.Result) { r.Outcome = engine.OutcomeNotReproduced },
			"environmental", "Next"},
		{"fails at base", func(r *engine.Result) { r.Outcome = engine.OutcomeFailsAtBase },
			"predates these dependency updates", "Next"},
		{"inconclusive flaky", func(r *engine.Result) { r.Outcome = engine.OutcomeInconclusive; r.Runs = 3 },
			"increase --runs to separate flakiness from the real signal (current: 3)", "Next"},
		{"inconclusive unresolved installs", func(r *engine.Result) {
			r.Outcome = engine.OutcomeInconclusive
			r.UnresolvedTrials = 2
		}, "rerun with --verbose to inspect the installer output", "Next"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := makeResult("", makeChange("lodash", "^4.17.20", "^4.17.21"))
			tc.mutate(res)
			hints := summaryNextHints(res)
			if len(hints) != 1 || !strings.Contains(hints[0], tc.want) {
				t.Fatalf("summaryNextHints = %q, want substring %q", hints, tc.want)
			}
			var buf bytes.Buffer
			printSummary(&buf, res, "", "", styleClassic)
			// Collapse the wrapping indentation before matching: long hints
			// legitimately wrap mid-sentence.
			compact := strings.Join(strings.Fields(buf.String()), " ")
			if !strings.Contains(buf.String(), tc.classic) || !strings.Contains(compact, tc.want) {
				t.Errorf("classic summary missing next hint:\n%s", buf.String())
			}
		})
	}
	// Outcomes with nothing useful to add stay silent.
	res := makeResult(engine.OutcomeNoChanges)
	if hints := summaryNextHints(res); hints != nil {
		t.Errorf("no-changes should have no hint, got %q", hints)
	}
}

func TestSummaryInteractionSentence(t *testing.T) {
	pair := []manifest.Change{
		makeChange("alpha", "1.0.0", "1.1.0"),
		makeChange("beta", "2.0.0", "2.1.0"),
	}
	res := makeResult(engine.OutcomeMinimalFound, pair...)

	var modern bytes.Buffer
	printModernSummary(&modern, res, "", "")
	if !strings.Contains(modern.String(), "break only in combination") {
		t.Errorf("modern summary missing interaction sentence for a 2-change set:\n%s", modern.String())
	}

	var classic bytes.Buffer
	printSummary(&classic, res, "", "", styleClassic)
	if !strings.Contains(classic.String(), "Interaction These 2 changes break only in combination") {
		t.Errorf("classic summary missing interaction line:\n%s", classic.String())
	}

	// A single-culprit verdict makes no combination claim.
	single := makeResult(engine.OutcomeMinimalFound, pair[0])
	var buf bytes.Buffer
	printModernSummary(&buf, single, "", "")
	if strings.Contains(buf.String(), "combination") {
		t.Errorf("single-culprit summary must not claim an interaction:\n%s", buf.String())
	}
}

func TestModernListStylingMatchesCertainty(t *testing.T) {
	ch := makeChange("lodash", "^4.17.20", "^4.17.21")

	var dry bytes.Buffer
	printModernSummary(&dry, makeResult(engine.OutcomeDryRun, ch), "", "")
	if strings.Contains(dry.String(), glyphFail) {
		t.Errorf("dry-run listing must not mark changes as culprits:\n%s", dry.String())
	}
	if !strings.Contains(dry.String(), glyphNeutral) {
		t.Errorf("dry-run listing should use the neutral bullet:\n%s", dry.String())
	}

	var inc bytes.Buffer
	printModernSummary(&inc, makeResult(engine.OutcomeInconclusive, ch), "", "")
	if !strings.Contains(inc.String(), paint(ansiYellow, glyphFail)) {
		t.Errorf("best-known set should be yellow, not certainty-red:\n%s", inc.String())
	}

	var found bytes.Buffer
	printModernSummary(&found, makeResult(engine.OutcomeMinimalFound, ch), "", "")
	if !strings.Contains(found.String(), paint(ansiRed, glyphFail)) {
		t.Errorf("certified minimal set keeps the red mark:\n%s", found.String())
	}
}

func TestModernSummaryManagerDimAndLockfileTint(t *testing.T) {
	res := makeResult(engine.OutcomeMinimalFound,
		manifest.Change{Name: "breakage", Section: manifest.PyDependencies, Kind: manifest.Updated,
			OldSpec: "==1.0.0", NewSpec: ">=1", OldResolved: "1.0.0", NewResolved: "2.0.0", LockfileOnly: true})
	res.PackageManager = "uv"
	res.PackageManagerVersion = "uv 0.11.27 (Homebrew 2026-07-06 aarch64-apple-darwin)"
	var buf bytes.Buffer
	printModernSummary(&buf, res, "", "")
	out := buf.String()
	if !strings.Contains(out, "uv 0.11.27 "+paint(ansiGray, "(Homebrew 2026-07-06 aarch64-apple-darwin)")) {
		t.Errorf("manager provenance should be dimmed:\n%q", out)
	}
	if !strings.Contains(out, paint(ansiCyanDim, "(lockfile-only)")) {
		t.Errorf("lockfile-only tag should carry the subtle tint:\n%q", out)
	}
}

func TestModernFactsWrapWithHangingIndent(t *testing.T) {
	res := makeResult(engine.OutcomeMinimalFound, makeChange("lodash", "^4.17.20", "^4.17.21"))
	res.Command = []string{"sh", "-c", strings.Repeat("averylongword ", 12)}
	var buf bytes.Buffer
	printModernSummary(&buf, res, "", "")
	lines := strings.Split(buf.String(), "\n")
	indent := strings.Repeat(" ", modernGutter+modernLabelWidth+1)
	found := false
	for _, line := range lines {
		if strings.HasPrefix(line, indent+"averylongword") {
			found = true
		}
	}
	if !found {
		t.Errorf("long command fact should wrap with a hanging indent:\n%s", buf.String())
	}
}

// TestRegistryLinks pins the registry URL schemes; they are typed into
// browsers verbatim.
func TestRegistryLinks(t *testing.T) {
	resolved := func(name, section, newResolved string) manifest.Change {
		return manifest.Change{Name: name, Section: manifest.Section(section), Kind: manifest.Updated, NewResolved: newResolved}
	}
	cases := []struct {
		manager string
		change  manifest.Change
		want    string
	}{
		{"npm", resolved("@acme/parser", "dependencies", "3.9.0"), "https://www.npmjs.com/package/@acme/parser/v/3.9.0"},
		{"pnpm", resolved("lodash", "dependencies", "4.17.21"), "https://www.npmjs.com/package/lodash/v/4.17.21"},
		{"cargo", resolved("serde", "dependencies", "1.0.200"), "https://crates.io/crates/serde/1.0.200"},
		{"go", resolved("gopkg.in/yaml.v2", "require", "v2.3.0"), "https://pkg.go.dev/gopkg.in/yaml.v2@v2.3.0"},
		{"uv", resolved("numpy", "dependencies", "2.0.0"), "https://pypi.org/project/numpy/2.0.0/"},
		{"composer", resolved("acme/lib", "require", "v2.10.0"), "https://packagist.org/packages/acme/lib#v2.10.0"},
	}
	for _, tc := range cases {
		got := registryLinks(tc.manager, []manifest.Change{tc.change})
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("registryLinks(%s) = %q, want [%q]", tc.manager, got, tc.want)
		}
	}
	// Non-registry resolutions are skipped, never linked wrongly.
	if got := registryLinks("npm", []manifest.Change{resolved("local", "dependencies", "file:pkgs/local-1.0.0")}); got != nil {
		t.Errorf("file: resolution should produce no link, got %q", got)
	}
	if got := registryLinks("brew", []manifest.Change{resolved("x", "dependencies", "1.0.0")}); got != nil {
		t.Errorf("unknown manager should produce no links, got %q", got)
	}
}

func TestSummaryFailureExcerpt(t *testing.T) {
	res := makeResult(engine.OutcomeMinimalFound, makeChange("lodash", "^4.17.20", "^4.17.21"))
	res.FailureExcerpt = "\x1b[31m✗ suite\x1b[0m\n\nAssertionError: expected 5, got NaN"

	var modern bytes.Buffer
	printModernSummary(&modern, res, "", "")
	out := modern.String()
	if !strings.Contains(out, "failure") || !strings.Contains(out, "AssertionError: expected 5, got NaN") {
		t.Errorf("modern summary missing failure excerpt:\n%s", out)
	}
	if strings.Contains(out, "\x1b[31m") {
		t.Errorf("excerpt must not leak the test runner's own ANSI codes:\n%s", out)
	}

	var classic bytes.Buffer
	printSummary(&classic, res, "", "", styleClassic)
	if !strings.Contains(classic.String(), "Failure ✗ suite") ||
		!strings.Contains(classic.String(), "AssertionError: expected 5, got NaN") {
		t.Errorf("classic summary missing failure lines:\n%s", classic.String())
	}
}

func TestExcerptLinesKeepsTail(t *testing.T) {
	got := excerptLines("one\ntwo\n\nthree\nfour\n", 3, 0)
	want := []string{"two", "three", "four"}
	if len(got) != len(want) {
		t.Fatalf("excerptLines = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExcerptLinesFiltersBoilerplateAndTruncates(t *testing.T) {
	excerpt := strings.Join([]string{
		"npm ERR! code ELIFECYCLE",
		"AssertionError: expected 5, got NaN plus a very long explanation trailing on",
		"npm ERR! A complete log of this run can be found in:",
		"npm ERR!     /Users/x/.npm/_logs/2026-07-12T00_00_00_000Z-debug-0.log",
		"Node.js v22.1.0",
	}, "\n")
	got := excerptLines(excerpt, 3, 40)
	if len(got) != 2 {
		t.Fatalf("excerptLines = %q, want the two real lines only", got)
	}
	if got[0] != "npm ERR! code ELIFECYCLE" {
		t.Errorf("line 0 = %q", got[0])
	}
	if len([]rune(got[1])) != 40 || !strings.HasSuffix(got[1], "...") {
		t.Errorf("long line should truncate to width with ellipsis: %q", got[1])
	}
}

func TestModernLongNextBreaksOutFullWidth(t *testing.T) {
	res := makeResult(engine.OutcomeMinimalFound,
		manifest.Change{Name: "extremely-long-package-name-one", Section: manifest.Dependencies,
			Kind: manifest.Updated, OldResolved: "1.0.0", NewResolved: "2.0.0"},
		manifest.Change{Name: "extremely-long-package-name-two", Section: manifest.Dependencies,
			Kind: manifest.Updated, OldResolved: "3.0.0", NewResolved: "4.0.0"},
	)
	res.PackageManager = "npm"
	var buf bytes.Buffer
	printModernSummary(&buf, res, "", "")
	cmd := "npm install --save-exact extremely-long-package-name-one@1.0.0 extremely-long-package-name-two@3.0.0"
	out := buf.String()
	// The command must survive as one physical line — a selection of that
	// line is the exact command.
	if !strings.Contains(out, cmd+ansiReset+"\n") {
		t.Errorf("long next command should sit unbroken on its own line:\n%s", out)
	}
}

func TestClassicNextCommandNeverHardWraps(t *testing.T) {
	res := makeResult(engine.OutcomeMinimalFound,
		manifest.Change{Name: "extremely-long-package-name-one", Section: manifest.Dependencies,
			Kind: manifest.Updated, OldResolved: "1.0.0", NewResolved: "2.0.0"},
		manifest.Change{Name: "extremely-long-package-name-two", Section: manifest.Dependencies,
			Kind: manifest.Updated, OldResolved: "3.0.0", NewResolved: "4.0.0"},
	)
	res.PackageManager = "npm"
	var buf bytes.Buffer
	printSummary(&buf, res, "", "", styleClassic)
	cmd := "npm install --save-exact extremely-long-package-name-one@1.0.0 extremely-long-package-name-two@3.0.0"
	if !strings.Contains(buf.String(), cmd) {
		t.Errorf("classic Next must keep the command contiguous on one line:\n%s", buf.String())
	}
}

func TestModernRuleSpansWidestFact(t *testing.T) {
	res := makeResult(engine.OutcomeMinimalFound, makeChange("lodash", "^4.17.20", "^4.17.21"))
	res.Command = []string{"npm", "run", "test:integration", "--", "--grep", "parser"}
	var buf bytes.Buffer
	printModernSummary(&buf, res, "", "")
	out := stripANSI(buf.String())
	rule, longest := 0, 0
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "─") {
			rule = len([]rune(trimmed))
		}
		if strings.HasPrefix(trimmed, "command") {
			longest = len([]rune(strings.TrimRight(line, " "))) - modernGutter
		}
	}
	if rule == 0 || longest == 0 || rule < longest {
		t.Errorf("rule width %d should span the widest fact %d:\n%s", rule, longest, out)
	}
}

func TestClassicSummaryDiagnosticPairNeverSplits(t *testing.T) {
	// A long diagnostic wraps, but the unspaced version-pair tokens must land
	// intact on whichever line they end up on.
	res := &engine.Result{
		Outcome:        engine.OutcomeNoChanges,
		PackageManager: "uv",
		Diagnostics: []string{"9 dependencies changed only in the lockfile (version spec unchanged): " +
			"alpha (1.0.0->2.0.0), beta (1.0.0->2.0.0), gamma (1.0.0->2.0.0), delta (1.0.0->2.0.0), " +
			"epsilon (1.0.0->2.0.0), zeta (1.0.0->2.0.0), eta (1.0.0->2.0.0), theta (1.0.0->2.0.0), " +
			"iota (1.0.0->2.0.0). DepBisect bisects manifest-level changes."},
	}
	var buf bytes.Buffer
	printSummary(&buf, res, "", "", styleClassic)
	for i, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "(1.0.0") != strings.Contains(line, "->2.0.0)") {
			t.Errorf("line %d splits a version pair: %q", i, line)
		}
	}
}
