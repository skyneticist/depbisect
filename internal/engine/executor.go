package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/skyneticist/depbisect/internal/ddmin"
	"github.com/skyneticist/depbisect/internal/manifest"
	"github.com/skyneticist/depbisect/internal/verify"
)

// executor evaluates candidate subsets against a pool of isolated worktrees.
// Each subset is materialized into one free lane, installed, and verified;
// results are memoized so a subset is never evaluated twice. With jobs > 1 the
// pool runs that work concurrently across lanes, but the algorithmic outcome is
// identical to sequential evaluation because batch selection always adopts the
// lowest-indexed failing subset (see ddmin.BatchTest).
type executor struct {
	git       GitClient
	installer Installer
	verifier  Verifier
	progress  Progress
	now       func() time.Time

	toSHA        string
	toManifest   manifest.Parsed
	eco          manifest.Ecosystem
	manifestName string
	changes      []manifest.Change
	totalChanges int

	stream         io.Writer
	installTimeout time.Duration

	jobs  int
	lanes chan int // buffered with the lane indices that are currently free
	dirs  []string // lane index -> worktree directory

	// mu guards every field below: the shared bookkeeping that concurrent
	// lane evaluations mutate.
	mu       sync.Mutex
	memo     map[string]Trial
	res      *Result
	trialNum int
	store    CheckpointStore
	storeOn  bool
}

// eval evaluates one subset, returning its memoized Trial. It is safe to call
// from multiple goroutines: distinct subsets run on distinct lanes, and the
// shared memo/result/checkpoint state is mutated only under mu.
func (ex *executor) eval(ctx context.Context, subset []manifest.Change, role string, stopOnPass bool) (Trial, error) {
	key := subsetKey(subset)
	ex.mu.Lock()
	if cached, ok := ex.memo[key]; ok {
		ex.mu.Unlock()
		return cached, nil
	}
	ex.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Trial{}, fmt.Errorf("bisection interrupted: %w", err)
	}

	// Acquire a worktree lane, blocking until one is free.
	var lane int
	select {
	case lane = <-ex.lanes:
	case <-ctx.Done():
		return Trial{}, fmt.Errorf("bisection interrupted: %w", ctx.Err())
	}
	defer func() { ex.lanes <- lane }()
	dir := ex.dirs[lane]

	ex.mu.Lock()
	ex.trialNum++
	num := ex.trialNum
	ex.mu.Unlock()

	ex.progress.Trial(num, role, len(subset), ex.totalChanges, "preparing", 0)
	start := ex.now()
	if err := ex.git.ResetWorktree(ctx, dir, ex.toSHA); err != nil {
		return Trial{}, fmt.Errorf("prepare isolated trial worktree: %w", err)
	}

	applied := make(map[string]bool, len(subset))
	trial := Trial{Role: role, Applied: make([]string, 0, len(subset))}
	for _, c := range subset {
		applied[c.ID()] = true
		trial.Applied = append(trial.Applied, c.ID())
	}
	sort.Strings(trial.Applied)

	rendered, err := ex.eco.Render(ex.toManifest, ex.changes, applied)
	if err != nil {
		return Trial{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ex.manifestName), rendered, 0o644); err != nil {
		return Trial{}, fmt.Errorf("write candidate manifest: %w", err)
	}
	afterPrepare := ex.now()
	trial.PrepareDuration = afterPrepare.Sub(start)
	ex.progress.Trial(num, role, len(subset), ex.totalChanges, "installing", trial.PrepareDuration)

	installCtx := ctx
	cancelInstall := func() {}
	if ex.installTimeout > 0 {
		installCtx, cancelInstall = context.WithTimeout(ctx, ex.installTimeout)
	}
	instRes, err := ex.installer.Install(installCtx, dir, ex.stream)
	installContextErr := installCtx.Err()
	cancelInstall()
	afterInstall := ex.now()
	trial.InstallDuration = afterInstall.Sub(afterPrepare)
	if err != nil {
		if ex.installTimeout > 0 &&
			errors.Is(installContextErr, context.DeadlineExceeded) &&
			!errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Trial{}, fmt.Errorf("dependency installation timed out after %s: %w",
				ex.installTimeout, context.DeadlineExceeded)
		}
		return Trial{}, fmt.Errorf("install dependencies: %w", err)
	}
	if instRes.ExitCode != 0 {
		trial.Outcome = "unresolved"
		trial.Duration = afterInstall.Sub(start)
		ex.progress.Trial(num, role, len(subset), ex.totalChanges, trial.Outcome, trial.Duration)
		ex.progress.Detail("Install failed for %d applied changes (%s); candidate skipped",
			len(subset), firstLine(instRes.Stderr))
		return ex.record(key, trial)
	}
	ex.progress.Trial(num, role, len(subset), ex.totalChanges, "verifying", afterInstall.Sub(start))
	verdict, err := ex.verifier.Verify(ctx, dir, stopOnPass)
	if err != nil {
		return Trial{}, fmt.Errorf("run verification command: %w", err)
	}
	afterVerify := ex.now()
	trial.RunsExecuted = len(verdict.Runs)
	trial.Failures = verdict.Failures
	trial.VerifyDuration = afterVerify.Sub(afterInstall)
	trial.Duration = afterVerify.Sub(start)
	switch verdict.Classification() {
	case verify.AlwaysFail:
		trial.Outcome = "fail"
	case verify.AlwaysPass:
		trial.Outcome = "pass"
	default:
		trial.Outcome = "pass" // flaky: does not deterministically reproduce
	}
	ex.progress.Trial(num, role, len(subset), ex.totalChanges, trial.Outcome, trial.Duration)
	ex.progress.Detail("Tested %d applied changes: %s (%s)", len(subset), trial.Outcome, verdict.String())
	return ex.record(key, trial)
}

// record persists a completed trial into the memo, the result, and the
// checkpoint under mu. If the subset was concurrently recorded by another lane
// it returns the existing trial and discards this one; in practice the caller
// structure guarantees no two in-flight evaluations share a key, so this is a
// safety net rather than an expected path.
func (ex *executor) record(key string, trial Trial) (Trial, error) {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if existing, ok := ex.memo[key]; ok {
		return existing, nil
	}
	ex.memo[key] = trial
	ex.res.Trials = append(ex.res.Trials, trial)
	if ex.storeOn {
		if err := ex.store.Append(trial); err != nil {
			return Trial{}, fmt.Errorf("save checkpoint trial: %w", err)
		}
	}
	return trial, nil
}

// lookup returns the memoized trial for a subset, or the zero Trial if it has
// not been evaluated.
func (ex *executor) lookup(subset []manifest.Change) Trial {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return ex.memo[subsetKey(subset)]
}

// batch implements ddmin.BatchTest over the worktree pool. With jobs <= 1 (or a
// trivial batch) it evaluates subsets in order and stops at the first failure,
// reproducing classic ddmin exactly. With jobs > 1 it evaluates the batch
// concurrently across lanes and returns the lowest-indexed failure, so the
// minimized result is identical regardless of scheduling.
func (ex *executor) batch(ctx context.Context, subsets [][]manifest.Change, role string, stopOnPass bool) (int, int, error) {
	if ex.jobs <= 1 || len(subsets) <= 1 {
		for i, subset := range subsets {
			trial, err := ex.eval(ctx, subset, role, stopOnPass)
			if err != nil {
				return -1, i + 1, err
			}
			if trial.Outcome == "fail" {
				return i, i + 1, nil
			}
		}
		return -1, len(subsets), nil
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	failed := make([]bool, len(subsets))
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	for i := range subsets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			trial, err := ex.eval(cctx, subsets[i], role, stopOnPass)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel() // stop sibling lanes; execx kills their subprocesses
				})
				return
			}
			failed[i] = trial.Outcome == "fail"
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return -1, len(subsets), firstErr
	}
	for i, f := range failed {
		if f {
			return i, len(subsets), nil
		}
	}
	return -1, len(subsets), nil
}

// minimize delta-debugs the change set and then proves (or refutes)
// 1-minimality by checking every single-element removal. It returns the
// best-known failing set and the IDs of neighbors whose removal was unresolved
// or flaky (empty when minimality is proven).
func (ex *executor) minimize(ctx context.Context, changes []manifest.Change) ([]manifest.Change, []string, error) {
	minimal, _, err := ddmin.MinimizeBatch(changes, func(subsets [][]manifest.Change) (int, int, error) {
		return ex.batch(ctx, subsets, "candidate", true)
	})
	if err != nil {
		return nil, nil, err
	}

	bestKnown := minimal
	var uncertain []string
	for {
		neighbors := make([][]manifest.Change, len(bestKnown))
		for i := range bestKnown {
			neighbors[i] = removeChange(bestKnown, i)
		}
		sel, _, err := ex.batch(ctx, neighbors, "minimality-check", true)
		if err != nil {
			return nil, nil, err
		}
		if sel >= 0 {
			bestKnown = neighbors[sel]
			continue
		}
		// No single removal still reproduced the failure. Classify each
		// neighbor (all evaluated, since none failed) for the minimality proof.
		uncertain = uncertain[:0]
		for i := range bestKnown {
			tr := ex.lookup(neighbors[i])
			if tr.Outcome == "unresolved" || (tr.Outcome == "pass" && tr.Failures > 0) {
				uncertain = append(uncertain, bestKnown[i].ID())
			}
		}
		return bestKnown, uncertain, nil
	}
}

// syncProgress serializes Progress calls so concurrent lane evaluations cannot
// interleave terminal writes or race on the renderer's internal state. Used
// only when jobs > 1; with a single lane the engine uses the bare Progress.
type syncProgress struct {
	mu sync.Mutex
	p  Progress
}

func (s *syncProgress) Step(label, format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Step(label, format, args...)
}

func (s *syncProgress) Detail(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Detail(format, args...)
}

func (s *syncProgress) Trial(number int, role string, applied, total int, phase string, elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Trial(number, role, applied, total, phase, elapsed)
}
