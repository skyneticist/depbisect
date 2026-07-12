package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/skyneticist/depbisect/internal/checkpoint"
	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/gitx"
	"github.com/skyneticist/depbisect/internal/manifest"
	"github.com/skyneticist/depbisect/internal/report"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main(args, &out, &errb, "test-version")
	return code, out.String(), errb.String()
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // stderr substring
	}{
		{"no args", nil, "Usage"},
		{"unknown subcommand", []string{"frobnicate"}, "unknown command"},
		{"run without base", []string{"run", "--", "npm", "test"}, "--base"},
		{"run without separator", []string{"run", "--base", "main", "npm", "test"}, "--"},
		{"run without command", []string{"run", "--base", "main", "--"}, "verification command"},
		{"stray arg before separator", []string{"run", "--base", "main", "stray", "--", "x"}, "unexpected argument"},
		{"bad runs", []string{"run", "--base", "main", "--runs", "0", "--", "x"}, "--runs"},
		{"bad jobs", []string{"run", "--base", "main", "--jobs", "0", "--", "x"}, "--jobs"},
		{"negative run timeout", []string{"run", "--base", "main", "--run-timeout", "-1s", "--", "x"}, "--run-timeout"},
		{"negative install timeout", []string{"run", "--base", "main", "--install-timeout", "-1s", "--", "x"}, "--install-timeout"},
		{"negative overall timeout", []string{"run", "--base", "main", "--overall-timeout", "-1s", "--", "x"}, "--overall-timeout"},
		{"quiet and verbose", []string{"run", "--base", "main", "--quiet", "--verbose", "--", "x"}, "--quiet"},
		{"bad style", []string{"run", "--base", "main", "--style", "fancy", "--", "x"}, "--style"},
		{"unknown flag", []string{"run", "--base", "main", "--bogus", "--", "x"}, "bogus"},
		{"completion unknown shell", []string{"completion", "fish"}, "fish"},
		{"completion no args", []string{"completion"}, "usage"},
		{"resume without checkpoint", []string{"run", "--base", "main", "--resume", "--checkpoint", "", "--", "x"}, "--resume"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, tc.args...)
			if code != ExitError {
				t.Errorf("exit = %d, want %d", code, ExitError)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want substring %q", stderr, tc.want)
			}
		})
	}
}

func TestParseRunOptions(t *testing.T) {
	opts, err := parseRunArgs([]string{
		"--base", "origin/main", "--to", "feature", "--runs", "3",
		"--pm", "pnpm", "--run-timeout", "90s", "--install-timeout", "2m",
		"--overall-timeout", "30m", "--keep-worktrees",
		"--dry-run", "--verbose", "--repo", "/some/repo",
		"--checkpoint", "state.jsonl", "--resume",
		"--report-md", "out.md", "--report-json", "out.json",
		"--", "pnpm", "test", "--filter", "has space",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.base != "origin/main" || opts.to != "feature" || opts.runs != 3 ||
		opts.pm != "pnpm" || !opts.keepWorktrees || !opts.dryRun || !opts.verbose ||
		opts.repo != "/some/repo" || opts.checkpoint != "state.jsonl" || !opts.resume ||
		opts.reportMD != "out.md" || opts.reportJSON != "out.json" {
		t.Errorf("opts = %+v", opts)
	}
	if opts.runTimeout.String() != "1m30s" {
		t.Errorf("runTimeout = %v", opts.runTimeout)
	}
	if opts.installTimeout != 2*time.Minute {
		t.Errorf("installTimeout = %v", opts.installTimeout)
	}
	if opts.overallTimeout != 30*time.Minute {
		t.Errorf("overallTimeout = %v", opts.overallTimeout)
	}
	want := []string{"pnpm", "test", "--filter", "has space"}
	if !reflect.DeepEqual(opts.command, want) {
		t.Errorf("command = %q, want %q", opts.command, want)
	}
}

func TestParseRunDefaults(t *testing.T) {
	opts, err := parseRunArgs([]string{"--base", "main", "--", "npm", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.to != "HEAD" || opts.runs != 1 || opts.repo != "." ||
		opts.runTimeout != 0 || opts.installTimeout != 0 || opts.overallTimeout != 0 ||
		opts.checkpoint != ".depbisect-checkpoint.jsonl" || opts.resume ||
		opts.quiet ||
		opts.reportMD != "depbisect-report.md" || opts.reportJSON != "depbisect-report.json" {
		t.Errorf("defaults = %+v", opts)
	}
}

func TestParseRunQuiet(t *testing.T) {
	opts, err := parseRunArgs([]string{"--base", "main", "--quiet", "--", "npm", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.quiet {
		t.Fatal("--quiet was not parsed")
	}
}

func TestFlagsAfterSeparatorBelongToCommand(t *testing.T) {
	opts, err := parseRunArgs([]string{"--base", "main", "--", "npm", "test", "--base", "trap"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"npm", "test", "--base", "trap"}
	if !reflect.DeepEqual(opts.command, want) {
		t.Errorf("command = %q, want %q", opts.command, want)
	}
	if opts.base != "main" {
		t.Errorf("base = %q", opts.base)
	}
}

func TestVersionCommand(t *testing.T) {
	code, stdout, _ := runCLI(t, "version")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "test-version") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestCompletionCommands(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		code, stdout, _ := runCLI(t, "completion", shell)
		if code != ExitOK {
			t.Fatalf("%s: exit = %d", shell, code)
		}
		if !strings.Contains(stdout, "depbisect") {
			t.Errorf("%s completion output = %q", shell, stdout)
		}
		if !strings.Contains(stdout, "--quiet") {
			t.Errorf("%s completion is missing --quiet:\n%s", shell, stdout)
		}
		if !strings.Contains(stdout, "--style") {
			t.Errorf("%s completion is missing --style:\n%s", shell, stdout)
		}
		if shell == "zsh" && !strings.Contains(stdout, "cargo") {
			t.Errorf("zsh completion is missing the cargo --pm value:\n%s", stdout)
		}
	}
}

func TestHelpMentionsCargo(t *testing.T) {
	code, stdout, _ := runCLI(t, "help")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "cargo") || !strings.Contains(stdout, "Cargo.toml") {
		t.Errorf("help should mention cargo and Cargo.toml:\n%s", stdout)
	}
}

func TestHelp(t *testing.T) {
	code, stdout, stderr := runCLI(t, "help")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	for _, want := range []string{"run", "--base", "--runs", "--run-timeout", "--install-timeout", "--overall-timeout", "--dry-run", "--quiet", "--style", "--keep-worktrees", "--checkpoint", "--resume", "Exit codes", "--"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q", want)
		}
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		outcome string
		want    int
	}{
		{engine.OutcomeMinimalFound, ExitOK},
		{engine.OutcomeDryRun, ExitOK},
		{engine.OutcomeNotReproduced, ExitNotReproduced},
		{engine.OutcomeFailsAtBase, ExitFailsAtBase},
		{engine.OutcomeInconclusive, ExitInconclusive},
		{engine.OutcomeNoChanges, ExitNoChanges},
		{"future-unknown", ExitError},
	}
	for _, tc := range cases {
		if got := exitCodeFor(tc.outcome); got != tc.want {
			t.Errorf("exitCodeFor(%q) = %d, want %d", tc.outcome, got, tc.want)
		}
	}
}

func TestSummaryMinimalFound(t *testing.T) {
	res := &engine.Result{
		Outcome:        engine.OutcomeMinimalFound,
		PackageManager: "npm",
		Changes:        make([]manifest.Change, 43),
		Minimal: []manifest.Change{
			{Name: "@acme/parser", Section: manifest.Dependencies, Kind: manifest.Updated,
				OldSpec: "3.8.1", NewSpec: "3.9.0", OldResolved: "3.8.1", NewResolved: "3.9.0"},
		},
		Confidence: engine.Confidence{Failures: 3, Runs: 3},
	}
	var buf bytes.Buffer
	printSummary(&buf, res, "depbisect-report.md", "", styleClassic)
	got := buf.String()
	for _, want := range []string{
		"Result Minimal breaking dependency set found",
		"Changes 43 analyzed",
		"Breaking dependencies",
		"@acme/parser  3.8.1 -> 3.9.0",
		"Evidence 3/3 failing runs",
		"Next npm install --save-exact @acme/parser@3.8.1",
		"Report depbisect-report.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	// The machine token belongs to the exit code and the JSON report, not the
	// human summary, where it would only duplicate the Result headline.
	if strings.Contains(got, "minimal-set-found") {
		t.Errorf("summary should not repeat the machine outcome token:\n%s", got)
	}
}

func TestSummaryOtherOutcomes(t *testing.T) {
	res := &engine.Result{
		Outcome:       engine.OutcomeNotReproduced,
		OutcomeDetail: "the command passed 3/3 runs",
		Diagnostics:   []string{"something noteworthy"},
	}
	var buf bytes.Buffer
	printSummary(&buf, res, "", "", styleClassic)
	got := buf.String()
	if !strings.Contains(got, "Result No breaking dependency update reproduced the failure") ||
		!strings.Contains(got, "Reason The command passed 3/3 runs") ||
		!strings.Contains(got, "something noteworthy") {
		t.Errorf("summary = %q", got)
	}
}

func TestSummaryInconclusiveBestKnownSet(t *testing.T) {
	res := &engine.Result{
		Outcome:       engine.OutcomeInconclusive,
		OutcomeDetail: "best-known failing set is not proven 1-minimal",
		Changes:       make([]manifest.Change, 3),
		Minimal: []manifest.Change{
			{Name: "alpha", Section: manifest.Dependencies, Kind: manifest.Updated, OldSpec: "1", NewSpec: "2"},
			{Name: "beta", Section: manifest.Dependencies, Kind: manifest.Updated, OldSpec: "1", NewSpec: "2"},
		},
	}
	var buf bytes.Buffer
	printSummary(&buf, res, "", "", styleClassic)
	got := buf.String()
	// Names pad to the widest ("alpha") plus a two-space column gap.
	for _, want := range []string{"Best-known failing set", "alpha  1 -> 2", "beta   1 -> 2", "not proven 1-minimal"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestParseRunStyle(t *testing.T) {
	t.Setenv("DEPBISECT_STYLE", "")

	opts, err := parseRunArgs([]string{"--base", "main", "--", "x"})
	if err != nil || opts.style != styleModern {
		t.Fatalf("default style = %v, err = %v; want modern", opts.style, err)
	}

	opts, err = parseRunArgs([]string{"--base", "main", "--style", "classic", "--", "x"})
	if err != nil || opts.style != styleClassic {
		t.Fatalf("--style classic = %v, err = %v", opts.style, err)
	}

	t.Setenv("DEPBISECT_STYLE", "classic")
	opts, err = parseRunArgs([]string{"--base", "main", "--", "x"})
	if err != nil || opts.style != styleClassic {
		t.Fatalf("env style = %v, err = %v; want classic", opts.style, err)
	}

	opts, err = parseRunArgs([]string{"--base", "main", "--style", "modern", "--", "x"})
	if err != nil || opts.style != styleModern {
		t.Fatalf("flag should override env: style = %v, err = %v", opts.style, err)
	}

	if _, err := parseRunArgs([]string{"--base", "main", "--style", "fancy", "--", "x"}); err == nil {
		t.Fatal("invalid --style should error")
	}
}

func TestModernSummaryRendersGlyphsWhenColored(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "")

	res := &engine.Result{
		Outcome: engine.OutcomeMinimalFound,
		Command: []string{"npm", "test"},
		Changes: make([]manifest.Change, 43),
		Minimal: []manifest.Change{
			{Name: "esbuild", Section: manifest.Dependencies, Kind: manifest.Updated,
				OldSpec: "0.20.2", NewSpec: "0.21.0"},
		},
		Confidence: engine.Confidence{Failures: 3, Runs: 3},
	}
	var buf bytes.Buffer
	printSummary(&buf, res, "depbisect-report.md", "depbisect-report.json", styleModern)
	got := buf.String()
	for _, want := range []string{
		glyphOK, "Minimal breaking dependency set found",
		"minimal set", glyphFail, "esbuild", glyphArrow,
		"certified minimal", "command", "43 analyzed", ansiCyan,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("modern summary missing %q:\n%s", want, got)
		}
	}
}

func TestModernSummaryFallsBackToClassicWithoutColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // force color off even though style is modern
	res := &engine.Result{
		Outcome: engine.OutcomeMinimalFound,
		Changes: make([]manifest.Change, 3),
		Minimal: []manifest.Change{{Name: "esbuild", Section: manifest.Dependencies,
			Kind: manifest.Updated, OldSpec: "1", NewSpec: "2"}},
		Confidence: engine.Confidence{Failures: 1, Runs: 1},
	}
	var buf bytes.Buffer
	printSummary(&buf, res, "", "", styleModern)
	got := buf.String()
	if !strings.Contains(got, "Result Minimal breaking dependency set found") {
		t.Errorf("modern without color should use classic layout:\n%s", got)
	}
	if strings.ContainsAny(got, "\x1b") {
		t.Errorf("plain fallback must not emit ANSI:\n%q", got)
	}
}

func TestModernProgressCollapsesLifecycle(t *testing.T) {
	var buf bytes.Buffer
	p := &progress{w: &buf, interactive: true, color: true, style: styleModern}
	p.Trial(1, "baseline-old", 0, 43, "preparing", 0)
	p.Trial(1, "baseline-old", 0, 43, "pass", 2*time.Second)
	p.Trial(2, "baseline-new", 43, 43, "preparing", 0)
	p.Trial(2, "baseline-new", 43, 43, "fail", 2*time.Second)
	p.Trial(3, "candidate", 20, 43, "preparing", 0)
	p.Trial(3, "candidate", 20, 43, "fail", time.Second)
	p.Step("Complete", "minimal failing set contains 1 of 43 changes")

	got := buf.String()
	for _, want := range []string{
		"baseline", "reproduced", "ddmin",
		"tests pass", "tests fail", "isolating",
		glyphOK, glyphFail, "1 of 43",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("modern progress missing %q:\n%q", want, got)
		}
	}
	if !strings.Contains(got, "\r\x1b[2K") {
		t.Errorf("modern progress should refresh in place:\n%q", got)
	}
}

func TestProgressFormatsTrialLifecycle(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, true, styleClassic, 1)
	p.Trial(7, "candidate", 2, 10, "preparing", 0)
	p.Trial(7, "candidate", 2, 10, "installing", 1200*time.Millisecond)
	p.Trial(7, "candidate", 2, 10, "verifying", 3200*time.Millisecond)
	p.Trial(7, "candidate", 2, 10, "fail", 5*time.Second)

	got := buf.String()
	for _, want := range []string{
		"Trial 7", "candidate", "2/10 changes", "preparing",
		"installing | 1.2s elapsed",
		"verifying | 3.2s elapsed", "FAIL trial  7", "5s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress missing %q:\n%s", want, got)
		}
	}
}

func TestProgressDefaultCompactsTrialLifecycle(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleClassic, 1)
	p.Trial(1, "baseline-old", 0, 10, "preparing", 0)
	p.Trial(1, "baseline-old", 0, 10, "installing", time.Second)
	p.Trial(1, "baseline-old", 0, 10, "verifying", 2*time.Second)
	p.Trial(1, "baseline-old", 0, 10, "pass", 3*time.Second)

	got := buf.String()
	if strings.Contains(got, "installing") || strings.Contains(got, "verifying") {
		t.Fatalf("default progress should hide intermediate phases:\n%s", got)
	}
	for _, want := range []string{
		"baseline without updates",
		"preparing",
		"EXPECTED trial  1",
		"PASS",
		"3s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress missing %q:\n%s", want, got)
		}
	}
}

func TestTrialExpectation(t *testing.T) {
	cases := []struct {
		role, outcome, want string
	}{
		{"baseline-old", "pass", "expected"},
		{"baseline-old", "fail", "unexpected"},
		{"baseline-new", "fail", "expected"},
		{"baseline-new", "pass", "unexpected"},
		{"candidate", "fail", ""},
	}
	for _, tc := range cases {
		if got := trialExpectation(tc.role, tc.outcome); got != tc.want {
			t.Errorf("trialExpectation(%q, %q) = %q, want %q",
				tc.role, tc.outcome, got, tc.want)
		}
	}
}

func TestProgressInteractiveRefreshesActiveTrial(t *testing.T) {
	var buf bytes.Buffer
	p := &progress{w: &buf, interactive: true}
	p.Trial(1, "baseline-new", 10, 10, "preparing", 0)
	p.Trial(1, "baseline-new", 10, 10, "installing", time.Second)
	p.Trial(1, "baseline-new", 10, 10, "verifying", 2*time.Second)
	p.Trial(1, "baseline-new", 10, 10, "fail", 3*time.Second)

	got := buf.String()
	if !strings.Contains(got, "\r\x1b[2K") {
		t.Fatalf("interactive progress did not clear the active line: %q", got)
	}
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "EXPECTED trial  1") ||
		!strings.Contains(got, "| FAIL |") ||
		!strings.Contains(got, "3s") {
		t.Fatalf("interactive progress should finish with one completed line: %q", got)
	}
}

func TestProgressMarksUnexpectedBaselineOutcome(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleClassic, 1)
	p.Trial(2, "baseline-new", 10, 10, "pass", 3*time.Second)

	got := buf.String()
	for _, want := range []string{"UNEXPECTED", "| PASS |", "baseline with all updates"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress missing %q:\n%s", want, got)
		}
	}
}

func TestOutcomeHeadlines(t *testing.T) {
	cases := map[string]string{
		engine.OutcomeMinimalFound:  "Minimal breaking dependency set found",
		engine.OutcomeNotReproduced: "No breaking dependency update reproduced the failure",
		engine.OutcomeFailsAtBase:   "Failure exists without dependency updates",
		engine.OutcomeInconclusive:  "Inconclusive",
		engine.OutcomeNoChanges:     "No dependency changes to bisect",
		engine.OutcomeDryRun:        "Dry run complete",
	}
	for outcome, want := range cases {
		if got := outcomeHeadline(outcome); got != want {
			t.Errorf("outcomeHeadline(%q) = %q, want %q", outcome, got, want)
		}
	}
}

func TestSentenceCase(t *testing.T) {
	for input, want := range map[string]string{
		"":                  "",
		"already lowercase": "Already lowercase",
		"Already uppercase": "Already uppercase",
	} {
		if got := sentenceCase(input); got != want {
			t.Errorf("sentenceCase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatCommandQuotesAmbiguousArguments(t *testing.T) {
	got := formatCommand([]string{"node", "-e", "process.exit(0)", "--filter", "has space", ""})
	if got != `node -e "process.exit(0)" --filter "has space" ""` {
		t.Fatalf("formatCommand = %q", got)
	}
}

func TestWriteStatusWrapsWithAlignedContinuation(t *testing.T) {
	var buf bytes.Buffer
	writeStatus(&buf, "Reason", strings.Repeat("word ", 30), false, "", true)
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("status did not wrap:\n%s", buf.String())
	}
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", statusWidth+1)) {
		t.Fatalf("continuation is not aligned:\n%s", buf.String())
	}
}

func TestTruncateText(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"0123456789", 7, "0123..."},
		{"abcdef", 3, "..."},
		{"abcdef", 0, ""},
	} {
		if got := truncateText(tc.text, tc.width); got != tc.want {
			t.Errorf("truncateText(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
		}
	}
}

func TestWriteLiveStatusFitsVeryNarrowLine(t *testing.T) {
	var buf bytes.Buffer
	writeLiveStatusWidth(&buf, "Trial 123", "a long active status", false, "", 8)
	if got := buf.String(); got != "Trial..." {
		t.Fatalf("narrow live status = %q, want %q", got, "Trial...")
	}
}

func TestTerminalModeColorEnvironment(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "")
	interactive, color := terminalMode(&buf)
	if interactive || !color {
		t.Fatalf("forced color mode = interactive %v, color %v", interactive, color)
	}

	t.Setenv("NO_COLOR", "1")
	_, color = terminalMode(&buf)
	if color {
		t.Fatal("NO_COLOR must disable forced color")
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "0")
	_, color = terminalMode(&buf)
	if color {
		t.Fatal("CLICOLOR=0 must disable forced color")
	}
}

func TestTerminalModeDoesNotTreatNullDeviceAsTTY(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()

	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("TERM", "")
	interactive, color := terminalMode(null)
	if interactive || color {
		t.Fatalf("null device mode = interactive %v, color %v", interactive, color)
	}
}

func TestTerminalModeDisablesColorForDumbTerminal(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("TERM", "dumb")
	interactive, color := terminalMode(&buf)
	if interactive || color {
		t.Fatalf("TERM=dumb mode = interactive %v, color %v; want plain output",
			interactive, color)
	}
}

// runMainWith installs a fake engine for the duration of the test, runs
// runMain with the given args, and returns the exit code plus captured output.
// It also sets --repo to a temp dir so gitx.New doesn't choke on the path.
func runMainWith(t *testing.T, engineFn func(context.Context, engine.Options) (*engine.Result, error), args ...string) (code int, stdout, stderr string) {
	t.Helper()
	prev := testEngineRun
	testEngineRun = engineFn
	t.Cleanup(func() { testEngineRun = prev })

	var out, errb bytes.Buffer
	full := append([]string{"--base", "HEAD~1", "--repo", t.TempDir()}, args...)
	full = append(full, "--", "true")
	code = runMain(full, &out, &errb, "test-version")
	return code, out.String(), errb.String()
}

func minimalResult() *engine.Result {
	return &engine.Result{
		Outcome: engine.OutcomeMinimalFound,
		Changes: []manifest.Change{
			{Name: "lodash", Section: manifest.Dependencies, Kind: manifest.Updated, OldSpec: "4.0.0", NewSpec: "4.17.21"},
		},
		Minimal: []manifest.Change{
			{Name: "lodash", Section: manifest.Dependencies, Kind: manifest.Updated, OldSpec: "4.0.0", NewSpec: "4.17.21"},
		},
		Confidence: engine.Confidence{Failures: 1, Runs: 1},
	}
}

func TestRunMainEngineError(t *testing.T) {
	code, _, stderr := runMainWith(t, func(_ context.Context, _ engine.Options) (*engine.Result, error) {
		return nil, errors.New("git clone failed")
	})
	if code != ExitError {
		t.Errorf("exit = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr, "git clone failed") {
		t.Errorf("stderr = %q, want error message", stderr)
	}
}

func TestRunMainEngineErrorContextCanceled(t *testing.T) {
	code, _, stderr := runMainWith(t, func(_ context.Context, _ engine.Options) (*engine.Result, error) {
		return nil, context.Canceled
	})
	if code != ExitError {
		t.Errorf("exit = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr, "interrupted") {
		t.Errorf("stderr = %q, want 'interrupted'", stderr)
	}
	if strings.Contains(stderr, "git clone") {
		t.Errorf("canceled error should not print raw error: %q", stderr)
	}
}

func TestRunMainEngineErrorWithCheckpointHint(t *testing.T) {
	cpPath := filepath.Join(t.TempDir(), "state.jsonl")
	// Seed a checkpoint that already holds a completed trial; only then is
	// suggesting --resume meaningful.
	store := checkpoint.NewFileStore(cpPath)
	if err := store.Start(engine.Checkpoint{}); err != nil {
		t.Fatalf("seed checkpoint header: %v", err)
	}
	if err := store.Append(engine.Trial{Role: "candidate", Outcome: "fail"}); err != nil {
		t.Fatalf("seed checkpoint trial: %v", err)
	}
	code, _, stderr := runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return nil, errors.New("something failed")
		},
		"--checkpoint", cpPath,
	)
	if code != ExitError {
		t.Errorf("exit = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr, "resume with --resume") {
		t.Errorf("stderr should mention --resume: %q", stderr)
	}
	if !strings.Contains(stderr, cpPath) {
		t.Errorf("stderr should include checkpoint path: %q", stderr)
	}
}

// TestRunMainEngineErrorNoCheckpointHintWhenEmpty verifies that a run which
// fails before any trial completes does not dangle a useless "resume with
// --resume" suggestion: there is nothing in the checkpoint to resume.
func TestRunMainEngineErrorNoCheckpointHintWhenEmpty(t *testing.T) {
	cpPath := filepath.Join(t.TempDir(), "state.jsonl")
	code, _, stderr := runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return nil, errors.New("something failed")
		},
		"--checkpoint", cpPath,
	)
	if code != ExitError {
		t.Errorf("exit = %d, want %d", code, ExitError)
	}
	if strings.Contains(stderr, "--resume") {
		t.Errorf("stderr should not suggest --resume with no completed trials: %q", stderr)
	}
}

// TestRunMainErrorHints verifies that recognized first-run failures append an
// actionable "hint:" line, while an unrecognized error does not.
func TestRunMainErrorHints(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantHint string // substring that must appear, or "" for no hint at all
	}{
		{"no commits", gitx.ErrNoCommits, "make at least one commit"},
		{"no such commit", gitx.ErrNoSuchCommit, "HEAD~1 needs at least two commits"},
		{"not a repo", gitx.ErrNotARepo, "--repo <path>"},
		{"unrecognized", errors.New("boom"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runMainWith(t, func(_ context.Context, _ engine.Options) (*engine.Result, error) {
				return nil, tc.err
			})
			if code != ExitError {
				t.Errorf("exit = %d, want %d", code, ExitError)
			}
			if tc.wantHint == "" {
				if strings.Contains(stderr, "hint:") {
					t.Errorf("unrecognized error should print no hint: %q", stderr)
				}
				return
			}
			if !strings.Contains(stderr, "hint:") || !strings.Contains(stderr, tc.wantHint) {
				t.Errorf("stderr = %q, want hint containing %q", stderr, tc.wantHint)
			}
		})
	}
}

// gitRepoWith creates a temporary git repository, running each step (a slice
// of git args, or a special {"write", path, body} form) in order, and returns
// the repo path.
func gitRepoWith(t *testing.T, steps ...[]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	for _, s := range steps {
		if s[0] == "write" {
			if err := os.WriteFile(filepath.Join(dir, s[1]), []byte(s[2]), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		git(s...)
	}
	return dir
}

func TestRunMainSuggestBaseListsDependencyCommits(t *testing.T) {
	dir := gitRepoWith(t,
		[]string{"write", "package.json", `{"name":"a","dependencies":{"x":"1.0.0"}}`},
		[]string{"add", "-A"}, []string{"commit", "-qm", "add deps"},
		[]string{"write", "main.js", "code"},
		[]string{"add", "-A"}, []string{"commit", "-qm", "feature (no deps)"},
		[]string{"write", "package.json", `{"name":"a","dependencies":{"x":"2.0.0"}}`},
		[]string{"add", "-A"}, []string{"commit", "-qm", "bump x"},
	)
	code, _, stderr := runCLI(t, "run", "--repo", dir, "--", "npm", "test")
	if code != ExitError {
		t.Errorf("exit = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr, "--base is required") {
		t.Errorf("stderr should explain --base: %q", stderr)
	}
	// Both dependency-touching commits appear; the no-deps commit does not.
	if !strings.Contains(stderr, "add deps") || !strings.Contains(stderr, "bump x") {
		t.Errorf("stderr should list dependency commits: %q", stderr)
	}
	if strings.Contains(stderr, "feature (no deps)") {
		t.Errorf("stderr should not list non-dependency commits: %q", stderr)
	}
	// The example reconstructs the user's actual command after the separator.
	if !strings.Contains(stderr, "Then run:") || !strings.Contains(stderr, "-- npm test") {
		t.Errorf("stderr should suggest a concrete command: %q", stderr)
	}
}

func TestRunMainSuggestBaseEmptyRepo(t *testing.T) {
	dir := gitRepoWith(t) // initialized, no commits
	code, _, stderr := runCLI(t, "run", "--repo", dir, "--", "npm", "test")
	if code != ExitError {
		t.Errorf("exit = %d, want %d", code, ExitError)
	}
	if !strings.Contains(stderr, "could not inspect") || !strings.Contains(stderr, "no commits yet") {
		t.Errorf("stderr should report the empty repo: %q", stderr)
	}
	if !strings.Contains(stderr, "hint:") {
		t.Errorf("stderr should include a hint: %q", stderr)
	}
}

func TestRunMainSuggestBaseNoDependencyHistory(t *testing.T) {
	dir := gitRepoWith(t,
		[]string{"write", "README.md", "hi"},
		[]string{"add", "-A"}, []string{"commit", "-qm", "docs only"},
	)
	code, _, stderr := runCLI(t, "run", "--repo", dir, "--", "npm", "test")
	if code != ExitError {
		t.Errorf("exit = %d, want %d", code, ExitError)
	}
	// No dependency files were ever touched: fall back to explicit guidance.
	if !strings.Contains(stderr, "No dependency changes found") {
		t.Errorf("stderr should fall back to explicit guidance: %q", stderr)
	}
	// Still offers a copy-pasteable command with a placeholder base.
	if !strings.Contains(stderr, "--base <rev> -- npm test") {
		t.Errorf("stderr should show a placeholder command: %q", stderr)
	}
}

func TestRunMainGuidanceMissingSeparator(t *testing.T) {
	dir := gitRepoWith(t,
		[]string{"write", "package.json", `{"name":"a","dependencies":{"x":"1.0.0"}}`},
		[]string{"add", "-A"}, []string{"commit", "-qm", "add deps"},
	)

	t.Run("no separator, base given", func(t *testing.T) {
		// --base is present, so no commit list is needed — just the syntax fix.
		code, _, stderr := runCLI(t, "run", "--repo", dir, "--base", "HEAD", "npm", "test")
		if code != ExitError {
			t.Errorf("exit = %d, want %d", code, ExitError)
		}
		if !strings.Contains(stderr, `"--" separator`) {
			t.Errorf("stderr should explain the separator: %q", stderr)
		}
		if strings.Contains(stderr, "Recent commits") {
			t.Errorf("a provided --base should not trigger a commit list: %q", stderr)
		}
		if !strings.Contains(stderr, "--base HEAD -- npm test") {
			t.Errorf("stderr should reconstruct the command: %q", stderr)
		}
	})

	t.Run("no separator, no base", func(t *testing.T) {
		// Both missing: combined headline, commit list, and full example.
		code, _, stderr := runCLI(t, "run", "--repo", dir, "cargo", "test")
		if code != ExitError {
			t.Errorf("exit = %d, want %d", code, ExitError)
		}
		if !strings.Contains(stderr, `"--" separator`) || !strings.Contains(stderr, "--base") {
			t.Errorf("stderr should name both missing pieces: %q", stderr)
		}
		if !strings.Contains(stderr, "Recent commits") || !strings.Contains(stderr, "-- cargo test") {
			t.Errorf("stderr should list commits and reconstruct the command: %q", stderr)
		}
	})

	t.Run("flag-like token in command stays a placeholder", func(t *testing.T) {
		// We cannot tell where the command begins, so avoid implying --release
		// is a depbisect flag.
		_, _, stderr := runCLI(t, "run", "--repo", dir, "cargo", "build", "--release")
		if strings.Contains(stderr, "cargo build") {
			t.Errorf("ambiguous command should not be reconstructed: %q", stderr)
		}
		if !strings.Contains(stderr, "-- <command>") {
			t.Errorf("stderr should use a command placeholder: %q", stderr)
		}
	})
}

func TestExampleCommand(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "<command>"},
		{[]string{"npm", "test"}, "npm test"},
		{[]string{"cargo", "build", "--release"}, "<command>"}, // flag-like token
		{[]string{"-x"}, "<command>"},
	}
	for _, tc := range cases {
		if got := exampleCommand(tc.in); got != tc.want {
			t.Errorf("exampleCommand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunMainSuccess(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	code, stdout, stderr := runMainWith(t, func(_ context.Context, _ engine.Options) (*engine.Result, error) {
		return minimalResult(), nil
	}, "--no-reports")
	if code != ExitOK {
		t.Errorf("exit = %d, want %d (stderr: %s)", code, ExitOK, stderr)
	}
	if !strings.Contains(stdout, "Minimal breaking dependency set found") {
		t.Errorf("stdout missing summary: %q", stdout)
	}
}

func TestRunMainWritesReports(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "report.json")
	mdPath := filepath.Join(dir, "report.md")

	code, _, stderr := runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return minimalResult(), nil
		},
		"--report-json", jsonPath,
		"--report-md", mdPath,
	)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("JSON report not written: %v", err)
	}
	if _, err := os.Stat(mdPath); err != nil {
		t.Errorf("Markdown report not written: %v", err)
	}
}

func TestRunMainNoReportsSkipsFiles(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "report.json")
	mdPath := filepath.Join(dir, "report.md")

	runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return minimalResult(), nil
		},
		"--no-reports",
		"--report-json", jsonPath,
		"--report-md", mdPath,
	)
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("--no-reports must not write JSON report")
	}
	if _, err := os.Stat(mdPath); !os.IsNotExist(err) {
		t.Error("--no-reports must not write Markdown report")
	}
}

func TestRunMainDryRunSkipsReports(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "report.json")

	runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return &engine.Result{Outcome: engine.OutcomeDryRun}, nil
		},
		"--dry-run",
		"--report-json", jsonPath,
	)
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("dry-run outcome must not write reports")
	}
}

func TestRunMainJSONReportWriteError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Point JSON report at a directory so os.WriteFile fails.
	badPath := t.TempDir()

	code, stdout, stderr := runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return minimalResult(), nil
		},
		"--report-json", badPath,
		"--no-reports", // skip the MD report; only test JSON error path
	)
	// A report write failure is non-fatal: exit should still reflect the outcome.
	_ = code
	_ = stdout
	_ = stderr
	// The real assertion: passing --no-reports suppresses the write entirely,
	// so to test the error path we need to not pass --no-reports.
	// Re-run without --no-reports.
	code2, _, stderr2 := runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return minimalResult(), nil
		},
		"--report-json", badPath,
		"--report-md", filepath.Join(t.TempDir(), "r.md"),
	)
	if code2 != ExitOK {
		t.Errorf("report write error should not change exit code; got %d", code2)
	}
	if !strings.Contains(stderr2, "depbisect:") {
		t.Errorf("stderr should contain error message: %q", stderr2)
	}
}

func TestRunMainMarkdownReportWriteError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Point --report-md at a directory so os.WriteFile fails.
	badMDPath := t.TempDir()
	jsonPath := filepath.Join(t.TempDir(), "r.json")

	code, _, stderr := runMainWith(t,
		func(_ context.Context, _ engine.Options) (*engine.Result, error) {
			return minimalResult(), nil
		},
		"--report-md", badMDPath,
		"--report-json", jsonPath,
	)
	if code != ExitOK {
		t.Errorf("MD write error must not change exit code; got %d", code)
	}
	if !strings.Contains(stderr, "write Markdown report") {
		t.Errorf("stderr should mention 'write Markdown report': %q", stderr)
	}
}

func TestRunMainQuietSuppressesProgress(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var engineOpts engine.Options
	runMainWith(t,
		func(_ context.Context, o engine.Options) (*engine.Result, error) {
			engineOpts = o
			return &engine.Result{Outcome: engine.OutcomeNotReproduced}, nil
		},
		"--quiet", "--no-reports",
	)
	// Quiet mode is exercised via the progress writer path; the engine still runs.
	// We verify the engine was called (not short-circuited) by checking Options.
	if engineOpts.BaseRev == "" {
		t.Error("engine was not called in quiet mode")
	}
}

func TestRunMainVerboseSetsStream(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var capturedStream bool
	runMainWith(t,
		func(_ context.Context, o engine.Options) (*engine.Result, error) {
			capturedStream = o.Stream != nil
			return &engine.Result{Outcome: engine.OutcomeNotReproduced}, nil
		},
		"--verbose", "--no-reports",
	)
	if !capturedStream {
		t.Error("--verbose should set a non-nil stream on engine options")
	}
}

func TestRunMainCheckpointNotSetOnDryRun(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var capturedCheckpoint engine.CheckpointStore
	runMainWith(t,
		func(_ context.Context, o engine.Options) (*engine.Result, error) {
			capturedCheckpoint = o.Checkpoint
			return &engine.Result{Outcome: engine.OutcomeDryRun}, nil
		},
		"--dry-run", "--checkpoint", filepath.Join(t.TempDir(), "cp.jsonl"),
	)
	if capturedCheckpoint != nil {
		t.Error("--dry-run must not pass a checkpoint store to the engine")
	}
}

// --- Coverage for runMain's real-engine wiring and writeJSONReport ---------
//
// The unit tests above stub testEngineRun, so the production NewInstaller and
// engineRun closures in runMain are never exercised in-process (the cmd e2e
// suite hits them, but via a subprocess that Go cannot attribute coverage to).
// The test below drives the real engine in-process against a tiny npm fixture
// with a stubbed `npm`, so a candidate actually "installs".

const baseFixturePkg = `{"name":"fixture","version":"1.0.0","dependencies":{"leftpad":"1.0.0"}}`
const newFixturePkg = `{"name":"fixture","version":"1.0.0","dependencies":{"leftpad":"2.0.0"}}`

func npmLockFixture(leftpad string) string {
	return fmt.Sprintf(`{"name":"fixture","lockfileVersion":3,"packages":{"":{},"node_modules/leftpad":{"version":%q}}}`, leftpad)
}

// stubNPM puts a fake `npm` (succeeds; reports a version) first on PATH for the
// duration of the test, so the engine can install without touching a registry.
func stubNPM(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("npm stub uses a POSIX shell script")
	}
	for _, bin := range []string{"git", "sh", "true"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 9.9.9-test; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunMainRealEngineInstalls(t *testing.T) {
	stubNPM(t)
	dir := gitRepoWith(t,
		[]string{"write", "package.json", baseFixturePkg},
		[]string{"write", "package-lock.json", npmLockFixture("1.0.0")},
		[]string{"add", "-A"}, []string{"commit", "-qm", "base"},
		[]string{"write", "package.json", newFixturePkg},
		[]string{"write", "package-lock.json", npmLockFixture("2.0.0")},
		[]string{"add", "-A"}, []string{"commit", "-qm", "bump leftpad"},
	)
	// `true` always passes, so the failure never reproduces (OutcomeNotReproduced),
	// but the engine still installs the candidate first — invoking NewInstaller.
	// Keep the checkpoint inside a temp dir so nothing lands in the package tree.
	cp := filepath.Join(t.TempDir(), "cp.jsonl")
	var out, errb bytes.Buffer
	code := runMain([]string{
		"--repo", dir, "--base", "HEAD~1", "--checkpoint", cp,
		"--no-reports", "--quiet", "--", "true",
	}, &out, &errb, "test-version")

	// The point is that the real engine ran to a verdict, not a usage/setup
	// error. NotReproduced is the expected outcome for an always-passing command.
	if code != ExitNotReproduced {
		t.Fatalf("exit = %d, want %d (ExitNotReproduced)\nstderr: %s", code, ExitNotReproduced, errb.String())
	}
}

func TestWriteJSONReportEncodeError(t *testing.T) {
	// NaN cannot be encoded as JSON, so rep.JSON() fails and writeJSONReport
	// must surface the wrapped error rather than panic or write a bad file.
	path := filepath.Join(t.TempDir(), "report.json")
	err := writeJSONReport(&report.Report{DurationSeconds: math.NaN()}, path)
	if err == nil || !strings.Contains(err.Error(), "encode JSON report") {
		t.Fatalf("err = %v, want an 'encode JSON report' error", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("no file should be written on encode failure (stat err: %v)", statErr)
	}
}

func TestParseRunJobsEnv(t *testing.T) {
	t.Setenv("DEPBISECT_JOBS", "")

	opts, err := parseRunArgs([]string{"--base", "main", "--", "x"})
	if err != nil || opts.jobs != 1 {
		t.Fatalf("default jobs = %d, err = %v; want 1", opts.jobs, err)
	}

	t.Setenv("DEPBISECT_JOBS", "4")
	opts, err = parseRunArgs([]string{"--base", "main", "--", "x"})
	if err != nil || opts.jobs != 4 {
		t.Fatalf("env jobs = %d, err = %v; want 4", opts.jobs, err)
	}

	opts, err = parseRunArgs([]string{"--base", "main", "--jobs", "2", "--", "x"})
	if err != nil || opts.jobs != 2 {
		t.Fatalf("--jobs should override env: jobs = %d, err = %v", opts.jobs, err)
	}

	opts, err = parseRunArgs([]string{"--base", "main", "-j", "3", "--", "x"})
	if err != nil || opts.jobs != 3 {
		t.Fatalf("-j should override env: jobs = %d, err = %v", opts.jobs, err)
	}

	for _, bad := range []string{"0", "-2", "two"} {
		t.Setenv("DEPBISECT_JOBS", bad)
		if _, err := parseRunArgs([]string{"--base", "main", "--", "x"}); err == nil {
			t.Errorf("DEPBISECT_JOBS=%q should error", bad)
		}
	}
}
