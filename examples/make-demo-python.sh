#!/usr/bin/env sh
# Generates examples/demo-python/: a small git repository with two commits that
# bump two packages, one of which (breakage 1.0.0 -> 2.0.0) breaks the check.
# Everything is offline: dependencies are served from committed wheels via a
# uv.toml with offline/no-index/find-links, so uv never touches PyPI. Re-run the
# script any time to start fresh.
set -eu

for tool in git uv python3 zip; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
chmod -R u+w demo-python 2>/dev/null || true
rm -rf demo-python
mkdir -p demo-python
ROOT=$(cd demo-python && pwd)
APP="$ROOT/app"
mkdir -p "$APP/wheels"

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# make_wheel NAME VERSION INIT_BODY writes one pure-Python wheel into wheels/.
# A wheel is a zip of the package plus a .dist-info with METADATA/WHEEL/RECORD;
# the RECORD hashes are left blank, which uv accepts for a local find-links wheel.
make_wheel() {
    name="$1"; ver="$2"; body="$3"
    stg=$(mktemp -d)
    mkdir -p "$stg/$name" "$stg/$name-$ver.dist-info"
    printf '%s\n' "$body" > "$stg/$name/__init__.py"
    printf 'Metadata-Version: 2.1\nName: %s\nVersion: %s\n' "$name" "$ver" \
        > "$stg/$name-$ver.dist-info/METADATA"
    printf 'Wheel-Version: 1.0\nGenerator: depbisect-demo\nRoot-Is-Purelib: true\nTag: py3-none-any\n' \
        > "$stg/$name-$ver.dist-info/WHEEL"
    printf '%s/__init__.py,,\n%s-%s.dist-info/METADATA,,\n%s-%s.dist-info/WHEEL,,\n%s-%s.dist-info/RECORD,,\n' \
        "$name" "$name" "$ver" "$name" "$ver" "$name" "$ver" \
        > "$stg/$name-$ver.dist-info/RECORD"
    ( cd "$stg" && zip -qr "$APP/wheels/$name-$ver-py3-none-any.whl" . )
    rm -rf "$stg"
}

# --- committed wheels: two versions of each package ----------------------
# breakage 2.0.0 regresses (healthy() returns False); helper is harmless noise.
make_wheel breakage 1.0.0 'def healthy():
    return True'
make_wheel breakage 2.0.0 'def healthy():
    return False  # regression'
make_wheel helper 1.0.0 'def touch():
    pass'
make_wheel helper 2.0.0 'def touch():
    pass  # harmless bump'

# --- the app repository -------------------------------------------------
cat > "$APP/uv.toml" <<'EOF'
# Resolve and install every package from the committed wheels/ directory, fully
# offline, so the demo bisects without any network access.
offline = true
no-index = true
find-links = ["wheels"]
EOF

cat > "$APP/check.py" <<'EOF'
"""Health check whose result depends on the breakage package."""
import breakage
import helper

helper.touch()
if not breakage.healthy():
    raise SystemExit("app regressed after a dependency bump")
print("ok")
EOF

printf '.venv\n__pycache__\n' > "$APP/.gitignore"

write_pyproject() {
    cat > "$APP/pyproject.toml" <<EOF
[project]
name = "demo-app"
version = "0.1.0"
requires-python = ">=3.9"
dependencies = [
    "breakage==$1",
    "helper==$2",
]
EOF
}

cd "$APP"
git init -q -b main

# base: working dependencies. `uv lock` resolves the wheels offline and writes
# uv.lock, matching what a real project commits.
write_pyproject 1.0.0 1.0.0
uv lock -q
git add -A
git_commit "base: working dependencies"

# bump both packages; only the breakage bump is the culprit
write_pyproject 2.0.0 2.0.0
uv lock -q
git add -A
git_commit "bump breakage and helper"

echo "Demo repository created at examples/demo-python/app"
echo "The check fails at HEAD and passed one commit earlier. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-python/app --base HEAD~1 --runs 3 -- uv run -- python check.py"
echo
echo "Expected result: minimal failing set = breakage 1.0.0 -> 2.0.0"
