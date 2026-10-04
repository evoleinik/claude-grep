#!/usr/bin/env bash
# Total test coverage, with a floor. Used by CI and by hand.
#
# The number includes the end-to-end tests: they run a `-cover` build of the
# real binary, and `go test` folds a child process's counters into its own
# profile. That is what covers main() and every subcommand that exits.
#
# Usage: scripts/coverage.sh [min-percent]     (default 80)
# Writes coverage.out. Exit 1 when a test fails or the total is under the floor.
set -euo pipefail

cd "$(dirname "$0")/.."
min="${1:-80}"

go test -count=1 -covermode=atomic -coverprofile=coverage.out ./...

total="$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$3); print $3}')"
echo "total coverage: ${total}% (floor ${min}%)"
if awk -v t="$total" -v m="$min" 'BEGIN { exit !(t < m) }'; then
  echo "FAIL: coverage ${total}% is under the ${min}% floor" >&2
  echo "  fix: go tool cover -func=coverage.out | sort -k3 -n | head -30   # least-covered functions" >&2
  exit 1
fi
