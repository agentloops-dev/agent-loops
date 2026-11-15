#!/usr/bin/env python3
"""Give each file and folder that git tracks a modification time made from its content (#5677).

Go's test cache keeps a package's pass only while every file and folder its tests opened has the
same size and modification time (cmd/go: hashOpen, hashWriteStat). A fresh checkout gives each
file the time of the checkout, so on CI the tests that read the repo (fixtures, deploy/, the
generated files) ran again on every run, even with Go's cache restored: on a Mac about 20 of the
module's 70 tested packages, internal/agentsd and internal/controlplane with them.

Here a file's time comes from its blob's hash, and a folder's from its tree's: the same content
has the same time in every checkout, and a change has a new one. Go writes the time to the
nanosecond, so the time carries about 58 bits of the hash (28 bits of seconds after 2000-01-01,
then the nanoseconds): a pass is kept for changed content only when the size is the same and 58
bits of two SHA-1 hashes are the same. Every time is years old, so Go's 2-second rule for new
files never applies. A file that git does not track (Web/dist, node_modules) keeps its own time.

    python3 scripts/ci-mtimes.py   # from the repo's root, after the checkout

Only CI runs it; a lane's worktree keeps the times that git gave it.
"""

import os
import subprocess
import sys

EPOCH = 946684800  # 2000-01-01T00:00:00Z
NANOS = 1_000_000_000


def stamp(sha: str) -> int:
    """The time, in nanoseconds since 1970, that this object's hash gives."""
    seconds = int(sha[:7], 16)  # 28 bits: up to about 2008
    nanos = int(sha[7:15], 16) % NANOS
    return (EPOCH + seconds) * NANOS + nanos


def main() -> int:
    listing = subprocess.run(
        ["git", "ls-tree", "-r", "-t", "-z", "HEAD"],
        check=True,
        capture_output=True,
    ).stdout
    root = subprocess.run(
        ["git", "rev-parse", "HEAD^{tree}"], check=True, capture_output=True, text=True
    ).stdout.strip()
    files = folders = 0
    trees = []
    for entry in listing.split(b"\0"):
        if not entry:
            continue
        meta, path = entry.split(b"\t", 1)
        mode, kind, sha = meta.decode().split(" ")
        name = os.fsdecode(path)
        if kind == "tree":
            trees.append((name, sha))
        elif kind == "blob" and mode != "120000":
            if os.path.lexists(name):
                t = stamp(sha)
                os.utime(name, ns=(t, t))
                files += 1
        # A symlink (120000) and a submodule (commit) keep their own time.
    # Folders last, deepest first: setting a time inside a folder does not change the folder's,
    # but this order holds whatever a file system does.
    trees.sort(key=lambda item: item[0].count("/"), reverse=True)
    for name, sha in trees + [(".", root)]:
        if os.path.isdir(name) and not os.path.islink(name):
            t = stamp(sha)
            os.utime(name, ns=(t, t))
            folders += 1
    print(f"ci-mtimes: {files} files and {folders} folders take their content's time")
    return 0


if __name__ == "__main__":
    sys.exit(main())
