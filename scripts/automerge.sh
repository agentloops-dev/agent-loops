#!/usr/bin/env bash
# Turn on squash auto-merge for a pull request with the commit trailers in its squash message
# (#6362, epic #6365).
#
#   scripts/automerge.sh PR TRAILERS          turn squash auto-merge on, with the message
#   scripts/automerge.sh --print PR TRAILERS  only print the message's body
#
# TRAILERS is a file with the lines that the app's tool commit_trailers gave for this PR
# (commit_trailers with pr: PR), as they came; an empty file, or /dev/null, when it gave none.
# The agent types no trailer by hand: the daemon's git/trailers makes the lines by the folder's
# commitTrailers rule (docs/reference/policy.md, "git/trailers").
#
# Why the body is set here: GitHub's squash runs no hook, and this repo's squash message is
# COMMIT_MESSAGES, so a PR of several commits would give several trailer blocks, and git reads
# only the last paragraph. The body is each commit's message (not a merge of the base in) with
# its own trailers taken out, then one block: the lines of TRAILERS, then each commit's
# Co-authored-by as its runtime wrote it (#7174), and each commit author but the PR's own, each
# line once (a person who made a commit stays a co-author on GitHub's commit page, as in
# GitHub's own squash message). Co-authored-by is the runtime's, not a key of the rule: a
# TRAILERS line of it fails, as any key that is not the rule's.
#
# Each key must be a trailer of the rule (the commitTrailers.<key> names in
# Packages/Wire/schema/org.schema.json), or Refs, which Fixes writes with no PR: a key that is
# not fails, so the keys cannot drift. `git log --format='%(trailers:key=Cost,valueonly)'`
# then reads them on main.
#
# scripts/rearm-automerge.sh keeps the message when it turns auto-merge off and on again. When
# a lane re-arms after more work on the PR, it asks commit_trailers again and runs this, so the
# totals count that work. GH_REPO picks the repository, as for gh.
set -euo pipefail

me=$(basename "$0")
die() { echo "$me: $*" >&2; exit 1; }
here=$(cd "$(dirname "$0")" && pwd)
schema=${AGENTS_TRAILER_SCHEMA:-$here/../Packages/Wire/schema/org.schema.json}

print=0
if [[ ${1:-} == --print ]]; then
	print=1
	shift
fi
[[ $# -eq 2 ]] || die "usage: $me [--print] PR TRAILERS"
pr=$1 trailers=$2
[[ $pr =~ ^[0-9]+$ ]] || die "PR must be a pull request's number, not '$pr'"
[[ -r $trailers ]] || die "cannot read $trailers"

# The keys: one a line.
keys=$(jq -r '.. | .enum? // empty | .[] | select(type == "string" and startswith("commitTrailers."))
	| ltrimstr("commitTrailers.")' "$schema" | sort -u)
[[ -n $keys ]] || die "no commitTrailers keys in $schema"
keys+=$'\nRefs'

# A trailer line: Key: value.
trailer_re='^([A-Za-z][A-Za-z0-9-]*): (.+)$'

block=()
seen=$'\n'
add() {
	local line=$1 low
	low=$(printf '%s' "$line" | tr '[:upper:]' '[:lower:]')
	[[ $seen == *$'\n'"$low"$'\n'* ]] && return 0
	seen+="$low"$'\n'
	block+=("$line")
}

while IFS= read -r line || [[ -n $line ]]; do
	line=${line%$'\r'}
	[[ -n ${line// /} ]] || continue
	[[ $line =~ $trailer_re ]] || die "$trailers: not a trailer line: '$line'"
	key=${BASH_REMATCH[1]}
	grep -qxF -- "$key" <<<"$keys" ||
		die "$trailers: '$key' is not a commit trailer of the rule (one of: $(tr '\n' ' ' <<<"$keys"))"
	add "$line"
done <"$trailers"

# Each commit as headline, body; its own last paragraph taken out when every line of it is a
# trailer. Records are split by the 0x1e byte, fields by 0x1f.
commits=$(gh pr view "$pr" --json commits \
	--jq '.commits[] | "\(.messageHeadline)\u001f\(.messageBody)\u001e"') ||
	die "could not read the commits of #$pr"

body=""
count=0
coauthors=()
while IFS= read -r -d $'\x1e' rec; do
	rec=${rec#$'\n'}
	[[ -n $rec ]] || continue
	headline=${rec%%$'\x1f'*}
	text=${rec#*$'\x1f'}
	# A merge of the base in says nothing about the change (#5712 merges, never rebases).
	[[ $headline =~ ^Merge\ (remote-tracking\ )?branch\  ]] && continue
	text=$(printf '%s' "$text" | sed -e 's/\r$//')
	# The last paragraph.
	last=$(printf '%s\n' "$text" | awk 'BEGIN{RS=""} {p=$0} END{print p}')
	if [[ -n $last ]] && ! grep -qvE "$trailer_re" <<<"$last"; then
		while IFS= read -r l; do
			# As the runtime wrote it; add() keeps one of each, whatever its case.
			if [[ $l =~ ^[Cc][Oo]-[Aa][Uu][Tt][Hh][Oo][Rr][Ee][Dd]-[Bb][Yy]:\ (.+)$ ]]; then
				coauthors+=("$l")
			fi
		done <<<"$last"
		text=$(printf '%s\n' "$text" | awk 'BEGIN{RS=""; ORS="\n\n"} {p[NR]=$0} END{for (i=1; i<NR; i++) print p[i]}')
	fi
	text=$(printf '%s' "$text" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')
	count=$((count + 1))
	body+="* $headline"$'\n'
	[[ -z $text ]] || body+=$'\n'"$text"$'\n'
	body+=$'\n'
done <<<"$commits"
[[ $count -gt 0 ]] || die "#$pr has no commits"
if [[ $count -eq 1 ]]; then
	# One commit: its body alone, as GitHub's own squash message, under the PR's title.
	body=${body#"* $headline"$'\n'}
	body=${body#$'\n'}
fi

# And each commit's authors but the PR's own, as GitHub's own squash message names them.
while IFS= read -r l; do
	[[ -z $l ]] || coauthors+=("Co-authored-by: $l")
done < <(gh pr view "$pr" --json author,commits --jq '.author.login as $me
	| [.commits[].authors[] | select(.login != $me and .email != "" and .name != "")
		| "\(.name) <\(.email)>"] | unique | .[]') || die "could not read the authors of #$pr"

for l in "${coauthors[@]+"${coauthors[@]}"}"; do
	add "$l"
done

msg=$(printf '%s' "$body" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')
if [[ ${#block[@]} -gt 0 ]]; then
	[[ -z $msg ]] || msg+=$'\n\n'
	msg+=$(printf '%s\n' "${block[@]}")
fi

if [[ $print == 1 ]]; then
	printf '%s\n' "$msg"
	exit 0
fi
file=$(mktemp "${TMPDIR:-/tmp}/automerge-$pr.XXXXXX")
trap 'rm -f "$file"' EXIT
printf '%s\n' "$msg" >"$file"
# The repository only allows squash merges.
gh pr merge "$pr" --auto --squash --body-file "$file" >/dev/null ||
	die "could not turn squash auto-merge on for #$pr"
echo "$me: squash auto-merge on for #$pr, with ${#block[@]} trailer lines"
