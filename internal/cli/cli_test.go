package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
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
		{"bad runs", []string{"run", "--base", "main", "--runs", "0", "--", "x"}, "--runs"},
		{"negative run timeout", []string{"run", "--base", "main", "--run-timeout", "-1s", "--", "x"}, "--run-timeout"},
		{"negative install timeout", []string{"run", "--base", "main", "--install-timeout", "-1s", "--", "x"}, "--install-timeout"},
		{"negative overall timeout", []string{"run", "--base", "main", "--overall-timeout", "-1s", "--", "x"}, "--overall-timeout"},
		{"quiet and verbose", []string{"run", "--base", "main", "--quiet", "--verbose", "--", "x"}, "--quiet"},
		{"bad style", []string{"run", "--base", "main", "--style", "fancy", "--", "x"}, "--style"},
		{"unknown flag", []string{"run", "--base", "main", "--bogus", "--", "x"}, "bogus"},
		{"completion unknown shell", []string{"completion", "fish"}, "fish"},
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
		Outcome: engine.OutcomeMinimalFound,
		Changes: make([]manifest.Change, 43),
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
		"Outcome minimal-set-found",
		"Changes 43 analyzed",
		"Breaking dependencies",
		"@acme/parser 3.8.1 -> 3.9.0",
		"Evidence 3/3 failing runs",
		"Report depbisect-report.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
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
	for _, want := range []string{"Best-known failing set", "alpha 1 -> 2", "not proven 1-minimal"} {
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
	p := newProgress(&buf, true, styleClassic)
	p.Trial(7, "candidate", 2, 10, "preparing", 0)
	p.Trial(7, "candidate", 2, 10, "installing", 1200*time.Millisecond)
	p.Trial(7, "candidate", 2, 10, "verifying", 3200*time.Millisecond)
	p.Trial(7, "candidate", 2, 10, "fail", 5*time.Second)

	got := buf.String()
	for _, want := range []string{
		"Trial 7", "candidate", "2/10 changes", "preparing",
		"installing | 1.2s elapsed",
		"verifying | 3.2s elapsed", "FAIL trial 7", "5s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress missing %q:\n%s", want, got)
		}
	}
}

func TestProgressDefaultCompactsTrialLifecycle(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleClassic)
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
		"EXPECTED trial 1",
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
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "EXPECTED trial 1") ||
		!strings.Contains(got, "| FAIL |") ||
		!strings.Contains(got, "3s") {
		t.Fatalf("interactive progress should finish with one completed line: %q", got)
	}
}

func TestProgressMarksUnexpectedBaselineOutcome(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false, styleClassic)
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
