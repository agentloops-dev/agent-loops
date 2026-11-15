#!/bin/sh
# Nx, the build tool over the native ones (epic #5627, #5628). Each task is cached by the hash of
# its inputs, in one cache that every worktree shares (~/.nx), so a task that ran at the same
# inputs anywhere on this Mac is not run again. No Nx Cloud, no analytics, no daemon.
#
#   scripts/nx.sh affected -t build vet test lint    what the branch changes, since origin/main
#   scripts/nx.sh run go:test                         one task
#   scripts/nx.sh run-many -t test                    every project's
#   scripts/nx.sh show projects                       go, web, wire, design
#
# The tasks: go:build, go:vet, go:test (with -race), web:build, web:test, web:lint, wire:gen and
# design:gen (each project's project.json). It installs the pinned packages first
# (scripts/pnpm.sh install --frozen-lockfile; under a second when nothing changed).
# Lease "build" first.
#
# A task's hash reads only the inputs its project.json names. Each night slow-tests.yml runs
# every task on main with no cache, and files a bug for a task that fails at a hash where the
# cache has a pass: it reads an input that nobody declared (#5629, scripts/nx-undeclared.mjs).
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
# Rule 2 of #5627: never Nx Cloud. A .env file is no input of any hash, so Nx loads none.
NX_NO_CLOUD=true NX_LOAD_DOT_ENV_FILES=false NX_DAEMON=false NX_TUI=false
export NX_NO_CLOUD NX_LOAD_DOT_ENV_FILES NX_DAEMON NX_TUI
"$root/scripts/pnpm.sh" install --frozen-lockfile --reporter=silent
exec "$root/scripts/pnpm.sh" exec nx "$@"
