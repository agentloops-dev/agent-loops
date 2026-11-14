#!/usr/bin/env bash
# CI's selection (#594, #1135): which SwiftPM suites, Go jobs and macOS jobs a change needs,
# as GITHUB_OUTPUT keys (agentskit, agentskit_filter, codetext, codetext_filter, go, apps,
# marks). The rules are the testset map, .github/testsets.json (#5583); scripts/checks.py
# reads it, and `scripts/checks.sh plan <base>` says why each one was chosen. With no base
# commit, or no changed file, everything runs.
#
# Usage: scripts/select-test-suites.sh <base-commit> >> "$GITHUB_OUTPUT"
set -euo pipefail

python3 "$(dirname "$0")/checks.py" outputs "${1:-}" >> "${GITHUB_OUTPUT:-/dev/stdout}"
