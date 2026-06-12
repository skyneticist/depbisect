package report

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
)

var update = flag.Bool("update", false, "rewrite golden files")

func fixtureResult() *engine.Result {
	start := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	return &engine.Result{
		Outcome:        engine.OutcomeMinimalFound,
		OutcomeDetail:  "minimal failing set has 1 of 3 changes after 6 candidate tests",
		BaseRev:        "origin/main",
		BaseSHA:        "1111111111111111111111111111111111111111",
		ToRev:          "HEAD",
		ToSHA:          "2222222222222222222222222222222222222222",
		PackageManager: "npm",
		Command:        []string{"npm", "test", "--", "--grep", "parser suite"},
		Runs:           3,
		Changes: []manifest.Change{
			{Name: "alpha", Section: manifest.Dependencies, Kind: manifest.Updated, OldSpec: "^1.0.0", NewSpec: "^1.5.0", OldResolved: "1.0.3", NewResolved: "1.5.2"},
			{Name: "beta", Section: manifest.Dependencies, Kind: manifest.Updated, OldSpec: "3.0.0", NewSpec: "3.2.0", OldResolved: "3.0.0", NewResolved: "3.2.0"},
			{Name: "tool", Section: manifest.DevDependencies, Kind: manifest.Added, NewSpec: "2.0.0", NewResolved: "2.0.0"},
		},
		LockfileOnly: []manifest.LockfileChange{
			{Name: "transit", Section: manifest.Dependencies, Spec: "^4.0.0", OldResolved: "4.0.1", NewResolved: "4.9.0"},
		},
		Minimal: []manifest.Change{
			{Name: "beta", Section: manifest.Dependencies, Kind: manifest.Updated, OldSpec: "3.0.0", NewSpec: "3.2.0", OldResolved: "3.0.0", NewResolved: "3.2.0"},
		},
		Confidence:  engine.Confidence{Failures: 3, Runs: 3},
		Diagnostics: []string{"1 dependencies changed only in the lockfile (version spec unchanged): transit (4.0.1 -> 4.9.0)."},
		Trials: []engine.Trial{
			{Role: "baseline-old", Applied: []string{}, Outcome: "pass", RunsExecuted: 3, Failures: 0, Duration: 12 * time.Second},
			{Role: "baseline-new", Applied: []string{"dependencies:alpha", "dependencies:beta", "devDependencies:tool"}, Outcome: "fail", RunsExecuted: 3, Failures: 3, Duration: 14 * time.Second},
			{Role: "candidate", Applied: []string{"dependencies:alpha"}, Outcome: "pass", RunsExecuted: 1, Failures: 0, Duration: 9 * time.Second},
			{Role: "candidate", Applied: []string{"dependencies:beta"}, Outcome: "fail", RunsExecuted: 3, Failures: 3, Duration: 13 * time.Second},
		},
		StartedAt:  start,
		FinishedAt: start.Add(61 * time.Second),
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestJSONGolden(t *testing.T) {
	r := New(fixtureResult(), "1.2.3")
	got, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "report.json.golden", got)
}

func TestJSONIsValidAndStable(t *testing.T) {
	r := New(fixtureResult(), "1.2.3")
	got, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion = %v", doc["schemaVersion"])
	}
	if doc["outcome"] != "minimal-set-found" {
		t.Errorf("outcome = %v", doc["outcome"])
	}
	// Marshalling twice must be byte-identical.
	again, _ := New(fixtureResult(), "1.2.3").JSON()
	if !bytes.Equal(got, again) {
		t.Error("JSON output not deterministic")
	}
}

func TestMarkdownGolden(t *testing.T) {
	r := New(fixtureResult(), "1.2.3")
	checkGolden(t, "report.md.golden", r.Markdown())
}

func TestMarkdownNotReproduced(t *testing.T) {
	res := fixtureResult()
	res.Outcome = engine.OutcomeNotReproduced
	res.OutcomeDetail = "the command passed 3/3 runs with all dependency updates applied"
	res.Minimal = nil
	res.Confidence = engine.Confidence{}
	md := string(New(res, "1.2.3").Markdown())
	if strings.Contains(md, "## Minimal failing set") {
		t.Error("not-reproduced report must not claim a minimal set")
	}
	if !strings.Contains(md, "not-reproduced") {
		t.Error("outcome missing")
	}
}

func TestMarkdownEscapesNothingButRendersCommandSafely(t *testing.T) {
	res := fixtureResult()
	res.Command = []string{"sh", "-c", "grep 'x y' file && echo `ok`"}
	md := string(New(res, "1.2.3").Markdown())
	if !strings.Contains(md, "grep 'x y' file") {
		t.Error("command argument content missing from markdown")
	}
}

func TestCommandDisplay(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"npm", "test"}, "npm test"},
		{[]string{"pnpm", "run", "test suite"}, `pnpm run "test suite"`},
		{[]string{"sh", "-c", `echo "hi"`}, `sh -c "echo \"hi\""`},
		{[]string{"x", ""}, `x ""`},
	}
	for _, tc := range cases {
		if got := commandDisplay(tc.in); got != tc.want {
			t.Errorf("commandDisplay(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
