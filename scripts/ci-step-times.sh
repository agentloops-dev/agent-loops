#!/usr/bin/env bash
# Say where a CI job's minutes went (#5673): each step of a minute or more, slowest first, in
# the job's summary. When the job failed near its limit (a step's budget from
# scripts/ci-time-left.sh ran out, #5609), an error names the slowest step, so a red job
# says which step was slow, not only "The action has timed out".
#
# Usage, as a job's last step, with `if: always()`:
#   env: GH_TOKEN, START (epoch seconds, from a "Start the clock" step), JOB_MINUTES (the
#        job's timeout-minutes), STATUS (${{ job.status }})
#   run: bash scripts/ci-step-times.sh
# GitHub sets GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_RUN_ATTEMPT and GITHUB_JOB; a job
# with no `name:` is called by its id, which is GITHUB_JOB.
set -euo pipefail

: "${START:?START is not set}" "${JOB_MINUTES:?JOB_MINUTES is not set}"
elapsed=$(( ($(date +%s) - START) / 60 ))
summary=${GITHUB_STEP_SUMMARY:-/dev/stdout}

steps=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/attempts/${GITHUB_RUN_ATTEMPT:-1}/jobs?per_page=100" \
	| jq --arg job "$GITHUB_JOB" '[.jobs[] | select(.name == $job)] | last | .steps // []
		| map(select(.started_at and .completed_at)
			| {name, conclusion,
			   seconds: ((.completed_at | fromdateiso8601) - (.started_at | fromdateiso8601))})
		| sort_by(-.seconds)') || {
	echo "::warning::Could not read this job's step times."
	exit 0
}

{
	echo "### ${GITHUB_JOB}: ${elapsed} of ${JOB_MINUTES} minutes"
	echo
	echo "| Step | Minutes | Result |"
	echo "|---|---:|---|"
	jq -r '.[] | select(.seconds >= 60)
		| "| \(.name) | \((.seconds / 6 | floor) / 10) | \(.conclusion) |"' <<<"$steps"
} >> "$summary"

# A job that failed with less than 4 of its minutes left ran out of time (a budget is the
# job's minutes less a reserve of 1 to 10): name the step that took them.
if [[ ${STATUS:-} == failure ]] && (( elapsed >= JOB_MINUTES - 4 )); then
	slow=$(jq -r 'first // {name: "no step", seconds: 0, conclusion: "none"} | "\(.name) (\((.seconds / 6 | floor) / 10) min, \(.conclusion))"' <<<"$steps")
	echo "::error::${GITHUB_JOB} ran ${elapsed} of its ${JOB_MINUTES} minutes (#5673). The slowest step: ${slow}."
fi
