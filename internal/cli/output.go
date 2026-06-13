package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
	"golang.org/x/term"
)

const (
	statusWidth = 12
	outputWidth = 100
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[1;36m"
	ansiGreen  = "\x1b[1;32m"
	ansiRed    = "\x1b[1;31m"
	ansiYellow = "\x1b[1;33m"
)

// progress prints phase updates to stderr. A TTY refreshes the active trial
// in place; redirected output stays line-oriented for CI logs. Verbose mode
// always preserves every lifecycle phase.
type progress struct {
	w           io.Writer
	verbose     bool
	interactive bool
	color       bool
	activeTrial bool
}

func newProgress(w io.Writer, verbose bool) *progress {
	interactive, color := terminalMode(w)
	return &progress{
		w:           w,
		verbose:     verbose,
		interactive: interactive,
		color:       color,
	}
}

func (p *progress) Step(label, format string, args ...any) {
	p.clearActiveTrial()
	writeStatus(p.w, label, fmt.Sprintf(format, args...), p.color, ansiCyan, true)
}

func (p *progress) Detail(format string, args ...any) {
	if !p.verbose {
		return
	}
	p.clearActiveTrial()
	fmt.Fprintf(p.w, "%*s %s\n", statusWidth, "", fmt.Sprintf(format, args...))
}

func (p *progress) Trial(number int, role string, applied, total int, phase string, elapsed time.Duration) {
	roleLabel := trialRoleLabel(role)
	scope := fmt.Sprintf("%s | %d/%d changes", roleLabel, applied, total)

	if phase == "preparing" || phase == "installing" || phase == "verifying" {
		message := scope + " | " + phase
		if rounded := elapsed.Round(100 * time.Millisecond); rounded > 0 {
			message += " | " + rounded.String() + " elapsed"
		}
		if p.interactive && !p.verbose {
			p.clearActiveTrial()
			writeLiveStatus(p.w, fmt.Sprintf("Trial %d", number), message, p.color, ansiCyan)
			p.activeTrial = true
			return
		}
		if phase == "preparing" || p.verbose {
			writeStatus(p.w, fmt.Sprintf("Trial %d", number), message, p.color, ansiCyan, true)
		}
		return
	}

	p.clearActiveTrial()
	expectation := trialExpectation(role, phase)
	status := strings.ToUpper(phase)
	statusColor := outcomeColor(phase)
	if expectation != "" {
		if expectation == "expected" {
			status = "EXPECTED"
			statusColor = ansiGreen
		} else {
			status = "UNEXPECTED"
			statusColor = ansiYellow
		}
		scope += " | " + strings.ToUpper(phase)
	}
	writeStatus(p.w, status,
		fmt.Sprintf("trial %d | %s | %s", number, scope, formatDuration(elapsed)),
		p.color, statusColor, true)
}

func (p *progress) clearActiveTrial() {
	if !p.interactive || !p.activeTrial {
		return
	}
	fmt.Fprint(p.w, "\r\x1b[2K")
	p.activeTrial = false
}

func outcomeColor(outcome string) string {
	switch outcome {
	case "pass":
		return ansiGreen
	case "fail":
		return ansiRed
	case "unresolved":
		return ansiYellow
	default:
		return ansiCyan
	}
}

func formatDuration(elapsed time.Duration) string {
	return elapsed.Round(100 * time.Millisecond).String()
}

func trialRoleLabel(role string) string {
	switch role {
	case "baseline-old":
		return "baseline without updates"
	case "baseline-new":
		return "baseline with all updates"
	case "minimality-check":
		return "minimality check"
	default:
		return role
	}
}

func trialExpectation(role, outcome string) string {
	var expected string
	switch role {
	case "baseline-old":
		expected = "pass"
	case "baseline-new":
		expected = "fail"
	default:
		return ""
	}
	if outcome == expected {
		return "expected"
	}
	return "unexpected"
}

// printSummary writes the final human-readable result to stdout.
func printSummary(w io.Writer, res *engine.Result, mdPath, jsonPath string) {
	_, color := terminalMode(w)
	fmt.Fprintln(w)
	writeStatus(w, "Result", outcomeHeadline(res.Outcome), color, resultColor(res.Outcome), true)

	switch res.Outcome {
	case engine.OutcomeMinimalFound:
		writeChangeSection(w, "Breaking dependencies", res.Minimal, color)
	case engine.OutcomeInconclusive:
		if len(res.Minimal) > 0 {
			writeChangeSection(w, "Best-known failing set", res.Minimal, color)
		}
	case engine.OutcomeDryRun:
		writeChangeSection(w, "Dependency changes", res.Changes, color)
	}

	if res.OutcomeDetail != "" && res.Outcome != engine.OutcomeMinimalFound {
		fmt.Fprintln(w)
		writeStatus(w, "Reason", sentenceCase(res.OutcomeDetail), color, reasonColor(res.Outcome), true)
	}

	fmt.Fprintln(w)
	if len(res.Command) > 0 {
		writeStatus(w, "Command", formatCommand(res.Command), color, ansiCyan, false)
		fmt.Fprintln(w)
	}
	if manager := managerLabel(res); manager != "" {
		writeStatus(w, "Manager", manager, color, ansiCyan, true)
	}
	if res.Outcome == engine.OutcomeMinimalFound {
		writeStatus(w, "Evidence",
			fmt.Sprintf("%d/%d failing runs", res.Confidence.Failures, res.Confidence.Runs),
			color, ansiGreen, true)
	}
	writeStatus(w, "Changes", fmt.Sprintf("%d analyzed", len(res.Changes)), color, ansiCyan, true)
	if len(res.Trials) > 0 {
		writeStatus(w, "Trials", strconv.Itoa(len(res.Trials)), color, ansiCyan, true)
	}
	if duration := res.FinishedAt.Sub(res.StartedAt); !res.StartedAt.IsZero() && duration > 0 {
		writeStatus(w, "Duration", formatDuration(duration), color, ansiCyan, true)
	}
	writeStatus(w, "Outcome", res.Outcome, color, ansiCyan, true)

	for _, d := range res.Diagnostics {
		writeStatus(w, "Note", d, color, ansiYellow, true)
	}

	if res.KeptWorktree != "" {
		writeStatus(w, "Worktree", res.KeptWorktree, color, ansiCyan, true)
	}
	switch {
	case mdPath != "" && jsonPath != "":
		writeStatus(w, "Report", mdPath, color, ansiCyan, true)
		writeStatus(w, "JSON", jsonPath, color, ansiCyan, true)
	case mdPath != "":
		writeStatus(w, "Report", mdPath, color, ansiCyan, true)
	case jsonPath != "":
		writeStatus(w, "JSON", jsonPath, color, ansiCyan, true)
	}
}

func writeChangeSection(w io.Writer, title string, changes []manifest.Change, color bool) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(w)
	if color {
		fmt.Fprintf(w, "%s%s%s\n", ansiBold, title, ansiReset)
	} else {
		fmt.Fprintln(w, title)
	}
	for _, change := range changes {
		fmt.Fprintf(w, "  - %s\n", change.String())
	}
}

func writeStatus(w io.Writer, label, message string, color bool, colorCode string, newline bool) {
	padded := fmt.Sprintf("%*s", statusWidth, label)
	if color {
		padded = colorCode + padded + ansiReset
	}
	if !newline {
		fmt.Fprintf(w, "%s %s", padded, message)
		return
	}

	lines := wrapWords(message, terminalLineWidth(w)-statusWidth-1)
	fmt.Fprintf(w, "%s %s\n", padded, lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(w, "%*s %s\n", statusWidth, "", line)
	}
}

func writeLiveStatus(w io.Writer, label, message string, color bool, colorCode string) {
	writeLiveStatusWidth(w, label, message, color, colorCode, terminalLineWidth(w))
}

func writeLiveStatusWidth(w io.Writer, label, message string, color bool, colorCode string, lineWidth int) {
	if lineWidth <= statusWidth+1 {
		label = truncateText(strings.TrimSpace(label), lineWidth)
		if color {
			label = colorCode + label + ansiReset
		}
		fmt.Fprint(w, label)
		return
	}
	messageWidth := lineWidth - statusWidth - 1
	writeStatus(w, label, truncateText(message, messageWidth), color, colorCode, false)
}

func terminalLineWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return outputWidth
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return outputWidth
	}
	return width
}

func terminalMode(w io.Writer) (interactive, color bool) {
	if f, ok := w.(*os.File); ok {
		interactive = term.IsTerminal(int(f.Fd()))
	}
	if os.Getenv("TERM") == "dumb" {
		return false, false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" {
		return interactive, false
	}
	forced := os.Getenv("CLICOLOR_FORCE")
	if forced != "" && forced != "0" {
		return interactive, true
	}
	return interactive, interactive
}

func resultColor(outcome string) string {
	switch outcome {
	case engine.OutcomeMinimalFound, engine.OutcomeNotReproduced:
		return ansiGreen
	case engine.OutcomeFailsAtBase, engine.OutcomeInconclusive, engine.OutcomeNoChanges:
		return ansiYellow
	default:
		return ansiCyan
	}
}

func reasonColor(outcome string) string {
	switch outcome {
	case engine.OutcomeFailsAtBase, engine.OutcomeInconclusive:
		return ansiYellow
	default:
		return ansiCyan
	}
}

func managerLabel(res *engine.Result) string {
	if res.PackageManagerVersion != "" {
		return res.PackageManagerVersion
	}
	return res.PackageManager
}

func formatCommand(command []string) string {
	formatted := make([]string, len(command))
	for i, arg := range command {
		if commandArgNeedsQuote(arg) {
			formatted[i] = strconv.Quote(arg)
		} else {
			formatted[i] = arg
		}
	}
	return strings.Join(formatted, " ")
}

func truncateText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	return string(runes[:width-3]) + "..."
}

func commandArgNeedsQuote(arg string) bool {
	if arg == "" {
		return true
	}
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("-._/:@%+=,", r):
		default:
			return true
		}
	}
	return false
}

func wrapWords(text string, width int) []string {
	if width < 1 || len(text) <= width {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, len(words))
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) <= width {
			line += " " + word
			continue
		}
		lines = append(lines, line)
		line = word
	}
	return append(lines, line)
}

func outcomeHeadline(outcome string) string {
	switch outcome {
	case engine.OutcomeMinimalFound:
		return "Minimal breaking dependency set found"
	case engine.OutcomeNotReproduced:
		return "No breaking dependency update reproduced the failure"
	case engine.OutcomeFailsAtBase:
		return "Failure exists without dependency updates"
	case engine.OutcomeInconclusive:
		return "Inconclusive"
	case engine.OutcomeNoChanges:
		return "No dependency changes to bisect"
	case engine.OutcomeDryRun:
		return "Dry run complete"
	default:
		return outcome
	}
}

func sentenceCase(text string) string {
	if text == "" || text[0] < 'a' || text[0] > 'z' {
		return text
	}
	return string(text[0]-'a'+'A') + text[1:]
}
