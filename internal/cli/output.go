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
	ansiGray   = "\x1b[90m"
)

// Glyphs used by the modern style. They render only when color is active,
// which in practice means a real terminal; classic output stays glyph-free.
const (
	glyphOK     = "✓"
	glyphFail   = "✗"
	glyphActive = "↻"
	glyphArrow  = "→"
)

// outputStyle selects how progress and the final summary are rendered.
type outputStyle int

const (
	styleClassic outputStyle = iota // label-column layout (the original output)
	styleModern                     // glyph lifecycle rows + dressed summary
)

// parseOutputStyle resolves a --style value. Callers handle the empty/default
// case before calling; an unrecognized value is a usage error.
func parseOutputStyle(name string) (outputStyle, error) {
	switch name {
	case "modern":
		return styleModern, nil
	case "classic":
		return styleClassic, nil
	default:
		return styleClassic, fmt.Errorf("unknown --style %q (valid: modern, classic)", name)
	}
}

// paint wraps s in an ANSI color and a reset.
func paint(code, s string) string { return code + s + ansiReset }

// progress prints phase updates to stderr. A TTY refreshes the active trial
// in place; redirected output stays line-oriented for CI logs. Verbose mode
// always preserves every lifecycle phase.
type progress struct {
	w           io.Writer
	verbose     bool
	interactive bool
	color       bool
	style       outputStyle
	activeTrial bool

	// modern lifecycle state for the collapsed ddmin row.
	ddminStart  time.Time
	ddminTested int
	barTick     int
}

func newProgress(w io.Writer, verbose bool, style outputStyle) *progress {
	interactive, color := terminalMode(w)
	return &progress{
		w:           w,
		verbose:     verbose,
		interactive: interactive,
		color:       color,
		style:       style,
	}
}

// modernActive reports whether the modern renderer should drive progress.
// It requires a colored, interactive terminal; verbose runs fall back to the
// classic line-oriented output so subprocess streams stay readable.
func (p *progress) modernActive() bool {
	return p.style == styleModern && p.interactive && p.color && !p.verbose
}

func (p *progress) Step(label, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.modernActive() {
		p.modernStep(label, msg)
		return
	}
	p.clearActiveTrial()
	writeStatus(p.w, label, msg, p.color, ansiCyan, true)
}

func (p *progress) Detail(format string, args ...any) {
	if !p.verbose {
		return
	}
	p.clearActiveTrial()
	fmt.Fprintf(p.w, "%*s %s\n", statusWidth, "", fmt.Sprintf(format, args...))
}

func (p *progress) Trial(number int, role string, applied, total int, phase string, elapsed time.Duration) {
	if p.modernActive() {
		p.modernTrial(role, total, phase)
		return
	}
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

// modernStep handles engine phase markers in the modern style. Most markers
// are implied by the glyph rows and stay silent; only the bisect completion
// finalizes the live ddmin row.
func (p *progress) modernStep(label, msg string) {
	switch label {
	case "Complete":
		p.clearActiveTrial()
		color := ansiGreen
		if strings.Contains(msg, "not proven") {
			color = ansiYellow
		}
		p.writeModernRow(glyphOK, color, "ddmin", paint(ansiGray, msg))
	case "Resume":
		p.clearActiveTrial()
		p.writeModernRow(glyphActive, ansiCyan, "resume", paint(ansiGray, msg))
	}
}

// modernTrial collapses the per-trial lifecycle into three rows: baseline,
// reproduced, and a single live ddmin row that refreshes in place.
func (p *progress) modernTrial(role string, total int, phase string) {
	switch role {
	case "baseline-old", "baseline-new":
		if isLivePhase(phase) {
			p.refreshBaselineWorking(role, phase)
			return
		}
		p.finalizeBaseline(role, total, phase)
	default: // candidate, minimality-check
		if p.ddminStart.IsZero() {
			p.ddminStart = time.Now()
		}
		if !isLivePhase(phase) {
			p.ddminTested++
		}
		p.refreshDdmin()
	}
}

func isLivePhase(phase string) bool {
	return phase == "preparing" || phase == "installing" || phase == "verifying"
}

func modernRoleLabel(role string) string {
	if role == "baseline-new" {
		return "reproduced"
	}
	return "baseline"
}

func (p *progress) refreshBaselineWorking(role, phase string) {
	p.clearActiveTrial()
	fmt.Fprintf(p.w, "%s %-11s %s",
		paint(ansiCyan, glyphActive), modernRoleLabel(role), paint(ansiGray, phase+"…"))
	p.activeTrial = true
}

func (p *progress) finalizeBaseline(role string, total int, phase string) {
	p.clearActiveTrial()
	verb := "reverted"
	if role == "baseline-new" {
		verb = "applied"
	}
	scope := fmt.Sprintf("%d updates %s", total, verb)
	glyph, color, verdict := glyphOK, ansiGreen, paint(ansiGreen, "tests pass")
	if phase != "pass" {
		glyph, color, verdict = glyphFail, ansiRed, paint(ansiRed, "tests fail")
	}
	msg := paint(ansiGray, scope) + " " + paint(ansiGray, glyphArrow) + " " + verdict
	if trialExpectation(role, phase) == "unexpected" {
		msg += " " + paint(ansiYellow, "(unexpected)")
	}
	p.writeModernRow(glyph, color, modernRoleLabel(role), msg)
}

func (p *progress) refreshDdmin() {
	p.clearActiveTrial()
	elapsed := formatDuration(time.Since(p.ddminStart))
	status := fmt.Sprintf("%s %d tested · %s",
		paint(ansiCyan, "isolating…"), p.ddminTested, paint(ansiGray, elapsed))
	bar := ""
	if terminalLineWidth(p.w) >= 60 {
		bar = p.indeterminateBar(12) + " "
	}
	fmt.Fprintf(p.w, "%s %-11s %s%s",
		paint(ansiCyan, glyphActive), "ddmin", bar, status)
	p.activeTrial = true
	p.barTick++
}

// indeterminateBar renders a moving "comet" since ddmin cannot know how many
// probes remain; it conveys activity, not a percentage.
func (p *progress) indeterminateBar(width int) string {
	const comet = 3
	pos := p.barTick % width
	var b strings.Builder
	for i := 0; i < width; i++ {
		lit := false
		for c := 0; c < comet; c++ {
			if (pos+c)%width == i {
				lit = true
			}
		}
		if lit {
			b.WriteString(paint(ansiCyan, "█"))
		} else {
			b.WriteString(paint(ansiGray, "░"))
		}
	}
	return b.String()
}

func (p *progress) writeModernRow(glyph, glyphColor, label, msg string) {
	fmt.Fprintf(p.w, "%s %-11s %s\n", paint(glyphColor, glyph), label, msg)
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

// printSummary writes the final human-readable result to stdout. The modern
// style renders only when color is active; otherwise (redirected output, CI,
// NO_COLOR) it falls back to the classic layout so logs stay plain and stable.
func printSummary(w io.Writer, res *engine.Result, mdPath, jsonPath string, style outputStyle) {
	_, color := terminalMode(w)
	if style == styleModern && color {
		printModernSummary(w, res, mdPath, jsonPath)
		return
	}
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

// printModernSummary renders the dressed report: a glyph headline, the
// colored culprit set, a rule, then a lowercase fact column.
func printModernSummary(w io.Writer, res *engine.Result, mdPath, jsonPath string) {
	glyph, glyphColor := summaryGlyph(res.Outcome)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s\n", paint(glyphColor, glyph), paint(ansiBold, outcomeHeadline(res.Outcome)))

	switch res.Outcome {
	case engine.OutcomeMinimalFound:
		writeModernChanges(w, "minimal set", res.Minimal)
	case engine.OutcomeInconclusive:
		if len(res.Minimal) > 0 {
			writeModernChanges(w, "best-known failing set", res.Minimal)
		}
	case engine.OutcomeDryRun:
		writeModernChanges(w, "dependency changes", res.Changes)
	}

	if res.OutcomeDetail != "" && res.Outcome != engine.OutcomeMinimalFound {
		fmt.Fprintf(w, "\n%s\n", paint(ansiGray, sentenceCase(res.OutcomeDetail)))
	}

	facts := make([][2]string, 0, 10)
	add := func(label, value string) { facts = append(facts, [2]string{label, value}) }
	if len(res.Command) > 0 {
		add("command", formatCommand(res.Command))
	}
	if manager := managerLabel(res); manager != "" {
		add("manager", manager)
	}
	if res.Outcome == engine.OutcomeMinimalFound {
		add("evidence", fmt.Sprintf("%d / %d failing runs · certified minimal",
			res.Confidence.Failures, res.Confidence.Runs))
	}
	add("changes", fmt.Sprintf("%d analyzed", len(res.Changes)))
	if len(res.Trials) > 0 {
		add("trials", strconv.Itoa(len(res.Trials)))
	}
	if duration := res.FinishedAt.Sub(res.StartedAt); !res.StartedAt.IsZero() && duration > 0 {
		add("duration", formatDuration(duration))
	}
	for _, d := range res.Diagnostics {
		add("note", paint(ansiYellow, d))
	}
	if res.KeptWorktree != "" {
		add("worktree", res.KeptWorktree)
	}
	if mdPath != "" {
		add("report", paint(ansiCyan, mdPath))
	}
	if jsonPath != "" {
		add("json", paint(ansiCyan, jsonPath))
	}
	add("outcome", res.Outcome)

	fmt.Fprintf(w, "\n%s\n", paint(ansiGray, strings.Repeat("─", 44)))
	for _, f := range facts {
		fmt.Fprintf(w, "  %s %s\n", paint(ansiGray, fmt.Sprintf("%-9s", f[0])), f[1])
	}
}

// writeModernChanges lists dependency changes with the name highlighted and the
// version arrow dimmed, matching the live ddmin culprit styling.
func writeModernChanges(w io.Writer, title string, changes []manifest.Change) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintf(w, "\n  %s\n", paint(ansiGray, title))
	for _, change := range changes {
		text := change.String()
		text = strings.Replace(text, "->", paint(ansiGray, glyphArrow), 1)
		text = strings.Replace(text, change.Name, paint(ansiRed, change.Name), 1)
		fmt.Fprintf(w, "    %s %s\n", paint(ansiRed, glyphFail), text)
	}
}

func summaryGlyph(outcome string) (glyph, color string) {
	switch outcome {
	case engine.OutcomeMinimalFound, engine.OutcomeNotReproduced:
		return glyphOK, ansiGreen
	case engine.OutcomeDryRun:
		return glyphArrow, ansiCyan
	default:
		return glyphActive, ansiYellow
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
