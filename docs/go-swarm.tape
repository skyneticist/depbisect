# docs/go-swarm.tape — VHS script for the Go swarm (--jobs 12) demo GIF.
#
#   Generate:  vhs docs/go-swarm.tape  ->  docs/go-swarm.gif
#
# Records DepBisect isolating the twelve replica-* modules (out of twenty-eight
# bumps) that fail only together, evaluated across twelve worktrees in parallel.

Output docs/go-swarm.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 1180
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 75ms

Hide
Type "./examples/make-demo-go-swarm.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && export GOPROXY=file://$PWD/examples/demo-go-swarm/proxy GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local && clear"
Enter
Sleep 35s
Show

Type "./depbisect run --repo examples/demo-go-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- go test ./..."
Sleep 600ms
Enter
Sleep 30s
