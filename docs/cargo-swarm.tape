# docs/cargo-swarm.tape — VHS script for the Cargo swarm (--jobs 12) demo GIF.
#
#   Generate:  vhs docs/cargo-swarm.tape  ->  docs/cargo-swarm.gif
#
# Records DepBisect isolating the twelve replica_* crates (out of twenty-eight
# bumps) that fail only together, evaluated across twelve worktrees in parallel.
# Fully offline via the committed vendored registry.

Output docs/cargo-swarm.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 1180
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 75ms

Hide
Type "./examples/make-demo-cargo-swarm.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && clear"
Enter
Sleep 28s
Show

Type "./depbisect run --repo examples/demo-cargo-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- cargo test"
Sleep 600ms
Enter
Sleep 30s
