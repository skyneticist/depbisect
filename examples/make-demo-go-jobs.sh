#!/usr/bin/env sh
# Generates examples/demo-go-jobs/: a larger offline Go repository built to show
# off --jobs. Its second commit bumps eighteen modules, but the failure appears
# only once all eight `consensus-*` participants upgrade together — a split-brain
# that compatibility mode masks while any single one still speaks v1. The minimal
# failing set is therefore all eight participants; the other ten bumps are noise.
#
# Because the eight culprits fail only in combination, ddmin evaluates wide
# batches of candidate subsets at each granularity level and the closing
# 1-minimality proof tests eight neighbors at once — exactly the batch work
# --jobs spreads across worktrees. The minimal set is identical at any job count.
#
# Go modules keep their import path within a major version, so every bump is
# v1.0.0 -> v1.1.0. Everything is offline: modules are served from a generated
# file:// proxy with GOSUMDB=off.
set -eu

for tool in git go zip; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
chmod -R u+w demo-go-jobs 2>/dev/null || true
rm -rf demo-go-jobs
mkdir -p demo-go-jobs
ROOT=$(cd demo-go-jobs && pwd)
APP="$ROOT/app"
PROXY="$ROOT/proxy"
STG="$ROOT/.staging"
mkdir -p "$APP" "$PROXY" "$STG"

# The eight interacting culprits and the ten noise modules.
CULPRITS="consensus-auth consensus-codec consensus-gossip consensus-ledger consensus-router consensus-session consensus-stream consensus-sync"
NOISE="lib-cache lib-color lib-config lib-format lib-hash lib-json lib-log lib-parse lib-time lib-uuid"
JOBS=4

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

run_go() {
    GOFLAGS=-mod=mod GOPROXY="file://$PROXY" GOSUMDB=off GOTOOLCHAIN=local GOWORK=off go "$@"
}

# add_module MODPATH VERSION PACKAGE BODY writes one module version into the file
# proxy (.info/.mod/list + a source zip prefixed "<modpath>@<version>/").
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

# Culprits: v1 speaks the compatible protocol, the bump speaks the new one.
for n in $CULPRITS; do
    pkg=$(printf '%s' "$n" | tr -d '-')
    add_module "example.com/$n" v1.0.0 "$pkg" 'func F() string { return "v1" }'
    add_module "example.com/$n" v1.1.0 "$pkg" 'func F() string { return "v2" }'
done

# Noise: both versions return the module name, so a botched install still fails
# the test rather than being silently discarded.
for n in $NOISE; do
    pkg=$(printf '%s' "$n" | tr -d '-')
    add_module "example.com/$n" v1.0.0 "$pkg" "func F() string { return \"$n\" }"
    add_module "example.com/$n" v1.1.0 "$pkg" "func F() string { return \"$n\" }"
done

# --- generate the app: aliased imports + the all-or-nothing check -------
{
printf 'package app\n\nimport (\n\t"errors"\n\t"fmt"\n\n'
i=0; for n in $CULPRITS; do printf '\tc%d "example.com/%s"\n' "$i" "$n"; i=$((i+1)); done
i=0; for n in $NOISE; do printf '\tn%d "example.com/%s"\n' "$i" "$n"; i=$((i+1)); done
printf ')\n\n'
printf 'func protocols() []string {\n\treturn []string{'
i=0; sep=''; for n in $CULPRITS; do printf '%sc%d.F()' "$sep" "$i"; sep=', '; i=$((i+1)); done
printf '}\n}\n\n'
printf 'var noise = map[string]string{\n'
i=0; for n in $NOISE; do printf '\t"%s": n%d.F(),\n' "$n" "$i"; i=$((i+1)); done
printf '}\n\n'
cat <<'GO'
// Run fails only when every participant reports v2 at once; while any one still
// reports v1, compatibility mode holds. The noise modules are checked first so a
// broken install is a hard error rather than a misattributed failure.
func Run() error {
	for name, got := range noise {
		if got != name {
			return fmt.Errorf("%s: broken install (got %q)", name, got)
		}
	}
	for _, p := range protocols() {
		if p != "v2" {
			return nil
		}
	}
	return errors.New("split-brain: all participants upgraded to v2 at once")
}
GO
} > "$APP/app.go"

cat > "$APP/app_test.go" <<'EOF'
package app

import "testing"

func TestQuorum(t *testing.T) {
	if err := Run(); err != nil {
		t.Fatal(err)
	}
}
EOF

write_gomod() {
    {
        printf 'module example.com/app\n\ngo 1.20\n\nrequire (\n'
        for n in $CULPRITS $NOISE; do printf '\texample.com/%s %s\n' "$n" "$1"; done
        printf ')\n'
    } > "$APP/go.mod"
}

cd "$APP"
git init -q -b main

# base: every participant on the compatible protocol
write_gomod v1.0.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "base: all consensus participants on the compatible protocol"

# bump eighteen modules; only the eight consensus-* fail, and only together
write_gomod v1.1.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "bump eighteen modules"

rm -rf "$STG"

echo "Jobs Go demo created at examples/demo-go-jobs/app"
echo "Eighteen modules changed; the eight consensus-* updates fail only together."
echo "Compare wall time across --jobs values (the result is identical):"
echo
echo "  go build ./cmd/depbisect"
echo "  GOPROXY=\"file://$PROXY\" GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local \\"
echo "    ./depbisect run --repo examples/demo-go-jobs/app --base HEAD~1 --runs 3 --jobs $JOBS -- go test ./..."
echo
echo "Expected minimal failing set: all eight example.com/consensus-* modules."
