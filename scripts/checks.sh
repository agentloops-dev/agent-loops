#!/usr/bin/env bash
# Which checks a change needs, and why (#5583): `scripts/checks.sh plan [BASE]` prints the
# testsets the map (.github/testsets.json) selects for BASE...HEAD and, for each, the rules
# and files that selected it. See scripts/checks.py for the other commands.
set -euo pipefail
exec python3 "$(dirname "$0")/checks.py" "$@"
