#!/usr/bin/env python3
"""The dead Swift declarations, from Periphery's scans (scripts/lint.sh periphery, #6336).

    periphery-report.py --allowed FILE (--xcode SCAN.json DERIVEDDATA | --package SCAN.json DIR)...
    periphery-report.py --selftest

Each scan is Periphery's JSON for one index store: one app scheme's DerivedData (--xcode), or
one Swift package built with its tests (--package). Periphery gives a different answer on each
run when one store holds two units for one file (two platforms or two archs), so each store has
one build of each file and is scanned alone. A declaration is dead only when every scan that
compiles its file reports it: a scan of the Mac app says that a Shared/UI view only the Remote
draws is unused, and a scan of the apps says that a type only AgentsKit's tests use is unused.
An Xcode scan compiles the files in its DerivedData's .SwiftFileList files; a package scan, every
file under its folder. A file with more than one path (AgentsKit's test targets share their
Support folder by a link) is a declaration in each target that compiles it: it is dead only when
it is dead at every one of its paths, and it is reported once, at its real path.

A declaration kept on purpose is a line in the allowlist: its path from the repository's root,
a colon, Periphery's name for it (or * for every declaration in a generated file), a space, then
why it stays. A line that matches no finding fails too: the declaration went, so its line goes.
Prints path:line:col: lines and exits 1 when there is one.
"""

import glob
import json
import os
import sys

SKIP = ("../", "build/", ".build/")


def rel(root, path):
    return os.path.relpath(path, root) if os.path.isabs(path) else path


def findings(root, scan):
    """{(path, line, col, kind, name, hint)} of one scan's JSON."""
    out = set()
    with open(scan) as f:
        for x in json.load(f):
            path, line, col = x["location"].rsplit(":", 2)
            path = rel(root, path)
            if path.startswith(SKIP) or "/.build/" in path or "/Library/Caches/" in path:
                continue
            hint = (x.get("hints") or ["unused"])[0]
            out.add((path, int(line), int(col), x["kind"], x["name"], hint))
    return out


def xcode_files(root, derived):
    files = set()
    for lst in glob.glob(os.path.join(derived, "Build/Intermediates.noindex/**/*.SwiftFileList"), recursive=True):
        with open(lst) as f:
            files.update(rel(root, line.strip()) for line in f if line.strip())
    return files


def read_allowed(path):
    """[(path, name, reason, line number)] of the allowlist."""
    allowed = []
    try:
        lines = open(path).read().splitlines()
    except FileNotFoundError:
        return allowed
    for n, line in enumerate(lines, 1):
        if not line.strip() or line.startswith("#"):
            continue
        where, sep, rest = line.partition(": ")
        name, _, reason = rest.partition(" ")
        if not sep or not name or not reason.strip():
            raise SystemExit(f"{path}:{n}: want 'path: name why', got {line!r}")
        allowed.append((where, name, reason.strip(), n))
    return allowed


def aliases(root, folders):
    """{path: real path} of every .swift file under folders reached through a link."""
    out = {}
    for folder in folders:
        for top, dirs, files in os.walk(os.path.join(root, folder), followlinks=True):
            dirs[:] = [d for d in dirs if d != ".build"]
            for name in files:
                if name.endswith(".swift"):
                    path = rel(root, os.path.join(top, name))
                    real = rel(root, os.path.realpath(os.path.join(top, name)))
                    if real != path:
                        out[path] = real
    return out


def dead(scans, alias=None):
    """scans: [(findings, covers(path) -> bool)]. The findings every covering scan reports, at
    every path of their file; alias maps a linked path to its real one."""
    alias = alias or {}
    every = set().union(*(f for f, _ in scans)) if scans else set()
    paths = {}
    for path, real in alias.items():
        paths.setdefault(real, {real}).add(path)
    found = set()
    for k in every:
        covering = [f for f, covers in scans if covers(k[0])]
        if covering and all(k in f for f in covering):
            found.add(k)
    out = set()
    for k in found:
        real = alias.get(k[0], k[0])
        if all((p,) + k[1:] in found for p in paths.get(real, {real})):
            out.add((real,) + k[1:])
    return sorted(out)


def report(found, allowed, allowed_path):
    """Lines to print: the findings no allowlist line names, then the lines that name none."""
    lines, used = [], set()
    for path, line, col, kind, name, hint in found:
        hit = [a for a in allowed if a[0] == path and a[1] in (name, "*")]
        used.update(a[3] for a in hit)
        if not hit:
            what = "assigned but never read" if hint == "assignOnlyProperty" else "unused"
            lines.append(f"{path}:{line}:{col}: {kind} {name} is {what}")
    for where, name, _, n in allowed:
        if n not in used:
            lines.append(f"{allowed_path}:{n}: {where}: {name} is allowed, but Periphery no longer reports it: remove the line")
    return lines


def main(argv):
    root = os.getcwd()
    allowed_path, scans, packages, i = None, [], [], 0
    while i < len(argv):
        a = argv[i]
        if a == "--allowed":
            allowed_path = argv[i + 1]
            i += 2
        elif a == "--xcode":
            files = xcode_files(root, argv[i + 2])
            if not files:
                raise SystemExit(f"periphery-report.py: no .SwiftFileList in {argv[i + 2]}")
            scans.append((findings(root, argv[i + 1]), lambda p, files=files: p in files))
            i += 3
        elif a == "--package":
            d = argv[i + 2].rstrip("/") + "/"
            packages.append(d)
            scans.append((findings(root, argv[i + 1]), lambda p, d=d: p.startswith(d)))
            i += 3
        else:
            raise SystemExit(__doc__)
    if not allowed_path or not scans:
        raise SystemExit(__doc__)
    lines = report(dead(scans, aliases(root, packages)), read_allowed(allowed_path), allowed_path)
    for line in lines:
        print(line)
    return 1 if lines else 0


def selftest():
    a = ("Shared/UI/A.swift", 1, 2, "struct", "A", "unused")
    b = ("Shared/UI/B.swift", 3, 4, "function.free", "b()", "unused")
    c = ("Packages/K/Sources/C.swift", 5, 6, "var.instance", "c", "assignOnlyProperty")
    mac = ({a, b, c}, lambda p: p.startswith(("Shared/", "Packages/")))
    ios = ({a, c}, lambda p: p.startswith(("Shared/", "Packages/")))
    pkg = ({c}, lambda p: p.startswith("Packages/K/"))
    # b is used by the Remote: live. a and c are reported by every scan that compiles them.
    assert dead([mac, ios, pkg]) == [c, a], dead([mac, ios, pkg])
    # A package's tests use c: live.
    assert dead([mac, ios, (set(), pkg[1])]) == [a]
    # A file linked into two test targets: dead only where both say so, reported at its real path.
    t1 = ("Packages/K/Tests/T1/Support/S.swift", 1, 1, "function.free", "s()", "unused")
    t2 = ("Packages/K/Tests/T2/Support/S.swift",) + t1[1:]
    link = {"Packages/K/Tests/T2/Support/S.swift": "Packages/K/Tests/T1/Support/S.swift"}
    every = lambda p: True  # noqa: E731
    assert dead([({t2}, every)], link) == [], "used in T1, so live"
    assert dead([({t1, t2}, every)], link) == [t1], dead([({t1, t2}, every)], link)
    allowed = [("Shared/UI/A.swift", "A", "why", 7), ("Shared/UI/Z.swift", "*", "why", 8)]
    out = report([a, c], allowed, "allowed.txt")
    assert out == [
        "Packages/K/Sources/C.swift:5:6: var.instance c is assigned but never read",
        "allowed.txt:8: Shared/UI/Z.swift: * is allowed, but Periphery no longer reports it: remove the line",
    ], out
    assert report([a], [("Shared/UI/A.swift", "*", "generated", 1)], "x") == []
    print("periphery-report.py: selftest passed")
    return 0


if __name__ == "__main__":
    sys.exit(selftest() if sys.argv[1:] == ["--selftest"] else main(sys.argv[1:]))
