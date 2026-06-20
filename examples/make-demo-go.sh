#!/usr/bin/env sh
# Generates examples/demo-go/: a small git repository with two commits that bump
# two modules, one of which (breakage v1.0.0 -> v1.1.0) breaks `go test`.
# Everything is offline: dependencies are served from a committed file-based
# module proxy (GOPROXY=file://...) with GOSUMDB=off, so the go tool never
# touches the network. Re-run the script any time to start fresh.
set -eu

for tool in git go zip; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
chmod -R u+w demo-go 2>/dev/null || true
rm -rf demo-go
mkdir -p demo-go
ROOT=$(cd demo-go && pwd)
APP="$ROOT/app"
PROXY="$ROOT/proxy"
STG="$ROOT/.staging"
mkdir -p "$APP" "$PROXY" "$STG"

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# run_go invokes the go tool fully offline against the local file proxy.
run_go() {
    GOFLAGS=-mod=mod GOPROXY="file://$PROXY" GOSUMDB=off GOTOOLCHAIN=local GOWORK=off go "$@"
}

# add_module MODPATH VERSION PACKAGE BODY writes one module version into the
# file proxy: the .info/.mod/list metadata plus a source zip whose entries are
# prefixed with "<modpath>@<version>/" as the module-zip format requires.
add_module() {
    mod="$1"; ver="$2"; pkg="$3"; body="$4"
    vdir="$PROXY/$mod/@v"
    mkdir -p "$vdir"
    printf 'module %s\n\ngo 1.20\n' "$mod" > "$vdir/$ver.mod"
    printf '{"Version":"%s","Time":"2020-01-01T00:00:00Z"}\n' "$ver" > "$vdir/$ver.info"
    echo "$ver" >> "$vdir/list"
    src="$STG/$mod@$ver"
    mkdir -p "$src"
    printf 'module %s\n\ngo 1.20\n' "$mod" > "$src/go.mod"
    printf 'package %s\n\n%s\n' "$pkg" "$body" > "$src/lib.go"
    ( cd "$STG" && zip -qr "$vdir/$ver.zip" "$mod@$ver" )
}

# --- file proxy: two versions of each module ----------------------------
# breakage v1.1.0 regresses (Healthy returns false); helper is harmless noise.
add_module example.com/breakage v1.0.0 breakage 'func Healthy() bool { return true }'
add_module example.com/breakage v1.1.0 breakage 'func Healthy() bool { return false } // regression'
add_module example.com/helper   v1.0.0 helper   'func Touch() {}'
add_module example.com/helper   v1.1.0 helper   'func Touch() {} // harmless bump'

# --- the app repository -------------------------------------------------
cat > "$APP/app.go" <<'EOF'
// Package app runs a health check whose result depends on the breakage module.
package app

import (
	"example.com/breakage"
	"example.com/helper"
)

// Run reports whether the app is healthy.
func Run() bool {
	helper.Touch()
	return breakage.Healthy()
}
EOF

cat > "$APP/app_test.go" <<'EOF'
package app

import "testing"

func TestHealthy(t *testing.T) {
	if !Run() {
		t.Fatal("app regressed after a dependency bump")
	}
}
EOF

write_gomod() {
    cat > "$APP/go.mod" <<EOF
module example.com/app

go 1.20

require (
	example.com/breakage $1
	example.com/helper $2
)
EOF
}

cd "$APP"
git init -q -b main

# base: working dependencies. `go build` compiles the app and records full
# go.sum checksums (content + go.mod) for both modules, offline via the proxy,
# matching what a real project commits. go.sum is reset each commit so it holds
# only that revision's versions.
write_gomod v1.0.0 v1.0.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "base: working dependencies"

# bump both modules; only the breakage bump is the culprit
write_gomod v1.1.0 v1.1.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "bump breakage and helper"

rm -rf "$STG"

echo "Demo repository created at examples/demo-go/app"
echo "The test fails at HEAD and passed one commit earlier. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  GOPROXY=\"file://$PROXY\" GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local \\"
echo "    ./depbisect run --repo examples/demo-go/app --base HEAD~1 --runs 3 -- go test ./..."
echo
echo "Expected result: minimal failing set = example.com/breakage v1.0.0 -> v1.1.0"
