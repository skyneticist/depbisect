#!/usr/bin/env sh
# Generates examples/demo-pnpm/: a small git repository with two commits that
# bump two dependencies, one of which (leftpad 1.0.0 -> 2.0.0) breaks
# `node test.js`. Everything is offline: dependencies are local file:
# packages, so pnpm never touches a registry.
#
# Like the other JS demos, the packages live *inside* the app repository
# (file:./pkgs/...): pnpm rewrites file: specifiers to lockfile-relative paths
# in pnpm-lock.yaml, so out-of-repo absolute paths would break the moment
# DepBisect materializes the repo in a temporary worktree. In-repo relative
# paths travel with every worktree. The generated pnpm-lock.yaml format
# follows the installed pnpm (v6 for pnpm 8, v9 for pnpm 9+); the parser
# supports v5/v6/v9. Re-run the script any time to start fresh.
set -eu

for tool in git node pnpm; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-pnpm
mkdir -p demo-pnpm
ROOT=$(cd demo-pnpm && pwd)

# --- the app repository -------------------------------------------------
mkdir "$ROOT/app"
cd "$ROOT/app"
git init -q -b main

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# --- local packages, committed with the app ------------------------------
# alpha 1.0.0 and 1.1.0 are both fine; leftpad 2.0.0 pads the wrong side.
for v in 1.0.0 1.1.0; do
    mkdir -p "pkgs/alpha-$v"
    printf '{"name":"alpha","version":"%s","main":"index.js"}\n' "$v" > "pkgs/alpha-$v/package.json"
    echo "module.exports = () => 'alpha';" > "pkgs/alpha-$v/index.js"
done

mkdir -p pkgs/leftpad-1.0.0 pkgs/leftpad-2.0.0
printf '{"name":"leftpad","version":"1.0.0","main":"index.js"}\n' > pkgs/leftpad-1.0.0/package.json
echo "module.exports = (s, n) => String(s).padStart(n, ' ');" > pkgs/leftpad-1.0.0/index.js
printf '{"name":"leftpad","version":"2.0.0","main":"index.js"}\n' > pkgs/leftpad-2.0.0/package.json
echo "module.exports = (s, n) => String(s).padEnd(n, ' '); // regression: pads the right side" > pkgs/leftpad-2.0.0/index.js

cat > package.json <<'EOF'
{
  "name": "demo-app",
  "version": "1.0.0",
  "dependencies": {
    "alpha": "file:./pkgs/alpha-1.0.0",
    "leftpad": "file:./pkgs/leftpad-1.0.0"
  }
}
EOF

cat > test.js <<'EOF'
const leftpad = require('leftpad');
const alpha = require('alpha');
if (alpha() !== 'alpha') { console.error('alpha broken'); process.exit(1); }
if (leftpad('7', 3) !== '  7') {
    console.error('leftpad broken:', JSON.stringify(leftpad('7', 3)));
    process.exit(1);
}
console.log('tests pass');
EOF

echo node_modules > .gitignore
# --no-frozen-lockfile: pnpm enables --frozen-lockfile automatically when
# CI=true, which would reject the manifest edits this generator commits.
pnpm install --no-frozen-lockfile --silent >/dev/null
git add .
git_commit "base: working dependencies"

# Bump both dependencies; only the leftpad bump is the culprit.
node -e "
const fs = require('fs');
const p = JSON.parse(fs.readFileSync('package.json'));
p.dependencies.alpha = 'file:./pkgs/alpha-1.1.0';
p.dependencies.leftpad = 'file:./pkgs/leftpad-2.0.0';
fs.writeFileSync('package.json', JSON.stringify(p, null, 2) + '\n');
"
pnpm install --no-frozen-lockfile --silent >/dev/null
git add .
git_commit "bump alpha and leftpad"

echo "Demo repository created at examples/demo-pnpm/app"
echo "The test fails at HEAD and passed one commit earlier. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-pnpm/app --base HEAD~1 --runs 3 -- node test.js"
echo
echo "Expected result: minimal failing set = leftpad 1.0.0 -> 2.0.0"
