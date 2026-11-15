#!/bin/sh
# Nx, the build tool over the native ones (epic #5627, #5628). Each task is cached by the hash of
# its inputs, in one cache that every worktree shares (~/.nx), so a task that ran at the same
# inputs anywhere on this Mac is not run again. No Nx Cloud, no analytics, no daemon.
#
#   scripts/nx.sh affected -t gen build vet lint test   what the branch changes, since origin/main
#   scripts/nx.sh affected -t test --exclude='tag:xcode' the same, with no Swift task
#   scripts/nx.sh run agentskit:test-unit                 one task
#   scripts/nx.sh run-many -t test                        every project's
#   scripts/nx.sh show projects                           the projects
#
# The tasks: go:build, go:vet, go:test (with -race), web:build, web:test, web:lint, wire:gen,
# design:gen, mac:build (AgentsHost), remote:build (the Remote), agentskit:test (test-unit,
# test-control, test-integration), agentskit-repo:test and codetext:test (each project's
# project.json; #5631). The Swift ones run through scripts/build-cache.sh, one at a time. A
# project tagged inputs-only has no tasks: it is a folder that other projects' tasks read.
# It installs the pinned packages first (scripts/pnpm.sh install --frozen-lockfile; under a
# second when nothing changed).
# Lease "build" first.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
# Rule 2 of #5627: never Nx Cloud. A .env file is no input of any hash, so Nx loads none.
NX_NO_CLOUD=true NX_LOAD_DOT_ENV_FILES=false NX_DAEMON=false NX_TUI=false
export NX_NO_CLOUD NX_LOAD_DOT_ENV_FILES NX_DAEMON NX_TUI
"$root/scripts/pnpm.sh" install --frozen-lockfile --reporter=silent
exec "$root/scripts/pnpm.sh" exec nx "$@"
