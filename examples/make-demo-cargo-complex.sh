#!/usr/bin/env sh
# Generates examples/demo-cargo-complex/: an offline Cargo repository whose
# second commit bumps twelve crates across [dependencies] and [dev-dependencies].
# The app has a primary wire path and a cache fallback; it fails only when BOTH
# break, which needs all three wire crates AND both cache crates at their new
# versions. The result is a five-crate minimal failing set among twelve changes.
# Like make-demo-cargo.sh it is fully offline (vendored directory registry).
set -eu

for tool in git cargo; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-cargo-complex
mkdir -p demo-cargo-complex
ROOT=$(cd demo-cargo-complex && pwd)
APP="$ROOT/app"
mkdir -p "$APP/.cargo" "$APP/src" "$APP/tests" "$APP/vendor"

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

# --- vendored registry --------------------------------------------------
# Culprits expose is_new(); their 2.0.0 returns true. Noise crates are inert.
for name in wire_encoder wire_transport wire_decoder cache_writer cache_reader; do
    vendor_crate "$name" 1.0.0 'pub fn is_new() -> bool { false }'
    vendor_crate "$name" 2.0.0 'pub fn is_new() -> bool { true }'
done
for name in noise_a noise_b noise_c noise_d noise_e noise_f noise_g; do
    vendor_crate "$name" 1.0.0 'pub fn touch() {}'
    vendor_crate "$name" 2.0.0 'pub fn touch() {} // harmless bump'
done

# --- the app repository -------------------------------------------------
cat > "$APP/.cargo/config.toml" <<EOF
[source.crates-io]
replace-with = "vendored"

[source.vendored]
directory = "vendor"

[net]
offline = true
EOF

cat > "$APP/src/lib.rs" <<'EOF'
// The primary wire path breaks only when all three wire crates are new; the
// cache fallback breaks only when both cache crates are new. The app survives
// as long as either path still works, so it fails only when both break.
fn primary_broken() -> bool {
    wire_encoder::is_new() && wire_transport::is_new() && wire_decoder::is_new()
}
fn cache_broken() -> bool {
    cache_writer::is_new() && cache_reader::is_new()
}
pub fn run() -> bool {
    noise_a::touch();
    noise_b::touch();
    noise_c::touch();
    noise_d::touch();
    noise_e::touch();
    !primary_broken() || !cache_broken()
}
EOF

cat > "$APP/tests/regression.rs" <<'EOF'
#[test]
fn app_is_healthy() {
    noise_f::touch();
    noise_g::touch();
    assert!(demo_app::run(), "app regressed after dependency bumps");
}
EOF

echo "/target" > "$APP/.gitignore"

write_manifest() {
    v=$1
    cat > "$APP/Cargo.toml" <<EOF
[package]
name = "demo-app"
version = "0.1.0"
edition = "2021"

[dependencies]
wire_encoder = "$v"
wire_transport = "$v"
wire_decoder = "$v"
cache_writer = "$v"
cache_reader = "$v"
noise_a = "$v"
noise_b = "$v"
noise_c = "$v"
noise_d = "$v"
noise_e = "$v"

[dev-dependencies]
noise_f = "$v"
noise_g = "$v"
EOF
}

cd "$APP"
git init -q -b main

write_manifest 1.0.0
cargo generate-lockfile -q
git add .
git_commit "base: working dependencies"

write_manifest 2.0.0
cargo generate-lockfile -q
git add .
git_commit "bump twelve crates"

echo "Complex demo repository created at examples/demo-cargo-complex/app"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-cargo-complex/app --base HEAD~1 --runs 3 -- cargo test"
echo
echo "Expected: 12 changes, five-crate minimal set"
echo "  (cache_reader, cache_writer, wire_decoder, wire_encoder, wire_transport)"
