#!/usr/bin/env bash
# Find the first bad commit of a red batch on main (#5587, spec §6 step 3 of #5581).
#
#   scripts/bisect-main.sh <good-sha> <bad-sha> <job>…
#
# <good-sha> passed the jobs and <bad-sha> failed them (a ci run's summary on main names both,
# scripts/ci-batch.sh). The jobs are ci.yml's: go, apps, test. main is linear (squash merges), so
# the script binary-searches the first-parent commits after <good-sha> up to <bad-sha>, each step a
# `workflow_dispatch` of ci.yml with that sha and only those jobs: about log2(n) runs. It prints
# the first bad commit and its pull request.
#
# Rule out a flake first (rerun the failed jobs once at <bad-sha>): a flaky test bisects to a
# random commit. Each step waits for its run, so one step takes as long as the jobs do.
#
# Env: BISECT_REPO (default: this checkout's GitHub repo), BISECT_POLL (seconds, default 60).
set -euo pipefail

usage() { echo "usage: $0 <good-sha> <bad-sha> <job>… (jobs: go apps test)" >&2; exit 2; }
(( $# >= 3 )) || usage
good_ref=$1 bad_ref=$2
shift 2
for job in "$@"; do
	case $job in go | apps | test) ;; *) echo "unknown job: $job" >&2; usage ;; esac
done
jobs="$*"
poll=${BISECT_POLL:-60}
repo=${BISECT_REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}

git fetch --quiet origin main
good=$(git rev-parse --verify "$good_ref^{commit}")
bad=$(git rev-parse --verify "$bad_ref^{commit}")
for sha in "$good" "$bad"; do
	git merge-base --is-ancestor "$sha" origin/main || { echo "$sha is not on main" >&2; exit 2; }
done
git merge-base --is-ancestor "$good" "$bad" || { echo "$good is not before $bad" >&2; exit 2; }

# Oldest first, <good> left out, <bad> last.
commits=()
while read -r sha; do commits+=("$sha"); done < <(git rev-list --first-parent --reverse "$good..$bad")
(( ${#commits[@]} > 0 )) || { echo "no commits between $good and $bad" >&2; exit 2; }

# One ci run at a sha with only these jobs. Prints pass or fail; exits non-zero when the run did
# not finish (cancelled, timed out), which a bisect cannot read.
run_at() {
	local sha=$1 started out id status conclusion
	started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	out=$(gh workflow run ci.yml --repo "$repo" --ref main -f sha="$sha" -f jobs="$jobs" 2>&1) || {
		echo "$out" >&2; return 1
	}
	id=$(grep -oE '/actions/runs/[0-9]+' <<<"$out" | grep -oE '[0-9]+$' | head -1) || true
	# An older gh prints no URL: take the newest dispatch made since.
	local tries=0
	while [[ -z $id ]] && (( tries++ < 20 )); do
		sleep 5
		id=$(gh run list --repo "$repo" --workflow ci.yml --event workflow_dispatch --limit 5 \
			--json databaseId,createdAt --jq "[.[] | select(.createdAt >= \"$started\")] | last | .databaseId // empty")
	done
	[[ -n $id ]] || { echo "could not find the run for $sha" >&2; return 1; }
	echo "  run https://github.com/$repo/actions/runs/$id" >&2
	while :; do
		read -r status conclusion < <(gh run view "$id" --repo "$repo" --json status,conclusion --jq '"\(.status) \(.conclusion)"')
		[[ $status == completed ]] && break
		sleep "$poll"
	done
	case $conclusion in
		success) echo pass ;;
		failure) echo fail ;;
		*) echo "run $id ended $conclusion" >&2; return 1 ;;
	esac
}

# commits[lo] passes (lo = -1 is <good>), commits[hi] fails (hi = n-1 is <bad>).
lo=-1 hi=$(( ${#commits[@]} - 1 ))
steps=0 n=$(( hi + 1 ))
while (( n > 1 )); do steps=$(( steps + 1 )) n=$(( (n + 1) / 2 )); done
echo "Bisecting ${#commits[@]} commit(s) with: $jobs (about $steps run(s))" >&2
while (( hi - lo > 1 )); do
	mid=$(( (lo + hi) / 2 ))
	sha=${commits[$mid]}
	echo "$(git log -1 --format='%h %s' "$sha")" >&2
	result=$(run_at "$sha")
	echo "  $result" >&2
	if [[ $result == pass ]]; then lo=$mid; else hi=$mid; fi
done

first=${commits[$hi]}
pr=$(gh api "repos/$repo/commits/$first/pulls" --jq '.[0].number // empty' 2>/dev/null) || pr=
[[ -n $pr ]] || pr=$(git log -1 --format=%s "$first" | grep -oE '\(#[0-9]+\)$' | tr -dc '0-9') || true
echo "First bad commit: $(git log -1 --format='%H %s' "$first")"
if [[ -n $pr ]]; then
	echo "Pull request: https://github.com/$repo/pull/$pr"
else
	echo "Pull request: none found"
fi
