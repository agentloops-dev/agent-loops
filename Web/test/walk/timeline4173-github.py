#!/usr/bin/env python3
"""A fixture GitHub for Work ▸ Timeline (#4173): the repo example/shop with epics whose children
started and closed around the time this starts (so Now is always in the middle of them), past
epics for the forecast to learn from, and three releases, as work/timeline reads them (one GraphQL
query: issues labelled epic, their subIssues with their dates, and the releases). Scratch data only.
Point a run-app host at it with --env AGENTS_TEST_GITHUB_URL=http://127.0.0.1:<port>, and give the
host a folder whose origin is github.com/example/shop.

    python3 Web/test/walk/timeline4173-github.py <port> [<seconds>]

The seconds delay the oldest epics, which work/timeline reads last, so the plot without its forecast can be seen (#7027).
"""
import json
import sys
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

REPO = "example/shop"
LABELS = {"epic": "3e4b9e", "story": "0e8a16", "task": "c5def5", "alex-todo": "d93f0b",
          "feature:checkout": "1d76db", "feature:accounts": "d93f0b", "feature:search": "0e8a16",
          "priority:high": "b60205", "priority:medium": "fbca04", "priority:low": "c2e0c6", "on hold": "ededed"}
START = datetime.now(timezone.utc).replace(second=0, microsecond=0)


def at(hours):
    return None if hours is None else (START + timedelta(hours=hours)).strftime("%Y-%m-%dT%H:%M:%SZ")


# Each epic: its title, feature, when it was filed, and its children as
# (title, filed, PR opened, closed, labels); hours from now, None for not yet.
EPICS = {
    100: ("Epic: Gift cards at checkout", "checkout", -26, [
        ("Gift card balances in the store's API", -26, -20, -18, []),
        ("A gift card field on the payment form", -26, -14, -12, []),
        ("Pay part with a gift card", -25, -9, -8, []),
        ("Refund to the gift card", -25, -5, -4, []),
        ("Show the card's balance after paying", -24, -2, -1, []),
        ("Gift cards in the order email", -24, -0.5, None, []),
        ("Gift cards on the receipt", -24, None, None, []),
    ]),
    110: ("Epic: Sign in with a passkey", "accounts", -22, [
        ("Passkeys in the store's API", -22, -17, -15, []),
        ("A passkey button on the sign-in page", -21, -11, -10, []),
        ("Register the passkey domain with Apple", -20, None, None, ["alex-todo"]),
    ]),
    120: ("Epic: One-page checkout", "checkout", -40, [
        ("One page for address and payment", -40, -30, -28, []),
        ("Keep the cart beside the form", -40, -26, -22, []),
        ("Remove the old checkout steps", -39, -9, -6, []),
    ]),
    130: ("Epic: Saved carts", "checkout", -6, [
        ("Keep a cart for 30 days", -6, -3, -2.5, []),
        ("Email a saved cart", -6, -1, None, []),
        ("A Saved carts page", -6, None, None, []),
    ]),
    140: ("Epic: Search by colour", "search", -50, [
        ("A colour index for products", -50, -36, -33, []),
        ("A colour filter in search", -49, -30, -19, []),
        ("Colours in the search API", -48, -16, None, []),
        ("Colour names in other languages", -48, None, None, []),
    ]),
}

# Past epics, closed in the last 30 days, for the forecast's history and backtest.
for i in range(10):
    end = -60 - 40 * i
    EPICS[200 + i] = (f"Epic: Past work {i + 1}", ["checkout", "accounts", "search"][i % 3], end - 30, [
        (f"Past step {i + 1}.{k + 1}", end - 30, end - 26 + 4 * k, end - 24 + 4 * k + (k % 2) * 2, []) for k in range(6)
    ])
    EPICS[200 + i][3][-1] = (EPICS[200 + i][3][-1][0], end - 30, end - 2, end, [])

# The open epics' priorities, for the bands of #6089 (High at the top, on hold shown only with the On hold toggle,
# #7058, #7335): #150 is on hold with work, #190 on hold with no work yet.
EPIC_LABELS = {100: ["priority:high"], 110: ["priority:medium"], 130: ["priority:high"], 140: ["priority:low"],
               150: ["on hold"], 190: ["on hold", "priority:medium"]}
EPICS[150] = ("Epic: Loyalty points", "accounts", -30, [
    ("Points in the store's API", -30, -20, -16, []),
    ("Show points at checkout", -29, -12, None, []),
])

# For #7058: an epic with a pause in its work, closed after its children; one that no child of
# has started; one closed as not planned.
EPICS[160] = ("Epic: Order tracking", "search", -40, [
    ("Carrier events in the store's API", -34, -32, -29, []),
    ("A tracking page", -12, -11, -9, []),
])
EPICS[170] = ("Epic: Wish lists", "accounts", -15, [
    ("Wish lists in the store's API", -14, None, None, []),
    ("A Wish list page", -14, None, None, []),
])
EPICS[180] = ("Epic: Pay by fax", "checkout", -20, [
    ("A fax number on the payment form", -19, -16, -13, []),
])
EPICS[190] = ("Epic: Gift wrapping", "checkout", -10, [
    ("Wrapping options in the store's API", -10, None, None, []),
    ("A gift wrap box on the cart", -10, None, None, []),
])
EPIC_CLOSED = {160: -4, 180: -10}
NOT_PLANNED = {180}

RELEASES = [("v0.41.0", -2), ("v0.40.0", -21), ("v0.39.0", -45), ("v0.38.0", -70)]


def issue(n, title, labels, filed, closed, parent=None):
    return {"__typename": "Issue", "number": n, "title": title, "issueState": "CLOSED" if closed is not None else "OPEN",
            "url": f"https://github.com/{REPO}/issues/{n}", "updatedAt": at(closed if closed is not None else 0),
            "author": {"login": "sam", "__typename": "User"}, "repository": {"nameWithOwner": REPO},
            "labels": {"nodes": [{"name": l, "color": LABELS[l]} for l in labels]}, "assignees": {"nodes": []},
            "milestone": None, "commentCount": {"totalCount": 0},
            "parent": {"number": parent, "repository": {"nameWithOwner": REPO}} if parent else None,
            "createdAt": at(filed), "closedAt": at(closed), "stateReason": "COMPLETED" if closed is not None else None}


def epic(n):
    title, feature, filed, kids = EPICS[n]
    closes = [k[3] for k in kids]
    closed = EPIC_CLOSED[n] if n in EPIC_CLOSED else max(closes) if all(c is not None for c in closes) else None
    node = issue(n, title, ["epic", "feature:" + feature] + EPIC_LABELS.get(n, []), filed, closed)
    if n in NOT_PLANNED:
        node["stateReason"] = "NOT_PLANNED"
    subs = []
    for j, (t, f, pr, c, labels) in enumerate(kids):
        sub = issue(n * 100 + j + 1, t, ["story", "feature:" + feature] + labels, f, c, parent=n)
        sub["closedByPullRequestsReferences"] = {"nodes": [{"createdAt": at(pr)}] if pr is not None else []}
        subs.append(sub)
    node["subIssues"] = {"nodes": subs}
    node["subIssuesSummary"] = {"total": len(kids), "completed": sum(c is not None for c in closes)}
    return node, closed


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
        # The epics' ids and the releases first (timelineListQuery), then the epics by id with their
        # sub-issues (timelineEpicsQuery), as work/timeline reads them since #5154.
        if "releases(" in q:
            states = v.get("states") or ["OPEN", "CLOSED"]
            nodes = [{"id": f"I_{n}", "number": n, "closedAt": epic(n)[0]["closedAt"], "updatedAt": epic(n)[0]["updatedAt"],
                      "labels": {"nodes": [{"name": l["name"]} for l in epic(n)[0]["labels"]["nodes"]]}}
                     for n in EPICS if epic(n)[0]["issueState"] in states]
            releases = [{"tagName": t, "publishedAt": at(h), "url": f"https://github.com/{REPO}/releases/tag/{t}",
                         "isDraft": False} for t, h in RELEASES][: v.get("releases", 50)]
            return self.answer(200, {"data": {"repository": {
                "issues": {"pageInfo": {"hasNextPage": False, "endCursor": None}, "nodes": nodes},
                "releases": {"nodes": releases}}}})
        if "nodes(ids:" in q and "subIssues(" in q:
            if "I_209" in v.get("ids", []) and len(sys.argv) > 2:
                time.sleep(float(sys.argv[2]))
            return self.answer(200, {"data": {"nodes": [epic(int(i[2:]))[0] for i in v.get("ids", [])
                                                        if i.startswith("I_") and int(i[2:]) in EPICS]}})
        if "search(" in q:
            return self.answer(200, {"data": {"search": {"pageInfo": {"hasNextPage": False, "endCursor": None}, "edges": []}}})
        self.answer(200, {"data": {}})

    def do_GET(self):
        # The token check of connections/sign-in/token (AGENTS_TEST_GITHUB_URL): any token is sam's.
        if self.path.split("?")[0] == "/api/user":
            return self.answer(200, {"login": "sam"})
        if self.path.split("?")[0] == f"/api/repos/{REPO}/labels":
            return self.answer(200, [{"name": k, "color": c} for k, c in LABELS.items()])
        self.answer(404, {"message": "Not Found"})


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
