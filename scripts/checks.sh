#!/bin/sh
# A lane's checks, attested (#5584, epic #5581).
#
#   scripts/checks.sh attest            run the affected test, vet and lint tasks with Nx and
#                                       no cache hits, and post the check run
#                                       attestation/<platform> on the pull request's head commit,
#                                       as the agents' bot: one in-toto test-result statement
#                                       for each task that passed on its first run
#   scripts/checks.sh attest --plan     the tasks, their Nx hashes and the inputs that changed;
#                                       runs nothing
#   scripts/checks.sh attest --no-post  run them, and print the check run in place of posting it
#   scripts/checks.sh attest --base REF the change since REF (default: its merge base with
#                                       origin/main)
#
# Commit and push first: the statements name each task by its Nx hash at the head commit, and
# the check run goes on the pull request's head. CI does not read it yet (#5585). The rules and
# the statement's shape are in scripts/checks-attest.mjs. Lease "build" first (not for --plan).
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
. "$root/scripts/node.sh"
use_pinned_node install
need_node attest

# The Xcode that .xcode-version names, when this Mac has it beside another (as CI's runners
# do); a task whose Xcode is another is not attested (scripts/checks-attest.mjs).
xcode="/Applications/Xcode_$(cat "$root/.xcode-version").app/Contents/Developer"
if [ -z "${DEVELOPER_DIR:-}" ] && [ -d "$xcode" ]; then
	DEVELOPER_DIR=$xcode
	export DEVELOPER_DIR
fi

case ${1:-} in
attest)
	shift
	# The packages that nx-hashes.cjs reads (scripts/nx.sh installs them too; under a second).
	"$root/scripts/pnpm.sh" install --frozen-lockfile --reporter=silent
	exec node "$root/scripts/checks-attest.mjs" "$@"
	;;
*)
	echo "usage: scripts/checks.sh attest [--plan] [--no-post] [--base REF]" >&2
	exit 2
	;;
esac
