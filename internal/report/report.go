// Package report renders an engine.Result as stable machine-readable JSON
// (schema version 1) and human-readable Markdown.
//
// Reports never include environment variables or captured command output;
// they identify revisions, dependency changes, the verification command,
// trial outcomes, and timings.
package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
)

// SchemaVersion identifies the JSON report layout. It only changes on
// breaking changes to the structure.
const SchemaVersion = 1

// Rev identifies one compared revision.
type Rev struct {
	Rev string `json:"rev"`
	SHA string `json:"sha"`
}

// Change mirrors manifest.Change with stable JSON names.
type Change struct {
	Name        string `json:"name"`
	Section     string `json:"section"`
	Kind        string `json:"kind"`
	OldSpec     string `json:"oldSpec,omitempty"`
	NewSpec     string `json:"newSpec,omitempty"`
	OldResolved string `json:"oldResolved,omitempty"`
	NewResolved string `json:"newResolved,omitempty"`
}

// LockfileChange mirrors manifest.LockfileChange.
type LockfileChange struct {
	Name        string `json:"name"`
	Section     string `json:"section"`
	Spec        string `json:"spec"`
	OldResolved string `json:"oldResolved"`
	NewResolved string `json:"newResolved"`
}

// Trial is one executed candidate evaluation.
type Trial struct {
	Role       string   `json:"role"`
	Applied    []string `json:"applied"`
	Outcome    string   `json:"outcome"`
	Runs       int      `json:"runs"`
	Failures   int      `json:"failures"`
	DurationMS int64    `json:"durationMs"`
}

// Confidence states how often the minimal set reproduced the failure.
type Confidence struct {
	Failures int `json:"failures"`
	Runs     int `json:"runs"`
}

// Report is the schema-stable top-level document.
type Report struct {
	SchemaVersion    int              `json:"schemaVersion"`
	Tool             string           `json:"tool"`
	ToolVersion      string           `json:"toolVersion"`
	Outcome          string           `json:"outcome"`
	OutcomeDetail    string           `json:"outcomeDetail"`
	Base             Rev              `json:"base"`
	Target           Rev              `json:"target"`
	PackageManager   string           `json:"packageManager"`
	Command          []string         `json:"command"`
	RunsPerCandidate int              `json:"runsPerCandidate"`
	Changes          []Change         `json:"changes"`
	LockfileOnly     []LockfileChange `json:"lockfileOnlyChanges"`
	MinimalSet       []Change         `json:"minimalSet"`
	Confidence       *Confidence      `json:"confidence,omitempty"`
	Diagnostics      []string         `json:"diagnostics"`
	Trials           []Trial          `json:"trials"`
	StartedAt        string           `json:"startedAt"`
	FinishedAt       string           `json:"finishedAt"`
	DurationSeconds  float64          `json:"durationSeconds"`
	KeptWorktree     string           `json:"keptWorktree,omitempty"`

	humanChanges []manifest.Change
	humanMinimal []manifest.Change
}

// New converts an engine.Result into a Report.
func New(res *engine.Result, toolVersion string) *Report {
	r := &Report{
		SchemaVersion:    SchemaVersion,
		Tool:             "depbisect",
		ToolVersion:      toolVersion,
		Outcome:          res.Outcome,
		OutcomeDetail:    res.OutcomeDetail,
		Base:             Rev{Rev: res.BaseRev, SHA: res.BaseSHA},
		Target:           Rev{Rev: res.ToRev, SHA: res.ToSHA},
		PackageManager:   res.PackageManager,
		Command:          append([]string{}, res.Command...),
		RunsPerCandidate: res.Runs,
		Changes:          make([]Change, 0, len(res.Changes)),
		LockfileOnly:     make([]LockfileChange, 0, len(res.LockfileOnly)),
		MinimalSet:       make([]Change, 0, len(res.Minimal)),
		Diagnostics:      append([]string{}, res.Diagnostics...),
		Trials:           make([]Trial, 0, len(res.Trials)),
		StartedAt:        res.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt:       res.FinishedAt.UTC().Format(time.RFC3339),
		DurationSeconds:  res.FinishedAt.Sub(res.StartedAt).Seconds(),
		KeptWorktree:     res.KeptWorktree,
		humanChanges:     res.Changes,
		humanMinimal:     res.Minimal,
	}
	for _, c := range res.Changes {
		r.Changes = append(r.Changes, toChange(c))
	}
	for _, lc := range res.LockfileOnly {
		r.LockfileOnly = append(r.LockfileOnly, LockfileChange{
			Name: lc.Name, Section: string(lc.Section), Spec: lc.Spec,
			OldResolved: lc.OldResolved, NewResolved: lc.NewResolved,
		})
	}
	for _, c := range res.Minimal {
		r.MinimalSet = append(r.MinimalSet, toChange(c))
	}
	if res.Outcome == engine.OutcomeMinimalFound {
		r.Confidence = &Confidence{Failures: res.Confidence.Failures, Runs: res.Confidence.Runs}
	}
	for _, t := range res.Trials {
		applied := t.Applied
		if applied == nil {
			applied = []string{}
		}
		r.Trials = append(r.Trials, Trial{
			Role: t.Role, Applied: applied, Outcome: t.Outcome,
			Runs: t.RunsExecuted, Failures: t.Failures,
			DurationMS: t.Duration.Milliseconds(),
		})
	}
	return r
}

func toChange(c manifest.Change) Change {
	return Change{
		Name: c.Name, Section: string(c.Section), Kind: c.Kind.String(),
		OldSpec: c.OldSpec, NewSpec: c.NewSpec,
		OldResolved: c.OldResolved, NewResolved: c.NewResolved,
	}
}

// JSON renders the report as deterministic, indented JSON. HTML escaping is
// disabled: the output is a file, not an HTML context.
func (r *Report) JSON() ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("encode JSON report: %w", err)
	}
	return []byte(buf.String()), nil
}

// Markdown renders the report for humans.
func (r *Report) Markdown() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# DepBisect Report\n\n")
	fmt.Fprintf(&b, "**Outcome:** `%s` — %s\n\n", r.Outcome, r.OutcomeDetail)

	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Base | `%s` (`%s`) |\n", r.Base.Rev, short(r.Base.SHA))
	fmt.Fprintf(&b, "| Target | `%s` (`%s`) |\n", r.Target.Rev, short(r.Target.SHA))
	fmt.Fprintf(&b, "| Package manager | %s |\n", r.PackageManager)
	fmt.Fprintf(&b, "| Command | `%s` |\n", commandDisplay(r.Command))
	fmt.Fprintf(&b, "| Runs per candidate | %d |\n", r.RunsPerCandidate)
	fmt.Fprintf(&b, "| Started | %s |\n", r.StartedAt)
	fmt.Fprintf(&b, "| Duration | %s |\n", durationDisplay(r.DurationSeconds))
	fmt.Fprintf(&b, "| Tool | depbisect %s |\n\n", r.ToolVersion)

	if len(r.humanMinimal) > 0 {
		fmt.Fprintf(&b, "## Minimal failing set\n\n")
		for _, c := range r.humanMinimal {
			fmt.Fprintf(&b, "- %s\n", c.String())
		}
		if r.Confidence != nil {
			fmt.Fprintf(&b, "\nReproduced %d/%d times.\n", r.Confidence.Failures, r.Confidence.Runs)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Dependency changes (%d)\n\n", len(r.humanChanges))
	if len(r.humanChanges) > 0 {
		fmt.Fprintf(&b, "| Dependency | Section | Change | Old | New |\n|---|---|---|---|---|\n")
		for _, c := range r.humanChanges {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				c.Name, c.Section, c.Kind, versionCell(c.OldSpec, c.OldResolved), versionCell(c.NewSpec, c.NewResolved))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("None.\n\n")
	}

	if len(r.LockfileOnly) > 0 {
		fmt.Fprintf(&b, "## Lockfile-only changes (%d, not bisectable)\n\n", len(r.LockfileOnly))
		fmt.Fprintf(&b, "| Dependency | Spec | Old resolved | New resolved |\n|---|---|---|---|\n")
		for _, lc := range r.LockfileOnly {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", lc.Name, lc.Spec, lc.OldResolved, lc.NewResolved)
		}
		b.WriteString("\n")
	}

	if len(r.Diagnostics) > 0 {
		fmt.Fprintf(&b, "## Diagnostics\n\n")
		for _, d := range r.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", d)
		}
		b.WriteString("\n")
	}

	if len(r.Trials) > 0 {
		fmt.Fprintf(&b, "## Trials (%d)\n\n", len(r.Trials))
		fmt.Fprintf(&b, "| # | Role | Applied changes | Outcome | Failed/Runs | Duration |\n|---|---|---|---|---|---|\n")
		for i, t := range r.Trials {
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %d/%d | %s |\n",
				i+1, t.Role, appliedDisplay(t.Applied), t.Outcome, t.Failures, t.Runs,
				durationDisplay(float64(t.DurationMS)/1000))
		}
		b.WriteString("\n")
	}

	if r.KeptWorktree != "" {
		fmt.Fprintf(&b, "Worktree kept at `%s`.\n", r.KeptWorktree)
	}
	return []byte(b.String())
}

// commandDisplay renders an argv for humans, quoting arguments that contain
// spaces or quotes. This is display only; DepBisect never builds shell
// command lines from it.
func commandDisplay(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'`$&|;<>()") {
			parts[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

func appliedDisplay(applied []string) string {
	if len(applied) == 0 {
		return "none (all reverted)"
	}
	const maxListed = 6
	if len(applied) > maxListed {
		return fmt.Sprintf("%s, … (%d total)", strings.Join(applied[:maxListed], ", "), len(applied))
	}
	return strings.Join(applied, ", ")
}

func versionCell(spec, resolved string) string {
	switch {
	case spec == "" && resolved == "":
		return "—"
	case resolved == "":
		return spec
	case spec == "":
		return resolved
	default:
		return fmt.Sprintf("%s (%s)", spec, resolved)
	}
}

func durationDisplay(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second)).Round(100 * time.Millisecond)
	return d.String()
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
