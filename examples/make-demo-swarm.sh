#!/usr/bin/env sh
# Generates examples/demo-swarm/: the largest offline example, purpose-built to
# make --jobs dramatic. Its second commit bumps TWENTY-EIGHT dependencies, but
# the cluster only loses quorum once all twelve `replica-*` nodes migrate to the
# v2 wire format together; while any single node still speaks v1, a translation
# shim keeps the tests green. The minimal failing set is therefore all twelve
# replicas; the other sixteen `lib-*` bumps are harmless noise.
#
# Why this is the strongest --jobs workload: because the twelve culprits fail
# only in combination, ddmin evaluates very wide batches of candidate subsets at
# every granularity level, and the closing 1-minimality proof tests twelve
# neighbors at once. That batch work is exactly what --jobs spreads across
# worktrees, so a higher job count finishes the same bisection markedly sooner.
# The minimal set is identical at any job count. Everything is offline: all
# packages use local file: paths.
#
# The packages live *inside* the app repository (file:./pkgs/...), like the
# pnpm demo: relative in-repo paths travel with every git worktree, survive a
# move to another machine, and work when the fixture is mounted into the
# Docker image. (Relative specs also sidestep the MSYS path-format problem
# that absolute file: paths hit under Git Bash on Windows.)
set -eu

for tool in git node npm; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-swarm
mkdir -p demo-swarm/app/pkgs
ROOT=$(cd demo-swarm && pwd)

# The twelve interacting culprits and the sixteen noise packages. Kept in shell
# variables so the package generator, manifest writer, and test all agree.
REPLICAS="replica-01 replica-02 replica-03 replica-04 replica-05 replica-06 replica-07 replica-08 replica-09 replica-10 replica-11 replica-12"
NOISE="lib-async lib-buffer lib-cache lib-color lib-config lib-crypto lib-format lib-hash lib-json lib-log lib-parse lib-stream lib-time lib-uuid lib-yaml lib-zip"

mkpkg() { # name version body
    mkdir -p "$ROOT/app/pkgs/$1-$2"
    printf '{"name":"%s","version":"%s","main":"index.js"}\n' "$1" "$2" > "$ROOT/app/pkgs/$1-$2/package.json"
    printf '%s\n' "$3" > "$ROOT/app/pkgs/$1-$2/index.js"
}

# Replica nodes: v1 speaks the legacy wire format, v2 the new one.
for name in $REPLICAS; do
    mkpkg "$name" 1.0.0 "module.exports = () => 'v1';"
    mkpkg "$name" 2.0.0 "module.exports = () => 'v2';"
done

# Noise: both versions behave identically (each returns its own name so a botched
# install is still caught by the test); the bisection must discard them.
for name in $NOISE; do
    mkpkg "$name" 1.0.0 "module.exports = () => '$name';"
    mkpkg "$name" 1.1.0 "module.exports = () => '$name';"
done

cd "$ROOT/app"
git init -q -b main

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# write_manifest <replica-version> <noise-version>: pin every replica-* to one
# version and every lib-* to another, spreading the noise across all three
# dependency sections.
write_manifest() {
    REPLICAS="$REPLICAS" NOISE="$NOISE" RV="$1" NV="$2" node <<'NODE'
const fs = require('fs');
const replicas = process.env.REPLICAS.split(' ');
const noise = process.env.NOISE.split(' ');
const { RV, NV } = process.env;
const ref = (name, ver) => `file:./pkgs/${name}-${ver}`;
const dependencies = {}, devDependencies = {}, optionalDependencies = {};
for (const name of replicas) dependencies[name] = ref(name, RV);
noise.forEach((name, i) => {
  const bucket = i % 3 === 1 ? devDependencies : i % 3 === 2 ? optionalDependencies : dependencies;
  bucket[name] = ref(name, NV);
});
const pkg = { name: 'demo-swarm-app', version: '1.0.0', dependencies, devDependencies, optionalDependencies };
fs.writeFileSync('package.json', JSON.stringify(pkg, null, 2) + '\n');
NODE
}

# test.js: pass while any replica still speaks v1; fail only when all twelve
# reach v2 at once. Noise packages are exercised so broken installs surface as a
# hard error rather than a misattributed failure.
cat > test.js <<'EOF'
const replicas = [
    'replica-01', 'replica-02', 'replica-03', 'replica-04', 'replica-05', 'replica-06',
    'replica-07', 'replica-08', 'replica-09', 'replica-10', 'replica-11', 'replica-12',
];
const noise = [
    'lib-async', 'lib-buffer', 'lib-cache', 'lib-color', 'lib-config', 'lib-crypto',
    'lib-format', 'lib-hash', 'lib-json', 'lib-log', 'lib-parse', 'lib-stream',
    'lib-time', 'lib-uuid', 'lib-yaml', 'lib-zip',
];

for (const name of noise) {
    if (require(name)() !== name) {
        console.error(name, 'broken install');
        process.exit(1);
    }
}

const wire = replicas.map((name) => require(name)());
if (wire.every((p) => p === 'v2')) {
    console.error('quorum lost: all twelve replicas migrated to v2 at once');
    process.exit(1);
}
console.log('tests pass: the translation shim holds while a replica speaks v1');
EOF

echo node_modules > .gitignore
write_manifest 1.0.0 1.0.0
npm install --no-audit --no-fund --loglevel=error >/dev/null
git add .
git_commit "base: all replicas on the legacy wire format"

write_manifest 2.0.0 1.1.0
npm install --no-audit --no-fund --loglevel=error >/dev/null
git add .
git_commit "bump twenty-eight dependencies"

echo "Swarm demo created at examples/demo-swarm/app"
echo "Twenty-eight dependencies changed; the twelve replica-* updates fail only together."
echo "Try it, comparing wall time across --jobs values (the result is identical):"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 1  -- node test.js"
echo "  ./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- node test.js"
echo
echo "Expected minimal failing set: all twelve replica-* packages."
