#!/usr/bin/env bash
# Which CI jobs have been failing, and how often (make ci-health [RUNS=n]).
#
# It exists because "CI is red again" was being answered from memory. The
# answer for 2026-10-21 to 2026-10-23 was that nine of the fourteen red runs
# were the macOS job never getting a machine, which is why the app now has a
# workflow of its own. Run this before changing anything about CI.
set -euo pipefail

runs=${RUNS:-30}
echo "The last $runs runs:"
gh run list -L "$runs" --json conclusion -q '.[].conclusion' |
  sort | uniq -c | sort -rn | sed 's/^/  /'

echo
echo "Jobs that failed or were cancelled in them, most often first:"
gh run list -L "$runs" --json databaseId,conclusion \
  -q '.[] | select(.conclusion != "success") | .databaseId' |
  while read -r id; do
    gh api "repos/{owner}/{repo}/actions/runs/$id/jobs?per_page=100" \
      -q '.jobs[] | select(.conclusion != "success" and .conclusion != "skipped") | "\(.conclusion) | \(.name)"' 2>/dev/null
  done | sort | uniq -c | sort -rn | sed 's/^/  /'
