// Package engine orchestrates a DepBisect run: discovering dependency
// changes between two revisions, verifying baselines, delta-debugging the
// change set, and assembling the result. All external effects flow through
// small interfaces so the orchestration logic is testable with fakes.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/skyneticist/depbisect/internal/ddmin"
	"github.com/skyneticist/depbisect/internal/execx"
	"github.com/skyneticist/depbisect/internal/manifest"
	"github.com/skyneticist/depbisect/internal/pm"
	"github.com/skyneticist/depbisect/internal/verify"
)

// GitClient is the slice of Git that the engine needs.
type GitClient interface {
	ResolveRev(ctx context.Context, rev string) (string, error)
	ShowFile(ctx context.Context, rev, path string) ([]byte, error)
	FileExists(ctx context.Context, rev, path string) (bool, error)
	AddWorktree(ctx context.Context, dir, rev string) error
	ResetWorktree(ctx context.Context, dir, rev string) error
	RemoveWorktree(ctx context.Context, dir string) error
	PruneWorktrees(ctx context.Context) error
	IsPathDirty(ctx context.Context, path string) (bool, error)
}

// Installer installs dependencies for a candidate manifest.
type Installer interface {
	Version(ctx context.Context) (string, error)
	Install(ctx context.Context, dir string, stream io.Writer) (execx.Result, error)
}

// Verifier runs the verification command in a candidate worktree.
type Verifier interface {
	Verify(ctx context.Context, dir string, stopOnPass bool) (verify.Verdict, error)
}

// Progress receives human-oriented status updates during a run.
type Progress interface {
	// Step announces a labeled major phase.
	Step(label, format string, args ...any)
	// Detail reports fine-grained progress (shown in verbose mode).
	Detail(format string, args ...any)
	// Trial reports the lifecycle of one newly executed trial.
	Trial(number int, role string, applied, total int, phase string, elapsed time.Duration)
}

type nopProgress struct{}

func (nopProgress) Step(string, string, ...any)                        {}
func (nopProgress) Detail(string, ...any)                              {}
func (nopProgress) Trial(int, string, int, int, string, time.Duration) {}

// Outcome codes are stable identifiers used in reports and exit codes.
const (
	OutcomeMinimalFound  = "minimal-set-found"
	OutcomeNotReproduced = "not-reproduced"
	OutcomeFailsAtBase   = "fails-without-updates"
	OutcomeInconclusive  = "inconclusive"
	OutcomeNoChanges     = "no-dependency-changes"
	OutcomeDryRun        = "dry-run"
)

// Options configures one run.
type Options struct {
	BaseRev string
	ToRev   string
	// Command is the verification command argv (no shell).
	Command []string
	// Runs is how many times each candidate is verified (default 1).
	Runs          int
	PMOverride    string
	KeepWorktrees bool
	DryRun        bool
	// Stream, when non-nil, receives live install/verify output.
	Stream io.Writer
	// Checkpoint persists completed trials. Resume reuses a matching
	// checkpoint instead of starting it over.
	Checkpoint        CheckpointStore
	Resume            bool
	CheckpointContext string
	// InstallTimeout bounds each package-manager install. Zero disables it.
	InstallTimeout time.Duration
	// OverallTimeout bounds the bisection itself. Cleanup uses a separate
	// bounded context so it can still run after this deadline.
	OverallTimeout time.Duration
}

// Trial records one executed candidate evaluation.
type Trial struct {
	// Role is "baseline-old", "baseline-new", "candidate", or
	// "minimality-check".
	Role string
	// Applied lists the change IDs whose new state was applied.
	Applied []string
	// Outcome is "fail", "pass", or "unresolved" (install failure).
	Outcome      string
	RunsExecuted int
	Failures     int
	// Phase timings are wall-clock durations for completed work in this trial.
	PrepareDuration time.Duration
	InstallDuration time.Duration
	VerifyDuration  time.Duration
	Duration        time.Duration
}

// Confidence states how often the final failing set reproduced the failure.
type Confidence struct {
	Failures int
	Runs     int
}

// CheckpointSchemaVersion identifies the append-only resume format.
const CheckpointSchemaVersion = 1

// CheckpointFingerprint identifies every input that affects trial outcomes.
type CheckpointFingerprint struct {
	BaseSHA               string
	ToSHA                 string
	PackageManager        string
	PackageManagerVersion string
	Command               []string
	Runs                  int
	Changes               []string
	Context               string
}

// Checkpoint is the durable state needed to resume a run.
type Checkpoint struct {
	SchemaVersion int
	Fingerprint   CheckpointFingerprint
	StartedAt     time.Time
	Trials        []Trial
}

// CheckpointStore persists a header followed by completed trial records.
type CheckpointStore interface {
	Load() (*Checkpoint, error)
	Start(Checkpoint) error
	Append(Trial) error
	Clear() error
}

// Result is the full account of a run.
type Result struct {
	Outcome               string
	OutcomeDetail         string
	BaseRev               string
	BaseSHA               string
	ToRev                 string
	ToSHA                 string
	PackageManager        string
	PackageManagerVersion string
	Command               []string
	Runs                  int
	Changes               []manifest.Change
	LockfileOnly          []manifest.LockfileChange
	Minimal               []manifest.Change
	Confidence            Confidence
	MinimalityProven      bool
	UnresolvedTrials      int
	ResumedTrials         int
	Diagnostics           []string
	Trials                []Trial
	StartedAt             time.Time
	FinishedAt            time.Time
	CleanupDuration       time.Duration
	KeptWorktree          string
}

// Engine wires the collaborators together.
type Engine struct {
	Git GitClient
	// NewInstaller builds an installer once the package manager is known.
	NewInstaller func(pm.Manager) Installer
	Verifier     Verifier
	Progress     Progress
	// Now and MkdirTemp are injectable for tests; nil selects the real
	// clock and os.MkdirTemp("", "depbisect-").
	Now       func() time.Time
	MkdirTemp func() (string, error)
}

const manifestName = "package.json"

// Run executes the bisection described by opts. The returned Result is
// non-nil even on error, carrying whatever was established before failure.
func (e *Engine) Run(ctx context.Context, opts Options) (*Result, error) {
	parentCtx := ctx
	now := e.Now
	if now == nil {
		now = time.Now
	}
	progress := e.Progress
	if progress == nil {
		progress = nopProgress{}
	}
	mkdirTemp := e.MkdirTemp
	if mkdirTemp == nil {
		mkdirTemp = func() (string, error) { return os.MkdirTemp("", "depbisect-") }
	}
	runs := opts.Runs
	if runs < 1 {
		runs = 1
	}
	if opts.OverallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.OverallTimeout)
		defer cancel()
	}

	res := &Result{
		BaseRev:   opts.BaseRev,
		ToRev:     opts.ToRev,
		Command:   append([]string(nil), opts.Command...),
		Runs:      runs,
		StartedAt: now().UTC(),
	}
	checkpointActive := false
	finish := func() (*Result, error) {
		res.FinishedAt = now().UTC()
		if checkpointActive {
			if err := opts.Checkpoint.Clear(); err != nil {
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("could not remove completed checkpoint: %v", err))
			}
		}
		return res, nil
	}
	fail := func(err error) (*Result, error) {
		res.FinishedAt = now().UTC()
		if opts.OverallTimeout > 0 &&
			errors.Is(ctx.Err(), context.DeadlineExceeded) &&
			parentCtx.Err() == nil {
			err = fmt.Errorf("bisection timed out after %s: %w", opts.OverallTimeout, context.DeadlineExceeded)
		}
		return res, err
	}

	// Phase 1: resolve revisions and read manifests.
	baseSHA, err := e.Git.ResolveRev(ctx, opts.BaseRev)
	if err != nil {
		return fail(err)
	}
	toSHA, err := e.Git.ResolveRev(ctx, opts.ToRev)
	if err != nil {
		return fail(err)
	}
	if baseSHA == toSHA {
		return fail(fmt.Errorf("base (%s) and target (%s) resolve to the same commit %s; nothing to compare",
			opts.BaseRev, opts.ToRev, shortSHA(baseSHA)))
	}
	res.BaseSHA, res.ToSHA = baseSHA, toSHA
	progress.Step("Compare", "%s (%s) -> %s (%s)",
		opts.BaseRev, shortSHA(baseSHA), opts.ToRev, shortSHA(toSHA))

	basePkg, err := e.readManifest(ctx, baseSHA, opts.BaseRev)
	if err != nil {
		return fail(err)
	}
	toPkg, err := e.readManifest(ctx, toSHA, opts.ToRev)
	if err != nil {
		return fail(err)
	}
	if basePkg.HasWorkspaces || toPkg.HasWorkspaces {
		return fail(fmt.Errorf("package.json declares workspaces; multi-package workspaces are not supported yet"))
	}

	// Phase 2: detect the package manager and read lockfiles.
	hasPnpmWorkspace, err := e.Git.FileExists(ctx, toSHA, "pnpm-workspace.yaml")
	if err != nil {
		return fail(err)
	}
	if hasPnpmWorkspace {
		return fail(fmt.Errorf("pnpm-workspace.yaml found; pnpm workspaces are not supported yet"))
	}
	hasNpmLock, err := e.Git.FileExists(ctx, toSHA, pm.NPM.LockfileName())
	if err != nil {
		return fail(err)
	}
	hasPnpmLock, err := e.Git.FileExists(ctx, toSHA, pm.PNPM.LockfileName())
	if err != nil {
		return fail(err)
	}
	manager, err := pm.Detect(hasNpmLock, hasPnpmLock, opts.PMOverride)
	if err != nil {
		return fail(err)
	}
	res.PackageManager = string(manager)

	oldResolved := e.readLockfile(ctx, manager, baseSHA, opts.BaseRev, res)
	newResolved := e.readLockfile(ctx, manager, toSHA, opts.ToRev, res)

	// Phase 3: diff.
	changes := manifest.Diff(basePkg, toPkg)
	manifest.AnnotateResolved(changes, oldResolved, newResolved)
	res.Changes = changes
	res.LockfileOnly = manifest.LockfileOnly(basePkg, toPkg, oldResolved, newResolved)
	if n := len(res.LockfileOnly); n > 0 {
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"%s changed only in the lockfile (version spec unchanged): %s. "+
				"DepBisect bisects manifest-level changes and cannot isolate these; "+
				"if the culprit is among them, results may be incomplete.",
			pluralize(n, "dependency", "dependencies"), lockfileOnlyNames(res.LockfileOnly)))
	}
	progress.Step("Changes", "%s",
		pluralize(len(changes), "direct dependency change", "direct dependency changes"))

	e.warnDirty(ctx, manager, res)

	if len(changes) == 0 {
		res.Outcome = OutcomeNoChanges
		res.OutcomeDetail = "no direct dependency changes between the two revisions"
		if len(res.LockfileOnly) > 0 {
			res.OutcomeDetail += "; only lockfile-level resolution changes were found (see diagnostics)"
		}
		return finish()
	}

	if opts.DryRun {
		res.Outcome = OutcomeDryRun
		res.OutcomeDetail = fmt.Sprintf("dry run: would bisect %d changes with %q using %s",
			len(changes), strings.Join(opts.Command, " "), manager)
		return finish()
	}

	installer := e.NewInstaller(manager)
	managerVersion, err := installer.Version(ctx)
	if err != nil {
		return fail(err)
	}
	res.PackageManagerVersion = managerVersion

	fingerprint := makeCheckpointFingerprint(
		baseSHA, toSHA, manager, managerVersion, opts.Command, runs, changes, opts.CheckpointContext,
	)
	memo := map[string]Trial{}
	trialNumber := 0
	if opts.Checkpoint != nil {
		if opts.Resume {
			cp, err := opts.Checkpoint.Load()
			if err != nil {
				return fail(fmt.Errorf("load checkpoint: %w", err))
			}
			if err := validateCheckpoint(cp, fingerprint); err != nil {
				return fail(err)
			}
			res.StartedAt = cp.StartedAt.UTC()
			for _, trial := range cp.Trials {
				memo[subsetKeyIDs(trial.Applied)] = trial
				res.Trials = append(res.Trials, trial)
			}
			res.ResumedTrials = len(cp.Trials)
			trialNumber = len(cp.Trials)
			progress.Step("Resume", "%d completed trials restored from checkpoint", len(cp.Trials))
		} else {
			if err := opts.Checkpoint.Start(Checkpoint{
				SchemaVersion: CheckpointSchemaVersion,
				Fingerprint:   fingerprint,
				StartedAt:     res.StartedAt,
			}); err != nil {
				return fail(fmt.Errorf("create checkpoint: %w", err))
			}
		}
		checkpointActive = true
	}

	// Phase 4: set up the isolated worktree.
	parent, err := mkdirTemp()
	if err != nil {
		return fail(fmt.Errorf("create temporary directory: %w", err))
	}
	wt := filepath.Join(parent, "worktree")
	if err := e.Git.AddWorktree(ctx, wt, toSHA); err != nil {
		os.RemoveAll(parent)
		return fail(err)
	}
	defer func() {
		cleanupStart := now()
		e.cleanup(parent, wt, opts.KeepWorktrees, res, progress)
		cleanupEnd := now()
		res.CleanupDuration = cleanupEnd.Sub(cleanupStart)
		res.FinishedAt = cleanupEnd.UTC()
	}()
	progress.Detail("Created worktree at %s", wt)

	// Phase 5: evaluate candidates with memoization.
	runSubset := func(subset []manifest.Change, role string, stopOnPass bool) (Trial, error) {
		key := subsetKey(subset)
		if cached, ok := memo[key]; ok {
			return cached, nil
		}
		if err := ctx.Err(); err != nil {
			return Trial{}, fmt.Errorf("bisection interrupted: %w", err)
		}
		trialNumber++
		progress.Trial(trialNumber, role, len(subset), len(changes), "preparing", 0)
		start := now()
		if err := e.Git.ResetWorktree(ctx, wt, toSHA); err != nil {
			return Trial{}, fmt.Errorf("prepare isolated trial worktree: %w", err)
		}

		applied := make(map[string]bool, len(subset))
		trial := Trial{Role: role, Applied: make([]string, 0, len(subset))}
		for _, c := range subset {
			applied[c.ID()] = true
			trial.Applied = append(trial.Applied, c.ID())
		}
		sort.Strings(trial.Applied)

		rendered, err := manifest.Render(toPkg, changes, applied)
		if err != nil {
			return Trial{}, err
		}
		if err := os.WriteFile(filepath.Join(wt, manifestName), rendered, 0o644); err != nil {
			return Trial{}, fmt.Errorf("write candidate manifest: %w", err)
		}
		afterPrepare := now()
		trial.PrepareDuration = afterPrepare.Sub(start)
		progress.Trial(trialNumber, role, len(subset), len(changes), "installing", trial.PrepareDuration)

		installCtx := ctx
		cancelInstall := func() {}
		if opts.InstallTimeout > 0 {
			installCtx, cancelInstall = context.WithTimeout(ctx, opts.InstallTimeout)
		}
		instRes, err := installer.Install(installCtx, wt, opts.Stream)
		installContextErr := installCtx.Err()
		cancelInstall()
		afterInstall := now()
		trial.InstallDuration = afterInstall.Sub(afterPrepare)
		if err != nil {
			if opts.InstallTimeout > 0 &&
				errors.Is(installContextErr, context.DeadlineExceeded) &&
				!errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Trial{}, fmt.Errorf("dependency installation timed out after %s: %w",
					opts.InstallTimeout, context.DeadlineExceeded)
			}
			return Trial{}, fmt.Errorf("install dependencies: %w", err)
		}
		if instRes.ExitCode != 0 {
			trial.Outcome = "unresolved"
			trial.Duration = afterInstall.Sub(start)
			progress.Trial(trialNumber, role, len(subset), len(changes), trial.Outcome, trial.Duration)
			progress.Detail("Install failed for %d applied changes (%s); candidate skipped",
				len(subset), firstLine(instRes.Stderr))
			if err := recordTrial(opts.Checkpoint, checkpointActive, memo, key, res, trial); err != nil {
				return Trial{}, err
			}
			return trial, nil
		}
		progress.Trial(trialNumber, role, len(subset), len(changes), "verifying", afterInstall.Sub(start))
		verdict, err := e.Verifier.Verify(ctx, wt, stopOnPass)
		if err != nil {
			return Trial{}, fmt.Errorf("run verification command: %w", err)
		}
		afterVerify := now()
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
		progress.Trial(trialNumber, role, len(subset), len(changes), trial.Outcome, trial.Duration)
		progress.Detail("Tested %d applied changes: %s (%s)", len(subset), trial.Outcome, verdict.String())
		if err := recordTrial(opts.Checkpoint, checkpointActive, memo, key, res, trial); err != nil {
			return Trial{}, err
		}
		return trial, nil
	}

	// Phase 6: baselines.
	progress.Step("Baseline", "1/2 | without updates (expect PASS)")
	oldTrial, err := runSubset(nil, "baseline-old", false)
	if err != nil {
		return fail(err)
	}
	if oldTrial.Outcome == "unresolved" {
		return fail(fmt.Errorf("dependency installation failed with all updates reverted; cannot establish a baseline"))
	}
	if oldTrial.Failures == oldTrial.RunsExecuted && oldTrial.Failures > 0 {
		res.Outcome = OutcomeFailsAtBase
		res.OutcomeDetail = fmt.Sprintf(
			"the command failed %d/%d runs with all dependency updates reverted; "+
				"the cause is likely not a direct dependency update from this range",
			oldTrial.Failures, oldTrial.RunsExecuted)
		res.OutcomeDetail += lockfileOnlyHint(res)
		return finish()
	}
	if oldTrial.Failures > 0 {
		res.Outcome = OutcomeInconclusive
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"the verification command is flaky with all updates reverted (failed %d/%d runs); "+
				"stabilize the test or increase --runs", oldTrial.Failures, oldTrial.RunsExecuted))
		res.OutcomeDetail = "baseline without updates is flaky; bisection would be unreliable"
		return finish()
	}

	progress.Step("Baseline", "2/2 | with all updates (expect FAIL)")
	newTrial, err := runSubset(changes, "baseline-new", false)
	if err != nil {
		return fail(err)
	}
	if newTrial.Outcome == "unresolved" {
		return fail(fmt.Errorf("dependency installation failed with all updates applied; cannot establish a baseline"))
	}
	if newTrial.Failures == 0 {
		res.Outcome = OutcomeNotReproduced
		res.OutcomeDetail = fmt.Sprintf(
			"the verification command passed %d/%d runs with all dependency updates applied; "+
				"no bisection was needed because the reported failure does not reproduce "+
				"in a clean worktree at %s",
			newTrial.RunsExecuted, newTrial.RunsExecuted, opts.ToRev)
		res.OutcomeDetail += lockfileOnlyHint(res)
		return finish()
	}
	if newTrial.Failures < newTrial.RunsExecuted {
		res.Outcome = OutcomeInconclusive
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"the verification command is flaky with all updates applied (failed %d/%d runs); "+
				"increase --runs to separate flakiness from the dependency-induced failure",
			newTrial.Failures, newTrial.RunsExecuted))
		res.OutcomeDetail = "failure does not reproduce deterministically; bisection would be unreliable"
		return finish()
	}

	// Phase 7: delta debugging.
	progress.Step("Bisect", "%d changes with ddmin", len(changes))
	minimal, _, err := ddmin.Minimize(changes, func(subset []manifest.Change) (ddmin.Outcome, error) {
		trial, err := runSubset(subset, "candidate", true)
		if err != nil {
			return ddmin.Unresolved, err
		}
		switch trial.Outcome {
		case "fail":
			return ddmin.Fail, nil
		case "unresolved":
			return ddmin.Unresolved, nil
		default:
			return ddmin.Pass, nil
		}
	})
	if err != nil {
		return fail(err)
	}
	// res.Trials holds each unique subset once, so this count is exact.
	flakyCandidates := 0
	for _, tr := range res.Trials {
		if tr.Role == "candidate" && tr.Outcome == "pass" && tr.Failures > 0 {
			flakyCandidates++
		}
	}
	if flakyCandidates > 0 {
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"%d candidate configurations showed mixed pass/fail results and were treated as not reproducing; "+
				"if the minimal set looks wrong, increase --runs", flakyCandidates))
	}

	bestKnown := minimal
	var uncertainNeighbors []string
	for {
		uncertainNeighbors = uncertainNeighbors[:0]
		reduced := false
		for i := range bestKnown {
			neighbor := removeChange(bestKnown, i)
			trial, err := runSubset(neighbor, "minimality-check", true)
			if err != nil {
				return fail(err)
			}
			if trial.Outcome == "fail" {
				bestKnown = neighbor
				reduced = true
				break
			}
			if trial.Outcome == "unresolved" || (trial.Outcome == "pass" && trial.Failures > 0) {
				uncertainNeighbors = append(uncertainNeighbors, bestKnown[i].ID())
			}
		}
		if !reduced {
			break
		}
	}

	res.Minimal = bestKnown
	final := memo[subsetKey(bestKnown)]
	res.Confidence = Confidence{Failures: final.Failures, Runs: final.RunsExecuted}
	for _, trial := range res.Trials {
		if trial.Outcome == "unresolved" {
			res.UnresolvedTrials++
		}
	}
	if len(uncertainNeighbors) == 0 {
		candidateTests := countCandidateTrials(res.Trials)
		res.MinimalityProven = true
		res.Outcome = OutcomeMinimalFound
		res.OutcomeDetail = fmt.Sprintf("minimal failing set has %d of %d changes after %d candidate tests",
			len(bestKnown), len(changes), candidateTests)
		progress.Step("Complete", "minimal failing set contains %d of %d changes",
			len(bestKnown), len(changes))
	} else {
		res.Outcome = OutcomeInconclusive
		res.OutcomeDetail = fmt.Sprintf(
			"best-known failing set has %d of %d changes, but 1-minimality could not be proven because %d required neighboring configurations were unresolved or flaky",
			len(bestKnown), len(changes), len(uncertainNeighbors))
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"minimality checks were unresolved or flaky when removing: %s",
			strings.Join(uncertainNeighbors, ", ")))
		progress.Step("Complete", "best-known failing set contains %d of %d changes; minimality not proven",
			len(bestKnown), len(changes))
	}
	return finish()
}

func countCandidateTrials(trials []Trial) int {
	count := 0
	for _, trial := range trials {
		if trial.Role == "candidate" || trial.Role == "minimality-check" {
			count++
		}
	}
	return count
}

func recordTrial(store CheckpointStore, checkpointActive bool, memo map[string]Trial, key string, res *Result, trial Trial) error {
	memo[key] = trial
	res.Trials = append(res.Trials, trial)
	if checkpointActive {
		if err := store.Append(trial); err != nil {
			return fmt.Errorf("save checkpoint trial: %w", err)
		}
	}
	return nil
}

func makeCheckpointFingerprint(baseSHA, toSHA string, manager pm.Manager, managerVersion string, command []string, runs int, changes []manifest.Change, context string) CheckpointFingerprint {
	ids := make([]string, len(changes))
	for i, change := range changes {
		ids[i] = change.ID()
	}
	return CheckpointFingerprint{
		BaseSHA:               baseSHA,
		ToSHA:                 toSHA,
		PackageManager:        string(manager),
		PackageManagerVersion: managerVersion,
		Command:               append([]string(nil), command...),
		Runs:                  runs,
		Changes:               ids,
		Context:               context,
	}
}

func validateCheckpoint(cp *Checkpoint, want CheckpointFingerprint) error {
	if cp == nil {
		return fmt.Errorf("checkpoint is empty")
	}
	if cp.SchemaVersion != CheckpointSchemaVersion {
		return fmt.Errorf("checkpoint schema version %d is unsupported (want %d)",
			cp.SchemaVersion, CheckpointSchemaVersion)
	}
	if !checkpointFingerprintsEqual(cp.Fingerprint, want) {
		return fmt.Errorf("checkpoint does not match this run; remove it or run without --resume")
	}
	if cp.StartedAt.IsZero() {
		return fmt.Errorf("checkpoint has no start time")
	}
	known := make(map[string]bool, len(want.Changes))
	for _, id := range want.Changes {
		known[id] = true
	}
	seen := make(map[string]bool, len(cp.Trials))
	for i, trial := range cp.Trials {
		switch trial.Outcome {
		case "pass", "fail", "unresolved":
		default:
			return fmt.Errorf("checkpoint trial %d has invalid outcome %q", i+1, trial.Outcome)
		}
		for _, id := range trial.Applied {
			if !known[id] {
				return fmt.Errorf("checkpoint trial %d refers to unknown change %q", i+1, id)
			}
		}
		key := subsetKeyIDs(trial.Applied)
		if seen[key] {
			return fmt.Errorf("checkpoint contains duplicate trial subset at record %d", i+1)
		}
		seen[key] = true
	}
	return nil
}

func checkpointFingerprintsEqual(a, b CheckpointFingerprint) bool {
	return a.BaseSHA == b.BaseSHA &&
		a.ToSHA == b.ToSHA &&
		a.PackageManager == b.PackageManager &&
		a.PackageManagerVersion == b.PackageManagerVersion &&
		a.Runs == b.Runs &&
		a.Context == b.Context &&
		equalStrings(a.Command, b.Command) &&
		equalStrings(a.Changes, b.Changes)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func removeChange(changes []manifest.Change, index int) []manifest.Change {
	out := make([]manifest.Change, 0, len(changes)-1)
	out = append(out, changes[:index]...)
	out = append(out, changes[index+1:]...)
	return out
}

// readManifest loads and parses package.json at a revision.
func (e *Engine) readManifest(ctx context.Context, sha, revLabel string) (*manifest.PackageJSON, error) {
	data, err := e.Git.ShowFile(ctx, sha, manifestName)
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", manifestName, revLabel, err)
	}
	pkg, err := manifest.ParsePackageJSON(data)
	if err != nil {
		return nil, fmt.Errorf("at %s: %w", revLabel, err)
	}
	return pkg, nil
}

// readLockfile loads resolved versions at a revision; failures degrade to a
// diagnostic because resolution info is helpful but not essential.
func (e *Engine) readLockfile(ctx context.Context, manager pm.Manager, sha, revLabel string, res *Result) manifest.Resolved {
	name := manager.LockfileName()
	data, err := e.Git.ShowFile(ctx, sha, name)
	if err != nil {
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"could not read %s at %s; resolved versions for that side are unknown", name, revLabel))
		return manifest.Resolved{}
	}
	var resolved manifest.Resolved
	if manager == pm.PNPM {
		resolved, err = manifest.ParsePnpmLock(data)
	} else {
		resolved, err = manifest.ParsePackageLock(data)
	}
	if err != nil {
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"could not parse lockfile %s at %s (%v); resolved versions for that side are unknown",
			name, revLabel, err))
		return manifest.Resolved{}
	}
	return resolved
}

// warnDirty surfaces uncommitted manifest/lockfile edits in the user's
// working tree, which DepBisect deliberately ignores.
func (e *Engine) warnDirty(ctx context.Context, manager pm.Manager, res *Result) {
	for _, path := range []string{manifestName, manager.LockfileName()} {
		dirty, err := e.Git.IsPathDirty(ctx, path)
		if err == nil && dirty {
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
				"%s has uncommitted changes in your working tree; DepBisect compares committed revisions only", path))
		}
	}
}

// cleanup removes the engine-owned worktree and temp directory. It uses a
// fresh context so cleanup still happens after cancellation.
func (e *Engine) cleanup(parent, wt string, keep bool, res *Result, progress Progress) {
	if keep {
		res.KeptWorktree = wt
		res.Diagnostics = append(res.Diagnostics,
			fmt.Sprintf("worktree kept at %s (remove with: git worktree remove --force %q)", wt, wt))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := e.Git.RemoveWorktree(ctx, wt); err != nil {
		// Fall back to direct deletion plus pruning of the stale entry.
		os.RemoveAll(wt)
		if err := e.Git.PruneWorktrees(ctx); err != nil {
			progress.Detail("worktree prune failed: %v", err)
		}
	}
	if err := os.RemoveAll(parent); err != nil {
		progress.Detail("temp dir removal failed: %v", err)
	}
}

// subsetKey is a canonical identity for a set of changes.
func subsetKey(subset []manifest.Change) string {
	ids := make([]string, len(subset))
	for i, c := range subset {
		ids[i] = c.ID()
	}
	return subsetKeyIDs(ids)
}

func subsetKeyIDs(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

func lockfileOnlyNames(lcs []manifest.LockfileChange) string {
	const maxListed = 8
	names := make([]string, 0, maxListed+1)
	for i, lc := range lcs {
		if i == maxListed {
			names = append(names, fmt.Sprintf("and %d more", len(lcs)-maxListed))
			break
		}
		names = append(names, fmt.Sprintf("%s (%s -> %s)", lc.Name, lc.OldResolved, lc.NewResolved))
	}
	return strings.Join(names, ", ")
}

func lockfileOnlyHint(res *Result) string {
	if len(res.LockfileOnly) == 0 {
		return ""
	}
	return fmt.Sprintf("; note: %d lockfile-only changes exist that DepBisect cannot bisect (see diagnostics)",
		len(res.LockfileOnly))
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "no output"
	}
	return s
}
