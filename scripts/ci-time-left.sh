#!/usr/bin/env bash
# Write the minutes a CI step can have as `minutes=<n>` to $GITHUB_OUTPUT (#5609).
#
# A job that reaches its timeout-minutes ends Cancelled, not Failed: no checks.failed, no
# red mark, and a pull request with auto-merge on waits for a required check that will never
# come. A step's own timeout-minutes fails the step. So a long step gets what its job has
# left, less RESERVE minutes for the steps after it, and a slow run ends red before the
# job's cap can cancel it.
#
# Usage, in a step before the long one (its `timeout-minutes` reads the output):
#   env: START (epoch seconds, from a "Start the clock" step), JOB_MINUTES (the job's
#        timeout-minutes; change both together), RESERVE (default 2), MAX (optional cap)
#   run: bash scripts/ci-time-left.sh
set -euo pipefail

: "${START:?START is not set}" "${JOB_MINUTES:?JOB_MINUTES is not set}"
reserve=${RESERVE:-2}
left=$(( JOB_MINUTES - reserve - ($(date +%s) - START) / 60 ))
if [[ -n ${MAX:-} ]] && (( left > MAX )); then left=$MAX; fi
(( left < 1 )) && left=1
echo "${STEP_NAME:-The next step} has ${left} minutes."
echo "minutes=${left}" >> "${GITHUB_OUTPUT:-/dev/stdout}"
