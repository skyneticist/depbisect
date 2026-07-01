#!/usr/bin/env sh
# Generates examples/demo-jobs/: a larger offline repository purpose-built to
# show off --jobs. Its second commit bumps eighteen dependencies, but the
# failure only appears once all eight `consensus-*` participants upgrade to v2
# together — a split-brain that compatibility mode masks while any single one
# still speaks v1. The minimal failing set is therefore all eight participants;
# the other ten bumps are harmless noise.
#
# Why this is a good --jobs workload: because the eight culprits only fail in
# combination, ddmin evaluates wide batches of candidate subsets at each
# granularity level, and the closing 1-minimality proof tests eight neighbors
# at once. That batch work is exactly what --jobs spreads across worktrees. The
# minimal set is identical at any job count; a higher --jobs just finishes
# sooner. Everything is offline: all packages use local file: paths.
set -eu

for tool in git node npm; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-jobs
mkdir -p demo-jobs/pkgs
ROOT=$(cd demo-jobs && pwd)

# Filesystem ops below use the POSIX $ROOT. npm can't resolve MSYS-style
# /d/a/... paths in file: specs on Windows, so under Git Bash the manifest
# writer receives the native D:/a/... path ($NPM_ROOT) as ROOT instead.
NPM_ROOT=$ROOT
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) NPM_ROOT=$(cd "$ROOT" && pwd -W) ;;
esac

# The eight interacting culprits and the ten noise packages. Kept in shell
# variables so the package generator, manifest writer, and test all agree.
CONSENSUS="consensus-auth consensus-codec consensus-gossip consensus-ledger consensus-router consensus-session consensus-stream consensus-sync"
NOISE="lib-cache lib-color lib-config lib-format lib-hash lib-json lib-log lib-parse lib-time lib-uuid"

mkpkg() { # name version body
    mkdir -p "$ROOT/pkgs/$1-$2"
    printf '{"name":"%s","version":"%s","main":"index.js"}\n' "$1" "$2" > "$ROOT/pkgs/$1-$2/package.json"
    printf '%s\n' "$3" > "$ROOT/pkgs/$1-$2/index.js"
}

# Consensus participants: v1 negotiates the compatible protocol, v2 the new one.
for name in $CONSENSUS; do
    mkpkg "$name" 1.0.0 "module.exports = () => 'v1';"
    mkpkg "$name" 2.0.0 "module.exports = () => 'v2';"
done

# Noise: both versions behave identically (each returns its own name so a botched
# install is still caught by the test); the bisection must discard them.
for name in $NOISE; do
    mkpkg "$name" 1.0.0 "module.exports = () => '$name';"
    mkpkg "$name" 1.1.0 "module.exports = () => '$name';"
done

mkdir "$ROOT/app"
cd "$ROOT/app"
git init -q -b main

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

# write_manifest <consensus-version> <noise-version>: pin every consensus-* to
# one version and every lib-* to another, spreading the noise across all three
# dependency sections.
write_manifest() {
    CONSENSUS="$CONSENSUS" NOISE="$NOISE" ROOT="$NPM_ROOT" CV="$1" NV="$2" node <<'NODE'
const fs = require('fs');
const consensus = process.env.CONSENSUS.split(' ');
const noise = process.env.NOISE.split(' ');
const { ROOT, CV, NV } = process.env;
const ref = (name, ver) => `file:${ROOT}/pkgs/${name}-${ver}`;
const dependencies = {}, devDependencies = {}, optionalDependencies = {};
for (const name of consensus) dependencies[name] = ref(name, CV);
noise.forEach((name, i) => {
  const bucket = i % 3 === 1 ? devDependencies : i % 3 === 2 ? optionalDependencies : dependencies;
  bucket[name] = ref(name, NV);
});
const pkg = { name: 'demo-jobs-app', version: '1.0.0', dependencies, devDependencies, optionalDependencies };
fs.writeFileSync('package.json', JSON.stringify(pkg, null, 2) + '\n');
NODE
}

# test.js: pass while any consensus participant still speaks v1; fail only when
# they all reach v2 at once. Noise packages are exercised so broken installs
# surface as a hard error rather than a misattributed failure.
cat > test.js <<'EOF'
const consensus = [
    'consensus-auth', 'consensus-codec', 'consensus-gossip', 'consensus-ledger',
    'consensus-router', 'consensus-session', 'consensus-stream', 'consensus-sync',
];
const noise = [
    'lib-cache', 'lib-color', 'lib-config', 'lib-format', 'lib-hash',
    'lib-json', 'lib-log', 'lib-parse', 'lib-time', 'lib-uuid',
];

for (const name of noise) {
    if (require(name)() !== name) {
        console.error(name, 'broken install');
        process.exit(1);
    }
}

const protocols = consensus.map((name) => require(name)());
if (protocols.every((p) => p === 'v2')) {
    console.error('split-brain: all consensus participants upgraded to v2 at once');
    process.exit(1);
}
console.log('tests pass: compatibility mode holds while a participant speaks v1');
EOF

echo node_modules > .gitignore
write_manifest 1.0.0 1.0.0
npm install --no-audit --no-fund --loglevel=error >/dev/null
git add .
git_commit "base: all consensus participants on the compatible protocol"

write_manifest 2.0.0 1.1.0
npm install --no-audit --no-fund --loglevel=error >/dev/null
git add .
git_commit "bump eighteen dependencies"

echo "Jobs demo created at examples/demo-jobs/app"
echo "Eighteen dependencies changed; the eight consensus-* updates fail only together."
echo "Try it, comparing wall time across --jobs values (the result is identical):"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-jobs/app --base HEAD~1 --runs 3 --jobs 4 -- node test.js"
echo
echo "Expected minimal failing set: all eight consensus-* packages."
