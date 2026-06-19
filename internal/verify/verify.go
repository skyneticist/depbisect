// Package verify runs a user-supplied verification command repeatedly and
// classifies the outcome, so flaky commands can be told apart from
// deterministic failures.
package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/skyneticist/depbisect/internal/execx"
)

// RunResult records one execution of the verification command.
type RunResult struct {
	ExitCode int
	Duration time.Duration
	TimedOut bool
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

// String renders e.g. "failed 3/3 runs".
func (v Verdict) String() string {
	return fmt.Sprintf("failed %d/%d runs", v.Failures, len(v.Runs))
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
			Dir:    dir,
			Name:   h.Command[0],
			Args:   h.Command[1:],
			Stream: h.Stream,
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
		v.Runs = append(v.Runs, rr)
		if rr.Failed() {
			v.Failures++
		} else if h.StopOnPass {
			break
		}
	}
	return v, nil
}
