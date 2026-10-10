#!/usr/bin/env bash
# Runs `go test` on the given packages and prints a readable report.
#
# Fails when a package skipped its PostgreSQL/Redis tests because the service was
# unreachable ("not running tests" in TestMain): CI always provides both services,
# so such a skip means those tests silently stopped running.
#
# Usage: go-test.sh PACKAGE...   (extra flags in GO_TEST_FLAGS, e.g. "-count=1")
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "No Go package affected: nothing to test."
  exit 0
fi

# Upstream tests that are obsolete and never ran in upstream CI (no database there).
# Each is replaced by a fork test; remove an entry once upstream fixes the test.
#   serverstats TestDailyStatsMessages, TestDailyMisc: insert PostgreSQL rows while
#     RetrieveDailyStats reads Redis -> serverstats/haxurus_dailystats_test.go
skip_tests='^(TestDailyStatsMessages|TestDailyMisc)$'

report=$(mktemp)
status=0
# shellcheck disable=SC2086 # GO_TEST_FLAGS is a list of flags
go test -json -skip "$skip_tests" ${GO_TEST_FLAGS:-} "$@" > "$report" || status=$?

jq -r 'select(.Action == "build-output") | .Output' "$report"

failed=$(jq -r 'select(.Test == null and .Action == "fail") | .Package' "$report" | sort -u)
for pkg in $failed; do
  echo "::group::FAIL $pkg"
  jq -r --arg pkg "$pkg" 'select(.Package == $pkg and .Action == "output") | .Output' "$report"
  echo "::endgroup::"
done

passed_count=$(jq -r 'select(.Test == null and .Action == "pass") | .Package' "$report" | sort -u | wc -l)
failed_count=$(printf '%s' "$failed" | grep -c . || true)
skipped=$(jq -r 'select(.Test != null and .Action == "skip") | "\(.Package) \(.Test)"' "$report" | sort -u)
silent=$(jq -r 'select(.Action == "output" and (.Output | test("not running tests"))) | .Package' "$report" | sort -u)

{
  echo "### Go tests"
  echo
  echo "Packages passed: ${passed_count}, failed: ${failed_count}"
  if [ -n "$failed" ]; then
    echo
    echo "Failed packages:"
    printf -- '- `%s`\n' $failed
  fi
  if [ -n "$silent" ]; then
    echo
    echo "Packages that skipped their database/redis tests:"
    printf -- '- `%s`\n' $silent
  fi
  if [ -n "$skipped" ]; then
    echo
    echo "Skipped tests:"
    printf '%s\n' "$skipped" | sed 's/^/- /'
  fi
  echo
  echo "Obsolete upstream tests excluded on purpose: \`$skip_tests\` (see .github/scripts/go-test.sh)"
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

if [ -n "$silent" ]; then
  echo "::error::Database/redis tests were skipped although CI provides the services: $(echo $silent)"
  status=1
fi

exit "$status"
