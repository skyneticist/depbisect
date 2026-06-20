#!/usr/bin/env sh
# Generates examples/demo-cargo/: a small git repository with two commits that
# bump two crates, one of which (breakage 1.0.0 -> 2.0.0) breaks `cargo test`.
# Everything is offline: dependencies are served from a committed vendored
# directory registry and `[net] offline = true`, so cargo never touches a
# network registry. Re-run the script any time to start fresh.
set -eu

for tool in git cargo; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-cargo
mkdir -p demo-cargo
ROOT=$(cd demo-cargo && pwd)
APP="$ROOT/app"
mkdir -p "$APP/.cargo" "$APP/src" "$APP/tests" "$APP/vendor"

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# vendor_crate NAME VERSION LIB_RS writes a crate into the directory registry.
# An empty checksum map is all a directory source requires.
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

# --- vendored registry: two versions of each crate ----------------------
# breakage 2.0.0 regresses (healthy() returns false); helper is harmless noise.
vendor_crate breakage 1.0.0 'pub fn healthy() -> bool { true }'
vendor_crate breakage 2.0.0 'pub fn healthy() -> bool { false } // regression'
vendor_crate helper   1.0.0 'pub fn touch() {}'
vendor_crate helper   2.0.0 'pub fn touch() {} // harmless bump'

# --- the app repository -------------------------------------------------
cat > "$APP/.cargo/config.toml" <<EOF
# Serve all crates from the committed vendor/ directory, fully offline, so the
# demo bisects without any network access.
[source.crates-io]
replace-with = "vendored"

[source.vendored]
directory = "vendor"

[net]
offline = true
EOF

cat > "$APP/src/lib.rs" <<'EOF'
pub fn run() -> bool {
    helper::touch();
    breakage::healthy()
}
EOF

cat > "$APP/tests/regression.rs" <<'EOF'
#[test]
fn app_is_healthy() {
    assert!(demo_app::run(), "app regressed after a dependency bump");
}
EOF

echo "/target" > "$APP/.gitignore"

write_manifest() {
    cat > "$APP/Cargo.toml" <<EOF
[package]
name = "demo-app"
version = "0.1.0"
edition = "2021"

[dependencies]
breakage = "$1"
helper = "$2"
EOF
}

cd "$APP"
git init -q -b main

# base: working dependencies
write_manifest 1.0.0 1.0.0
cargo generate-lockfile -q
git add .
git_commit "base: working dependencies"

# bump both crates; only the breakage bump is the culprit
write_manifest 2.0.0 2.0.0
cargo generate-lockfile -q
git add .
git_commit "bump breakage and helper"

echo "Demo repository created at examples/demo-cargo/app"
echo "The test fails at HEAD and passed one commit earlier. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-cargo/app --base HEAD~1 --runs 3 -- cargo test"
echo
echo "Expected result: minimal failing set = breakage 1.0.0 -> 2.0.0"
