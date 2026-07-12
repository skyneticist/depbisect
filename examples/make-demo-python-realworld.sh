#!/usr/bin/env sh
# Generates examples/demo-python-realworld/: a git repository with REAL PyPI
# packages and a real-world breakage — NumPy 2.0's removal of np.float_.
#
# Unlike the other example generators this one NEEDS NETWORK ACCESS (it
# resolves and installs from PyPI), so it is used for demo recordings, not CI.
# Reproducibility comes from uv's --exclude-newer: the base commit's lockfile
# is resolved as of 2024-06-01 — two weeks before NumPy 2.0.0 shipped
# (2024-06-16) — so the ranges lock to the last 1.x era no matter when the
# script runs.
#
# The head commit mixes both change kinds DepBisect bisects:
#   - manifest bumps:  rich 13.3.5 -> 13.7.1, click 8.1.3 -> 8.1.7 (innocent)
#   - lockfile-only:   `uv lock -U` drifts numpy/requests/attrs/python-dateutil
#                      under unchanged range specs — numpy 1.26.x -> 2.x is the
#                      culprit (np.float_ was removed in 2.0)
set -eu

for tool in git uv python3; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

# The time-travel cutoff for the base lockfile: pre-NumPy-2.0.
BASE_CUTOFF="2024-06-01T00:00:00Z"

cd "$(dirname "$0")"
chmod -R u+w demo-python-realworld 2>/dev/null || true
rm -rf demo-python-realworld
mkdir -p demo-python-realworld
APP="$(cd demo-python-realworld && pwd)/app"
mkdir -p "$APP"

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

write_pyproject() {
    cat > "$APP/pyproject.toml" <<EOF
[project]
name = "nightly-stats"
version = "0.1.0"
requires-python = ">=3.10"
dependencies = [
    "numpy>=1.26",
    "requests>=2.31",
    "attrs>=23.2",
    "python-dateutil>=2.8",
    "rich==$1",
    "click==$2",
]
EOF
}

cat > "$APP/check.py" <<'EOF'
"""Nightly stats job: crunch a series and print a one-line summary."""
import attrs, click, requests  # noqa: F401  (the rest of the stack)
from dateutil import tz        # noqa: F401
import numpy as np

series = np.arange(12, dtype=np.float64)
mean = float(series.mean())
# np.float_ was removed in NumPy 2.0 — the real-world breakage this demo
# exists to catch.
total = np.float_(series.sum())
print(f"ok: mean={mean:.2f} total={total:.1f}")
EOF

printf '.venv\n__pycache__\n' > "$APP/.gitignore"

cd "$APP"
git init -q -b main

# base: ranges resolved as of the cutoff — numpy locks to its last 1.26.x.
write_pyproject 13.3.5 8.1.3
uv lock -q --exclude-newer "$BASE_CUTOFF"
git add -A
git_commit "base: working dependencies"

# head: bump the rich/click pins by hand and refresh the whole lockfile with
# today's registry state. The range-specified deps move only in uv.lock.
write_pyproject 13.7.1 8.1.7
uv lock -q -U
git add -A
git_commit "chore: bump rich and click; refresh uv.lock"

echo "Demo repository created at examples/demo-python-realworld/app (network was used)"
echo "rich/click moved in pyproject.toml; numpy/requests/attrs/dateutil moved only in uv.lock. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-python-realworld/app --base HEAD~1 --runs 3 -- uv run -- python check.py"
echo
echo "Expected result: minimal failing set = numpy 1.26.4 -> 2.x (lockfile-only)"
