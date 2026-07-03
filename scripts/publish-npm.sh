#!/usr/bin/env bash
#
# Build and publish the npm packages from a local machine.
#
# CI (.github/workflows/npm-publish.yml) is the normal publishing path; this
# script exists for the two local cases:
#
#   * bootstrapping — npm Trusted Publishing (OIDC) only works for packages
#     that already exist, so the FIRST version of every package (the
#     `depbisect` launcher and each `@depbisect/<platform>`) must be
#     published manually, once;
#   * recovery — backfilling a version whose workflow run was missed.
#
# It cross-compiles all platform binaries, then publishes the platform
# packages BEFORE the launcher so the launcher's pinned optionalDependencies
# resolve. Local publishes cannot use --provenance (that needs CI OIDC).
#
# Usage:
#   scripts/publish-npm.sh <version> [--dry-run]
#
# <version> is the release version (leading "v" allowed). --dry-run builds
# everything and shows what npm would upload without publishing; it does not
# require being logged in.
set -euo pipefail

usage() { echo "usage: scripts/publish-npm.sh <version> [--dry-run]" >&2; exit 1; }

version=${1:-}
[ -n "$version" ] || usage
shift
dry=""
for arg in "$@"; do
    case $arg in
        --dry-run) dry="--dry-run" ;;
        *) echo "publish-npm: unknown argument $arg" >&2; usage ;;
    esac
done

cd "$(dirname "$0")/.."

for tool in node npm go; do
    command -v "$tool" >/dev/null 2>&1 || { echo "publish-npm: $tool is required on PATH" >&2; exit 1; }
done

if [ -z "$dry" ]; then
    npm whoami >/dev/null 2>&1 || {
        echo "publish-npm: not logged in to npm — run \`npm login\` first" >&2
        exit 1
    }
fi

node scripts/build-npm.mjs "$version"

# Platform packages first, launcher last, so optionalDependencies resolve.
# The ./ prefix forces npm to treat each argument as a directory; a bare
# "npm/depbisect" parses as the GitHub shorthand github.com/npm/depbisect.
for dir in ./npm/@depbisect/*/; do
    echo "publishing $dir"
    npm publish "$dir" --access public $dry
done
echo "publishing ./npm/depbisect"
npm publish ./npm/depbisect --access public $dry

if [ -n "$dry" ]; then
    echo "dry run complete; nothing was published."
fi
