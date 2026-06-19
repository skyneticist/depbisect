# docs/demo.tape — VHS script for the README demo GIF.
#
# Records DepBisect finding the one breaking dependency in the bundled offline
# example repo (no network required).
#
#   Install VHS:  brew install vhs        (or: go install github.com/charmbracelet/vhs@latest)
#   Generate:     vhs docs/demo.tape  ->  docs/demo.gif
#   Then:         commit docs/demo.gif and enable the embed in README.md
#
# Tune the Sleep values to your machine: the GIF records in real time, so the
# final Sleep must cover the actual bisection run (a few seconds offline).

Output docs/demo.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 720
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 75ms

# --- Setup (hidden): build the binary and generate the demo repo ---
Hide
Type "./examples/make-demo-complex.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && clear"
Enter
Sleep 25s
Show

# --- The actual run ---
# --jobs 4 evaluates candidate subsets across four isolated worktrees in
# parallel; the minimal set is identical to a sequential run, just found faster.
Type "./depbisect run --repo examples/demo-complex/app --base HEAD~1 --runs 3 --jobs 4 -- node test.js"
Sleep 600ms
Enter
Sleep 15s
