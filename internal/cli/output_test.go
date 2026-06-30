package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeChange(name, old, newV string) manifest.Change {
	return manifest.Change{
		Name:    name,
		OldSpec: old,
		NewSpec: newV,
		Kind:    manifest.Updated,
		Section: manifest.Dependencies,
	}
}

func makeResult(outcome string, changes ...manifest.Change) *engine.Result {
	r := &engine.Result{
		Outcome: outcome,
		Changes: changes,
		Minimal: changes,
		Command: []string{"npm", "test"},
		Confidence: engine.Confidence{
			Failures: 3,
			Runs:     3,
		},
	}
	r.StartedAt = time.Now().Add(-5 * time.Second)
	r.FinishedAt = time.Now()
	return r
}

// ---------------------------------------------------------------------------
// outcomeColor
// ---------------------------------------------------------------------------

func TestOutcomeColor(t *testing.T) {
	cases := []struct {
		outcome string
		want    string
	}{
		{"pass", ansiGreen},
		{"fail", ansiRed},
		{"unresolved", ansiYellow},
		{"other", ansiCyan},
		{"", ansiCyan},
	}
	for _, tc := range cases {
		got := outcomeColor(tc.outcome)
		if got != tc.want {
			t.Errorf("outcomeColor(%q) = %q, want %q", tc.outcome, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// trialRoleLabel
// ---------------------------------------------------------------------------

func TestTrialRoleLabel(t *testing.T) {
	cases := []struct {
		role string
		want string
	}{
		{"baseline-old", "baseline without updates"},
		{"baseline-new", "baseline with all updates"},
		{"minimality-check", "minimality check"},
		{"candidate", "candidate"},
		{"", ""},
	}
	for _, tc := range cases {
		got := trialRoleLabel(tc.role)
		if got != tc.want {
			t.Errorf("trialRoleLabel(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// splitPackageName
// ---------------------------------------------------------------------------

func TestSplitPackageName(t *testing.T) {
	cases := []struct {
		name       string
		wantPrefix string
		wantLeaf   string
	}{
		// Go-style module path (contains dot in first segment)
		{"github.com/org/pkg", "github.com/org/", "pkg"},
		{"golang.org/x/text", "golang.org/x/", "text"},
		// npm scoped (@ prefix, no dot)
		{"@scope/pkg", "", "@scope/pkg"},
		// plain name
		{"lodash", "", "lodash"},
		// no slash at all
		{"react", "", "react"},
	}
	for _, tc := range cases {
		prefix, leaf := splitPackageName(tc.name)
		if prefix != tc.wantPrefix || leaf != tc.wantLeaf {
			t.Errorf("splitPackageName(%q) = (%q, %q), want (%q, %q)",
				tc.name, prefix, leaf, tc.wantPrefix, tc.wantLeaf)
		}
	}
}

// ---------------------------------------------------------------------------
// changeVersionStr
// ---------------------------------------------------------------------------

func TestChangeVersionStr(t *testing.T) {
	t.Run("updated uses OldSpec->NewSpec by default", func(t *testing.T) {
		c := manifest.Change{Kind: manifest.Updated, OldSpec: "^1.0.0", NewSpec: "^2.0.0", Section: manifest.Dependencies}
		got := changeVersionStr(c)
		if got != "^1.0.0 -> ^2.0.0" {
			t.Errorf("got %q, want %q", got, "^1.0.0 -> ^2.0.0")
		}
	})

	t.Run("updated uses resolved when both present", func(t *testing.T) {
		c := manifest.Change{Kind: manifest.Updated, OldSpec: "^1.0.0", NewSpec: "^2.0.0",
			OldResolved: "1.0.5", NewResolved: "2.0.1", Section: manifest.Dependencies}
		got := changeVersionStr(c)
		if got != "1.0.5 -> 2.0.1" {
			t.Errorf("got %q, want %q", got, "1.0.5 -> 2.0.1")
		}
	})

	t.Run("added shows new resolved when present", func(t *testing.T) {
		c := manifest.Change{Kind: manifest.Added, NewSpec: "^3.0.0", NewResolved: "3.0.2", Section: manifest.Dependencies}
		got := changeVersionStr(c)
		if !strings.Contains(got, "3.0.2") || !strings.Contains(got, "added") {
			t.Errorf("got %q, want contains '3.0.2' and 'added'", got)
		}
	})

	t.Run("added shows NewSpec when no resolved", func(t *testing.T) {
		c := manifest.Change{Kind: manifest.Added, NewSpec: "^3.0.0", Section: manifest.Dependencies}
		got := changeVersionStr(c)
		if !strings.Contains(got, "^3.0.0") || !strings.Contains(got, "added") {
			t.Errorf("got %q, want contains '^3.0.0' and 'added'", got)
		}
	})

	t.Run("removed shows old resolved when present", func(t *testing.T) {
		c := manifest.Change{Kind: manifest.Removed, OldSpec: "^1.0.0", OldResolved: "1.0.5", Section: manifest.Dependencies}
		got := changeVersionStr(c)
		if !strings.Contains(got, "1.0.5") || !strings.Contains(got, "removed") {
			t.Errorf("got %q, want contains '1.0.5' and 'removed'", got)
		}
	})

	t.Run("removed shows OldSpec when no resolved", func(t *testing.T) {
		c := manifest.Change{Kind: manifest.Removed, OldSpec: "^1.0.0", Section: manifest.Dependencies}
		got := changeVersionStr(c)
		if !strings.Contains(got, "^1.0.0") || !strings.Contains(got, "removed") {
			t.Errorf("got %q, want contains '^1.0.0' and 'removed'", got)
		}
	})

	t.Run("non-dependencies section appends section name", func(t *testing.T) {
		c := manifest.Change{Kind: manifest.Updated, OldSpec: "^1.0.0", NewSpec: "^2.0.0", Section: manifest.DevDependencies}
		got := changeVersionStr(c)
		if !strings.Contains(got, "devDependencies") {
			t.Errorf("got %q, want contains section name", got)
		}
	})
}

// ---------------------------------------------------------------------------
// summaryGlyph
// ---------------------------------------------------------------------------

func TestSummaryGlyph(t *testing.T) {
	cases := []struct {
		outcome   string
		wantGlyph string
		wantColor string
	}{
		{engine.OutcomeMinimalFound, glyphOK, ansiGreen},
		{engine.OutcomeNotReproduced, glyphOK, ansiGreen},
		{engine.OutcomeDryRun, glyphArrow, ansiCyan},
		{engine.OutcomeInconclusive, glyphActive, ansiYellow},
		{engine.OutcomeFailsAtBase, glyphActive, ansiYellow},
		{"unknown", glyphActive, ansiYellow},
	}
	for _, tc := range cases {
		g, c := summaryGlyph(tc.outcome)
		if g != tc.wantGlyph || c != tc.wantColor {
			t.Errorf("summaryGlyph(%q) = (%q, %q), want (%q, %q)",
				tc.outcome, g, c, tc.wantGlyph, tc.wantColor)
		}
	}
}

// ---------------------------------------------------------------------------
// terminalLineWidth — non-terminal writer always returns outputWidth
// ---------------------------------------------------------------------------

func TestTerminalLineWidthNonTerminal(t *testing.T) {
	var buf bytes.Buffer
	got := terminalLineWidth(&buf)
	if got != outputWidth {
		t.Errorf("terminalLineWidth(buf) = %d, want %d (outputWidth)", got, outputWidth)
	}
}

// ---------------------------------------------------------------------------
// writeLiveStatusWidth — narrow-width path (≤ statusWidth+1)
// ---------------------------------------------------------------------------

func TestWriteLiveStatusWidthNarrow(t *testing.T) {
	var buf bytes.Buffer
	writeLiveStatusWidth(&buf, "Trial 1", "some long message here", false, ansiCyan, statusWidth)
	out := buf.String()
	// narrow path truncates label to lineWidth, emits no space+message
	if strings.Contains(out, "some long") {
		t.Errorf("narrow path should not include message, got %q", out)
	}
}

func TestWriteLiveStatusWidthNarrowColor(t *testing.T) {
	var buf bytes.Buffer
	writeLiveStatusWidth(&buf, "Trial 1", "some message", true, ansiCyan, statusWidth)
	out := buf.String()
	if !strings.Contains(out, ansiCyan) {
		t.Errorf("narrow+color path should wrap label in color code, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// managerLabel
// ---------------------------------------------------------------------------

func TestManagerLabel(t *testing.T) {
	t.Run("prefers version string", func(t *testing.T) {
		r := &engine.Result{PackageManager: "npm", PackageManagerVersion: "npm/9.8.1"}
		got := managerLabel(r)
		if got != "npm/9.8.1" {
			t.Errorf("got %q, want %q", got, "npm/9.8.1")
		}
	})
	t.Run("falls back to manager name", func(t *testing.T) {
		r := &engine.Result{PackageManager: "yarn"}
		got := managerLabel(r)
		if got != "yarn" {
			t.Errorf("got %q, want %q", got, "yarn")
		}
	})
	t.Run("empty when both blank", func(t *testing.T) {
		r := &engine.Result{}
		got := managerLabel(r)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// ---------------------------------------------------------------------------
// printSummary — exercising all outcome branches
// ---------------------------------------------------------------------------

func TestPrintSummaryAllOutcomes(t *testing.T) {
	ch := makeChange("lodash", "^4.17.20", "^4.17.21")
	cases := []struct {
		outcome      string
		wantSection  string // non-empty: a change section must appear
		wantHeadline string // exact headline substring always required
	}{
		{
			outcome:      engine.OutcomeMinimalFound,
			wantSection:  "Breaking dependencies",
			wantHeadline: "Minimal breaking dependency set found",
		},
		{
			outcome:      engine.OutcomeInconclusive,
			wantSection:  "Best-known failing set",
			wantHeadline: "Inconclusive",
		},
		{
			outcome:      engine.OutcomeDryRun,
			wantSection:  "Dependency changes",
			wantHeadline: "Dry run complete",
		},
		{
			outcome:      engine.OutcomeNotReproduced,
			wantHeadline: "No breaking dependency update reproduced the failure",
		},
		{
			outcome:      engine.OutcomeFailsAtBase,
			wantHeadline: "Failure exists without dependency updates",
		},
		{
			outcome:      engine.OutcomeNoChanges,
			wantHeadline: "No dependency changes to bisect",
		},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			var buf bytes.Buffer
			res := makeResult(tc.outcome, ch)
			printSummary(&buf, res, "", "", styleClassic)
			out := buf.String()
			if !strings.Contains(out, tc.wantHeadline) {
				t.Errorf("outcome %q: want headline %q in output, got:\n%s", tc.outcome, tc.wantHeadline, out)
			}
			if tc.wantSection != "" && !strings.Contains(out, tc.wantSection) {
				t.Errorf("outcome %q: want change section %q in output, got:\n%s", tc.outcome, tc.wantSection, out)
			}
		})
	}
}

func TestPrintSummaryWithReports(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound, makeChange("a", "1.0.0", "2.0.0"))
	printSummary(&buf, res, "report.md", "report.json", styleClassic)
	out := buf.String()
	if !strings.Contains(out, "report.md") {
		t.Errorf("want report.md in output, got:\n%s", out)
	}
	if !strings.Contains(out, "report.json") {
		t.Errorf("want report.json in output, got:\n%s", out)
	}
}

func TestPrintSummaryMdOnly(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound)
	printSummary(&buf, res, "out.md", "", styleClassic)
	out := buf.String()
	if !strings.Contains(out, "out.md") {
		t.Errorf("want out.md in output, got:\n%s", out)
	}
	if strings.Contains(out, "JSON") {
		t.Errorf("unexpected JSON label in output when jsonPath is empty")
	}
}

func TestPrintSummaryJSONOnly(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound)
	printSummary(&buf, res, "", "out.json", styleClassic)
	out := buf.String()
	if !strings.Contains(out, "out.json") {
		t.Errorf("want out.json in output, got:\n%s", out)
	}
}

func TestPrintSummaryKeptWorktree(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound)
	res.KeptWorktree = "/tmp/wt-abc"
	printSummary(&buf, res, "", "", styleClassic)
	out := buf.String()
	if !strings.Contains(out, "/tmp/wt-abc") {
		t.Errorf("want worktree path in output, got:\n%s", out)
	}
}

func TestPrintSummaryDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeNotReproduced)
	res.Diagnostics = []string{"something unusual happened"}
	printSummary(&buf, res, "", "", styleClassic)
	out := buf.String()
	if !strings.Contains(out, "something unusual happened") {
		t.Errorf("want diagnostic in output, got:\n%s", out)
	}
}

func TestPrintSummaryOutcomeDetail(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeInconclusive)
	res.OutcomeDetail = "baseline-old trial did not pass"
	printSummary(&buf, res, "", "", styleClassic)
	out := buf.String()
	if !strings.Contains(out, "Baseline-old trial did not pass") {
		t.Errorf("want capitalized detail in output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// printSummary — modern style falls back on non-color writer
// ---------------------------------------------------------------------------

func TestPrintSummaryModernFallsBackWithoutColor(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound, makeChange("a", "1.0.0", "2.0.0"))
	// bytes.Buffer is not a terminal, so terminalMode returns color=false
	// → modern style falls back to classic
	printSummary(&buf, res, "", "", styleModern)
	out := buf.String()
	if !strings.Contains(out, "Result") {
		t.Errorf("expected classic fallback with Result label, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Step / Detail — progress methods exercised without a real terminal
// ---------------------------------------------------------------------------

func TestProgressStepClassic(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleClassic)
	p.Step("Prepare", "cloning worktrees")
	out := buf.String()
	if !strings.Contains(out, "Prepare") || !strings.Contains(out, "cloning worktrees") {
		t.Errorf("Step output missing label or message, got: %q", out)
	}
}

func TestProgressDetailSuppressedWhenNotVerbose(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleClassic)
	p.Detail("this should not appear")
	if buf.Len() != 0 {
		t.Errorf("Detail wrote output in non-verbose mode: %q", buf.String())
	}
}

func TestProgressDetailAppearsWhenVerbose(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, true, styleClassic)
	p.Detail("extra debug %s", "info")
	out := buf.String()
	if !strings.Contains(out, "extra debug info") {
		t.Errorf("Detail in verbose mode missing message, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// outcomeHeadline — default branch
// ---------------------------------------------------------------------------

func TestOutcomeHeadlineDefault(t *testing.T) {
	got := outcomeHeadline("custom-outcome")
	if got != "custom-outcome" {
		t.Errorf("got %q, want %q", got, "custom-outcome")
	}
}

// ---------------------------------------------------------------------------
// writeChangeSection — color=true path
// ---------------------------------------------------------------------------

func TestWriteChangeSectionColor(t *testing.T) {
	var buf bytes.Buffer
	changes := []manifest.Change{makeChange("lodash", "^4.0.0", "^4.17.21")}
	writeChangeSection(&buf, "Breaking changes", changes, true)
	out := buf.String()
	if !strings.Contains(out, ansiBold) {
		t.Errorf("color=true: want bold in output, got: %q", out)
	}
	if !strings.Contains(out, "Breaking changes") {
		t.Errorf("color=true: missing title, got: %q", out)
	}
}

func TestWriteChangeSectionEmpty(t *testing.T) {
	var buf bytes.Buffer
	writeChangeSection(&buf, "Empty", nil, false)
	if buf.Len() != 0 {
		t.Errorf("empty changes should write nothing, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// writeStatus — word-wrap long message
// ---------------------------------------------------------------------------

func TestWriteStatusWrapsLongMessage(t *testing.T) {
	var buf bytes.Buffer
	// outputWidth is 100; padding is statusWidth+1 = 13; message width = 87
	// Construct a message that will exceed 87 chars and trigger wrapping
	long := strings.Repeat("word ", 25) // 125 chars
	writeStatus(&buf, "Label", long, false, ansiCyan, true)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Errorf("expected wrapped output (multiple lines), got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// wrapWords — zero-length / empty-string edge cases
// ---------------------------------------------------------------------------

func TestWrapWordsEmpty(t *testing.T) {
	got := wrapWords("", 80)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("wrapWords(%q, 80) = %v, want [\"\"]", "", got)
	}
}

func TestWrapWordsWidthZero(t *testing.T) {
	got := wrapWords("hello world", 0)
	if len(got) != 1 || got[0] != "hello world" {
		t.Errorf("wrapWords with width=0 should return input unchanged, got %v", got)
	}
}

func TestWrapWordsSingleWordExceedsWidth(t *testing.T) {
	got := wrapWords("averylongword", 5)
	if len(got) != 1 || got[0] != "averylongword" {
		t.Errorf("single word exceeding width should not be broken, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// finalizeBaseline — unexpected phase branch
// ---------------------------------------------------------------------------

func TestFinalizeBaselineUnexpected(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleClassic)
	p.modernStarted = true // skip the blank-line preamble

	// baseline-old expects "pass"; giving it "fail" is unexpected
	p.finalizeBaseline("baseline-old", 3, "fail")
	out := buf.String()
	if !strings.Contains(out, "unexpected") {
		t.Errorf("finalizeBaseline with unexpected outcome should include 'unexpected', got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// modernStep — Resume branch (called directly, bypassing modernActive check)
// ---------------------------------------------------------------------------

func TestModernStepResume(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleModern)
	p.modernStarted = true // skip blank-line preamble so output is deterministic
	// Call modernStep directly (it's unexported but we are in package cli)
	p.modernStep("Resume", "resuming from checkpoint at trial 5")
	out := buf.String()
	if !strings.Contains(out, "resume") {
		t.Errorf("modernStep Resume should emit 'resume' label, got: %q", out)
	}
}

func TestModernStepComplete(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleModern)
	p.modernStarted = true
	p.modernStep("Complete", "minimal set found")
	out := buf.String()
	if !strings.Contains(out, "ddmin") {
		t.Errorf("modernStep Complete should emit 'ddmin' label, got: %q", out)
	}
}

func TestModernStepCompleteNotProven(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleModern)
	p.modernStarted = true
	p.modernStep("Complete", "not proven minimal")
	out := buf.String()
	if !strings.Contains(out, ansiYellow) {
		t.Errorf("modernStep 'not proven' should use yellow color, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// printModernSummary — called directly to bypass the color-terminal guard
// ---------------------------------------------------------------------------

func TestPrintModernSummaryOutcomes(t *testing.T) {
	ch := makeChange("lodash", "^4.17.20", "^4.17.21")
	cases := []struct {
		outcome string
		want    string
	}{
		{engine.OutcomeMinimalFound, "minimal set"},
		{engine.OutcomeInconclusive, "best-known failing set"},
		{engine.OutcomeDryRun, "dependency changes"},
		{engine.OutcomeNotReproduced, "No breaking"},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			var buf bytes.Buffer
			res := makeResult(tc.outcome, ch)
			printModernSummary(&buf, res, "out.md", "out.json")
			out := buf.String()
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Errorf("outcome %q: want %q in output, got:\n%s", tc.outcome, tc.want, out)
			}
			// All outcomes should have the fact section
			if !strings.Contains(out, "outcome") {
				t.Errorf("outcome %q: missing fact 'outcome' in modern summary", tc.outcome)
			}
		})
	}
}

func TestPrintModernSummaryInconclusiveNoMinimal(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeInconclusive) // no changes → Minimal is empty
	res.Minimal = nil
	printModernSummary(&buf, res, "", "")
	out := buf.String()
	// No change section should appear
	if strings.Contains(out, "best-known") {
		t.Errorf("inconclusive with empty Minimal should not show change section, got:\n%s", out)
	}
}

func TestPrintModernSummaryWithOutcomeDetail(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeInconclusive)
	res.OutcomeDetail = "baseline verification failed"
	printModernSummary(&buf, res, "", "")
	out := buf.String()
	if !strings.Contains(out, "Baseline verification failed") {
		t.Errorf("want capitalized OutcomeDetail in modern summary, got:\n%s", out)
	}
}

func TestPrintModernSummaryMinimalFoundDetail(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound, makeChange("a", "1.0.0", "2.0.0"))
	res.OutcomeDetail = "should not appear"
	printModernSummary(&buf, res, "", "")
	out := buf.String()
	// OutcomeDetail is suppressed for MinimalFound
	if strings.Contains(out, "should not appear") {
		t.Errorf("OutcomeDetail should be hidden for OutcomeMinimalFound, got:\n%s", out)
	}
}

func TestPrintModernSummaryWorktreeAndReports(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound)
	res.KeptWorktree = "/kept/wt"
	printModernSummary(&buf, res, "report.md", "data.json")
	out := buf.String()
	for _, want := range []string{"/kept/wt", "report.md", "data.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in modern summary, got:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// writeModernChanges — empty case and Go-module path splitting
// ---------------------------------------------------------------------------

func TestWriteModernChangesEmpty(t *testing.T) {
	var buf bytes.Buffer
	writeModernChanges(&buf, "changes", nil)
	if buf.Len() != 0 {
		t.Errorf("empty changes should write nothing, got: %q", buf.String())
	}
}

func TestWrapWordsAllWhitespaceExceedsWidth(t *testing.T) {
	// len > width but no words → len(words)==0 branch
	got := wrapWords(strings.Repeat(" ", 101), 80)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("all-whitespace exceeding width should return [\"\"], got %v", got)
	}
}

// ---------------------------------------------------------------------------
// writeStatus — !newline path via writeLiveStatusWidth with normal width
// ---------------------------------------------------------------------------

func TestWriteStatusNoNewline(t *testing.T) {
	var buf bytes.Buffer
	// writeLiveStatusWidth with lineWidth > statusWidth+1 calls writeStatus(..., false)
	writeLiveStatusWidth(&buf, "Trial 1", "running", false, ansiCyan, 80)
	out := buf.String()
	// Should NOT end with a newline (the !newline branch omits it)
	if strings.HasSuffix(out, "\n") {
		t.Errorf("!newline path should not end with \\n, got: %q", out)
	}
	if !strings.Contains(out, "Trial 1") {
		t.Errorf("missing label in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// printSummary — OutcomeInconclusive with no minimal changes (no section)
// ---------------------------------------------------------------------------

func TestPrintSummaryInconclusiveNoMinimal(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeInconclusive)
	res.Minimal = nil
	printSummary(&buf, res, "", "", styleClassic)
	out := buf.String()
	if strings.Contains(out, "Best-known") {
		t.Errorf("no minimal: should not print change section, got:\n%s", out)
	}
	if !strings.Contains(out, "Result") {
		t.Errorf("missing Result label, got:\n%s", out)
	}
}

func TestWriteModernChangesGoModule(t *testing.T) {
	var buf bytes.Buffer
	changes := []manifest.Change{
		{Name: "github.com/org/pkg", OldSpec: "v1.0.0", NewSpec: "v2.0.0", Kind: manifest.Updated, Section: manifest.Dependencies},
	}
	writeModernChanges(&buf, "minimal set", changes)
	out := buf.String()
	if !strings.Contains(out, "pkg") {
		t.Errorf("want leaf name 'pkg' in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// writeStatus — color=true path
// ---------------------------------------------------------------------------

func TestWriteStatusColor(t *testing.T) {
	var buf bytes.Buffer
	writeStatus(&buf, "Label", "hello world", true, ansiGreen, true)
	out := buf.String()
	if !strings.Contains(out, ansiGreen) {
		t.Errorf("color=true: want green ANSI code in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// printSummary — manager label and trials fields
// ---------------------------------------------------------------------------

func TestPrintSummaryManagerAndTrials(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound, makeChange("a", "1.0.0", "2.0.0"))
	res.PackageManager = "pnpm"
	res.Trials = []engine.Trial{{}} // non-empty trials slice
	printSummary(&buf, res, "", "", styleClassic)
	out := buf.String()
	if !strings.Contains(out, "pnpm") {
		t.Errorf("want manager 'pnpm' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Trials") {
		t.Errorf("want 'Trials' fact in output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// printModernSummary — diagnostics, manager, and trials facts
// ---------------------------------------------------------------------------

func TestPrintModernSummaryFactsManagerTrialsDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	res := makeResult(engine.OutcomeMinimalFound, makeChange("a", "1.0.0", "2.0.0"))
	res.PackageManager = "yarn"
	res.Trials = []engine.Trial{{}} // non-empty
	res.Diagnostics = []string{"lockfile drift detected"}
	printModernSummary(&buf, res, "", "")
	out := buf.String()
	for _, want := range []string{"yarn", "1", "lockfile drift"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in modern summary, got:\n%s", want, out)
		}
	}
}
