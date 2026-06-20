# docs/go-complex.tape — VHS script for the Go complex demo GIF.
#
#   Generate:  vhs docs/go-complex.tape  ->  docs/go-complex.gif
#
# Records DepBisect narrowing twelve Go module bumps to the five-module set that
# breaks the build, fully offline (generated file:// module proxy).

Output docs/go-complex.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 820
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 75ms

# --- Setup (hidden): generate the demo, build the binary, set the offline env ---
Hide
Type "./examples/make-demo-go-complex.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && export GOPROXY=file://$PWD/examples/demo-go-complex/proxy GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local && clear"
Enter
Sleep 25s
Show

# --- The actual run ---
Type "./depbisect run --repo examples/demo-go-complex/app --base HEAD~1 --runs 3 -- go test ./..."
Sleep 600ms
Enter
Sleep 14s
