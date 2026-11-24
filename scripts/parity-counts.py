#!/usr/bin/env python3
"""Count the parity table's rows by verdict, tier and role, a line per screen (#2020, #2796, #5290).

  scripts/parity-counts.py              the counts; fails on a row that breaks the rules below
  scripts/parity-counts.py --mismatches the rows marked "(tier moves)", by client (Mac, Remote, Web)

Each screen of specs/071-web-remote/walks/parity.md is a file of its own under parity/. A
row's verdict is its last cell: a row with any **delta** counts as delta, else one with
**by design** as by design, else **same**. A row's tier is its second cell: Core,
Mac + web, Web + Mac link (#4937), Mac only, Web only or Remote only (#4759). The script
fails on a row with no tier it knows, and names the tier to use for the old One client.

A row's role is its third cell (#5290, epic #5288): Engineer (agent work), Engineer (set-up),
Lead, Admin or Report reader. The role gives the tier (ROLE_TIERS, the rule of parity.md
"Tiers"). A row whose tier is not one its role gives fails, unless its role cell ends in
"(tier moves)": a known gap that a child of the epic closes by changing a client, and then
the mark goes. A mark on a row that agrees fails too, so the marks stay true.

A row's maturity is its fourth cell (#6052, #6580): `ga`, or a level that is not GA with the
capabilities it is about, as `beta (`chat.branch/1`)` or `alpha (`a/1`, `b/1`)`. The levels
are in Packages/Wire/maturity.json, and a capability it does not list is GA. The script
fails on a row whose level is not the one that file gives each capability it names, and on a
capability in the file that no row names, so the table and the wire agree.

  scripts/parity-counts.py --selftest   the maturity cell's checks, on made-up rows
"""

import glob
import json
import os
import re
import sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
SCREENS = os.path.join(ROOT, "specs", "071-web-remote", "walks", "parity", "*.md")
VERDICTS = ("same", "by design", "delta")
TIERS = ("Core", "Mac + web", "Web + Mac link", "Mac only", "Web only", "Remote only")
ROLES = ("Engineer (agent work)", "Engineer (set-up)", "Lead", "Admin", "Report reader")
MARK = " (tier moves)"
# The tiers a role gives, the first one is the target. The one-client tiers are what only a
# Mac or the web page can do (Mac only), or the web's own frame (Web only), not a role's rule.
ROLE_TIERS = {
    "Engineer (agent work)": ("Core", "Mac only", "Web only", "Remote only"),
    "Engineer (set-up)": ("Mac + web", "Mac only", "Web only"),
    "Lead": ("Web + Mac link",),  # and Core for Velocity and Timeline (LEAD_CORE)
    "Admin": ("Web + Mac link", "Web only"),
    "Report reader": ("Web + Mac link",),
}
LEAD_CORE = ("Velocity", "Timeline")
# What each tier draws: (Mac, Remote, Web), a link on the Mac for Web + Mac link.
DRAWS = {
    "Core": ("native", "native", "native"),
    "Mac + web": ("native", "none", "native"),
    "Web + Mac link": ("link", "none", "native"),
    "Mac only": ("native", "none", "none"),
    "Web only": ("none", "none", "native"),
    "Remote only": ("none", "native", "none"),
}
OLD = {"One client": "Mac only, Web only or Remote only (#4759)"}
MATURITY = os.path.join(ROOT, "Packages", "Wire", "maturity.json")
LEVELS = ("ga", "pilot", "alpha", "beta")
NOT_GA = re.compile(r"^(pilot|alpha|beta) \((`[^`]+`(?:, `[^`]+`)*)\)$")


def verdict(row):
    last = row.rstrip().rstrip("|").split(" | ")[-1]
    for v in ("delta", "by design", "same"):
        if f"**{v}" in last:
            return v
    return None


def tier(row):
    cells = row.split(" | ")
    return cells[1].strip() if len(cells) > 1 else None


def role(row):
    cells = row.split(" | ")
    return cells[2].strip() if len(cells) > 2 else None


def maturity(row):
    cells = row.split(" | ")
    return cells[3].strip() if len(cells) > 3 else None


def levels(path=MATURITY):
    """The capabilities that are not GA, by name: {capability: level}."""
    with open(path, encoding="utf-8") as f:
        return {c["name"]: c["level"] for c in json.load(f)["capabilities"]}


def check_maturity(cell, known):
    """(level, capabilities named, problem or None) for one row's Maturity cell."""
    if cell == "ga":
        return "ga", [], None
    m = NOT_GA.match(cell or "")
    if not m:
        return None, [], (f"maturity {cell!r} is not `ga` or a level with its capabilities, "
                          f"as `beta (`chat.branch/1`)`; levels: {', '.join(LEVELS)}")
    level, caps = m.group(1), re.findall(r"`([^`]+)`", m.group(2))
    for c in caps:
        want = known.get(c)
        if want is None:
            return level, caps, (f"{c} is GA in Packages/Wire/maturity.json (not listed), "
                                 f"not {level}: make the cell `ga`, or list it there")
        if want != level:
            return level, caps, (f"{c} is {want} in Packages/Wire/maturity.json, not {level}")
    return level, caps, None


def selftest():
    known = {"chat.branch/1": "beta", "lineage.forecast/1": "alpha", "lineage.estimate/1": "alpha"}
    cases = [
        ("ga", "ga", None),
        ("beta (`chat.branch/1`)", "beta", None),
        ("alpha (`lineage.forecast/1`, `lineage.estimate/1`)", "alpha", None),
        ("beta", None, "is not `ga`"),
        ("GA", None, "is not `ga`"),
        ("rc (`chat.branch/1`)", None, "is not `ga`"),
        ("alpha (`chat.branch/1`)", "alpha", "is beta in"),
        ("beta (`host.team/1`)", "beta", "is GA in"),
        ("alpha (`lineage.forecast/1`, `chat.branch/1`)", "alpha", "is beta in"),
    ]
    for cell, level, problem in cases:
        got, _, why = check_maturity(cell, known)
        assert got == level, (cell, got)
        assert (why is None) == (problem is None) and (problem is None or problem in why), (cell, why)
    for name, level in levels().items():  # the real list reads, and has only levels a cell takes
        assert level in LEVELS[1:], (name, level)
    print("parity-counts selftest: ok")


def allowed(r, row):
    t = ROLE_TIERS[r]
    if r == "Lead" and any(k in row.split(" | ")[0] for k in LEAD_CORE):
        t = ("Core",) + t
    return t


def moves(row_tier, r, row):
    """The clients whose drawing the role changes: a list of 'Mac', 'Remote', 'Web'."""
    want = DRAWS[allowed(r, row)[0]]
    have = DRAWS[row_tier]
    return [c for c, w, h in zip(("Mac", "Remote", "Web"), want, have) if w != h]


def mismatches():
    """Rows marked as a known gap: [(where, tier, role, clients, feature)]."""
    out = []
    for path in sorted(glob.glob(SCREENS)):
        with open(path) as f:
            for n, line in enumerate(f.read().splitlines(), 1):
                if line.startswith("| ") and verdict(line) and (r := role(line)) and r.endswith(MARK):
                    base = r.removesuffix(MARK)
                    if base in ROLES and tier(line) in TIERS:
                        out.append((f"{os.path.relpath(path, ROOT)}:{n}", tier(line), base,
                                    moves(tier(line), base, line), line.split(" | ")[0][2:90]))
    return out


def main():
    if "--selftest" in sys.argv:
        selftest()
        return
    if "--mismatches" in sys.argv:
        for client in ("Mac", "Remote", "Web"):
            rows = [m for m in mismatches() if client in m[3]]
            print(f"\n## {client}: {len(rows)} rows\n")
            for where, t, r, _, feat in rows:
                print(f"- {where}: {r}, now {t}: {feat}")
        return
    known = levels()
    named = set()
    totals = dict.fromkeys(VERDICTS + TIERS + ROLES + LEVELS, 0)
    bad = []
    print("| Screen | Same | By design | Delta | " + " | ".join(TIERS + ROLES + LEVELS) + " |")
    print("|---" * (4 + len(TIERS) + len(ROLES) + len(LEVELS)) + "|")
    for path in sorted(glob.glob(SCREENS)):
        with open(path) as f:
            lines = f.read().splitlines()
        title = lines[0].removeprefix("# Parity: ")
        counts = dict.fromkeys(VERDICTS + TIERS + ROLES + LEVELS, 0)
        for n, line in enumerate(lines, 1):
            if not line.startswith("| ") or not (v := verdict(line)):
                continue
            counts[v] += 1
            t = tier(line)
            if t in TIERS:
                counts[t] += 1
            else:
                where = f"{os.path.relpath(path, ROOT)}:{n}"
                if t in OLD:
                    bad.append(f"{where}: tier {t!r} is split now: use {OLD[t]}")
                else:
                    bad.append(f"{where}: tier {t!r} is not one of {', '.join(TIERS)}")
                continue
            where = f"{os.path.relpath(path, ROOT)}:{n}"
            level, caps, why = check_maturity(maturity(line), known)
            named.update(caps)
            if level:
                counts[level] += 1
            if why:
                bad.append(f"{where}: {why}")
            r = role(line) or ""
            marked = r.endswith(MARK)
            base = r.removesuffix(MARK)
            if base not in ROLES:
                bad.append(f"{where}: role {r!r} is not one of {', '.join(ROLES)}")
                continue
            counts[base] += 1
            agrees = t in allowed(base, line)
            if not agrees and not marked:
                bad.append(f"{where}: {base} gives {' or '.join(allowed(base, line))}, not {t}: "
                           f"change the role, the tier, or mark the role {base + MARK!r}")
            elif agrees and marked:
                bad.append(f"{where}: marked {MARK.strip()} but {t} is what {base} gives: remove the mark")
        for k in counts:
            totals[k] += counts[k]
        print(f"| {title} | " + " | ".join(str(counts[k]) for k in VERDICTS + TIERS + ROLES + LEVELS) + " |")
    print(f"\nOf {sum(totals[v] for v in VERDICTS)} rows: **{totals['same']} same**, "
          f"**{totals['by design']} by design**, **{totals['delta']} delta**; "
          + ", ".join(f"**{totals[t]} {t}**" for t in TIERS) + ".")
    print("By role: " + ", ".join(f"**{totals[r]} {r}**" for r in ROLES) + f"; {len(mismatches())} rows "
          "marked (tier moves).")
    print("By maturity: " + ", ".join(f"**{totals[m]} {m}**" for m in LEVELS) + ".")
    for c in sorted(set(known) - named):
        bad.append(f"Packages/Wire/maturity.json: {c} is {known[c]}, but no row names it: "
                   f"put `{known[c]} (`{c}`)` in its feature's Maturity cell")
    if bad:
        print("\n" + "\n".join(bad), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
