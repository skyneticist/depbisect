#!/usr/bin/env sh
# Generates examples/demo-complex/: a git repository with two commits that
# bump twelve dependencies across dependencies, devDependencies, and
# optionalDependencies.
#
# Five updates are required to reproduce the failure:
#   - wire-encoder, wire-transport, and wire-decoder break the primary path
#     only when all three new versions are installed.
#   - cache-writer and cache-reader break the fallback path only when both new
#     versions are installed.
#
# The application fails only when both paths are broken, so the expected
# minimal failing set contains all five packages. The other seven updates are
# harmless noise. Everything is offline: all packages use local file: paths.
set -eu

for tool in git node npm; do
    command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

cd "$(dirname "$0")"
rm -rf demo-complex
mkdir -p demo-complex/pkgs
ROOT=$(cd demo-complex && pwd)

mkpkg() { # name version body
    mkdir -p "$ROOT/pkgs/$1-$2"
    printf '{"name":"%s","version":"%s","main":"index.js"}\n' "$1" "$2" > "$ROOT/pkgs/$1-$2/package.json"
    printf '%s\n' "$3" > "$ROOT/pkgs/$1-$2/index.js"
}

# Seven harmless updates provide noise around the interacting failures.
mkpkg analytics-core 1.0.0 "module.exports = () => 'analytics-ready';"
mkpkg analytics-core 1.1.0 "module.exports = () => 'analytics-ready';"
mkpkg feature-flags 2.4.0 "module.exports = (name) => name === 'fallback';"
mkpkg feature-flags 2.5.0 "module.exports = (name) => name === 'fallback';"
mkpkg logger 3.0.0 "module.exports = (message) => '[demo] ' + message;"
mkpkg logger 3.1.0 "module.exports = (message) => '[demo] ' + message;"
mkpkg metrics-sink 0.8.0 "module.exports = (values) => values.length;"
mkpkg metrics-sink 0.9.0 "module.exports = (values) => values.length;"
mkpkg request-id 1.2.0 "module.exports = (value) => 'req-' + value;"
mkpkg request-id 1.3.0 "module.exports = (value) => 'req-' + value;"
mkpkg retry-policy 4.0.0 "module.exports = () => 3;"
mkpkg retry-policy 4.1.0 "module.exports = () => 3;"
mkpkg schema-tools 0.6.0 "module.exports = (value) => typeof value === 'string';"
mkpkg schema-tools 0.7.0 "module.exports = (value) => typeof value === 'string';"

# Primary path: every proper subset of these updates remains compatible.
mkpkg wire-encoder 1.0.0 "module.exports = (value) => 'v1:' + value;"
mkpkg wire-encoder 2.0.0 "module.exports = (value) => 'v2:' + value;"
mkpkg wire-transport 1.0.0 "module.exports = (payload) => payload;"
mkpkg wire-transport 2.0.0 "module.exports = (payload) => payload.startsWith('v2:') ? payload + '#checksum=inline' : payload;"
mkpkg wire-decoder 1.0.0 "module.exports = (payload) => { const clean = String(payload).split('#')[0]; if (!/^v[12]:/.test(clean)) throw new Error('unknown wire format'); return clean.slice(3); };"
mkpkg wire-decoder 2.0.0 "module.exports = (payload) => { const raw = String(payload); if (raw.includes('#')) throw new Error('inline checksum rejected'); if (!/^v[12]:/.test(raw)) throw new Error('unknown wire format'); return raw.slice(3); };"

# Fallback path: the new writer and strict reader are incompatible together.
mkpkg cache-writer 1.0.0 "module.exports = (value) => JSON.stringify({ value });"
mkpkg cache-writer 2.0.0 "module.exports = (value) => JSON.stringify({ value }) + '\\n';"
mkpkg cache-reader 1.0.0 "module.exports = (payload) => JSON.parse(String(payload).trim()).value;"
mkpkg cache-reader 2.0.0 "module.exports = (payload) => { const raw = String(payload); if (raw !== raw.trim()) throw new Error('trailing data rejected'); return JSON.parse(raw).value; };"

mkdir "$ROOT/app"
cd "$ROOT/app"
git init -q -b main

git_commit() {
    git -c user.name=demo -c user.email=demo@example.invalid commit -q -m "$1"
}

write_manifest() { # analytics flags logger metrics request retry schema cacheR cacheW wireD wireE wireT
    cat > package.json <<EOF
{
  "name": "demo-complex-app",
  "version": "1.0.0",
  "dependencies": {
    "analytics-core": "file:$ROOT/pkgs/analytics-core-$1",
    "cache-reader": "file:$ROOT/pkgs/cache-reader-$8",
    "cache-writer": "file:$ROOT/pkgs/cache-writer-$9",
    "feature-flags": "file:$ROOT/pkgs/feature-flags-$2",
    "logger": "file:$ROOT/pkgs/logger-$3",
    "request-id": "file:$ROOT/pkgs/request-id-$5",
    "retry-policy": "file:$ROOT/pkgs/retry-policy-$6",
    "wire-decoder": "file:$ROOT/pkgs/wire-decoder-${10}",
    "wire-encoder": "file:$ROOT/pkgs/wire-encoder-${11}",
    "wire-transport": "file:$ROOT/pkgs/wire-transport-${12}"
  },
  "devDependencies": {
    "schema-tools": "file:$ROOT/pkgs/schema-tools-$7"
  },
  "optionalDependencies": {
    "metrics-sink": "file:$ROOT/pkgs/metrics-sink-$4"
  }
}
EOF
}

cat > test.js <<'EOF'
const checks = [
    ['analytics-core', require('analytics-core')(), 'analytics-ready'],
    ['feature-flags', require('feature-flags')('fallback'), true],
    ['logger', require('logger')('ok'), '[demo] ok'],
    ['metrics-sink', require('metrics-sink')([1, 2, 3]), 3],
    ['request-id', require('request-id')(7), 'req-7'],
    ['retry-policy', require('retry-policy')(), 3],
    ['schema-tools', require('schema-tools')('payload'), true],
];
for (const [name, actual, expected] of checks) {
    if (actual !== expected) {
        console.error(name, 'broken:', { actual, expected });
        process.exit(1);
    }
}

const wireEncoder = require('wire-encoder');
const wireTransport = require('wire-transport');
const wireDecoder = require('wire-decoder');
const cacheWriter = require('cache-writer');
const cacheReader = require('cache-reader');

function primaryRoundTrip(value) {
    return wireDecoder(wireTransport(wireEncoder(value)));
}

function fallbackRoundTrip(value) {
    return cacheReader(cacheWriter(value));
}

try {
    const value = primaryRoundTrip('hello');
    if (value !== 'hello') throw new Error('primary path returned ' + JSON.stringify(value));
    console.log('tests pass via primary path');
} catch (primaryError) {
    try {
        const value = fallbackRoundTrip('hello');
        if (value !== 'hello') throw new Error('fallback path returned ' + JSON.stringify(value));
        console.log('tests pass via fallback path');
    } catch (fallbackError) {
        console.error('primary path failed:', primaryError.message);
        console.error('fallback path failed:', fallbackError.message);
        process.exit(1);
    }
}
EOF

echo node_modules > .gitignore
write_manifest 1.0.0 2.4.0 3.0.0 0.8.0 1.2.0 4.0.0 0.6.0 1.0.0 1.0.0 1.0.0 1.0.0 1.0.0
npm install --no-audit --no-fund --loglevel=error >/dev/null
git add .
git_commit "base: compatible primary and fallback paths"

write_manifest 1.1.0 2.5.0 3.1.0 0.9.0 1.3.0 4.1.0 0.7.0 2.0.0 2.0.0 2.0.0 2.0.0 2.0.0
npm install --no-audit --no-fund --loglevel=error >/dev/null
git add .
git_commit "bump twelve dependencies"

echo "Complex demo created at examples/demo-complex/app"
echo "Twelve dependencies changed; five interacting updates are required to fail."
echo "Try:"
echo
echo "  go build ./cmd/depbisect"
echo "  ./depbisect run --repo examples/demo-complex/app --base HEAD~1 --runs 3 -- node test.js"
echo
echo "Expected minimal failing set:"
echo "  cache-reader, cache-writer, wire-decoder, wire-encoder, wire-transport"
