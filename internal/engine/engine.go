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
	"slices"
	"strings"
	"time"

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
	// NoLockfilePins disables bisecting lockfile-only changes through
	// synthesized exact version pins; such changes are then only reported as
	// diagnostics, as before the pinning feature existed.
	NoLockfilePins bool
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
	// Jobs is the number of candidate trials to evaluate concurrently, each
	// in its own isolated worktree. Values < 1 mean 1 (sequential). Parallel
	// evaluation requires the verification command to be safe to run
	// concurrently; the minimized result is identical regardless of Jobs.
	Jobs int
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
	// FailureExcerpt is the tail of the failing verification output for this
	// trial ("" for passing or unresolved trials), size-capped by the verify
	// package. It rides through the checkpoint so resumed runs keep their
	// evidence.
	FailureExcerpt string `json:",omitempty"`
	// Phase timings are wall-clock durations for completed work in this trial.
	PrepareDuration time.Duration
	InstallDuration time.Duration
	VerifyDuration  time.Duration
	Duration        time.Duration

	// timedOut is set when every verification run in this trial hit the
	// per-run timeout. Unexported so it never enters the checkpoint or report
	// JSON; it exists only to hint, at fails-without-updates, that a too-short
	// --run-timeout — not a broken command — is the likely cause.
	timedOut bool
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
	// FailureExcerpt is the tail of the verification output from the trial
	// that convicted the final failing set (or, for fails-without-updates,
	// from the failing all-reverted baseline): the symptom behind the verdict.
	FailureExcerpt   string
	MinimalityProven bool
	UnresolvedTrials int
	ResumedTrials    int
	Diagnostics      []string
	Trials           []Trial
	StartedAt        time.Time
	FinishedAt       time.Time
	CleanupDuration  time.Duration
	KeptWorktree     string
}

// Engine wires the collaborators together.
type Engine struct {
	Git GitClient
	// NewInstaller builds an installer once the package manager is known.
	NewInstaller func(pm.Manager) Installer
	// NewVerifier builds a verifier once the package manager is known, so
	// manager-specific execution bridges (pip's per-worktree virtual
	// environment) can be configured without the engine knowing the details.
	NewVerifier func(pm.Manager) Verifier
	Progress    Progress
	// Now and MkdirTemp are injectable for tests; nil selects the real
	// clock and os.MkdirTemp("", "depbisect-").
	Now       func() time.Time
	MkdirTemp func() (string, error)
}

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
	jobs := opts.Jobs
	if jobs < 1 {
		jobs = 1
	}
	if jobs > 1 {
		// Serialize progress writes/state across concurrent lanes.
		progress = &syncProgress{p: progress}
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

	// Phase 2: detect the package manager / ecosystem and select handlers.
	manager, err := e.detectManager(ctx, toSHA, opts.PMOverride)
	if err != nil {
		return fail(err)
	}
	res.PackageManager = string(manager)
	eco, err := manifest.EcosystemFor(string(manager))
	if err != nil {
		return fail(err)
	}
	// pnpm declares workspaces in a separate file rather than in the manifest.
	if manager == pm.NPM || manager == pm.PNPM {
		hasPnpmWorkspace, err := e.Git.FileExists(ctx, toSHA, "pnpm-workspace.yaml")
		if err != nil {
			return fail(err)
		}
		if hasPnpmWorkspace {
			return fail(fmt.Errorf("pnpm-workspace.yaml found; pnpm workspaces are not supported yet"))
		}
	}
	// Go likewise declares workspaces in a separate go.work file, not in go.mod.
	if manager == pm.GO {
		hasGoWork, err := e.Git.FileExists(ctx, toSHA, "go.work")
		if err != nil {
			return fail(err)
		}
		if hasGoWork {
			return fail(fmt.Errorf("go.work found; Go workspaces are not supported yet"))
		}
	}

	// Phase 3: read manifests.
	manifestName := manager.ManifestName()
	basePkg, err := e.readManifest(ctx, eco, manifestName, baseSHA, opts.BaseRev)
	if err != nil {
		return fail(err)
	}
	toPkg, err := e.readManifest(ctx, eco, manifestName, toSHA, opts.ToRev)
	if err != nil {
		return fail(err)
	}
	if basePkg.HasWorkspaceLayout() || toPkg.HasWorkspaceLayout() {
		return fail(fmt.Errorf("%s declares workspaces; multi-package workspaces are not supported yet", manifestName))
	}

	oldResolved := e.readLockfile(ctx, eco, manager.LockfileName(), baseSHA, opts.BaseRev, res)
	newResolved := e.readLockfile(ctx, eco, manager.LockfileName(), toSHA, opts.ToRev, res)

	// Phase 4: diff. Lockfile-only drift (spec unchanged, resolution moved)
	// becomes bisectable through synthesized exact version pins; entries no
	// ecosystem can pin faithfully stay behind as diagnostics.
	changes := eco.Diff(basePkg, toPkg)
	lockOnly := eco.LockfileOnly(basePkg, toPkg, oldResolved, newResolved)
	pinned := 0
	if len(lockOnly) > 0 && !opts.NoLockfilePins {
		if pins := eco.PinChanges(lockOnly); len(pins) > 0 {
			pinnedIDs := make(map[string]bool, len(pins))
			for _, p := range pins {
				pinnedIDs[p.ID()] = true
			}
			remainder := lockOnly[:0]
			for _, lc := range lockOnly {
				if !pinnedIDs[lc.ID()] {
					remainder = append(remainder, lc)
				}
			}
			lockOnly = remainder
			changes = append(changes, pins...)
			manifest.SortChanges(changes)
			pinned = len(pins)
		}
	}
	manifest.AnnotateResolved(changes, oldResolved, newResolved)
	res.Changes = changes
	res.LockfileOnly = lockOnly
	if n := len(res.LockfileOnly); n > 0 {
		reason := "DepBisect bisects manifest-level changes and cannot isolate these"
		if !opts.NoLockfilePins {
			reason = "these resolutions cannot be pinned to an exact registry version"
		}
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"%s changed only in the lockfile (version spec unchanged): %s. %s; "+
				"if the culprit is among them, results may be incomplete.",
			pluralize(n, "dependency", "dependencies"), lockfileOnlyNames(res.LockfileOnly), reason))
	}
	// go.sum is a hash allowlist rather than a resolution record (it keeps
	// entries for versions no longer used), so transitive drift is only
	// meaningful for ecosystems with a real lockfile.
	if manager != pm.GO {
		if drift := transitiveDrift(basePkg, toPkg, oldResolved, newResolved, changes, lockOnly); len(drift) > 0 {
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
				"%s resolved differently between the two revisions without any direct dependency changing: %s. "+
					"DepBisect bisects direct dependencies only; if the culprit is among these transitive changes, "+
					"results may be incomplete.",
				pluralize(len(drift), "transitive dependency", "transitive dependencies"), lockfileOnlyNames(drift)))
		}
	}
	summary := pluralize(len(changes), "direct dependency change", "direct dependency changes")
	if pinned > 0 {
		summary += fmt.Sprintf(" (%d lockfile-only, bisected via exact version pins)", pinned)
	}
	progress.Step("Changes", "%s", summary)

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
				if errors.Is(err, os.ErrNotExist) {
					return fail(errors.New("no checkpoint found to resume; run without --resume to start a fresh bisection"))
				}
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

	// Phase 5: set up the isolated worktree pool. Lane count never exceeds the
	// number of changes, since no batch evaluates more subsets than that.
	laneCount := jobs
	if laneCount > len(changes) {
		laneCount = len(changes)
	}
	if laneCount < 1 {
		laneCount = 1
	}
	parent, err := mkdirTemp()
	if err != nil {
		return fail(fmt.Errorf("create temporary directory: %w", err))
	}
	dirs := make([]string, 0, laneCount)
	for i := 0; i < laneCount; i++ {
		// A single lane keeps the original directory name for stable output.
		dir := filepath.Join(parent, "worktree")
		if laneCount > 1 {
			dir = filepath.Join(parent, fmt.Sprintf("worktree-%d", i))
		}
		if err := e.Git.AddWorktree(ctx, dir, toSHA); err != nil {
			for _, created := range dirs {
				e.removeWorktree(created, progress)
			}
			os.RemoveAll(parent)
			return fail(err)
		}
		dirs = append(dirs, dir)
	}
	defer func() {
		cleanupStart := now()
		e.cleanup(parent, dirs, opts.KeepWorktrees, res, progress)
		cleanupEnd := now()
		res.CleanupDuration = cleanupEnd.Sub(cleanupStart)
		res.FinishedAt = cleanupEnd.UTC()
	}()
	if laneCount == 1 {
		progress.Detail("Created worktree at %s", dirs[0])
	} else {
		progress.Detail("Created %d worktrees under %s", laneCount, parent)
	}

	// Phase 6: build the candidate executor over the lane pool. Each lane is a
	// free worktree index; eval acquires one for the duration of a trial.
	lanes := make(chan int, laneCount)
	for i := 0; i < laneCount; i++ {
		lanes <- i
	}
	ex := &executor{
		git:            e.Git,
		installer:      installer,
		verifier:       e.NewVerifier(manager),
		progress:       progress,
		now:            now,
		toSHA:          toSHA,
		toManifest:     toPkg,
		eco:            eco,
		manifestName:   manifestName,
		changes:        changes,
		totalChanges:   len(changes),
		stream:         opts.Stream,
		installTimeout: opts.InstallTimeout,
		jobs:           jobs,
		lanes:          lanes,
		dirs:           dirs,
		memo:           memo,
		res:            res,
		trialNum:       trialNumber,
		store:          opts.Checkpoint,
		storeOn:        checkpointActive,
	}

	// Phase 7: baselines.
	progress.Step("Baseline", "1/2 | without updates (expect PASS)")
	oldTrial, err := ex.eval(ctx, nil, "baseline-old", false)
	if err != nil {
		return fail(err)
	}
	if oldTrial.Outcome == "unresolved" {
		return fail(fmt.Errorf("dependency installation failed with all updates reverted; cannot establish a baseline"))
	}
	if oldTrial.Failures == oldTrial.RunsExecuted && oldTrial.Failures > 0 {
		res.Outcome = OutcomeFailsAtBase
		res.FailureExcerpt = oldTrial.FailureExcerpt
		res.OutcomeDetail = fmt.Sprintf(
			"the command failed %d/%d runs with all dependency updates reverted; "+
				"the cause is likely not a direct dependency update from this range",
			oldTrial.Failures, oldTrial.RunsExecuted)
		if oldTrial.timedOut {
			res.OutcomeDetail += " (every run hit the per-run timeout — --run-timeout may be too short)"
		}
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
	newTrial, err := ex.eval(ctx, changes, "baseline-new", false)
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

	// Phase 8: delta debugging plus a 1-minimality proof.
	progress.Step("Bisect", "%d changes with ddmin", len(changes))
	bestKnown, uncertainNeighbors, err := ex.minimize(ctx, changes)
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

	res.Minimal = bestKnown
	final := ex.lookup(bestKnown)
	res.Confidence = Confidence{Failures: final.Failures, Runs: final.RunsExecuted}
	res.FailureExcerpt = final.FailureExcerpt
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

// validateCheckpoint rejects a loaded checkpoint that does not match the
// current run. It verifies the schema version and fingerprint, then confirms
// every recorded trial has a valid outcome, refers only to known changes, and
// applies a distinct subset. This guarantees resumed state is safe to reuse
// without re-testing.
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
		slices.Equal(a.Command, b.Command) &&
		slices.Equal(a.Changes, b.Changes)
}

// readManifest loads and parses the ecosystem's manifest at a revision.
func (e *Engine) readManifest(ctx context.Context, eco manifest.Ecosystem, name, sha, revLabel string) (manifest.Parsed, error) {
	data, err := e.Git.ShowFile(ctx, sha, name)
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", name, revLabel, err)
	}
	pkg, err := eco.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("at %s: %w", revLabel, err)
	}
	return pkg, nil
}

// detectManager chooses the package manager. A non-empty override wins;
// otherwise the manifest present at the target revision selects the ecosystem
// (Cargo.toml -> cargo, go.mod -> go, pyproject.toml -> uv or pip,
// requirements.txt -> pip, composer.json -> composer, package.json ->
// npm/pnpm/yarn by lockfile). More than one manifest is ambiguous and
// requires --pm, except that pyproject.toml and requirements.txt count as one
// Python manifest because they routinely coexist in a single project.
func (e *Engine) detectManager(ctx context.Context, toSHA, override string) (pm.Manager, error) {
	if override != "" {
		return pm.Detect(false, false, false, override)
	}
	hasPackageJSON, err := e.Git.FileExists(ctx, toSHA, pm.NPM.ManifestName())
	if err != nil {
		return "", err
	}
	hasCargoToml, err := e.Git.FileExists(ctx, toSHA, pm.CARGO.ManifestName())
	if err != nil {
		return "", err
	}
	hasGoMod, err := e.Git.FileExists(ctx, toSHA, pm.GO.ManifestName())
	if err != nil {
		return "", err
	}
	hasPyproject, err := e.Git.FileExists(ctx, toSHA, pm.UV.ManifestName())
	if err != nil {
		return "", err
	}
	hasComposerJSON, err := e.Git.FileExists(ctx, toSHA, pm.COMPOSER.ManifestName())
	if err != nil {
		return "", err
	}
	hasRequirements, err := e.Git.FileExists(ctx, toSHA, pm.PIP.ManifestName())
	if err != nil {
		return "", err
	}
	var found []string
	if hasPackageJSON {
		found = append(found, "package.json")
	}
	if hasCargoToml {
		found = append(found, "Cargo.toml")
	}
	if hasGoMod {
		found = append(found, "go.mod")
	}
	// pyproject.toml and requirements.txt contribute one Python entry: they
	// commonly coexist, and which manager handles the project is decided below.
	switch {
	case hasPyproject:
		found = append(found, "pyproject.toml")
	case hasRequirements:
		found = append(found, "requirements.txt")
	}
	if hasComposerJSON {
		found = append(found, "composer.json")
	}
	if len(found) > 1 {
		return "", fmt.Errorf("multiple manifests found (%s); choose one with --pm", strings.Join(found, ", "))
	}
	switch {
	case hasCargoToml:
		return pm.CARGO, nil
	case hasGoMod:
		return pm.GO, nil
	case hasPyproject:
		// pyproject.toml is shared by every Python tool; uv is identified by
		// its uv.lock, which wins even when a requirements.txt (often an
		// export) also exists. Without one, a requirements.txt selects pip.
		// Other tools (Poetry, PDM) need --pm once they are added.
		hasUvLock, err := e.Git.FileExists(ctx, toSHA, pm.UV.LockfileName())
		if err != nil {
			return "", err
		}
		if hasUvLock {
			return pm.UV, nil
		}
		if hasRequirements {
			return pm.PIP, nil
		}
		return "", fmt.Errorf("pyproject.toml found but no uv.lock or requirements.txt at %s; only uv and pip are supported for Python so far (generate uv.lock with `uv lock`, or pass --pm explicitly)", shortSHA(toSHA))
	case hasRequirements:
		return pm.PIP, nil
	case hasComposerJSON:
		return pm.COMPOSER, nil
	case hasPackageJSON:
		hasNpmLock, err := e.Git.FileExists(ctx, toSHA, pm.NPM.LockfileName())
		if err != nil {
			return "", err
		}
		hasPnpmLock, err := e.Git.FileExists(ctx, toSHA, pm.PNPM.LockfileName())
		if err != nil {
			return "", err
		}
		hasYarnLock, err := e.Git.FileExists(ctx, toSHA, pm.YARN.LockfileName())
		if err != nil {
			return "", err
		}
		return pm.Detect(hasNpmLock, hasPnpmLock, hasYarnLock, "")
	default:
		return "", fmt.Errorf("no supported manifest found at %s (package.json, Cargo.toml, go.mod, pyproject.toml, composer.json, or requirements.txt)", shortSHA(toSHA))
	}
}

// readLockfile loads resolved versions at a revision; failures degrade to a
// diagnostic because resolution info is helpful but not essential.
func (e *Engine) readLockfile(ctx context.Context, eco manifest.Ecosystem, name, sha, revLabel string, res *Result) manifest.Resolved {
	data, err := e.Git.ShowFile(ctx, sha, name)
	if err != nil {
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"could not read %s at %s; resolved versions for that side are unknown", name, revLabel))
		return manifest.Resolved{}
	}
	resolved, err := eco.ParseLock(data)
	if err != nil {
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
			"could not parse lockfile %s at %s (%v); resolved versions for that side are unknown",
			name, revLabel, err))
		return manifest.Resolved{}
	}
	return resolved
}

// warnDirty surfaces uncommitted manifest/lockfile edits in the user's working
// tree, which DepBisect deliberately ignores. For JavaScript all three
// lockfiles are checked regardless of the detected/overridden manager, so
// forcing --pm on a repo whose actual lockfile differs still surfaces a dirty
// lockfile. Probing a path that does not exist is harmless: git status
// reports it as clean.
func (e *Engine) warnDirty(ctx context.Context, manager pm.Manager, res *Result) {
	var paths []string
	switch manager {
	case pm.CARGO, pm.GO, pm.UV, pm.COMPOSER, pm.PIP:
		paths = []string{manager.ManifestName(), manager.LockfileName()}
		if paths[1] == paths[0] {
			// pip: requirements.txt is manifest and lockfile at once.
			paths = paths[:1]
		}
	default:
		paths = []string{pm.NPM.ManifestName(), pm.NPM.LockfileName(), pm.PNPM.LockfileName(), pm.YARN.LockfileName()}
	}
	for _, path := range paths {
		dirty, err := e.Git.IsPathDirty(ctx, path)
		if err == nil && dirty {
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
				"%s has uncommitted changes in your working tree; DepBisect compares committed revisions only", path))
		}
	}
}

// cleanup removes the engine-owned worktrees and temp directory. It uses a
// fresh context so cleanup still happens after cancellation.
func (e *Engine) cleanup(parent string, dirs []string, keep bool, res *Result, progress Progress) {
	if keep {
		if len(dirs) > 0 {
			res.KeptWorktree = dirs[0]
			res.Diagnostics = append(res.Diagnostics,
				fmt.Sprintf("worktree kept at %s (remove with: git worktree remove --force %q)", dirs[0], dirs[0]))
		}
		if len(dirs) > 1 {
			// Extra lanes hold arbitrary candidates; remove all but the first.
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf(
				"%d worktrees were used for parallel trials; the kept one reflects an arbitrary candidate, not necessarily the minimal set",
				len(dirs)))
			for _, dir := range dirs[1:] {
				e.removeWorktree(dir, progress)
			}
		}
		return
	}
	for _, dir := range dirs {
		e.removeWorktree(dir, progress)
	}
	if err := os.RemoveAll(parent); err != nil {
		progress.Detail("temp dir removal failed: %v", err)
	}
}

// removeWorktree removes one engine-owned worktree, falling back to direct
// deletion and pruning the stale administrative entry if git refuses.
func (e *Engine) removeWorktree(dir string, progress Progress) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := e.Git.RemoveWorktree(ctx, dir); err != nil {
		os.RemoveAll(dir)
		if err := e.Git.PruneWorktrees(ctx); err != nil {
			progress.Detail("worktree prune failed: %v", err)
		}
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
	slices.Sort(sorted)
	return strings.Join(sorted, "\x00")
}

// transitiveDrift lists packages resolved differently between the two
// revisions that correspond to no direct dependency change: transitive
// resolution drift, which no manifest edit can bisect. The root project's own
// lockfile entry (cargo and uv record one) is excluded, as are packages
// present in only one lockfile — added or dropped transitives ride along with
// whichever change introduced them.
func transitiveDrift(basePkg, toPkg manifest.Parsed, oldR, newR manifest.Resolved,
	changes []manifest.Change, lockOnly []manifest.LockfileChange) []manifest.LockfileChange {
	direct := map[string]bool{basePkg.ProjectName(): true, toPkg.ProjectName(): true}
	for _, c := range changes {
		direct[c.Name] = true
	}
	for _, lc := range lockOnly {
		direct[lc.Name] = true
	}
	var out []manifest.LockfileChange
	for name, ov := range oldR {
		nv := newR[name]
		if nv == "" || nv == ov || direct[name] {
			continue
		}
		out = append(out, manifest.LockfileChange{Name: name, OldResolved: ov, NewResolved: nv})
	}
	slices.SortFunc(out, func(a, b manifest.LockfileChange) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func lockfileOnlyNames(lcs []manifest.LockfileChange) string {
	const maxListed = 8
	names := make([]string, 0, maxListed+1)
	for i, lc := range lcs {
		if i == maxListed {
			names = append(names, fmt.Sprintf("and %d more", len(lcs)-maxListed))
			break
		}
		// The version pair is one unspaced token so line wrapping in any
		// renderer can never split a pair across lines.
		names = append(names, fmt.Sprintf("%s (%s->%s)", lc.Name, lc.OldResolved, lc.NewResolved))
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
