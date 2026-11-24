#!/usr/bin/env python3
"""Keep new Go file stores behind the bounded store constructors (#6683).

The baseline is an allow-list of existing direct folder-store openings. It may
shrink as callers move to a kind constructor. A new opening is always an error.
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
BASELINE = ROOT / "scripts/store-bounds-baseline.txt"
OPEN = re.compile(r"\bstore\.NewFolder\s*\(")


def findings(files):
    sources = {path.relative_to(ROOT).as_posix(): path.read_text() for path in files}
    return check_sources(sources, set(BASELINE.read_text().splitlines()) if BASELINE.exists() else set())


def check_sources(sources, baseline):
    present = set()
    found = []
    for rel, source in sources.items():
        if rel.endswith("_test.go"):
            continue
        for number, line in enumerate(source.splitlines(), 1):
            if OPEN.search(line):
                key = f"{rel}:store.NewFolder"
                present.add(key)
                found.append((key, number))
    unlisted = sorted({key for key, _ in found} - baseline)
    stale = sorted(baseline - present)
    return unlisted, stale


def selftest():
    old = "Go/internal/old/store.go:store.NewFolder"
    new = "Go/internal/new/store.go:store.NewFolder"
    baseline = {old}
    assert check_sources({"Go/internal/old/store.go": "s := store.NewFolder(path)"}, baseline) == ([], [])
    assert check_sources({"Go/internal/old/store.go": "", "Go/internal/new/store.go": "s := store.NewFolder(path)"}, baseline) == ([new], [old])
    assert check_sources({"Go/internal/new/store_test.go": "s := store.NewFolder(path)"}, set()) == ([], [])
    print("check-store-bounds: selftest ok")


def main():
    if sys.argv[1:] == ["--selftest"]:
        selftest()
        return 0
    files = sorted((ROOT / "Go").rglob("*.go"))
    unlisted, stale = findings(files)
    for item in unlisted:
        print(f"{item}: direct store has no kind or bounds; use a bounded kind constructor", file=sys.stderr)
    for item in stale:
        print(f"{item}: baseline entry is no longer present; remove it", file=sys.stderr)
    return 1 if unlisted or stale else 0


if __name__ == "__main__":
    sys.exit(main())
