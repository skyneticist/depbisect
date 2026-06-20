#!/usr/bin/env sh
# Generates examples/demo-cargo-jobs/: a larger offline Cargo repository built to
# show off --jobs. Its second commit bumps eighteen crates, but the failure
# appears only once all eight `consensus_*` participants upgrade together — a
# split-brain that compatibility mode masks while any single one stays on 1.0.0.
# The minimal failing set is therefore all eight participants; the other ten
# bumps are harmless noise, spread across [dependencies] and [dev-dependencies].
#
# Because the eight culprits fail only in combination, ddmin evaluates wide
# batches of candidate subsets at each granularity level and the closing
# 1-minimality proof tests eight neighbors at once — exactly the batch work
# --jobs spreads across worktrees. The minimal set is identical at any job count.
# Fully offline via a committed vendored directory registry.
set -eu

for tool in git cargo; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-cargo-jobs
mkdir -p demo-cargo-jobs
ROOT=$(cd demo-cargo-jobs && pwd)
APP="$ROOT/app"
mkdir -p "$APP/.cargo" "$APP/src" "$APP/tests" "$APP/vendor"

# Eight interacting culprits, eight noise crates in [dependencies], two in
# [dev-dependencies]. Crate names use underscores (Rust identifiers).
CULPRITS="consensus_auth consensus_codec consensus_gossip consensus_ledger consensus_router consensus_session consensus_stream consensus_sync"
NOISE_DEP="lib_cache lib_color lib_config lib_format lib_hash lib_json lib_log lib_parse"
NOISE_DEV="lib_time lib_uuid"
JOBS=4

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

# Culprits expose is_new(): false at 1.0.0, true at 2.0.0. Noise crates are inert.
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

# src/lib.rs: healthy unless every consensus participant has upgraded. The
# [dependencies] noise crates are touched so a botched install fails to compile.
{
    printf '// Split-brain only when every consensus participant reports is_new();\n'
    printf '// while any one is still on 1.0.0 compatibility mode holds.\n'
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
git_commit "base: all consensus participants on the compatible protocol"

write_manifest 2.0.0
cargo generate-lockfile -q
git add .
git_commit "bump eighteen crates"

echo "Jobs Cargo demo created at examples/demo-cargo-jobs/app"
echo "Eighteen crates changed; the eight consensus_* updates fail only together."
echo "Compare wall time across --jobs values (the result is identical):"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-cargo-jobs/app --base HEAD~1 --runs 3 --jobs $JOBS -- cargo test"
echo
echo "Expected minimal failing set: all eight consensus_* crates."
