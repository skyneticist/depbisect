#!/usr/bin/env bash
#
# Verify the Makefile's optional-tool guards (the `need` macro).
#
# Two properties matter, and both are easy to regress by hand:
#   1. No target recommends `brew install`. A bare `brew install <tool>` can
#      resolve to an untrusted third-party tap, so install hints must point at
#      `go install <module>@version` (toolchain-native, reproducible) or an
#      official docs URL instead.
#   2. A guarded target fails *cleanly* with its hint — not make's cryptic
#      "<tool>: No such file or directory" — when the tool is absent.
#
# Run:  bash scripts/test-make-guards.sh   (or: make test-make)

set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
note() { printf '%s\n' "$*"; }

# 1. Policy: no `brew install` recommendation anywhere in the Makefile.
if grep -nE 'brew[[:space:]]+install' Makefile; then
	note "FAIL: Makefile must not recommend 'brew install' (use 'go install <module>@version' or an official docs URL)."
	fail=1
else
	note "ok: no 'brew install' recommendations in Makefile"
fi

# 2. Functional: a guarded target prints its hint and exits non-zero when the
#    tool is missing. Run with an empty PATH so the optional tool cannot be
#    found regardless of the host, with make invoked by absolute path. The guard
#    uses only shell builtins (command -v / printf / exit), so it still runs.
make_bin="$(command -v make)"
set +e
out="$(PATH="" "$make_bin" release-snapshot 2>&1)"
code=$?
set -e

if [ "$code" -eq 0 ]; then
	note "FAIL: 'make release-snapshot' should fail when goreleaser is absent, but exited 0."
	fail=1
fi
case "$out" in
*"goreleaser not found"*) note "ok: guard prints a 'not found' hint" ;;
*) note "FAIL: expected a 'goreleaser not found' hint; got:"; note "$out"; fail=1 ;;
esac
case "$out" in
*"go install github.com/goreleaser/goreleaser/v2@latest"*) note "ok: hint recommends 'go install'" ;;
*) note "FAIL: expected a 'go install ...' hint; got:"; note "$out"; fail=1 ;;
esac
case "$out" in
*"brew install"*) note "FAIL: the guard hint must not recommend 'brew install'"; fail=1 ;;
*) note "ok: hint does not mention 'brew install'" ;;
esac

if [ "$fail" -eq 0 ]; then
	note "PASS: Makefile guards OK"
else
	note "make-guard tests FAILED"
	exit 1
fi
