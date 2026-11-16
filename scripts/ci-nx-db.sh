#!/usr/bin/env bash
# Give a restored Nx cache's database this Mac's name, so Nx can use the cache (#5762).
#
# Nx keeps what it knows about its cache in a SQLite file in its workspace-data folder,
# named for the machine: `<IOPlatformUUID>-v<n>.db` on macOS. When a runner's UUID differs
# from the one that saved the cache (the runners of one image share theirs, so a new image),
# Nx makes a new, empty database next to the restored one, says "Unrecognized Cache
# Artifacts", and runs every task again. The slow Swift steps of #5762 were a cold start
# after the Xcode pin of #5838, not this; this keeps a new image from adding to one.
#
# This renames the newest restored database (with its -wal and -shm files) to this Mac's
# name and deletes the others, which were for runners that no longer exist. It does
# nothing when no database was restored, or when this Mac's own is already there.
#
# Usage, in a step after the Nx cache restore and before any Nx command:
#   run: bash scripts/ci-nx-db.sh "$NX_WORKSPACE_DATA_DIRECTORY"
set -euo pipefail

dir=${1:?usage: ci-nx-db.sh <Nx workspace-data folder>}
[[ -d $dir ]] || { echo "No Nx workspace data at $dir: nothing to rename."; exit 0; }

uuid=$(ioreg -rd1 -c IOPlatformExpertDevice | awk -F'"' '/IOPlatformUUID/ { print $4 }')
[[ -n $uuid ]] || { echo "No IOPlatformUUID on this machine: nothing to rename."; exit 0; }

# The newest database first (the cache's tar keeps each file's time).
newest=""
while IFS= read -r f; do
  newest=$f
  break
done < <(ls -t "$dir"/*-v*.db 2>/dev/null || true)
[[ -n $newest ]] || { echo "No Nx database was restored."; exit 0; }

name=$(basename "$newest")
version=${name##*-}             # v6.db
target="$dir/$uuid-$version"
if [[ -e $target ]]; then
  echo "This Mac's Nx database is there already: $(basename "$target")."
  exit 0
fi

for ext in "" -wal -shm; do
  if [[ -e $newest$ext ]]; then mv "$newest$ext" "$target$ext"; fi
done
echo "Nx database $name is now $(basename "$target")."

# The other runners' databases: no runner opens them again.
for f in "$dir"/*-v*.db "$dir"/*-v*.db-wal "$dir"/*-v*.db-shm; do
  [[ -e $f ]] || continue
  case $(basename "$f") in "$uuid"-*) continue ;; esac
  rm -f "$f"
  echo "Deleted the old runner's $(basename "$f")."
done
