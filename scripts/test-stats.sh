#!/usr/bin/env bash
# Tally one test run: test-stats.sh <gotestsum.json> <coverprofile>
#
# Prints key=value lines (passed, failed, skipped, coverage), shaped to be
# appended straight onto $GITHUB_OUTPUT. Subtests count as tests, the way
# gotestsum's own "DONE n tests" line counts them. A missing file — the
# suite never compiled, or never ran — leaves the counts blank and the
# coverage "—", never an error: this runs after failures, which is when the
# numbers matter most.
set -uo pipefail

json="${1:?usage: test-stats.sh <gotestsum.json> <coverprofile>}"
profile="${2:?usage: test-stats.sh <gotestsum.json> <coverprofile>}"

count() {
  [ -s "$json" ] || return 0
  jq -s --arg a "$1" '[.[] | select(.Test != null and .Action == $a)] | length' "$json" 2>/dev/null || echo 0
}

coverage="—"
if [ -s "$profile" ]; then
  # Last line of -func is "total: (statements) 70.3%".
  total=$(go tool cover -func="$profile" 2>/dev/null | tail -1 | awk '{print $NF}')
  [ -n "$total" ] && coverage="$total"
fi

echo "passed=$(count pass)"
echo "failed=$(count fail)"
echo "skipped=$(count skip)"
echo "coverage=$coverage"
