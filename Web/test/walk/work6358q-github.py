#!/usr/bin/env python3
"""A fixture GitHub shaped like agentloops-dev/agent-loops on 2026-11-20 (#6358, PR #6386), for timing Work's query
cache: the repo example/shop with 6,300 issues, 279 of them open (10 priority:high, 45 priority:medium, 3 priority:low,
32 epic, 32 assigned to sam), and 600 pull requests, 2 of them open. Scratch data only.

It answers the GraphQL the PoC sends: `search` (labels match all, -label, assignee, is:open, at most 1,000 results)
and `repository.issues` / `pullRequests` (labels, states, filterBy assignee, totalCount), newest updatedAt first, pages
of `first` after a cursor, with a delay a request (1.5 s by default, about GitHub's for 100 items).

    GET /truth?kind=issue|pr&label=L&assignee=A   the open numbers a list should hold, newest first (as `gh issue list`)
    GET /label?n=N&add=L | &remove=L              change one item's labels; it becomes the newest
    GET /calls                                    the GraphQL requests so far, by kind
    GET /log?since=T                              each GraphQL request since T (Unix seconds)

    python3 -I Web/test/walk/work6358q-github.py <port> [seconds a request]
Point a run-app host at it with --env AGENTS_TEST_GITHUB_URL=http://127.0.0.1:<port>.
"""
import json
import re
import sys
import threading
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

REPO = "example/shop"
DELAY = float(sys.argv[2]) if len(sys.argv) > 2 else 1.5
START = datetime(2026, 1, 1, tzinfo=timezone.utc)
ISSUES, OPEN_ISSUES, PRS, OPEN_PRS = 6300, 279, 600, 2
LOCK = threading.Lock()
CALLS = {"list": 0, "search": 0, "other": 0}
LOG = []  # each GraphQL request: when, what, the words or labels

# Every item: kind, open, labels, assignees, updated. Open issues are spread over the numbers, every 22nd or so.
ITEMS = {}
open_ns = [6300 - 22 * k for k in range(OPEN_ISSUES)]
for n in range(1, ISSUES + 1):
    ITEMS[n] = {"kind": "issue", "open": False, "labels": [["bug"], ["enhancement"], ["task"], ["story"]][n % 4],
                "assignees": [], "updated": START + timedelta(minutes=70 * n)}
for k, n in enumerate(open_ns):
    it = ITEMS[n]
    it["open"] = True
    labels = list(it["labels"])
    if k % 27 == 3 and k < 270:
        labels.append("priority:high")      # 10
    elif k % 6 == 1 and k < 270:
        labels.append("priority:medium")    # 45
    elif k in (100, 200, 250):
        labels.append("priority:low")       # 3
    if k % 9 == 5 and k < 288:
        labels = ["epic"] + [l for l in labels if l.startswith("priority:")]  # 31
    it["labels"] = labels
    if k % 9 == 2 and k < 288:
        it["assignees"] = ["sam"]          # 32
ITEMS[open_ns[0]]["labels"].append("epic")  # 32 epics
for n in range(ISSUES + 1, ISSUES + PRS + 1):
    ITEMS[n] = {"kind": "pr", "open": n > ISSUES + PRS - OPEN_PRS, "labels": [], "assignees": [],
                "updated": START + timedelta(minutes=70 * (n - ISSUES) * 10)}


def stamp(d):
    return d.strftime("%Y-%m-%dT%H:%M:%SZ")


def node(n):
    it = ITEMS[n]
    base = {"number": n, "title": f"Scratch {it['kind']} {n}: a realistic title of some length",
            "url": f"https://github.com/{REPO}/{'issues' if it['kind'] == 'issue' else 'pull'}/{n}",
            "updatedAt": stamp(it["updated"]), "createdAt": stamp(it["updated"] - timedelta(days=3)),
            "closedAt": None if it["open"] else stamp(it["updated"]),
            "author": {"login": "sam", "__typename": "User"}, "repository": {"nameWithOwner": REPO},
            "labels": {"nodes": [{"name": l, "color": "0e8a16"} for l in it["labels"]]},
            "assignees": {"nodes": [{"login": a} for a in it["assignees"]]},
            "milestone": None, "commentCount": {"totalCount": n % 7}}
    if it["kind"] == "issue":
        base.update({"__typename": "Issue", "issueState": "OPEN" if it["open"] else "CLOSED", "parent": None,
                     "subIssuesSummary": {"total": 0, "completed": 0}, "stateReason": None if it["open"] else "COMPLETED"})
    else:
        base.update({"__typename": "PullRequest", "pullState": "OPEN" if it["open"] else "MERGED", "isDraft": False,
                     "mergeable": "MERGEABLE", "reviewDecision": None, "autoMergeRequest": None,
                     "headRefName": f"b{n}", "baseRefName": "main", "mergedBy": None, "mergeCommit": None,
                     "checkState": {"nodes": []}})
    return base


def select(kind, state=None, labels=(), not_labels=(), assignee=None):
    """The numbers of a list, newest first."""
    out = []
    for n, it in ITEMS.items():
        if it["kind"] != kind or (state is not None and it["open"] != state):
            continue
        if any(l not in it["labels"] for l in labels) or any(l in it["labels"] for l in not_labels):
            continue
        if assignee and assignee not in it["assignees"]:
            continue
        out.append(n)
    return sorted(out, key=lambda n: ITEMS[n]["updated"], reverse=True)


def connection(ns, first, after):
    at = int(after or 0)
    page = ns[at:at + first]
    end = at + len(page)
    return {"totalCount": len(ns), "issueCount": len(ns),
            "pageInfo": {"hasNextPage": end < len(ns), "endCursor": str(end) if page else None},
            "edges": [{"cursor": str(at + i + 1), "node": node(n)} for i, n in enumerate(page)]}


def parse_search(words):
    kind = "pr" if "is:pr" in words else "issue"
    state = True if "is:open" in words else False if "is:closed" in words else None
    labels = re.findall(r'(?<![-\w])label:"?([^"\s]+)"?', words)
    not_labels = re.findall(r'-label:"?([^"\s]+)"?', words)
    m = re.search(r"assignee:(\S+)", words)
    return dict(kind=kind, state=state, labels=labels, not_labels=not_labels, assignee=m.group(1) if m else None)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def answer(self, code, body):
        b = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_POST(self):
        b = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        if self.path.split("?")[0] != "/api/graphql":
            return self.answer(404, {"message": "Not Found"})
        q, v = b.get("query", ""), b.get("variables", {}) or {}
        first = int(v.get("first") or 50)
        if "repository(owner:" in q and re.search(r"page: (issues|pullRequests)\(", q):
            kind = "pr" if "page: pullRequests(" in q else "issue"
            args = re.search(r"page: \w+\(([^)]*)\)", q).group(1)
            state = True if "states: [OPEN]" in args else None if "states:" not in args else False
            labels = re.findall(r'labels: \["([^"]+)"\]', args)
            assignee = v.get("assignee") if "filterBy: {assignee" in args else None
            with LOCK:
                CALLS["list"] += 1
                LOG.append([round(time.time(), 3), kind, "list", labels, assignee, v.get("after")])
                ns = select(kind, state, labels, (), assignee)
                data = {"page": connection(ns, first, v.get("after"))}
                if "total: " in q:
                    data["total"] = {"totalCount": len(select(kind, state, (), (), assignee))}
            time.sleep(DELAY)
            return self.answer(200, {"data": {"repository": data}})
        if "search(" in q:
            with LOCK:
                CALLS["search"] += 1
                LOG.append([round(time.time(), 3), "", "search", v.get("q", ""), None, v.get("after")])
                ns = select(**parse_search(v.get("q", "")))[:1000]  # GitHub's search: at most 1,000 results.
                page = connection(ns, first, v.get("after"))
                data = {"page": page, "search": page}
                if "$all" in q:
                    data["total"] = {"issueCount": len(select(**parse_search(v.get("all", ""))))}
            time.sleep(DELAY)
            return self.answer(200, {"data": data})
        with LOCK:
            CALLS["other"] += 1
        self.answer(200, {"data": {}})

    def do_GET(self):
        url = urlparse(self.path)
        qs = {k: v[0] for k, v in parse_qs(url.query).items()}
        # The token check of connections/sign-in/token (AGENTS_TEST_GITHUB_URL): any token is sam's (#4092).
        if url.path == "/api/user":
            return self.answer(200, {"login": "sam"})
        if url.path == f"/api/repos/{REPO}/labels":
            return self.answer(200, [{"name": l, "color": "0e8a16"} for l in
                                     ["bug", "enhancement", "task", "story", "epic", "priority:high", "priority:medium",
                                      "priority:low"]])
        if url.path == "/truth":
            with LOCK:
                ns = select(qs.get("kind", "issue"), True, [qs["label"]] if qs.get("label") else [], (),
                            qs.get("assignee"))
            return self.answer(200, ns)
        if url.path == "/label":
            n = int(qs["n"])
            with LOCK:
                it = ITEMS[n]
                if qs.get("add") and qs["add"] not in it["labels"]:
                    it["labels"].append(qs["add"])
                if qs.get("remove") in it["labels"]:
                    it["labels"].remove(qs["remove"])
                it["updated"] = max(i["updated"] for i in ITEMS.values()) + timedelta(minutes=1)
                return self.answer(200, {"number": n, "labels": it["labels"]})
        if url.path == "/log":
            with LOCK:
                return self.answer(200, [e for e in LOG if e[0] >= float(qs.get("since", 0))])
        if url.path == "/calls":
            with LOCK:
                return self.answer(200, CALLS)
        self.answer(404, {"message": "Not Found"})


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
