// Package verify runs a user-supplied verification command repeatedly and
// classifies the outcome, so flaky commands can be told apart from
// deterministic failures.
package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/skyneticist/depbisect/internal/execx"
)

// failureTailLimit caps the output evidence retained per failing run. Real
// failure messages live at the end of output; anything past a couple of
// kilobytes is context the report files can carry, not the terminal.
const failureTailLimit = 2048

// RunResult records one execution of the verification command.
type RunResult struct {
	ExitCode int
	Duration time.Duration
	TimedOut bool
	// OutputTail is the end of a failing run's output — stdout then stderr,
	// since evidence location differs by runner (Rust's libtest reports
	// failures on stdout while cargo's own trailer is stderr; node asserts
	// on stderr) — capped at failureTailLimit bytes. Passing runs carry
	// none; it exists purely as failure evidence.
	OutputTail string
}

// Failed reports whether this run counts as a failure.
func (r RunResult) Failed() bool { return r.ExitCode != 0 || r.TimedOut }

// Classification summarizes repeated runs.
type Classification int

const (
	// AlwaysPass: every executed run passed.
	AlwaysPass Classification = iota
	// AlwaysFail: every executed run failed.
	AlwaysFail
	// Mixed: some runs passed and some failed — the command is flaky
	// under the tested configuration.
	Mixed
)

// String returns a stable lower-case name.
func (c Classification) String() string {
	switch c {
	case AlwaysPass:
		return "always-pass"
	case AlwaysFail:
		return "always-fail"
	case Mixed:
		return "mixed"
	default:
		return fmt.Sprintf("classification(%d)", int(c))
	}
}

// Verdict is the outcome of verifying one candidate configuration.
type Verdict struct {
	// Planned is the number of runs requested.
	Planned int
	// Runs holds the executed runs; it may be shorter than Planned when
	// StopOnPass ended verification early.
	Runs []RunResult
	// Failures counts failed runs.
	Failures int
}

// Classification derives the flakiness class from the executed runs.
func (v Verdict) Classification() Classification {
	switch {
	case v.Failures == 0:
		return AlwaysPass
	case v.Failures == len(v.Runs):
		return AlwaysFail
	default:
		return Mixed
	}
}

// String renders e.g. "failed 3/3 runs". When StopOnPass ended verification
// before every planned run executed, it says so — "failed 0/1 runs (stopped
// at first pass; 3 planned)" — so a short denominator is not mistaken for a
// full flakiness sample.
func (v Verdict) String() string {
	s := fmt.Sprintf("failed %d/%d runs", v.Failures, len(v.Runs))
	if v.Planned > len(v.Runs) {
		s += fmt.Sprintf(" (stopped at first pass; %d planned)", v.Planned)
	}
	return s
}

// FailureTail returns the captured output tail of the most recent failing
// run, or "" when no failing run produced output. The last failure is chosen
// because a repeated failure's final occurrence is the freshest evidence.
func (v Verdict) FailureTail() string {
	for i := len(v.Runs) - 1; i >= 0; i-- {
		if v.Runs[i].Failed() && v.Runs[i].OutputTail != "" {
			return v.Runs[i].OutputTail
		}
	}
	return ""
}

// failureTail joins and caps the evidence of a failed run: stdout first,
// stderr last. Runners disagree about where failures go — Rust's libtest
// prints the panic on stdout while cargo appends its own trailer to stderr;
// node asserts on stderr — so keeping both (tail-capped) lets the renderer's
// boilerplate filter surface whichever half carries the real error.
func failureTail(res execx.Result) string {
	parts := make([]string, 0, 2)
	if _, ok := execx.FirstLine(res.Stdout); ok {
		parts = append(parts, strings.TrimRight(string(res.Stdout), "\n"))
	}
	if _, ok := execx.FirstLine(res.Stderr); ok {
		parts = append(parts, strings.TrimRight(string(res.Stderr), "\n"))
	}
	s := strings.Join(parts, "\n")
	if len(s) > failureTailLimit {
		s = s[len(s)-failureTailLimit:]
	}
	return s
}

// AllRunsTimedOut reports whether every executed run hit the per-run timeout.
// The engine uses it to explain a baseline that fails only because the command
// never finished in time, rather than because a dependency broke it.
func (v Verdict) AllRunsTimedOut() bool {
	if len(v.Runs) == 0 {
		return false
	}
	for _, r := range v.Runs {
		if !r.TimedOut {
			return false
		}
	}
	return true
}

// Harness executes the verification command.
type Harness struct {
	Runner execx.Runner
	// Command is the exact argument vector; Command[0] is the program.
	// No shell is involved.
	Command []string
	// Runs is how many times to execute the command (default 1).
	Runs int
	// Timeout bounds each individual run (0 = unbounded). A timed-out
	// run counts as a failure.
	Timeout time.Duration
	// StopOnPass ends verification at the first passing run. Useful
	// during bisection, where a single pass already refutes
	// "fails in every run".
	StopOnPass bool
	// Stream, when non-nil, receives live command output.
	Stream io.Writer
	// VenvDir, when non-empty, is a worktree-relative Python virtual
	// environment that the install step creates in each trial (pip). Verify
	// then resolves Command[0] in the venv's executable directory before
	// falling back to PATH, and exports VIRTUAL_ENV plus a PATH with that
	// directory prepended, so plain commands like "pytest" or
	// "python check.py" run inside the trial's environment.
	VenvDir string
}

// Verify runs the command in dir. It returns an error only for fatal
// conditions (parent cancellation, unrunnable command); ordinary command
// failures and per-run timeouts are part of the Verdict.
func (h Harness) Verify(ctx context.Context, dir string) (Verdict, error) {
	runs := h.Runs
	if runs < 1 {
		runs = 1
	}
	if len(h.Command) == 0 {
		return Verdict{}, errors.New("verify: empty command")
	}
	program := h.Command[0]
	var extraEnv []string
	if h.VenvDir != "" {
		program, extraEnv = venvCommand(dir, h.VenvDir, program)
	}
	v := Verdict{Planned: runs}
	for i := 0; i < runs; i++ {
		if err := ctx.Err(); err != nil {
			return v, fmt.Errorf("verify: %w", err)
		}
		runCtx := ctx
		cancel := func() {}
		if h.Timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, h.Timeout)
		}
		res, err := h.Runner.Run(runCtx, execx.Cmd{
			Dir:      dir,
			Name:     program,
			Args:     h.Command[1:],
			ExtraEnv: extraEnv,
			Stream:   h.Stream,
		})
		cancel()
		rr := RunResult{ExitCode: res.ExitCode, Duration: res.Duration}
		if err != nil {
			if ctx.Err() != nil {
				return v, fmt.Errorf("verify: %w", ctx.Err())
			}
			if runCtx.Err() != nil {
				rr.TimedOut = true
				rr.ExitCode = -1
			} else {
				return v, fmt.Errorf("verify: %w", err)
			}
		}
		if rr.Failed() {
			rr.OutputTail = failureTail(res)
		}
		v.Runs = append(v.Runs, rr)
		if rr.Failed() {
			v.Failures++
		} else if h.StopOnPass {
			break
		}
	}
	return v, nil
}

// venvCommand resolves program against the virtual environment venv (relative
// to dir) and builds the environment overrides that activate the venv for the
// command and its subprocesses. A bare program name is replaced with its
// absolute path inside the venv when one exists — the OS resolves a bare name
// against the parent process's PATH, so exporting PATH alone could never
// redirect the command itself into the venv. Programs given with a path
// separator, or absent from the venv (e.g. "make"), are left for the normal
// PATH lookup and only the environment is exported.
func venvCommand(dir, venv, program string) (string, []string) {
	root := filepath.Join(dir, venv)
	bin := filepath.Join(root, venvBinDir())
	env := []string{
		"VIRTUAL_ENV=" + root,
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	if strings.ContainsAny(program, `/\`) {
		return program, env
	}
	candidates := []string{filepath.Join(bin, program)}
	// Windows venvs install console scripts as .exe; accept a bare "python"
	// or "pytest" the same way PATH lookup would.
	if runtime.GOOS == "windows" && filepath.Ext(program) == "" {
		candidates = append(candidates, filepath.Join(bin, program+".exe"))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, env
		}
	}
	return program, env
}

// venvBinDir returns a venv's executable directory name, which differs
// between the POSIX (bin) and Windows (Scripts) layouts.
func venvBinDir() string {
	if runtime.GOOS == "windows" {
		return "Scripts"
	}
	return "bin"
}
