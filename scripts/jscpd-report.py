#!/usr/bin/env python3
"""jscpd's JSON report as lint lines, held against a baseline (#5672).

    jscpd-report.py --baseline FILE REPORT        print each clone; exit 1 when one is new
    jscpd-report.py --write-baseline FILE REPORT  write every clone to FILE
    jscpd-report.py --selftest

A clone's key is its pair of files, sorted, with no line numbers: `a.swift <-> b.swift`. The
baseline has one line per clone, so a pair with two clones has its key twice. A clone that only
moves keeps its key; a new clone, also a second one between files that already have one, is
not in the baseline and fails. Only the new clones print, and how many old ones were not shown.
"""

import argparse
import json
import sys
from collections import Counter
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def relative_name(file):
    path = Path(file["name"])
    if str(path).startswith("/src/"):
        return str(path).removeprefix("/src/")
    try:
        return path.resolve().relative_to(ROOT).as_posix()
    except ValueError:
        return path.as_posix()


def location(file):
    return f"{relative_name(file)}:{file['startLoc']['line']}"


def key(duplicate):
    a, b = sorted((relative_name(duplicate["firstFile"]), relative_name(duplicate["secondFile"])))
    return f"{a} <-> {b}"


def check(duplicates, baseline):
    """The lint lines of the new clones, and how many clones are old."""
    left = Counter(baseline)
    lines, old = [], 0
    for duplicate in sorted(duplicates, key=lambda d: (key(d), location(d["firstFile"]))):
        k = key(duplicate)
        if left[k] > 0:
            left[k] -= 1
            old += 1
            continue
        first, second = duplicate["firstFile"], duplicate["secondFile"]
        lines.append(f"{location(first)}: clone with {location(second)} ({duplicate['lines']} lines)")
    return lines, old


def selftest():
    def dup(a, b, line=1):
        return {"firstFile": {"name": a, "startLoc": {"line": line}},
                "secondFile": {"name": b, "startLoc": {"line": line}}, "lines": 12}
    lines, old = check([dup("/src/b.go", "/src/a.go")], ["a.go <-> b.go"])
    assert (lines, old) == ([], 1), lines
    lines, old = check([dup("/src/a.go", "/src/b.go"), dup("/src/a.go", "/src/b.go", 40)], ["a.go <-> b.go"])
    assert (len(lines), old) == (1, 1), lines
    lines, old = check([dup("/src/a.go", "/src/c.go")], ["a.go <-> b.go"])
    assert lines == ["a.go:1: clone with c.go:1 (12 lines)"] and old == 0, lines
    print("jscpd-report.py: selftest ok")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("report", type=Path, nargs="?")
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--baseline", type=Path)
    group.add_argument("--write-baseline", type=Path)
    group.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        selftest()
        return 0
    duplicates = json.loads(args.report.read_text())["duplicates"]
    if args.write_baseline:
        args.write_baseline.write_text("".join(f"{k}\n" for k in sorted(key(d) for d in duplicates)))
        print(f"jscpd-report.py: wrote {len(duplicates)} clone(s) to {args.write_baseline}", file=sys.stderr)
        return 0
    baseline = args.baseline.read_text().splitlines() if args.baseline.exists() else []
    lines, old = check(duplicates, baseline)
    for line in lines:
        print(line)
    if old:
        print(f"jscpd-report.py: {old} old clone(s) in the baseline not shown", file=sys.stderr)
    if lines:
        print(f"jscpd-report.py: {len(lines)} new clone(s), not in {args.baseline}: share the code, "
              "or add the path to .jscpd.json's ignore and say why in the pull request", file=sys.stderr)
    return 1 if lines else 0


if __name__ == "__main__":
    sys.exit(main())
