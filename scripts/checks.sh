#!/bin/sh
# A lane's checks, attested (#5584, epic #5581).
#
#   scripts/checks.sh attest run        before the push: run the affected test, vet and lint
#                                       tasks with Nx and no cache hits, at the head commit, and
#                                       keep the check run on disk by head sha (#6059)
#   scripts/checks.sh attest post       just after the push: post the kept check run
#                                       attestation/<platform> on the pull request's head, as the
#                                       agents' bot, when the head is that commit and no task's
#                                       hash changed; runs no task, so it takes seconds
#   scripts/checks.sh attest            both, after the push: one in-toto test-result statement
#                                       for each task that passed on its first run
#   scripts/checks.sh attest --plan     the tasks, their Nx hashes and the inputs that changed;
#                                       runs nothing
#   scripts/checks.sh attest --no-post  run them, keep them, and print the check run
#   scripts/checks.sh attest --base REF the change since REF (default: its merge base with
#                                       origin/main)
#
# Commit first: the statements name each task by its Nx hash at the head commit. Run before the
# push and post just after it, so the check run is there when CI's `changes` job reads it, about
# a minute after the push; CI skips the tasks it covers (#5585). The rules and the statement's
# shape are in scripts/checks-attest.mjs. Lease "build" for the run (not for post or --plan).
# The run exits 1, and names them, when a planned task failed or did not start here (#6154); it
# still keeps the statements of the tasks that passed. A task skipped by its pin is not one.
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
	echo "usage: scripts/checks.sh attest [run | post] [--plan] [--no-post] [--base REF]" >&2
	exit 2
	;;
esac
