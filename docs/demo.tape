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
Set FontSize 18
Set Width 1100
Set Height 700
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 55ms

# --- Setup (hidden): build the binary and generate the demo repo ---
Hide
Type "./examples/make-demo.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && clear"
Enter
Sleep 25s
Show

# --- The actual run ---
Type "./depbisect run --repo examples/demo/app --base HEAD~1 --runs 3 -- node test.js"
Sleep 600ms
Enter
Sleep 12s
