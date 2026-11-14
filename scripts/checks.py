#!/usr/bin/env python3
"""Which checks a change needs, and why, from the testset map (#5583, epic #5581).

The map, .github/testsets.json, says which changed files (a fileset, by rule) select which
testsets, each on its platform. This is its one reader, so CI, a lane and a merge wave
choose the same checks for the same files:

  checks.py plan [--json] [--worktree] [BASE]  the testsets and, for each, the rules and
                                                files that chose it (default BASE: the
                                                merge base with origin/main)
  checks.py outputs [BASE]                      CI's keys (agentskit=, go=, apps=, ...) for
                                                scripts/select-test-suites.sh
  checks.py builds                              merge-wave: the builds for paths on stdin
  checks.py go-packages [BASE]                  scripts/go-changed.sh: the Go packages to
                                                test, committed or not ('./...' for all)
  checks.py --selftest                          the map still selects at least what the
                                                scripts it replaced selected (principle 8)

BASE...HEAD is the diff, as `git diff BASE...HEAD`; --worktree adds uncommitted and
untracked files. With no BASE commit, or no changed file, every group selects all its
testsets: no diff problem turns into a green run that tested nothing.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MAP = os.path.join(ROOT, ".github", "testsets.json")
PACKAGES = {"agentskit": "Packages/AgentsKit", "codetext": "Packages/CodeText"}


def load_map(path: str = MAP) -> dict:
    with open(path, encoding="utf-8") as f:
        m = json.load(f)
    for rule in m["rules"]:
        rule["_re"] = re.compile("|".join(glob_re(p) for p in rule["paths"]))
    return m


def glob_re(glob: str) -> str:
    """A glob as a regex: * within one folder, ** across folders, **/ also none."""
    out, i = "", 0
    while i < len(glob):
        if glob.startswith("**/", i):
            out += "(?:.*/)?"
            i += 3
        elif glob.startswith("**", i):
            out += ".*"
            i += 2
        elif glob[i] == "*":
            out += "[^/]*"
            i += 1
        elif glob[i] == "?":
            out += "[^/]"
            i += 1
        else:
            out += re.escape(glob[i])
            i += 1
    return "(?:" + out + ")$"


def git(*args: str, cwd: str) -> str:
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout


def changed_files(base: str | None, cwd: str, worktree: bool = False) -> list[str] | None:
    """The files changed since BASE, or None when BASE is no commit."""
    if not base or subprocess.run(["git", "cat-file", "-e", base + "^{commit}"], cwd=cwd, capture_output=True).returncode:
        return None
    if worktree:
        mb = git("merge-base", base, "HEAD", cwd=cwd).strip()
        names = git("diff", "--name-only", "-z", mb, cwd=cwd).split("\0")
        names += git("ls-files", "--others", "--exclude-standard", "-z", cwd=cwd).split("\0")
    else:
        names = git("diff", "--name-only", "-z", base + "...HEAD", cwd=cwd).split("\0")
    return sorted({n for n in names if n})


class Plan:
    """The testsets chosen, each with its reasons: (rule id, file) pairs and notes."""

    def __init__(self, m: dict):
        self.m = m
        self.why: dict[str, list[tuple[str, str]]] = {}
        self.notes: dict[str, str] = {}

    def add(self, testset: str, rule: str, path: str) -> None:
        reasons = self.why.setdefault(testset, [])
        if (rule, path) not in reasons:
            reasons.append((rule, path))

    def platform(self, testset: str) -> str:
        return self.m["testsets"][testset.split(":", 1)[0]]["platform"]


def suite_declared(path: str, cwd: str) -> str | None:
    """The suite a <Name>Tests.swift file declares, or None."""
    name = os.path.basename(path)
    if not name.endswith("Tests.swift"):
        return None
    suite = name[: -len(".swift")]
    try:
        with open(os.path.join(cwd, path), encoding="utf-8", errors="replace") as f:
            text = f.read()
    except OSError:
        return None
    if re.search(r"(^|\s)(struct|class)\s+" + re.escape(suite) + r"([\s<{:]|$)", text):
        return suite
    return None


def go_package(path: str, cwd: str) -> str | None:
    """./<dir> for a Go file's package (a testdata file's is the folder above it)."""
    d = os.path.dirname(path)
    d = re.sub(r"/testdata(/.*)?$", "", d)
    full = os.path.join(cwd, d)
    if not os.path.isdir(full) or not any(n.endswith(".go") for n in os.listdir(full)):
        return None
    return "./" + d[len("Go/"):] if d.startswith("Go/") else None


def plan(m: dict, files: list[str] | None, cwd: str) -> Plan:
    p = Plan(m)
    if not files:
        reason = "no-base" if files is None else "empty-diff"
        for g in m["groups"].values():
            for t in g["all"]:
                p.add(t, reason, "")
        p.notes["*"] = "no base commit" if files is None else "no changed file"
        return p
    raw: list[tuple[str, str, str]] = []  # (testset, rule, file) before packages resolve
    if_filtered: list[tuple[str, str, str]] = []
    for group, g in m["groups"].items():
        rules = [r for r in m["rules"] if r["group"] == group]
        for f in files:
            rule = next((r for r in rules if r["_re"].match(f)), None)
            if rule is None:
                chosen, rid = (g["all"] if g["unmatched"] == "all" else []), "unmatched"
            else:
                chosen = g["all"] if rule["testsets"] == "all" else rule["testsets"]
                rid = rule["id"]
                if_filtered += [(t, rid, f) for t in rule.get("if_filtered", [])]
            raw += [(t, rid, f) for t in chosen]
    resolve(p, raw, if_filtered, cwd)
    return p


def resolve(p: Plan, raw: list, if_filtered: list, cwd: str) -> None:
    """Turn the markers into testsets: a package runs named suites only when every file
    that touched it allows that (the rules of the old select-test-suites.sh); else whole."""
    for pkg in PACKAGES:
        mine = [(t, r, f) for t, r, f in raw if t == pkg or t.startswith(pkg + ":")]
        if not mine:
            continue
        whole = any(t == pkg for t, _, _ in mine)
        source = any(t == pkg + ":{source}" for t, _, _ in mine)
        only_tests, suites = True, []
        for t, r, f in mine:
            if t == pkg + ":{file-suite}":
                s = suite_declared(f, cwd)
                if s is None:
                    only_tests = False
                else:
                    suites.append((pkg + ":" + s, r, f))
            elif not t.endswith("}") and t != pkg:
                suites.append((t, r, f))
        if not whole and suites and (source or only_tests):
            for t, r, f in suites + [x for x in if_filtered if x[0].startswith(pkg + ":")]:
                p.add(t, r, f)
        else:
            for _, r, f in mine:
                p.add(pkg, r, f)
            p.notes[pkg] = (
                "a rule selects the whole package" if whole
                else "a test file that declares no suite of its name" if not only_tests and not source
                else "no test suite changed with the source" if source
                else "no suite named"
            )
    if any(t == "go-changed" for t, _, _ in raw):
        for t, r, f in raw:
            if t.startswith("go-changed"):
                p.add("go-changed", r, f)
    else:
        for t, r, f in raw:
            if t == "go-changed:{package}":
                pkg = go_package(f, cwd)
                if pkg:
                    p.add("go-changed:" + pkg, r, f)
    for t, r, f in raw:
        if t.split(":", 1)[0] not in PACKAGES and not t.startswith("go-changed"):
            p.add(t, r, f)


def ordered(p: Plan) -> list[str]:
    """Testsets in the map's order, then suites and packages by name."""
    order = list(p.m["testsets"])
    return sorted(p.why, key=lambda t: (order.index(t.split(":", 1)[0]), t != t.split(":", 1)[0], t))


def outputs(p: Plan) -> dict[str, str]:
    """CI's keys, as scripts/select-test-suites.sh always printed them."""
    out: dict[str, str] = {}
    for pkg in PACKAGES:
        suites = [t.split(":", 1)[1] for t in p.why if t.startswith(pkg + ":")]
        out[pkg] = "full" if pkg in p.why else "filter" if suites else "skip"
        # SwiftPM reads --filter as a regex over the test's full name; suites are identifiers.
        out[pkg + "_filter"] = "|".join(suites) if out[pkg] == "filter" else ""
    out["go"] = "run" if "go" in p.why else "skip"
    out["apps"] = "build" if "apps" in p.why else "skip"
    out["marks"] = "run" if "marks" in p.why else "skip"
    return out


def print_plan(p: Plan, files: list[str] | None, base: str | None, as_json: bool) -> None:
    rows = [{"testset": t, "platform": p.platform(t),
             "why": [{"rule": r, "file": f} for r, f in p.why[t]],
             **({"note": p.notes[t]} if t in p.notes else {})} for t in ordered(p)]
    if as_json:
        json.dump({"base": base, "files": files, "testsets": rows, **({"note": p.notes["*"]} if "*" in p.notes else {})}, sys.stdout, indent=1)
        print()
        return
    print(f"checks for {len(files or [])} changed files since {base or '(no base)'} ({os.path.relpath(MAP, ROOT)}):")
    if "*" in p.notes:
        print(f"  every testset: {p.notes['*']}")
    for row in rows:
        print(f"{row['testset']}  [{row['platform']}]" + (f"  ({row['note']})" if "note" in row else ""))
        by_rule: dict[str, list[str]] = {}
        for w in row["why"]:
            by_rule.setdefault(w["rule"], []).append(w["file"])
        for r, fs in by_rule.items():
            shown = [f for f in fs if f]
            more = f" and {len(shown) - 3} more" if len(shown) > 3 else ""
            print(f"  {r}: {', '.join(shown[:3])}{more}" if shown else f"  {r}")
    if not rows:
        print("  none")


def default_base(cwd: str) -> str | None:
    r = subprocess.run(["git", "merge-base", "origin/main", "HEAD"], cwd=cwd, capture_output=True, text=True)
    return r.stdout.strip() or None


def main(argv: list[str]) -> int:
    cwd = os.getcwd()
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__)
        return 0 if argv else 2
    cmd, rest = argv[0], argv[1:]
    if cmd == "--selftest":
        return selftest()
    m = load_map()
    flags = {a for a in rest if a.startswith("--")}
    args = [a for a in rest if not a.startswith("--")]
    if cmd == "builds":
        files = sorted({line.strip() for line in sys.stdin if line.strip()})
        p = plan(m, files, cwd) if files else Plan(m)
        for t in m["groups"]["builds"]["all"]:
            if t in p.why:
                print(t)
        return 0
    # CI's outputs take no default: an empty BASE is no base, so every testset runs.
    base = args[0] if args else None if cmd == "outputs" else default_base(cwd)
    if cmd == "plan":
        files = changed_files(base, cwd, "--worktree" in flags)
        print_plan(plan(m, files, cwd), files, base, "--json" in flags)
    elif cmd == "outputs":
        files = changed_files(base, cwd)
        for k, v in outputs(plan(m, files, cwd)).items():
            print(f"{k}={v}")
    elif cmd == "go-packages":
        files = changed_files(base, cwd, worktree=True) or []
        p = plan(m, files, cwd) if files else Plan(m)
        if "go-changed" in p.why:
            print("./...")
        else:
            for t in ordered(p):
                if t.startswith("go-changed:"):
                    print(t.split(":", 1)[1])
    else:
        print(f"checks.py: no command {cmd}", file=sys.stderr)
        return 2
    return 0


# --- selftest ------------------------------------------------------------------------
# checks-today.json holds sample diffs and what the old scripts chose for them on
# 2026-11-14 (scripts/select-test-suites.sh, merge-wave.sh's builds_for_paths,
# go-changed.sh's packages and CI's marks step). The map must choose at least that.

RANK = {"skip": 0, "filter": 1, "full": 2, "run": 1, "build": 1}


def at_least(new: dict, old: dict, case: list) -> list[str]:
    bad = []
    for key in ("agentskit", "codetext", "go", "apps", "marks"):
        o, n = old.get(key, "skip"), new[key]
        if RANK[n] < RANK[o]:
            bad.append(f"{case}: {key}={n}, was {o}")
        elif o == "filter" and n == "filter":
            lost = set(old[key + "_filter"].split("|")) - set(new[key + "_filter"].split("|"))
            if lost:
                bad.append(f"{case}: {key} lost suites {sorted(lost)}")
    return bad


def selftest() -> int:
    m = load_map()
    with open(os.path.join(ROOT, "scripts", "checks-today.json"), encoding="utf-8") as f:
        cases = json.load(f)
    bad: list[str] = []
    for case in cases:
        with tempfile.TemporaryDirectory() as d:
            git("init", "-q", cwd=d)
            for path in case.get("base", []):
                write(d, path)
            git("add", "-A", cwd=d)
            git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base", cwd=d)
            for path in case["paths"]:
                write(d, path, nodecl=path in case.get("nodecl", []))
            git("add", "-A", cwd=d)
            git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "change", cwd=d)
            files = changed_files("HEAD~1", d)
            p = plan(m, files, d)
            bad += at_least(outputs(p), case["today"], case["paths"])
            builds = [t for t in m["groups"]["builds"]["all"] if t in p.why]
            lost = set(case["today"].get("builds", "").split()) - set(builds)
            if lost:
                bad.append(f"{case['paths']}: lost builds {sorted(lost)}")
            pkgs = {"./..."} if "go-changed" in p.why else {t.split(":", 1)[1] for t in p.why if t.startswith("go-changed:")}
            want = set(case["today"].get("go_packages", "").split())
            if not want <= pkgs and "./..." not in pkgs:
                bad.append(f"{case['paths']}: go-changed {sorted(pkgs)}, was {sorted(want)}")
            for t in p.why:
                if not p.why[t]:
                    bad.append(f"{case['paths']}: {t} has no reason")
    # No base, and a glob or two.
    p = plan(m, None, ROOT)
    if outputs(p) != {"agentskit": "full", "agentskit_filter": "", "codetext": "full", "codetext_filter": "", "go": "run", "apps": "build", "marks": "run"}:
        bad.append(f"no base: {outputs(p)}")
    for glob, path, want in [("**/*.md", "README.md", True), ("**/*.md", "a/b/c.md", True), ("Go/*", "Go/a/b.go", False),
                             ("Go/**", "Go/a/b.go", True), ("App/Resources/Assets.xcassets/AppIcon**", "App/Resources/Assets.xcassets/AppIcon.appiconset/x.png", True)]:
        if bool(re.match(glob_re(glob), path)) != want:
            bad.append(f"glob {glob} on {path}: want {want}")
    ids = [r["id"] for r in m["rules"]]
    if len(ids) != len(set(ids)):
        bad.append("rule ids are not unique")
    for r in m["rules"]:
        for t in r["testsets"] if isinstance(r["testsets"], list) else []:
            if t.split(":", 1)[0] not in m["testsets"]:
                bad.append(f"rule {r['id']}: no testset {t}")
    for b in bad:
        print("checks.py selftest:", b, file=sys.stderr)
    if not bad:
        print(f"checks.py selftest: {len(cases)} sample diffs select at least what they did before")
    return 1 if bad else 0


def write(d: str, path: str, nodecl: bool = False) -> None:
    full = os.path.join(d, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    name = os.path.basename(path)
    with open(full, "w", encoding="utf-8") as f:
        if name.endswith("Tests.swift") and not nodecl:
            f.write("import Testing\nstruct %s {}\n" % name[: -len(".swift")])
        elif name.endswith(".go"):
            f.write("package x\n")
        else:
            f.write("x\n")


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
