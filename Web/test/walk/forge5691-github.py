#!/usr/bin/env python3
"""A fixture GitHub for Work ▸ Next (#5691): forge4391-github.py's example/shop, with the work that
forge/next (#5689) puts in order. Open issues with priority:high, priority:medium and priority:low,
two milestones with due dates (Checkout, due in 3 days; Search, due in 10), an issue with no
milestone, one with the on hold label (#16), one with an assignee (#14, taken, so not listed), and
the epic #10 with its children (#13 and #17 wait for #12). Scratch data only. Run it as forge4391:

    python3 Web/test/walk/forge5691-github.py <port>
"""
import datetime
import importlib.util
import os
import sys
from http.server import ThreadingHTTPServer

spec = importlib.util.spec_from_file_location("forge4391", os.path.join(os.path.dirname(os.path.abspath(__file__)), "forge4391-github.py"))
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)

base.LABELS.update({"priority:medium": "d93f0b", "priority:low": "c2e0c6", "on hold": "ededed"})
DUE = {"Checkout": 3, "Search": 10}
PLAN = {
    12: (["story", "feature:checkout", "priority:high"], "Checkout"),
    13: (["story", "feature:checkout"], "Checkout"),
    15: (["spike", "feature:search", "priority:medium"], "Search"),
    16: (["docs", "on hold"], None),
    17: (["task", "feature:checkout", "priority:low"], "Checkout"),
    10: (["epic", "feature:checkout", "priority:high"], "Checkout"),
}
for n, (labels, _) in PLAN.items():
    base.ITEMS[n]["labels"] = labels
    if n != 14:
        base.ITEMS[n].pop("assignees", None)
base.ITEMS[30] = {"title": "Search: suggest a spelling", "state": "OPEN", "labels": ["story", "feature:search"], "author": "robin", "at": 9 * 1440}
base.ITEMS[31] = {"title": "Epic: Faster search", "state": "OPEN", "labels": ["epic", "feature:search", "priority:medium"], "author": "kai", "at": 5 * 1440}
base.ITEMS[30]["parent"] = 31
PLAN[30] = (base.ITEMS[30]["labels"], "Search")
PLAN[31] = (base.ITEMS[31]["labels"], "Search")

node = base.node


def planned(n):
    got = node(n)
    if not base.ITEMS[n].get("pr"):
        title = PLAN.get(n, (None, None))[1]
        due = (base.START + datetime.timedelta(days=DUE[title])).strftime("%Y-%m-%dT%H:%M:%SZ") if title else None
        got["milestone"] = {"title": title, "dueOn": due} if title else None
    return got


base.node = planned

if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), base.Handler).serve_forever()
