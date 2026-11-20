#!/usr/bin/env python3
"""A fixture GitHub with a realistic number of issues (#6358, epic #6348), for timing Work's local cache: the repo
example/shop with N issues (6,000 by default, about this repo's count), a tenth of them open, each updated at its own
time. GraphQL `search` answers as GitHub does: newest updatedAt first (sort:updated-desc), pages of `first` after a
cursor, at most 1,000 results a query, and a delay a page (1.5 s by default, as measured on GitHub for 50 items).
`GET /bump?n=K` makes K issues newer, as people change them. Scratch data only. Point a run-app host at it with
--env AGENTS_TEST_GITHUB_URL=http://127.0.0.1:<port>.

    python3 -I Web/test/walk/work6358-github.py <port> [issues] [seconds a page]
"""
import json
import sys
import threading
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

REPO = "example/shop"
COUNT = int(sys.argv[2]) if len(sys.argv) > 2 else 6000
DELAY = float(sys.argv[3]) if len(sys.argv) > 3 else 1.5
START = datetime(2026, 9, 1, tzinfo=timezone.utc)
LABELS = ["bug", "enhancement", "story", "task", "priority:high", "priority:medium"]
LOCK = threading.Lock()
# Each issue's update time: one every 20 minutes or so from START, so the newest is the highest number.
UPDATED = {n: START + timedelta(minutes=20 * n) for n in range(1, COUNT + 1)}
CALLS = {"search": 0}


def stamp(d):
    return d.strftime("%Y-%m-%dT%H:%M:%SZ")


def node(n):
    open_ = n % 10 == 0
    return {"__typename": "Issue", "number": n, "title": f"Scratch issue {n}: a realistic title of some length",
            "issueState": "OPEN" if open_ else "CLOSED", "url": f"https://github.com/{REPO}/issues/{n}",
            "updatedAt": stamp(UPDATED[n]), "createdAt": stamp(START + timedelta(minutes=19 * n)),
            "closedAt": None if open_ else stamp(UPDATED[n]),
            "author": {"login": "sam", "__typename": "User"}, "repository": {"nameWithOwner": REPO},
            "labels": {"nodes": [{"name": LABELS[n % len(LABELS)], "color": "0e8a16"}]}, "assignees": {"nodes": []},
            "milestone": None, "commentCount": {"totalCount": n % 7}, "parent": None,
            "subIssuesSummary": {"total": 0, "completed": 0}}


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
        q, v = b.get("query", ""), b.get("variables", {})
        if "search(" not in q:
            return self.answer(200, {"data": {}})
        words = v.get("q", "")
        if "is:pr" in words:
            return self.answer(200, {"data": {"search": {"issueCount": 0, "pageInfo": {"hasNextPage": False, "endCursor": None},
                                                         "edges": []}}})
        with LOCK:
            CALLS["search"] += 1
            ns = sorted(UPDATED, key=lambda n: UPDATED[n], reverse=True)
        if "is:open" in words:
            ns = [n for n in ns if n % 10 == 0]
        elif "is:closed" in words:
            ns = [n for n in ns if n % 10 != 0]
        total = len(ns)
        ns = ns[:1000]  # GitHub's search gives at most 1,000 results a query.
        first = int(v.get("first") or 50)
        at = int(v.get("after") or 0)
        page = ns[at:at + first]
        time.sleep(DELAY)
        end = at + len(page)
        self.answer(200, {"data": {"search": {"issueCount": total,
                                              "pageInfo": {"hasNextPage": end < len(ns), "endCursor": str(end) if page else None},
                                              "edges": [{"cursor": str(at + i + 1), "node": node(n)} for i, n in enumerate(page)]}}})

    def do_GET(self):
        url = urlparse(self.path)
        # The token check of connections/sign-in/token (AGENTS_TEST_GITHUB_URL): any token is sam's (#4092).
        if url.path == "/api/user":
            return self.answer(200, {"login": "sam"})
        if url.path == f"/api/repos/{REPO}/labels":
            return self.answer(200, [{"name": l, "color": "0e8a16"} for l in LABELS])
        if url.path == "/bump":
            k = int(parse_qs(url.query).get("n", ["10"])[0])
            with LOCK:
                now = max(UPDATED.values()) + timedelta(minutes=5)
                for n in range(1, k + 1):
                    UPDATED[n * 37 % COUNT + 1] = now
            return self.answer(200, {"bumped": k})
        if url.path == "/calls":
            return self.answer(200, CALLS)
        self.answer(404, {"message": "Not Found"})


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
