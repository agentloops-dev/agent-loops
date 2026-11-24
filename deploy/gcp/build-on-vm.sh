#!/usr/bin/env bash
# The image's build on the VM (#7030): deploy/gcp/up.sh --build runs this there, over ssh, with
# the build context (a gzipped tar) on its standard input.
#
#   build-on-vm.sh IMAGE:TAG VERSION
#
# - It writes each step as it starts and ends, so the log says which step it is on. A build on
#   the e2-micro took 15 to 27 minutes, and some steps say nothing for 7 of them.
# - It ends in AGENTS_BUILD_LIMIT seconds (45 minutes), with 124 (or 137 when it must be killed).
# - It stops when the ssh connection that started it ends: without a terminal, a remote command
#   gets no signal then, and the build went on with nobody to see it.
# - One at a time: 75 says another build holds the lock.
set -euo pipefail
ref=$1
version=$2
limit=${AGENTS_BUILD_LIMIT:-2700}
run=${AGENTS_BUILD_RUN:-/run/lock}
parent=$(ps -o ppid= -p $$)

# A background command's standard input is /dev/null, unless it is given.
exec 3<&0 </dev/null
# The lock's descriptor goes to docker too, so the lock lasts as long as the build does. The pid
# is of timeout, which passes a signal on to docker.
# shellcheck disable=SC2016 # expanded by the sh that sudo starts
sudo flock -n -E 75 "$run/agents-build.lock" sh -c 'echo $$ >"$1"; shift; exec timeout -k 30 "$@"' \
	build "$run/agents-build.pid" "$limit" \
	docker build --progress=plain --build-arg BINARIES=prebuilt --build-arg "VERSION=$version" \
	-f deploy/Containerfile -t "$ref" - <&3 &
build=$!
exec 3<&-

# sshd is this shell's parent until the connection ends. Its output goes nowhere, or ssh would
# wait for it.
(
	while [[ $(ps -o ppid= -p $$) == "$parent" ]]; do sleep "${AGENTS_BUILD_POLL:-5}"; done
	# shellcheck disable=SC2016
	sudo sh -c 'kill "$(cat "$1")"' stop "$run/agents-build.pid"
) </dev/null >/dev/null 2>&1 &
watch=$!

rc=0
wait "$build" || rc=$?
kill "$watch" 2>/dev/null || true
exit "$rc"
