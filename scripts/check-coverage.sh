#!/usr/bin/env bash
#
# Enforce a minimum total statement-coverage floor.
#
# This is a floor, not a freeze: it sits a few points below the current level
# so it catches a genuine regression (a test file or a whole package going
# dark) without breaking unrelated PRs over normal drift. Two deliberate
# choices keep it from flaking:
#
#   * It runs on a single OS in CI. internal/gitx, internal/execx and
#     internal/verify branch on the operating system, so per-OS coverage
#     differs by a few tenths; gating across the matrix would be flaky.
#   * The float compare runs under LC_ALL=C. go tool cover prints the total
#     with a period decimal ("total: ... 84.4%"); pinning the locale keeps
#     awk from misparsing it where the locale decimal is a comma.
#
# Usage: scripts/check-coverage.sh [coverage-profile] [min-percent]
set -euo pipefail

profile="${1:-coverage.out}"
min="${2:-80.0}"

if [ ! -f "$profile" ]; then
  echo "coverage profile not found: $profile" >&2
  exit 1
fi

total="$(LC_ALL=C go tool cover -func="$profile" | awk '/^total:/ {print $3}' | tr -d '%')"

if [ -z "$total" ]; then
  echo "could not parse a total from $profile (is it a valid coverage profile?)" >&2
  exit 1
fi

echo "total coverage: ${total}% (floor: ${min}%)"

if ! LC_ALL=C awk -v t="$total" -v m="$min" 'BEGIN { exit (t + 0 < m + 0) ? 1 : 0 }'; then
  echo "coverage ${total}% is below the ${min}% floor" >&2
  exit 1
fi
