package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

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
		"--pm", "pnpm", "--run-timeout", "90s", "--keep-worktrees",
		"--dry-run", "--verbose", "--repo", "/some/repo",
		"--report-md", "out.md", "--report-json", "out.json",
		"--", "pnpm", "test", "--filter", "has space",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.base != "origin/main" || opts.to != "feature" || opts.runs != 3 ||
		opts.pm != "pnpm" || !opts.keepWorktrees || !opts.dryRun || !opts.verbose ||
		opts.repo != "/some/repo" || opts.reportMD != "out.md" || opts.reportJSON != "out.json" {
		t.Errorf("opts = %+v", opts)
	}
	if opts.runTimeout.String() != "1m30s" {
		t.Errorf("runTimeout = %v", opts.runTimeout)
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
		opts.reportMD != "depbisect-report.md" || opts.reportJSON != "depbisect-report.json" {
		t.Errorf("defaults = %+v", opts)
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
	}
}

func TestHelp(t *testing.T) {
	code, stdout, stderr := runCLI(t, "help")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	for _, want := range []string{"run", "--base", "--runs", "--dry-run", "--keep-worktrees", "Exit codes", "--"} {
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
	printSummary(&buf, res, "depbisect-report.md", "")
	got := buf.String()
	for _, want := range []string{
		"Analyzed 43 dependency changes",
		"Minimal failing set:",
		"@acme/parser 3.8.1 -> 3.9.0",
		"Reproduced 3/3 times",
		"Report: depbisect-report.md",
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
	printSummary(&buf, res, "", "")
	got := buf.String()
	if !strings.Contains(got, "the command passed 3/3 runs") || !strings.Contains(got, "something noteworthy") {
		t.Errorf("summary = %q", got)
	}
}
