# docs/go-jobs.tape — VHS script for the Go --jobs demo GIF.
#
#   Generate:  vhs docs/go-jobs.tape  ->  docs/go-jobs.gif
#
# Records DepBisect isolating the eight consensus-* modules (out of eighteen
# bumps) that fail only together, evaluated across four worktrees in parallel.

Output docs/go-jobs.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 1000
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 75ms

Hide
Type "./examples/make-demo-go-jobs.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && export GOPROXY=file://$PWD/examples/demo-go-jobs/proxy GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local && clear"
Enter
Sleep 28s
Show

Type "./depbisect run --repo examples/demo-go-jobs/app --base HEAD~1 --runs 3 --jobs 4 -- go test ./..."
Sleep 600ms
Enter
Sleep 20s
