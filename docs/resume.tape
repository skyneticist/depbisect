# docs/resume.tape — VHS script for the README "resumable" GIF.
#
# Records DepBisect being interrupted mid-bisection with Ctrl-C and then picked
# back up with --resume: completed trials are restored from the on-disk
# checkpoint instead of being re-run. Uses the larger offline jobs example
# (eighteen dependency changes, no network required).
#
#   Install VHS:  brew install vhs        (or: go install github.com/charmbracelet/vhs@latest)
#   Generate:     vhs docs/resume.tape    ->  docs/resume.gif
#
# Tune the Sleep before Ctrl-C to your machine: it must land *during* the
# bisection so there are completed trials to restore. The hidden Sleep only
# covers the one-time build + example-repo generation.

Output docs/resume.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 760
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 40ms
Set Framerate 30

# --- Setup (hidden): build, generate the demo repo, clear any old checkpoint ---
Hide
Type "./examples/make-demo-jobs.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && rm -f .depbisect-checkpoint.jsonl depbisect-report.* && clear"
Enter
Sleep 8s
Show

# --- Start a run, then interrupt it partway through ---
# --jobs 4 keeps the run brief; Ctrl-C lands mid-bisection so there are
# completed trials to checkpoint.
Type "./depbisect run --repo examples/demo-jobs/app --base HEAD~1 --runs 3 --jobs 4 -- node test.js"
Sleep 600ms
Enter
Sleep 3s
Ctrl+C
Sleep 2s

# --- Resume: the same command plus --resume restores the completed trials ---
Type "./depbisect run --repo examples/demo-jobs/app --base HEAD~1 --runs 3 --jobs 4 --resume -- node test.js"
Sleep 600ms
Enter
Sleep 6s
