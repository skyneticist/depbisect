# docs/dry-run.tape — VHS script for the README "preview" GIF.
#
# Records DepBisect's --dry-run: it diffs package.json between two revisions and
# prints the dependency changes and the plan, then exits WITHOUT installing or
# running anything. Uses the small offline example repo (no network required).
#
#   Install VHS:  brew install vhs        (or: go install github.com/charmbracelet/vhs@latest)
#   Generate:     vhs docs/dry-run.tape   ->  docs/dry-run.gif
#
# The visible run is near-instant; the hidden Sleep only needs to cover the
# one-time build + example-repo generation on the recording machine.

Output docs/dry-run.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 460
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 40ms
Set Framerate 30

# --- Setup (hidden): build the binary and generate the demo repo ---
Hide
Type "./examples/make-demo.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && clear"
Enter
Sleep 8s
Show

# --- The actual run ---
# --dry-run resolves --base..HEAD, diffs the declared dependencies, and reports
# the plan. Nothing is installed and the verification command is never executed.
Type "./depbisect run --repo examples/demo/app --base HEAD~1 --dry-run -- node test.js"
Sleep 600ms
Enter
Sleep 4s
