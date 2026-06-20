#!/usr/bin/env sh
# Generates examples/demo-cargo-swarm/: the largest offline Cargo example, built
# to make --jobs dramatic. Its second commit bumps TWENTY-EIGHT crates, but the
# cluster loses quorum only once all twelve `replica_*` nodes migrate together;
# while any single node stays on 1.0.0 a translation shim keeps the tests green.
# The minimal failing set is all twelve replicas; the other sixteen `lib_*` bumps
# are harmless noise, spread across [dependencies] and [dev-dependencies].
#
# Because the twelve culprits fail only in combination, ddmin evaluates very wide
# candidate batches at every granularity level and the closing 1-minimality proof
# tests twelve neighbors at once — exactly the batch work --jobs spreads across
# worktrees, so a higher job count finishes the same bisection markedly sooner.
# Fully offline via a committed vendored directory registry.
set -eu

for tool in git cargo; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-cargo-swarm
mkdir -p demo-cargo-swarm
ROOT=$(cd demo-cargo-swarm && pwd)
APP="$ROOT/app"
mkdir -p "$APP/.cargo" "$APP/src" "$APP/tests" "$APP/vendor"

# Twelve interacting culprits, fourteen noise crates in [dependencies], two in
# [dev-dependencies]. Crate names use underscores (Rust identifiers).
CULPRITS="replica_01 replica_02 replica_03 replica_04 replica_05 replica_06 replica_07 replica_08 replica_09 replica_10 replica_11 replica_12"
NOISE_DEP="lib_async lib_buffer lib_cache lib_color lib_config lib_crypto lib_format lib_hash lib_json lib_log lib_parse lib_stream lib_time lib_uuid"
NOISE_DEV="lib_yaml lib_zip"
JOBS=12

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

vendor_crate() {
    d="$APP/vendor/$1-$2"
    mkdir -p "$d/src"
    cat > "$d/Cargo.toml" <<EOF
[package]
name = "$1"
version = "$2"
edition = "2021"
EOF
    printf '%s\n' "$3" > "$d/src/lib.rs"
    printf '{"files":{},"package":null}\n' > "$d/.cargo-checksum.json"
}

# Replica nodes expose is_new(): false at 1.0.0, true at 2.0.0. Noise is inert.
for name in $CULPRITS; do
    vendor_crate "$name" 1.0.0 'pub fn is_new() -> bool { false }'
    vendor_crate "$name" 2.0.0 'pub fn is_new() -> bool { true }'
done
for name in $NOISE_DEP $NOISE_DEV; do
    vendor_crate "$name" 1.0.0 'pub fn touch() {}'
    vendor_crate "$name" 2.0.0 'pub fn touch() {} // harmless bump'
done

cat > "$APP/.cargo/config.toml" <<EOF
[source.crates-io]
replace-with = "vendored"

[source.vendored]
directory = "vendor"

[net]
offline = true
EOF

# src/lib.rs: healthy unless every replica has migrated. The [dependencies] noise
# crates are touched so a botched install fails to compile.
{
    printf '// Quorum is lost only when every replica reports is_new(); while any one\n'
    printf '// is still on 1.0.0 the translation shim holds.\n'
    printf 'pub fn run() -> bool {\n'
    for c in $NOISE_DEP; do printf '    %s::touch();\n' "$c"; done
    printf '    let all_new = '
    sep=''; for c in $CULPRITS; do printf '%s%s::is_new()' "$sep" "$c"; sep=' && '; done
    printf ';\n'
    printf '    !all_new\n}\n'
} > "$APP/src/lib.rs"

# tests/regression.rs touches the [dev-dependencies] noise and asserts health.
{
    printf '#[test]\nfn app_is_healthy() {\n'
    for c in $NOISE_DEV; do printf '    %s::touch();\n' "$c"; done
    printf '    assert!(demo_app::run(), "app regressed after dependency bumps");\n}\n'
} > "$APP/tests/regression.rs"

echo "/target" > "$APP/.gitignore"

write_manifest() {
    v=$1
    {
        printf '[package]\nname = "demo-app"\nversion = "0.1.0"\nedition = "2021"\n\n'
        printf '[dependencies]\n'
        for c in $CULPRITS $NOISE_DEP; do printf '%s = "%s"\n' "$c" "$v"; done
        printf '\n[dev-dependencies]\n'
        for c in $NOISE_DEV; do printf '%s = "%s"\n' "$c" "$v"; done
    } > "$APP/Cargo.toml"
}

cd "$APP"
git init -q -b main

write_manifest 1.0.0
cargo generate-lockfile -q
git add .
git_commit "base: all replicas on the legacy wire format"

write_manifest 2.0.0
cargo generate-lockfile -q
git add .
git_commit "bump twenty-eight crates"

echo "Swarm Cargo demo created at examples/demo-cargo-swarm/app"
echo "Twenty-eight crates changed; the twelve replica_* updates fail only together."
echo "Compare wall time across --jobs values (the result is identical):"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-cargo-swarm/app --base HEAD~1 --runs 3 --jobs $JOBS -- cargo test"
echo
echo "Expected minimal failing set: all twelve replica_* crates."
