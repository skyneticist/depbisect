package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/manifest"
	"golang.org/x/term"
)

const (
	statusWidth = 12
	outputWidth = 100
	// modernLabelWidth is the label column for modern glyph rows. It is one
	// narrower than statusWidth because a modern row prefixes the label with a
	// status glyph and a space; deriving it from statusWidth keeps the two
	// output styles from silently drifting apart.
	modernLabelWidth = statusWidth - 1
	// modernGutter is the left indent shared by every modern summary body line
	// (changes, reason, rule, facts). It equals a progress row's glyph+space
	// prefix, so a fact's value column lands on the same column as a progress
	// row's message: gutter + label(modernLabelWidth) + space lines up with
	// glyph + space + label(modernLabelWidth) + space.
	modernGutter = 2
	// modernRuleWidth is the length of the divider drawn above the fact column.
	modernRuleWidth = 44
)

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiCyan    = "\x1b[1;36m"
	ansiCyanDim = "\x1b[36m" // non-bold cyan: the fading tail of the ddmin eye
	ansiGreen   = "\x1b[1;32m"
	ansiRed     = "\x1b[1;31m"
	ansiYellow  = "\x1b[1;33m"
	ansiGray    = "\x1b[90m"
)

// barFrameInterval is the frame period of the live ddmin animation (~10 fps).
// Frames redraw one short stderr line on an interactive terminal only.
const barFrameInterval = 100 * time.Millisecond

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
//
// Methods are safe for concurrent use: mu serializes every terminal write, so
// frames drawn by the animation goroutine (see startTicker) can never
// interleave with engine-driven progress rows.
type progress struct {
	w           io.Writer
	verbose     bool
	interactive bool
	color       bool
	style       outputStyle
	// frame is the live-animation frame period; tests shorten it.
	frame time.Duration
	// lineWidth reports the terminal width for live-row sizing; tests
	// override it to exercise narrow terminals (nil means terminalLineWidth).
	lineWidth func(io.Writer) int

	// mu guards every field below and all writes to w. The exported-style
	// entry points (Step, Detail, Trial, clearActiveTrial) acquire it; the
	// unexported helpers below them assume it is held.
	mu          sync.Mutex
	activeTrial bool

	// modern lifecycle state for the collapsed ddmin row.
	modernStarted bool
	ddminStart    time.Time
	ddminTested   int
	barTick       int
	// tickerStop, when non-nil, is the stop channel of the running animation
	// goroutine. Owned exclusively by startTicker/stopTicker.
	tickerStop chan struct{}
}

func newProgress(w io.Writer, verbose bool, style outputStyle) *progress {
	interactive, color := terminalMode(w)
	return &progress{
		w:           w,
		verbose:     verbose,
		interactive: interactive,
		color:       color,
		style:       style,
		frame:       barFrameInterval,
		lineWidth:   terminalLineWidth,
	}
}

// modernActive reports whether the modern renderer should drive progress.
// It requires a colored, interactive terminal; verbose runs fall back to the
// classic line-oriented output so subprocess streams stay readable.
func (p *progress) modernActive() bool {
	return p.style == styleModern && p.interactive && p.color && !p.verbose
}

func (p *progress) Step(label, format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	// In the modern style, use the glyph arrow so a redirected modern run keeps
	// the same → as its summary and changes, not a stray ASCII "->". Only the
	// space-delimited token is rewritten: it appears solely in the engine's own
	// message templates, while interpolated values (revisions, SHAs, package
	// names) can never contain spaces, so their content is left untouched.
	if p.style == styleModern {
		msg = strings.ReplaceAll(msg, " -> ", " "+glyphArrow+" ")
	}
	if p.modernActive() {
		p.modernStep(label, msg)
		return
	}
	p.interrupt()
	writeStatus(p.w, label, msg, p.color, ansiCyan, true)
}

func (p *progress) Detail(format string, args ...any) {
	if !p.verbose {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interrupt()
	fmt.Fprintf(p.w, "%*s %s\n", statusWidth, "", fmt.Sprintf(format, args...))
}

func (p *progress) Trial(number int, role string, applied, total int, phase string, elapsed time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
			p.interrupt()
			writeLiveStatus(p.w, fmt.Sprintf("Trial %d", number), message, p.color, ansiCyan)
			p.activeTrial = true
			return
		}
		if phase == "preparing" || p.verbose {
			writeStatus(p.w, fmt.Sprintf("Trial %d", number), message, p.color, ansiCyan, true)
		}
		return
	}

	p.interrupt()
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

// clearActiveTrial stops the live animation and erases the in-place trial
// line. It is the external interruption point — the CLI calls it before
// printing an error — and acquires the lock; internal writers use interrupt.
func (p *progress) clearActiveTrial() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interrupt()
}

// interrupt stops the frame ticker and clears the live line so an ordinary
// row can be printed. Caller holds p.mu.
func (p *progress) interrupt() {
	p.stopTicker()
	p.clearLine()
}

// clearLine erases the in-place live row, if any. Unlike interrupt it leaves
// a running ticker alone; frame redraws use it to repaint without stopping
// their own animation. Caller holds p.mu.
func (p *progress) clearLine() {
	if !p.interactive || !p.activeTrial {
		return
	}
	fmt.Fprint(p.w, "\r\x1b[2K")
	p.activeTrial = false
}

// startTicker begins the animation loop that sweeps the ddmin eye and keeps
// the elapsed time live between trial events. It is a no-op when already
// running or when the writer is not an interactive terminal (CI, redirected
// output, and tests never animate). Caller holds p.mu.
func (p *progress) startTicker() {
	if p.tickerStop != nil || !p.interactive {
		return
	}
	stop := make(chan struct{})
	p.tickerStop = stop
	interval := p.frame
	if interval <= 0 {
		// A zero-value progress (constructed without newProgress) still
		// animates at the standard rate rather than panicking NewTicker.
		interval = barFrameInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				p.mu.Lock()
				if p.tickerStop != stop {
					// Stopped between this tick firing and acquiring the
					// lock; the row now belongs to whoever stopped us.
					p.mu.Unlock()
					return
				}
				p.barTick++
				p.refreshDdmin()
				p.mu.Unlock()
			}
		}
	}()
}

// stopTicker halts the animation loop, if running. Caller holds p.mu; once
// the owning lock is released, no further frames will be drawn.
func (p *progress) stopTicker() {
	if p.tickerStop != nil {
		close(p.tickerStop)
		p.tickerStop = nil
	}
}

// modernBegin emits a single blank line before the first modern progress row so
// the live block is set apart from any preceding terminal output. It is a no-op
// after the first call.
func (p *progress) modernBegin() {
	if p.modernStarted {
		return
	}
	p.modernStarted = true
	fmt.Fprintln(p.w)
}

// modernStep handles engine phase markers in the modern style. Most markers
// are implied by the glyph rows and stay silent; only the bisect completion
// finalizes the live ddmin row.
func (p *progress) modernStep(label, msg string) {
	p.modernBegin()
	switch label {
	case "Complete":
		p.interrupt()
		color := ansiGreen
		if strings.Contains(msg, "not proven") {
			color = ansiYellow
		}
		p.writeModernRow(glyphOK, color, "ddmin", paint(ansiGray, msg))
	case "Resume":
		p.interrupt()
		p.writeModernRow(glyphActive, ansiCyan, "resume", paint(ansiGray, msg))
	}
}

// modernTrial collapses the per-trial lifecycle into three rows: baseline,
// reproduced, and a single live ddmin row that refreshes in place.
func (p *progress) modernTrial(role string, total int, phase string) {
	p.modernBegin()
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
			fmt.Fprintln(p.w) // set the ddmin search apart from the baseline pair
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
	p.interrupt()
	fmt.Fprintf(p.w, "%s %-*s %s",
		paint(ansiCyan, glyphActive), modernLabelWidth, modernRoleLabel(role), paint(ansiGray, phase+"…"))
	p.activeTrial = true
}

func (p *progress) finalizeBaseline(role string, total int, phase string) {
	p.interrupt()
	verb := "reverted"
	if role == "baseline-new" {
		verb = "applied"
	}
	// Pad the verb to the width of the longer one ("reverted") so the → arrow
	// lands in the same column when the baseline and reproduced rows stack.
	scope := fmt.Sprintf("%d updates %-8s", total, verb)
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

// refreshDdmin repaints the live ddmin row and ensures the animation ticker
// is running; the ticker advances barTick and calls back in for each frame.
// The row must never exceed the terminal width: a wrapped live line cannot be
// erased by \r + erase-line (only the last wrapped row would clear), and at
// ~10 fps the leaked rows would flood the screen. Trailing fields are dropped
// on narrow terminals instead. Caller holds p.mu.
func (p *progress) refreshDdmin() {
	p.clearLine()
	// Visible widths: glyph+label prefix ≈ 14; "isolating…" 10; " N tested"
	// ≤ ~11; " · <elapsed>" ≤ ~12; bar+space 13. Thresholds are those sums
	// with a little slack.
	widthFn := p.lineWidth
	if widthFn == nil {
		widthFn = terminalLineWidth
	}
	width := widthFn(p.w)
	status := paint(ansiCyan, "isolating…")
	switch {
	case width >= 50:
		status = fmt.Sprintf("%s %d tested · %s",
			paint(ansiCyan, "isolating…"), p.ddminTested, paint(ansiGray, formatDuration(time.Since(p.ddminStart))))
	case width >= 38:
		status = fmt.Sprintf("%s %d tested", paint(ansiCyan, "isolating…"), p.ddminTested)
	}
	bar := ""
	if width >= 60 {
		bar = p.indeterminateBar(12) + " "
	}
	fmt.Fprintf(p.w, "%s %-*s %s%s",
		paint(ansiCyan, glyphActive), modernLabelWidth, "ddmin", bar, status)
	p.activeTrial = true
	p.startTicker()
}

// indeterminateBar renders a "cylon eye": a bright head sweeping back and
// forth with a two-cell tail fading behind it. It conveys activity, not a
// percentage — ddmin cannot know how many probes remain. Motion comes from
// the frame ticker advancing barTick; the tail cells are simply the head's
// two previous positions, which keeps the fade correct through edge bounces
// (where the head masks its own trail).
func (p *progress) indeterminateBar(width int) string {
	head := eyePos(p.barTick, width)
	trail1, trail2 := -1, -1
	if p.barTick >= 1 {
		trail1 = eyePos(p.barTick-1, width)
	}
	if p.barTick >= 2 {
		trail2 = eyePos(p.barTick-2, width)
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		switch i {
		case head:
			b.WriteString(paint(ansiCyan, "█"))
		case trail1:
			b.WriteString(paint(ansiCyanDim, "▓"))
		case trail2:
			b.WriteString(paint(ansiCyanDim, "▒"))
		default:
			b.WriteString(paint(ansiGray, "░"))
		}
	}
	return b.String()
}

// eyePos maps a tick to the eye's cell via a triangle wave — 0,1,…,w-1,w-2,
// …,1 and repeat — so the eye bounces at the edges instead of wrapping.
// Negative ticks (the trail's lookback before the first frames) normalize
// into the same cycle.
func eyePos(tick, width int) int {
	if width <= 1 {
		return 0
	}
	period := 2 * (width - 1)
	pos := ((tick % period) + period) % period
	if pos >= width {
		pos = period - pos
	}
	return pos
}

func (p *progress) writeModernRow(glyph, glyphColor, label, msg string) {
	fmt.Fprintf(p.w, "%s %-*s %s\n", paint(glyphColor, glyph), modernLabelWidth, label, msg)
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
		fmt.Fprintf(w, "\n%*s%s\n", modernGutter, "", paint(ansiGray, sentenceCase(res.OutcomeDetail)))
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

	fmt.Fprintf(w, "\n%*s%s\n", modernGutter, "", paint(ansiGray, strings.Repeat("─", modernRuleWidth)))
	for _, f := range facts {
		fmt.Fprintf(w, "%*s%s %s\n", modernGutter, "", paint(ansiGray, fmt.Sprintf("%-*s", modernLabelWidth, f[0])), f[1])
	}
}

// writeModernChanges lists dependency changes with the path prefix dimmed and
// the leaf name highlighted, with version columns aligned across all entries.
func writeModernChanges(w io.Writer, title string, changes []manifest.Change) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%*s%s\n", modernGutter, "", paint(ansiGray, title))

	maxLen := 0
	for _, c := range changes {
		if n := utf8.RuneCountInString(c.Name); n > maxLen {
			maxLen = n
		}
	}

	for _, change := range changes {
		prefix, leaf := splitPackageName(change.Name)
		pad := strings.Repeat(" ", maxLen-utf8.RuneCountInString(change.Name)+2)
		// changeVersionStr is the only source of the space-delimited " -> "
		// token; no version-spec syntax produces it (range specs may contain
		// spaces, but never "->"), so spec content cannot be rewritten.
		ver := changeVersionStr(change)
		ver = strings.Replace(ver, " -> ", " "+paint(ansiGray, glyphArrow)+" ", 1)
		namePart := paint(ansiGray, prefix) + paint(ansiRed, leaf)
		fmt.Fprintf(w, "%*s%s %s%s%s\n", modernGutter*2, "", paint(ansiRed, glyphFail), namePart, pad, paint(ansiGray, ver))
	}
}

// splitPackageName splits a package name into a dimmed path prefix and a
// highlighted leaf. Only names whose first path segment looks like a host
// (contains a dot, e.g. github.com/org/pkg) are split; npm scoped packages
// (@scope/pkg) and plain names are returned as leaf only.
func splitPackageName(name string) (prefix, leaf string) {
	slash := strings.Index(name, "/")
	if slash < 0 || !strings.Contains(name[:slash], ".") {
		return "", name
	}
	i := strings.LastIndex(name, "/")
	return name[:i+1], name[i+1:]
}

// changeVersionStr returns the version portion of a change for terminal
// display, mirroring the logic in Change.String() but without the name.
func changeVersionStr(c manifest.Change) string {
	oldV, newV := c.OldSpec, c.NewSpec
	if c.OldResolved != "" && c.NewResolved != "" {
		oldV, newV = c.OldResolved, c.NewResolved
	}
	suffix := ""
	if c.Section != manifest.Dependencies {
		suffix = " (" + string(c.Section) + ")"
	}
	switch c.Kind {
	case manifest.Added:
		if c.NewResolved != "" {
			newV = c.NewResolved
		}
		return newV + " (added)" + suffix
	case manifest.Removed:
		if c.OldResolved != "" {
			oldV = c.OldResolved
		}
		return oldV + " (removed)" + suffix
	default:
		if oldV == newV {
			// Same resolved version on both sides (a content-only or spec-only
			// change, e.g. a local file: dependency): "1.0.0 -> 1.0.0" reads as
			// "nothing changed", so mark it instead.
			return newV + " (rebuilt)" + suffix
		}
		return oldV + " -> " + newV + suffix
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

// wrapWords greedily wraps text at word boundaries. Width is measured in
// runes, not bytes, so multi-byte characters (like the → glyph) do not cause
// premature wrapping.
func wrapWords(text string, width int) []string {
	if width < 1 || utf8.RuneCountInString(text) <= width {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, len(words))
	line := words[0]
	lineLen := utf8.RuneCountInString(line)
	for _, word := range words[1:] {
		wordLen := utf8.RuneCountInString(word)
		if lineLen+1+wordLen <= width {
			line += " " + word
			lineLen += 1 + wordLen
			continue
		}
		lines = append(lines, line)
		line, lineLen = word, wordLen
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
