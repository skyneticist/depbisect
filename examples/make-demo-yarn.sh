#!/usr/bin/env sh
# Generates examples/demo-yarn/: a small git repository with two commits that
# bump two dependencies, one of which (leftpad 1.0.0 -> 2.0.0) breaks
# `node test.js`. Everything is offline: dependencies are local file:
# packages, so yarn never touches a registry. The generated yarn.lock is the
# classic v1 format — the toolchain CI runners preinstall; Berry lockfile
# parsing is covered by the manifest unit tests.
#
# The packages live *inside* the app repository (file:./pkgs/...), like the
# pnpm demo: relative in-repo paths travel with every git worktree, survive a
# move to another machine, and work when the fixture is mounted into the
# Docker image. (Relative specs also sidestep the MSYS path-format problem
# that absolute file: paths hit under Git Bash on Windows.) Re-run the script
# any time to start fresh.
set -eu

for tool in git node yarn; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

# Berry (yarn 2+) defaults to Plug'n'Play, which plain `node test.js` cannot
# resolve without the PnP runtime, and it writes a different lockfile format —
# the demo would no longer be deterministic across environments. Require the
# classic 1.x toolchain this generator targets.
case "$(yarn --version)" in
    1.*) ;;
    *) echo "error: this demo generator needs classic yarn (1.x); found yarn $(yarn --version)" >&2; exit 1 ;;
esac

cd "$(dirname "$0")"
rm -rf demo-yarn
mkdir -p demo-yarn/app/pkgs
ROOT=$(cd demo-yarn && pwd)

# --- local packages, committed with the app ------------------------------
# alpha 1.0.0 and 1.1.0 are both fine; leftpad 2.0.0 pads the wrong side.
for v in 1.0.0 1.1.0; do
    mkdir -p "$ROOT/app/pkgs/alpha-$v"
    printf '{"name":"alpha","version":"%s","main":"index.js","license":"MIT"}\n' "$v" > "$ROOT/app/pkgs/alpha-$v/package.json"
    echo "module.exports = () => 'alpha';" > "$ROOT/app/pkgs/alpha-$v/index.js"
done

mkdir -p "$ROOT/app/pkgs/leftpad-1.0.0" "$ROOT/app/pkgs/leftpad-2.0.0"
printf '{"name":"leftpad","version":"1.0.0","main":"index.js","license":"MIT"}\n' > "$ROOT/app/pkgs/leftpad-1.0.0/package.json"
echo "module.exports = (s, n) => String(s).padStart(n, ' ');" > "$ROOT/app/pkgs/leftpad-1.0.0/index.js"
printf '{"name":"leftpad","version":"2.0.0","main":"index.js","license":"MIT"}\n' > "$ROOT/app/pkgs/leftpad-2.0.0/package.json"
echo "module.exports = (s, n) => String(s).padEnd(n, ' '); // regression: pads the right side" > "$ROOT/app/pkgs/leftpad-2.0.0/index.js"

# --- the app repository -------------------------------------------------
cd "$ROOT/app"
git init -q -b main

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

cat > package.json <<EOF
{
  "name": "demo-app",
  "version": "1.0.0",
  "license": "MIT",
  "private": true,
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
yarn install --silent >/dev/null
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
yarn install --silent >/dev/null
git add .
git_commit "bump alpha and leftpad"

echo "Demo repository created at examples/demo-yarn/app"
echo "The test fails at HEAD and passed one commit earlier. Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-yarn/app --base HEAD~1 --runs 3 -- node test.js"
echo
echo "Expected result: minimal failing set = leftpad 1.0.0 -> 2.0.0"
