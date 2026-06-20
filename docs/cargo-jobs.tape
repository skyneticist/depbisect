# docs/cargo-jobs.tape — VHS script for the Cargo --jobs demo GIF.
#
#   Generate:  vhs docs/cargo-jobs.tape  ->  docs/cargo-jobs.gif
#
# Records DepBisect isolating the eight consensus_* crates (out of eighteen
# bumps) that fail only together, evaluated across four worktrees in parallel.
# Fully offline via the committed vendored registry.

Output docs/cargo-jobs.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 1000
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 75ms

Hide
Type "./examples/make-demo-cargo-jobs.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && clear"
Enter
Sleep 22s
Show

Type "./depbisect run --repo examples/demo-cargo-jobs/app --base HEAD~1 --runs 3 --jobs 4 -- cargo test"
Sleep 600ms
Enter
Sleep 20s
