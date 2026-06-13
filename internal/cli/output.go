package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/skyneticist/depbisect/internal/engine"
)

// progress prints phase updates to stderr. Inside a TTY (and without
// NO_COLOR), step lines are bold; otherwise output is plain text, one line
// per update, suitable for CI logs.
type progress struct {
	w       io.Writer
	verbose bool
	color   bool
}

func newProgress(w io.Writer, verbose bool) *progress {
	color := false
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			color = info.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == ""
		}
	}
	return &progress{w: w, verbose: verbose, color: color}
}

func (p *progress) Step(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.color {
		fmt.Fprintf(p.w, "\x1b[1m==>\x1b[0m %s\n", msg)
	} else {
		fmt.Fprintf(p.w, "==> %s\n", msg)
	}
}

func (p *progress) Detail(format string, args ...any) {
	if !p.verbose {
		return
	}
	fmt.Fprintf(p.w, "    %s\n", fmt.Sprintf(format, args...))
}

func (p *progress) Trial(number int, role string, applied, total int, phase string, elapsed time.Duration) {
	switch phase {
	case "preparing":
		fmt.Fprintf(p.w, "--> Trial %d [%s, %d/%d changes]: preparing\n",
			number, role, applied, total)
	case "installing":
		fmt.Fprintf(p.w, "--> Trial %d [%s, %d/%d changes]: installing after %s\n",
			number, role, applied, total, elapsed.Round(100*time.Millisecond))
	case "verifying":
		fmt.Fprintf(p.w, "--> Trial %d [%s, %d/%d changes]: verifying after %s\n",
			number, role, applied, total, elapsed.Round(100*time.Millisecond))
	default:
		fmt.Fprintf(p.w, "--> Trial %d [%s, %d/%d changes]: %s in %s\n",
			number, role, applied, total, phase, elapsed.Round(100*time.Millisecond))
	}
}

// printSummary writes the final human-readable result to stdout.
func printSummary(w io.Writer, res *engine.Result, mdPath, jsonPath string) {
	fmt.Fprintf(w, "\nAnalyzed %d dependency changes\n", len(res.Changes))

	switch res.Outcome {
	case engine.OutcomeMinimalFound:
		fmt.Fprintf(w, "\nMinimal failing set:\n")
		for _, c := range res.Minimal {
			fmt.Fprintf(w, "  %s\n", c.String())
		}
		fmt.Fprintf(w, "\nReproduced %d/%d times\n", res.Confidence.Failures, res.Confidence.Runs)
	case engine.OutcomeInconclusive:
		if len(res.Minimal) > 0 {
			fmt.Fprintf(w, "\nBest-known failing set:\n")
			for _, c := range res.Minimal {
				fmt.Fprintf(w, "  %s\n", c.String())
			}
		}
		fmt.Fprintf(w, "\nOutcome: %s\n%s\n", res.Outcome, res.OutcomeDetail)
	default:
		fmt.Fprintf(w, "\nOutcome: %s\n%s\n", res.Outcome, res.OutcomeDetail)
	}

	for _, d := range res.Diagnostics {
		fmt.Fprintf(w, "\nNote: %s\n", d)
	}

	if res.KeptWorktree != "" {
		fmt.Fprintf(w, "\nWorktree kept at: %s\n", res.KeptWorktree)
	}
	switch {
	case mdPath != "" && jsonPath != "":
		fmt.Fprintf(w, "\nReport: %s (JSON: %s)\n", mdPath, jsonPath)
	case mdPath != "":
		fmt.Fprintf(w, "\nReport: %s\n", mdPath)
	case jsonPath != "":
		fmt.Fprintf(w, "\nReport: %s\n", jsonPath)
	}
}
