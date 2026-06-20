#!/usr/bin/env sh
# Generates examples/demo-go-swarm/: the largest offline Go example, built to make
# --jobs dramatic. Its second commit bumps TWENTY-EIGHT modules, but the cluster
# loses quorum only once all twelve `replica-*` nodes migrate to the new wire
# format together; while any single node still speaks v1 a translation shim keeps
# the tests green. The minimal failing set is all twelve replicas; the other
# sixteen `lib-*` bumps are harmless noise.
#
# Because the twelve culprits fail only in combination, ddmin evaluates very wide
# candidate batches at every granularity level and the closing 1-minimality proof
# tests twelve neighbors at once — exactly the batch work --jobs spreads across
# worktrees, so a higher job count finishes the same bisection markedly sooner.
#
# Go modules keep their import path within a major version, so every bump is
# v1.0.0 -> v1.1.0. Everything is offline: modules are served from a generated
# file:// proxy with GOSUMDB=off.
set -eu

for tool in git go zip; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
chmod -R u+w demo-go-swarm 2>/dev/null || true
rm -rf demo-go-swarm
mkdir -p demo-go-swarm
ROOT=$(cd demo-go-swarm && pwd)
APP="$ROOT/app"
PROXY="$ROOT/proxy"
STG="$ROOT/.staging"
mkdir -p "$APP" "$PROXY" "$STG"

# The twelve interacting culprits and the sixteen noise modules.
CULPRITS="replica-01 replica-02 replica-03 replica-04 replica-05 replica-06 replica-07 replica-08 replica-09 replica-10 replica-11 replica-12"
NOISE="lib-async lib-buffer lib-cache lib-color lib-config lib-crypto lib-format lib-hash lib-json lib-log lib-parse lib-stream lib-time lib-uuid lib-yaml lib-zip"
JOBS=12

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

# Replica nodes: v1 speaks the legacy wire format, the bump speaks the new one.
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
printf 'func wire() []string {\n\treturn []string{'
i=0; sep=''; for n in $CULPRITS; do printf '%sc%d.F()' "$sep" "$i"; sep=', '; i=$((i+1)); done
printf '}\n}\n\n'
printf 'var noise = map[string]string{\n'
i=0; for n in $NOISE; do printf '\t"%s": n%d.F(),\n' "$n" "$i"; i=$((i+1)); done
printf '}\n\n'
cat <<'GO'
// Run loses quorum only when every replica reports v2 at once; while any one
// still reports v1, the translation shim holds. The noise modules are checked
// first so a broken install is a hard error rather than a misattributed failure.
func Run() error {
	for name, got := range noise {
		if got != name {
			return fmt.Errorf("%s: broken install (got %q)", name, got)
		}
	}
	for _, p := range wire() {
		if p != "v2" {
			return nil
		}
	}
	return errors.New("quorum lost: all replicas migrated to v2 at once")
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

# base: every replica on the legacy wire format
write_gomod v1.0.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "base: all replicas on the legacy wire format"

# bump twenty-eight modules; only the twelve replica-* fail, and only together
write_gomod v1.1.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "bump twenty-eight modules"

rm -rf "$STG"

echo "Swarm Go demo created at examples/demo-go-swarm/app"
echo "Twenty-eight modules changed; the twelve replica-* updates fail only together."
echo "Compare wall time across --jobs values (the result is identical):"
echo
echo "  go build ./cmd/depbisect"
echo "  GOPROXY=\"file://$PROXY\" GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local \\"
echo "    ./depbisect run --repo examples/demo-go-swarm/app --base HEAD~1 --runs 3 --jobs $JOBS -- go test ./..."
echo
echo "Expected minimal failing set: all twelve example.com/replica-* modules."
