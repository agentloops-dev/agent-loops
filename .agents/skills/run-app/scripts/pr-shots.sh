#!/bin/zsh
# pr-shots.sh <pr> <file>... — put a UI enhancement's screenshots under `## Screenshots` in
# its PR's body (#2811). It replaces what is in that section, so a second round shows only
# the new set; write captions after it, not before.
# pr-shots.sh comment <issue> <body.md> <file>... — post body.md as a comment on an issue or
# an epic with the pictures. body.md refers to each one as ![alt](./<file name>); one it does
# not refer to goes at the end.
# Name each file <client>-<state>.png (mac-empty.png, remote-question.png, web-error.png).
# A silent clip for motion (#4094, scripts/video/clip.sh) is an animated .webp or .gif,
# named the same way (mac-scroll.webp). Each file is at most 5 MB, so a page stays quick to open.
# `gh --attach` uploads the pictures (#6963): no branch, no push, no CI. GitHub refuses a GitHub
# App's token there (cli/cli#14309), so this command, and nothing else, uses the person's own gh
# login on this machine (Alex, 2026-11-23). The PR's author stays the bot.
set -euo pipefail

usage() {
  print -u2 "usage: pr-shots.sh <pr> <png | webp | gif>..."
  print -u2 "       pr-shots.sh comment <issue> <body.md> <png | webp | gif>..."
  exit 2
}
fail() { print -u2 "pr-shots: $1"; exit ${2:-1}; }

mode=pr note=
if [[ ${1:-} == comment ]]; then
  mode=comment; shift
  (( $# >= 3 )) || usage
  number=$1 note=$2; shift 2
  [[ -f $note ]] || fail "no file $note" 2
else
  (( $# >= 2 )) || usage
  number=$1; shift
fi
[[ $number == <-> ]] || fail "'$number' is not a number" 2
(( $# <= 50 )) || fail "gh attaches at most 50 files" 2
limit=$(( 5 * 1024 * 1024 ))
typeset -A seen
for f in "$@"; do
  [[ -f $f && $f == *.(png|webp|gif) ]] || fail "$f is not a PNG, WebP or GIF file" 2
  [[ ${f:t} =~ '^[A-Za-z0-9._-]+$' ]] || fail "${f:t}: use only letters, digits, '.', '_' and '-'" 2
  [[ -z ${seen[${f:t}]:-} ]] || fail "two files named ${f:t}" 2
  seen[${f:t}]=1
  bytes=$(wc -c < "$f")
  (( bytes <= limit )) || fail "$f is $(( bytes / 1024 / 1024 )) MB, over 5 MB: a shorter or smaller clip" 2
done

# The app's own gh comes first on PATH and posts as the bot, so name the machine's.
gh=${PR_SHOTS_GH:-}
if [[ -z $gh ]]; then
  for c in /usr/local/bin/gh /opt/homebrew/bin/gh /usr/bin/gh; do
    [[ -x $c ]] && { gh=$c; break; }
  done
fi
[[ -n $gh ]] || fail "no gh in /usr/local/bin, /opt/homebrew/bin or /usr/bin"
as_person() { env -u GH_TOKEN -u GITHUB_TOKEN "$gh" "$@"; }

version=$(as_person --version | sed -n '1s/^gh version \([0-9.]*\).*/\1/p')
autoload -Uz is-at-least
is-at-least 2.99.0 "$version" || fail "$gh is $version; --attach needs 2.99.0 or later"
as_person auth status --hostname github.com >/dev/null 2>&1 ||
  fail "no gh login of a person on this machine (a bot's token cannot upload): take the shots on a machine that has one"

repo=${PR_SHOTS_REPO:-$(as_person repo view --json nameWithOwner -q .nameWithOwner)}
dir=$(mktemp -d "${TMPDIR:-/tmp}/pr-shots.XXXXXX")
trap 'rm -rf "$dir"' EXIT
attach=()
for f in "$@"; do
  cp "$f" "$dir/${f:t}"
  attach+=(--attach "./${f:t}")
done

if [[ $mode == comment ]]; then
  { print -r -- '<!-- agent -->'; cat "$note"; } > "$dir/body.md"
  cd "$dir"
  as_person issue comment "$number" -R "$repo" --body-file body.md "${attach[@]}"
  exit
fi

as_person pr view "$number" -R "$repo" --json body -q .body > "$dir/old.md"
cd "$dir"
python3 -I - "${@:t}" <<'PY'
import re
import sys
from pathlib import Path

lines = Path("old.md").read_text(encoding="utf-8").splitlines()
shots = ["## Screenshots", ""]
for name in sys.argv[1:]:
    shots += [f"![{Path(name).stem}](./{name})", ""]
# A heading inside a code fence is not a heading.
start, end, fence = None, len(lines), None
for i, line in enumerate(lines):
    mark = re.match(r" {0,3}(```+|~~~+)", line)
    if mark:
        kind = mark[1][:3]
        fence = kind if fence is None else (None if fence == kind else fence)
    elif fence is None and start is None and re.match(r"## +Screenshots\s*$", line):
        start = i
    elif fence is None and start is not None and (re.match(r"#{1,2} ", line) or line.startswith("🤖 Generated")):
        end = i
        break
if start is None:
    start = next((i for i, line in enumerate(lines) if line.startswith("🤖 Generated")), len(lines))
    end = start
    if start and lines[start - 1].strip():
        shots.insert(0, "")
Path("body.md").write_text("\n".join(lines[:start] + shots + lines[end:]) + "\n", encoding="utf-8")
PY
as_person pr edit "$number" -R "$repo" --body-file body.md "${attach[@]}"
