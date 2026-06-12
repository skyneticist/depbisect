package verify

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyneticist/depbisect/internal/execx"
)

// seqRunner returns scripted exit codes in sequence.
type seqRunner struct {
	mu    sync.Mutex
	codes []int
	calls []execx.Cmd
}

func (s *seqRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if err := ctx.Err(); err != nil {
		return execx.Result{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	code := 0
	if len(s.codes) > 0 {
		code, s.codes = s.codes[0], s.codes[1:]
	}
	return execx.Result{ExitCode: code, Duration: time.Millisecond}, nil
}

func TestVerifyAlwaysFailing(t *testing.T) {
	r := &seqRunner{codes: []int{1, 1, 1}}
	h := Harness{Runner: r, Command: []string{"pnpm", "test"}, Runs: 3}
	v, err := h.Verify(context.Background(), "/dir")
	if err != nil {
		t.Fatal(err)
	}
	if v.Failures != 3 || len(v.Runs) != 3 {
		t.Errorf("verdict = %+v", v)
	}
	if v.Classification() != AlwaysFail {
		t.Errorf("classification = %v", v.Classification())
	}
	if got := v.String(); !strings.Contains(got, "3/3") {
		t.Errorf("String() = %q", got)
	}
	// Command argv must be preserved exactly.
	if r.calls[0].Name != "pnpm" || r.calls[0].Args[0] != "test" || r.calls[0].Dir != "/dir" {
		t.Errorf("cmd = %+v", r.calls[0])
	}
}

func TestVerifyAlwaysPassing(t *testing.T) {
	r := &seqRunner{codes: []int{0, 0}}
	h := Harness{Runner: r, Command: []string{"x"}, Runs: 2}
	v, err := h.Verify(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Failures != 0 || v.Classification() != AlwaysPass {
		t.Errorf("verdict = %+v cls %v", v, v.Classification())
	}
}

func TestVerifyMixedIsFlaky(t *testing.T) {
	r := &seqRunner{codes: []int{1, 0, 1}}
	h := Harness{Runner: r, Command: []string{"x"}, Runs: 3}
	v, err := h.Verify(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Classification() != Mixed {
		t.Errorf("classification = %v, want Mixed", v.Classification())
	}
}

func TestVerifyStopOnPass(t *testing.T) {
	r := &seqRunner{codes: []int{1, 0, 1, 1, 1}}
	h := Harness{Runner: r, Command: []string{"x"}, Runs: 5, StopOnPass: true}
	v, err := h.Verify(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Runs) != 2 {
		t.Errorf("executed %d runs, want 2 (stop after first pass)", len(v.Runs))
	}
	if v.Classification() != Mixed {
		t.Errorf("classification = %v", v.Classification())
	}
}

func TestVerifyDefaultsToOneRun(t *testing.T) {
	r := &seqRunner{codes: []int{1}}
	h := Harness{Runner: r, Command: []string{"x"}}
	v, err := h.Verify(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Runs) != 1 || v.Classification() != AlwaysFail {
		t.Errorf("verdict = %+v", v)
	}
}

func TestVerifyParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := Harness{Runner: &seqRunner{}, Command: []string{"x"}, Runs: 3}
	if _, err := h.Verify(ctx, ""); err == nil {
		t.Fatal("expected error when parent context is canceled")
	}
}

func TestVerifyPerRunTimeout(t *testing.T) {
	// A run that exceeds Timeout counts as a failure, not a fatal error.
	hang := func(ctx context.Context, c execx.Cmd) (execx.Result, error) {
		<-ctx.Done()
		return execx.Result{}, ctx.Err()
	}
	h := Harness{Runner: runnerFunc(hang), Command: []string{"x"}, Runs: 2, Timeout: 20 * time.Millisecond}
	v, err := h.Verify(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Failures != 2 {
		t.Errorf("Failures = %d, want 2", v.Failures)
	}
	for _, r := range v.Runs {
		if !r.TimedOut {
			t.Errorf("run %+v not marked TimedOut", r)
		}
	}
	if v.Classification() != AlwaysFail {
		t.Errorf("classification = %v", v.Classification())
	}
}

type runnerFunc func(context.Context, execx.Cmd) (execx.Result, error)

func (f runnerFunc) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) { return f(ctx, c) }

func TestVerdictStrings(t *testing.T) {
	v := Verdict{Planned: 3, Runs: []RunResult{{ExitCode: 1}, {ExitCode: 1}, {ExitCode: 1}}, Failures: 3}
	if got := v.String(); got != "failed 3/3 runs" {
		t.Errorf("String() = %q", got)
	}
	v = Verdict{Planned: 3, Runs: []RunResult{{ExitCode: 1}, {ExitCode: 0}}, Failures: 1}
	if got := v.String(); got != "failed 1/2 runs" {
		t.Errorf("String() = %q", got)
	}
}
