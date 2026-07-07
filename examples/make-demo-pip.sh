#!/usr/bin/env sh
# Generates examples/demo-pip/: a small git repository with two commits that
# bump two pinned packages in requirements.txt, one of which (breakage
# 1.0.0 -> 2.0.0) breaks the check. Everything is offline: dependencies are
# served from committed wheels via --no-index/--find-links lines inside
# requirements.txt itself, so pip never touches PyPI. Re-run the script any
# time to start fresh.
#
# Generation needs git plus either zip or a Python interpreter (the wheels
# are zipped with `python -m zipfile` where zip is unavailable, notably Git
# Bash on Windows runners); bisecting the result additionally needs pip
# (>= 22.3, for --python) and a python3/python on PATH.
set -eu

command -v git >/dev/null 2>&1 || { echo "error: git is required" >&2; exit 1; }

# WHEEL_PY is the interpreter for the `python -m zipfile` fallback; empty
# means zip(1) is available and preferred.
WHEEL_PY=
if ! command -v zip >/dev/null 2>&1; then
    if command -v python3 >/dev/null 2>&1; then
        WHEEL_PY=python3
    elif command -v python >/dev/null 2>&1; then
        WHEEL_PY=python
    else
        echo "error: zip or a python interpreter is required to build the demo wheels" >&2
        exit 1
    fi
fi

cd "$(dirname "$0")"
chmod -R u+w demo-pip 2>/dev/null || true
rm -rf demo-pip
mkdir -p demo-pip
ROOT=$(cd demo-pip && pwd)
APP="$ROOT/app"
mkdir -p "$APP/wheels"

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# make_wheel NAME VERSION INIT_BODY writes one pure-Python wheel into wheels/.
# A wheel is a zip of the package plus a .dist-info with METADATA/WHEEL/RECORD;
# the RECORD hashes are left blank, which pip accepts for a local find-links
# wheel.
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
    # The archive is built inside the staging directory and moved into place:
    # the python fallback runs a native interpreter that cannot open MSYS-style
    # /d/... destination paths, while mv handles them fine.
    if [ -z "$WHEEL_PY" ]; then
        ( cd "$stg" && zip -qr wheel.zip . )
    else
        ( cd "$stg" && "$WHEEL_PY" -m zipfile -c wheel.zip "$name" "$name-$ver.dist-info" )
    fi
    mv "$stg/wheel.zip" "$APP/wheels/$name-$ver-py3-none-any.whl"
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

# The option lines make the file self-sufficiently offline: pip resolves every
# pin from the committed wheels/ directory and never contacts an index.
write_requirements() {
    cat > "$APP/requirements.txt" <<EOF
# Install every package from the committed wheels/ directory, fully offline,
# so the demo bisects without any network access.
--no-index
--find-links wheels

breakage==$1
helper==$2
EOF
}

cd "$APP"
git init -q -b main

# base: working dependencies. The pinned requirements.txt is both the manifest
# and the resolution, matching what pip-compile or pip freeze would commit.
write_requirements 1.0.0 1.0.0
git add -A
git_commit "base: working dependencies"

# bump both packages; only the breakage bump is the culprit
write_requirements 2.0.0 2.0.0
git add -A
git_commit "bump breakage and helper"

echo "Demo repository created at examples/demo-pip/app"
echo "The check fails at HEAD and passed one commit earlier. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-pip/app --base HEAD~1 --runs 3 -- python check.py"
echo
echo "Expected result: minimal failing set = breakage 1.0.0 -> 2.0.0"
