#!/usr/bin/env sh
# Generates examples/demo-go-complex/: a Go repository whose second commit bumps
# twelve modules. Five updates are required to reproduce the failure:
#   - wire-encoder, wire-transport, and wire-decoder break the primary path only
#     when all three new versions are in use.
#   - cache-writer and cache-reader break the fallback path only when both new
#     versions are in use.
# The app fails only when both paths are broken, so the expected minimal failing
# set is all five. The other seven bumps are harmless noise.
#
# Go modules keep the same import path within a major version, so every bump is
# v1.0.0 -> v1.1.0 (a v2.0.0 would change the path); the "v1"/"v2" protocol is
# encoded in behavior, not the version number. Everything is offline: modules are
# served from a generated file:// proxy with GOSUMDB=off.
set -eu

for tool in git go zip; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
chmod -R u+w demo-go-complex 2>/dev/null || true
rm -rf demo-go-complex
mkdir -p demo-go-complex
ROOT=$(cd demo-go-complex && pwd)
APP="$ROOT/app"
PROXY="$ROOT/proxy"
STG="$ROOT/.staging"
mkdir -p "$APP" "$PROXY" "$STG"

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

run_go() {
    GOFLAGS=-mod=mod GOPROXY="file://$PROXY" GOSUMDB=off GOTOOLCHAIN=local GOWORK=off go "$@"
}

# add_module MODPATH VERSION PACKAGE BODY writes one module version into the file
# proxy (.info/.mod/list + a source zip prefixed "<modpath>@<version>/"). BODY is
# the file content after the package clause and may include its own imports.
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

# --- seven harmless noise modules (identical across versions) -----------
for n in analytics-core feature-flags logger metrics-sink request-id retry-policy schema-tools; do
    pkg=$(printf '%s' "$n" | tr -d '-')
    add_module "example.com/$n" v1.0.0 "$pkg" "func F() string { return \"$n\" }"
    add_module "example.com/$n" v1.1.0 "$pkg" "func F() string { return \"$n\" }"
done

# --- primary path: fails only when all three new versions are in use ----
add_module example.com/wire-encoder v1.0.0 wireencoder 'func F(v string) string { return "v1:" + v }'
add_module example.com/wire-encoder v1.1.0 wireencoder 'func F(v string) string { return "v2:" + v }'
add_module example.com/wire-transport v1.0.0 wiretransport 'func F(p string) string { return p }'
add_module example.com/wire-transport v1.1.0 wiretransport 'import "strings"

func F(p string) string {
	if strings.HasPrefix(p, "v2:") {
		return p + "#checksum=inline"
	}
	return p
}'
add_module example.com/wire-decoder v1.0.0 wiredecoder 'import (
	"errors"
	"strings"
)

func F(p string) (string, error) {
	clean := strings.Split(p, "#")[0]
	if !strings.HasPrefix(clean, "v1:") && !strings.HasPrefix(clean, "v2:") {
		return "", errors.New("unknown wire format")
	}
	return clean[3:], nil
}'
add_module example.com/wire-decoder v1.1.0 wiredecoder 'import (
	"errors"
	"strings"
)

func F(p string) (string, error) {
	if strings.Contains(p, "#") {
		return "", errors.New("inline checksum rejected")
	}
	if !strings.HasPrefix(p, "v1:") && !strings.HasPrefix(p, "v2:") {
		return "", errors.New("unknown wire format")
	}
	return p[3:], nil
}'

# --- fallback path: the new writer and strict reader clash -------------
add_module example.com/cache-writer v1.0.0 cachewriter 'func F(v string) string { return v }'
add_module example.com/cache-writer v1.1.0 cachewriter 'func F(v string) string { return v + "\n" }'
add_module example.com/cache-reader v1.0.0 cachereader 'import "strings"

func F(p string) (string, error) { return strings.TrimSpace(p), nil }'
add_module example.com/cache-reader v1.1.0 cachereader 'import (
	"errors"
	"strings"
)

func F(p string) (string, error) {
	if p != strings.TrimSpace(p) {
		return "", errors.New("trailing data rejected")
	}
	return p, nil
}'

# --- the app -----------------------------------------------------------
cat > "$APP/app.go" <<'EOF'
// Package app round-trips a value through a primary wire path and, if that
// fails, a cache fallback. It is healthy while either path works.
package app

import (
	"errors"
	"fmt"

	analyticscore "example.com/analytics-core"
	cachereader "example.com/cache-reader"
	cachewriter "example.com/cache-writer"
	featureflags "example.com/feature-flags"
	logger "example.com/logger"
	metricssink "example.com/metrics-sink"
	requestid "example.com/request-id"
	retrypolicy "example.com/retry-policy"
	schematools "example.com/schema-tools"
	wiredecoder "example.com/wire-decoder"
	wireencoder "example.com/wire-encoder"
	wiretransport "example.com/wire-transport"
)

func primary(v string) (string, error) {
	return wiredecoder.F(wiretransport.F(wireencoder.F(v)))
}

func fallback(v string) (string, error) {
	return cachereader.F(cachewriter.F(v))
}

// Run reports an error if both the primary and fallback round-trips fail. The
// noise modules are exercised first so a botched install is a hard error rather
// than a misattributed path failure.
func Run() error {
	noise := map[string]string{
		"analytics-core": analyticscore.F(),
		"feature-flags":  featureflags.F(),
		"logger":         logger.F(),
		"metrics-sink":   metricssink.F(),
		"request-id":     requestid.F(),
		"retry-policy":   retrypolicy.F(),
		"schema-tools":   schematools.F(),
	}
	for name, got := range noise {
		if got != name {
			return fmt.Errorf("%s: broken install (got %q)", name, got)
		}
	}
	if v, err := primary("hello"); err == nil && v == "hello" {
		return nil
	}
	if v, err := fallback("hello"); err == nil && v == "hello" {
		return nil
	}
	return errors.New("both primary and fallback paths failed")
}
EOF

cat > "$APP/app_test.go" <<'EOF'
package app

import "testing"

func TestRoundTrip(t *testing.T) {
	if err := Run(); err != nil {
		t.Fatal(err)
	}
}
EOF

write_gomod() {
    cat > "$APP/go.mod" <<EOF
module example.com/app

go 1.20

require (
	example.com/analytics-core $1
	example.com/cache-reader $1
	example.com/cache-writer $1
	example.com/feature-flags $1
	example.com/logger $1
	example.com/metrics-sink $1
	example.com/request-id $1
	example.com/retry-policy $1
	example.com/schema-tools $1
	example.com/wire-decoder $1
	example.com/wire-encoder $1
	example.com/wire-transport $1
)
EOF
}

cd "$APP"
git init -q -b main

# base: every module on its compatible version
write_gomod v1.0.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "base: compatible primary and fallback paths"

# bump all twelve; only the five interacting updates matter
write_gomod v1.1.0
rm -f go.sum
run_go build ./...
git add -A
git_commit "bump twelve modules"

rm -rf "$STG"

echo "Complex Go demo created at examples/demo-go-complex/app"
echo "Twelve modules changed; five interacting updates are required to fail. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  GOPROXY=\"file://$PROXY\" GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local \\"
echo "    ./depbisect run --repo examples/demo-go-complex/app --base HEAD~1 --runs 3 -- go test ./..."
echo
echo "Expected minimal failing set:"
echo "  example.com/cache-reader, example.com/cache-writer, example.com/wire-decoder,"
echo "  example.com/wire-encoder, example.com/wire-transport"
