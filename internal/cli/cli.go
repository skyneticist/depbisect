// Package cli implements the depbisect command-line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/skyneticist/depbisect/internal/checkpoint"
	"github.com/skyneticist/depbisect/internal/engine"
	"github.com/skyneticist/depbisect/internal/execx"
	"github.com/skyneticist/depbisect/internal/gitx"
	"github.com/skyneticist/depbisect/internal/pm"
	"github.com/skyneticist/depbisect/internal/report"
	"github.com/skyneticist/depbisect/internal/verify"
)

// Exit codes are part of the CLI contract; see `depbisect help`.
const (
	ExitOK            = 0 // minimal set found, or informational command succeeded
	ExitError         = 1 // usage or runtime error
	ExitNotReproduced = 2 // failure did not reproduce with all updates applied
	ExitFailsAtBase   = 3 // command fails even with all updates reverted
	ExitInconclusive  = 4 // verification command too flaky to bisect
	ExitNoChanges     = 5 // no direct dependency changes to bisect
)

const usageText = `depbisect — git bisect, but for dependency updates.

Usage:
  depbisect run --base <rev> [flags] -- <command> [args...]
  depbisect version
  depbisect completion bash|zsh
  depbisect help

Finds the smallest set of direct dependency updates (package.json or
Cargo.toml) between --base and --to that makes <command> fail, using delta
debugging in an isolated git worktree. Your checkout is never modified.

The verification command after -- is executed directly with its arguments
preserved exactly; no shell is involved. Wrap it yourself if you need shell
features: depbisect run --base main -- sh -c 'npm test 2>&1 | grep -v warn'

Flags for run:
  --base <rev>          base revision to compare against (required)
  --to <rev>            target revision (default "HEAD")
  --repo <path>         repository to operate on (default ".")
  --runs <n>            verification runs per candidate; raises confidence
                        for flaky tests (default 1)
  --jobs, -j <n>        candidate trials to evaluate in parallel, each in its
                        own worktree (default 1). The minimal set is identical
                        at any value; requires the verification command to be
                        safe to run concurrently (no shared ports/files/state)
  --run-timeout <dur>   timeout per verification run, e.g. 10m (default none)
  --install-timeout <dur>
                        timeout per dependency install (default none)
  --overall-timeout <dur>
                        timeout for the complete bisection (default none)
  --pm <npm|pnpm|cargo> package manager (default: auto-detected)
  --report-md <path>    Markdown report path (default "depbisect-report.md")
  --report-json <path>  JSON report path (default "depbisect-report.json")
  --no-reports          write no report files
  --checkpoint <path>   resumable checkpoint path (default
                        ".depbisect-checkpoint.jsonl"; empty disables)
  --resume              resume completed trials from --checkpoint
  --keep-worktrees      keep the temporary worktree for inspection
  --dry-run             show detected changes and plan; run nothing
  --quiet               suppress progress; print only the final result
  --verbose             stream subprocess output and extra progress detail
  --style <name>        output style: modern (default) or classic; also set
                        with DEPBISECT_STYLE

Exit codes:
  0  minimal failing set found (or version/completion/dry-run success)
  1  usage or runtime error
  2  failure did not reproduce with all updates applied
  3  command fails even with all updates reverted
  4  inconclusive: flaky verification or minimality could not be proven
  5  no direct dependency changes between the revisions
`

// Main is the testable entry point. It returns the process exit code.
func Main(args []string, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return ExitError
	}
	switch args[0] {
	case "run":
		return runMain(args[1:], stdout, stderr, version)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "depbisect %s\n", version)
		return ExitOK
	case "completion":
		return completionMain(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "depbisect: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usageText)
		return ExitError
	}
}

// runOptions is the parsed flag set for `depbisect run`.
type runOptions struct {
	base           string
	to             string
	repo           string
	runs           int
	jobs           int
	runTimeout     time.Duration
	installTimeout time.Duration
	overallTimeout time.Duration
	pm             string
	reportMD       string
	reportJSON     string
	noReports      bool
	checkpoint     string
	resume         bool
	keepWorktrees  bool
	dryRun         bool
	quiet          bool
	verbose        bool
	style          outputStyle
	command        []string
}

// parseRunArgs splits args at the first "--": flags before it, the
// verification command after it.
func parseRunArgs(args []string) (*runOptions, error) {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		return nil, errors.New(`missing "--" separator: depbisect run --base <rev> -- <command>`)
	}
	flagArgs, command := args[:sep], args[sep+1:]

	opts := &runOptions{}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.base, "base", "", "")
	fs.StringVar(&opts.to, "to", "HEAD", "")
	fs.StringVar(&opts.repo, "repo", ".", "")
	fs.IntVar(&opts.runs, "runs", 1, "")
	fs.IntVar(&opts.jobs, "jobs", 1, "")
	fs.IntVar(&opts.jobs, "j", 1, "")
	fs.DurationVar(&opts.runTimeout, "run-timeout", 0, "")
	fs.DurationVar(&opts.installTimeout, "install-timeout", 0, "")
	fs.DurationVar(&opts.overallTimeout, "overall-timeout", 0, "")
	fs.StringVar(&opts.pm, "pm", "", "")
	fs.StringVar(&opts.reportMD, "report-md", "depbisect-report.md", "")
	fs.StringVar(&opts.reportJSON, "report-json", "depbisect-report.json", "")
	fs.BoolVar(&opts.noReports, "no-reports", false, "")
	fs.StringVar(&opts.checkpoint, "checkpoint", ".depbisect-checkpoint.jsonl", "")
	fs.BoolVar(&opts.resume, "resume", false, "")
	fs.BoolVar(&opts.keepWorktrees, "keep-worktrees", false, "")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "")
	fs.BoolVar(&opts.quiet, "quiet", false, "")
	fs.BoolVar(&opts.verbose, "verbose", false, "")
	var styleName string
	fs.StringVar(&styleName, "style", "", "")
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	// Precedence: --style flag, then DEPBISECT_STYLE, then the modern default.
	if styleName == "" {
		styleName = os.Getenv("DEPBISECT_STYLE")
	}
	if styleName == "" {
		styleName = "modern"
	}
	style, err := parseOutputStyle(styleName)
	if err != nil {
		return nil, err
	}
	opts.style = style
	if extra := fs.Args(); len(extra) > 0 {
		return nil, fmt.Errorf("unexpected argument %q before %q (the verification command goes after the separator)", extra[0], "--")
	}
	if opts.base == "" {
		return nil, errors.New("--base is required")
	}
	if opts.runs < 1 {
		return nil, errors.New("--runs must be at least 1")
	}
	if opts.jobs < 1 {
		return nil, errors.New("--jobs must be at least 1")
	}
	if opts.runTimeout < 0 {
		return nil, errors.New("--run-timeout must not be negative")
	}
	if opts.installTimeout < 0 {
		return nil, errors.New("--install-timeout must not be negative")
	}
	if opts.overallTimeout < 0 {
		return nil, errors.New("--overall-timeout must not be negative")
	}
	if opts.quiet && opts.verbose {
		return nil, errors.New("--quiet and --verbose cannot be used together")
	}
	if opts.resume && opts.checkpoint == "" {
		return nil, errors.New("--resume requires a non-empty --checkpoint path")
	}
	if len(command) == 0 {
		return nil, errors.New("no verification command given after \"--\"")
	}
	opts.command = command
	return opts, nil
}

// runMain parses `depbisect run` arguments, executes the bisection, writes the
// report files, prints the summary, and returns the mapped exit code.
func runMain(args []string, stdout, stderr io.Writer, version string) int {
	opts, err := parseRunArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "depbisect: %v\nRun \"depbisect help\" for usage.\n", err)
		return ExitError
	}

	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	go func() {
		// After the first signal cancels the run, restore default signal
		// behavior so a second Ctrl-C terminates immediately even if
		// cleanup hangs.
		<-ctx.Done()
		stop()
	}()

	runner := execx.Local{}
	git := gitx.New(runner, opts.repo)

	var stream io.Writer
	if opts.verbose {
		stream = stderr
	}
	var checkpointStore engine.CheckpointStore
	if opts.checkpoint != "" && !opts.dryRun {
		checkpointStore = checkpoint.NewFileStore(opts.checkpoint)
	}
	progressWriter := stderr
	if opts.quiet {
		progressWriter = io.Discard
	}
	progressOutput := newProgress(progressWriter, opts.verbose, opts.style)
	eng := &engine.Engine{
		Git: git,
		NewInstaller: func(m pm.Manager) engine.Installer {
			return pm.Installer{Runner: runner, Manager: m}
		},
		Verifier: engine.HarnessVerifier{Harness: verify.Harness{
			Runner:  runner,
			Command: opts.command,
			Runs:    opts.runs,
			Timeout: opts.runTimeout,
			Stream:  stream,
		}},
		Progress: progressOutput,
	}

	res, err := eng.Run(ctx, engine.Options{
		BaseRev:       opts.base,
		ToRev:         opts.to,
		Command:       opts.command,
		Runs:          opts.runs,
		Jobs:          opts.jobs,
		PMOverride:    opts.pm,
		KeepWorktrees: opts.keepWorktrees,
		DryRun:        opts.dryRun,
		Stream:        stream,
		Checkpoint:    checkpointStore,
		Resume:        opts.resume,
		CheckpointContext: fmt.Sprintf("run-timeout=%s;install-timeout=%s;overall-timeout=%s",
			opts.runTimeout, opts.installTimeout, opts.overallTimeout),
		InstallTimeout: opts.installTimeout,
		OverallTimeout: opts.overallTimeout,
	})
	if err != nil {
		progressOutput.clearActiveTrial()
		if errors.Is(err, context.Canceled) {
			fmt.Fprint(stderr, "depbisect: interrupted; temporary worktrees were cleaned up")
		} else {
			fmt.Fprintf(stderr, "depbisect: %v", err)
		}
		if checkpointStore != nil {
			fmt.Fprintf(stderr, "; completed trials remain in %s (resume with --resume)", opts.checkpoint)
		}
		fmt.Fprintln(stderr)
		return ExitError
	}

	mdPath, jsonPath := "", ""
	if !opts.noReports && res.Outcome != engine.OutcomeDryRun {
		rep := report.New(res, version)
		if opts.reportJSON != "" {
			if err := writeJSONReport(rep, opts.reportJSON); err != nil {
				fmt.Fprintf(stderr, "depbisect: %v\n", err)
			} else {
				jsonPath = opts.reportJSON
			}
		}
		if opts.reportMD != "" {
			if err := os.WriteFile(opts.reportMD, rep.Markdown(), 0o644); err != nil {
				fmt.Fprintf(stderr, "depbisect: write Markdown report: %v\n", err)
			} else {
				mdPath = opts.reportMD
			}
		}
	}

	printSummary(stdout, res, mdPath, jsonPath, opts.style)
	return exitCodeFor(res.Outcome)
}

func writeJSONReport(rep *report.Report, path string) error {
	data, err := rep.JSON()
	if err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}
	return nil
}

// exitCodeFor maps an engine outcome to the documented exit code.
func exitCodeFor(outcome string) int {
	switch outcome {
	case engine.OutcomeMinimalFound, engine.OutcomeDryRun:
		return ExitOK
	case engine.OutcomeNotReproduced:
		return ExitNotReproduced
	case engine.OutcomeFailsAtBase:
		return ExitFailsAtBase
	case engine.OutcomeInconclusive:
		return ExitInconclusive
	case engine.OutcomeNoChanges:
		return ExitNoChanges
	default:
		return ExitError
	}
}
