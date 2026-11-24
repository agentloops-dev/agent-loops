#!/usr/bin/env python3
"""A fixture GitHub at realistic volumes for the review of Work's cache (#6355, epic #6348). Scratch data only.

Two sizes, the volumes of #6348 ("Storage and access"):
    repo  example/shop, shaped like agentloops-dev/agent-loops: 6,300 issues, 300 open (11 priority:high,
          50 priority:medium, 3 priority:low, 33 epic, 34 assigned to sam), and 600 pull requests, 20 open.
    big   example/shop with 50,000 issues, 5,000 open (186 high, 834 medium, 48 low, 555 epic, 556 assigned to
          sam), and 5,000 pull requests, 200 open.

It answers what agentsd asks on main (#6538):
- `search(query: $q…)`, also as aliases (`b0: search(…)`, `total: search(…)`) in one request: labels match all,
  -label, assignee, is:open, is:issue / is:pr, at most 1,000 results, as GitHub's search;
- `repository { b0: issues(…) total: issues(first: 0 …) }` (and pullRequests), aliased: labels any of, states,
  filterBy assignee, orderBy UPDATED_AT, totalCount;
- `issueOrPullRequest(number:)`: an item's page, with a body and no comments; `issue(number:) { id }`: a write's look-up;
- REST writes: POST/DELETE labels, PATCH state, POST/DELETE assignees (each makes the item the newest).
Each GraphQL request waits DELAY seconds (1.5 by default, about GitHub's for 100 items; #6358) and each REST
request 0.4 s. GitHub's GraphQL cost is noted for each request: 1 point per 100 nodes asked, at least 1.

    GET /truth?kind=issue|pr&label=L&assignee=A   the open numbers a list should hold, newest first
    GET /label?n=N&add=L | &remove=L              change one item's labels on the forge's side; it becomes the newest
    GET /calls                                    the requests so far, by kind, and the points they cost
    GET /log?since=T                              each request since T (Unix seconds)
    GET /down?on=1|0                              GitHub does not answer (HTTP 502) while on
    GET /refuse?on=1|0                            every write is refused (HTTP 403) while on

    python3 -I Web/test/walk/work6355-github.py [port] [repo|big] [seconds a request]
With no port, the system picks a free one (#6660), so two lanes never share a fixture. The fixture writes
{"port", "pid", "url"} to build/walk/work6355-github.json in its worktree, and removes it when it stops. It
does not start on a port that is in use, or when that file names a fixture that still runs. A walk script and a
run-app host read the port from the file:
    python3 -I Web/test/walk/work6355-github.py & FIXTURE=$!
    F=build/walk/work6355-github.json; until [ -s $F ]; do sleep 0.2; done
    eval "$($S/launch.sh --slug work --env AGENTS_TEST_GITHUB_URL=$(jq -r .url $F))"
"""
import atexit
import errno
import json
import os
import re
import signal
import sys
import threading
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlparse

REPO = "example/shop"
ARGS = sys.argv[1:]
PORT = int(ARGS.pop(0)) if ARGS and ARGS[0].isdigit() else 0
SIZE = ARGS[0] if ARGS else "repo"
DELAY = float(ARGS[1]) if len(ARGS) > 1 else 1.5
PORT_FILE = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", "build", "walk",
                         "work6355-github.json"))
REST_DELAY = 0.4
SIZES = {"repo": (6300, 300, 600, 20), "big": (50000, 5000, 5000, 200)}
ISSUES, OPEN_ISSUES, PRS, OPEN_PRS = SIZES[SIZE]
START = datetime(2024, 1, 1, tzinfo=timezone.utc)
LOCK = threading.Lock()
CALLS = {"graphql": 0, "connections": 0, "search": 0, "list": 0, "item": 0, "rest": 0, "points": 0}
LOG = []
STATE = {"down": False, "refuse": False, "clock": 0}

ITEMS = {}
step = max(1, ISSUES // OPEN_ISSUES)
open_ns = set(ISSUES - step * k for k in range(OPEN_ISSUES))
for n in range(1, ISSUES + 1):
    ITEMS[n] = {"kind": "issue", "open": n in open_ns, "labels": [["bug"], ["enhancement"], ["task"], ["story"]][n % 4],
                "assignees": [], "updated": START + timedelta(minutes=7 * n)}
for k, n in enumerate(sorted(open_ns, reverse=True)):
    it = ITEMS[n]
    labels = list(it["labels"])
    if k % 27 == 3:
        labels.append("priority:high")
    elif k % 6 == 1:
        labels.append("priority:medium")
    elif k % 100 == 50:
        labels.append("priority:low")
    if k % 9 == 5:
        labels = ["epic"] + [l for l in labels if l.startswith("priority:")]
    it["labels"] = labels
    if k % 9 == 2:
        it["assignees"] = ["sam"]
for n in range(ISSUES + 1, ISSUES + PRS + 1):
    ITEMS[n] = {"kind": "pr", "open": n > ISSUES + PRS - OPEN_PRS, "labels": [], "assignees": [],
                "updated": START + timedelta(minutes=7 * (n - ISSUES) * 10)}
NEWEST = max(i["updated"] for i in ITEMS.values())


def touch(it):
    global NEWEST
    NEWEST = NEWEST + timedelta(minutes=1)
    it["updated"] = NEWEST


def stamp(d):
    return d.strftime("%Y-%m-%dT%H:%M:%SZ")


def node(n, body=False):
    it = ITEMS[n]
    base = {"number": n, "title": f"Scratch {it['kind']} {n}: a realistic title of some length for a row",
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
    if body:
        base.update({"body": f"Scratch body of {n}.\n\n" + "A line of text. " * 40,
                     "comments": {"pageInfo": {"hasPreviousPage": False, "startCursor": None}, "nodes": []}})
    return base


def select(kind, state=None, labels=(), any_labels=(), not_labels=(), assignee=None):
    """The numbers of a list, newest first."""
    out = []
    for n, it in ITEMS.items():
        if it["kind"] != kind or (state is not None and it["open"] != state):
            continue
        if any(l not in it["labels"] for l in labels) or any(l in it["labels"] for l in not_labels):
            continue
        if any_labels and not any(l in it["labels"] for l in any_labels):
            continue
        if assignee and assignee not in it["assignees"]:
            continue
        out.append(n)
    return sorted(out, key=lambda n: ITEMS[n]["updated"], reverse=True)


def connection(ns, first, after, total=None):
    at = int(after or 0)
    page = ns[at:at + first]
    end = at + len(page)
    count = len(ns) if total is None else total
    return {"totalCount": count, "issueCount": count,
            "pageInfo": {"hasNextPage": end < len(ns), "endCursor": str(end) if page else None},
            "edges": [{"cursor": str(at + i + 1), "node": node(n)} for i, n in enumerate(page)]}


def parse_search(words):
    kind = "pr" if "is:pr" in words else "issue"
    state = True if "is:open" in words else False if "is:closed" in words else None
    labels = re.findall(r'(?<![-\w])label:"?([^"\s]+)"?', words)
    not_labels = re.findall(r'-label:"?([^"\s]+)"?', words)
    m = re.search(r"assignee:(\S+)", words)
    return dict(kind=kind, state=state, labels=labels, not_labels=not_labels, assignee=m.group(1) if m else None)


def value(arg, v):
    """A GraphQL argument's value: a $variable from v, a number or a quoted string."""
    arg = arg.strip()
    if arg.startswith("$"):
        return v.get(arg[1:])
    if arg.startswith('"'):
        return arg.strip('"')
    try:
        return int(arg)
    except ValueError:
        return arg


def args_of(text):
    """The top-level `name: value` pairs of an argument list (values with brackets or braces kept whole)."""
    out, depth, cur = {}, 0, ""
    for ch in text + ",":
        if ch in "[{":
            depth += 1
        elif ch in "]}":
            depth -= 1
        if ch == "," and depth == 0:
            if ":" in cur:
                k, _, val = cur.partition(":")
                out[k.strip()] = val.strip()
            cur = ""
        else:
            cur += ch
    return out


SEARCH = re.compile(r"(?:(\w+):\s*)?search\(([^)]*)\)")
LIST = re.compile(r"(\w+):\s*(issues|pullRequests)\(([^)]*)\)")


def graphql(q, v):
    """The data of one request, and how many connections and nodes it asked."""
    data, conns, nodes = {}, 0, 0
    if "issueOrPullRequest(" in q:
        n = int(v.get("number") or 0)
        CALLS["item"] += 1
        if n in ITEMS:
            data["repository"] = {"issueOrPullRequest": node(n, body=True), "viewerDefaultMergeMethod": "SQUASH",
                                  "squashMergeAllowed": True, "mergeCommitAllowed": False, "rebaseMergeAllowed": False}
        else:
            data["repository"] = {"issueOrPullRequest": None}
        return data, 1, 1
    if re.search(r"\bissue\(number: \$number\)", q):
        # A priority write's look-up (SetPriorityField): the issue's id, and no issue fields, so labels are written.
        n = int(v.get("number") or 0)
        issue = {"id": f"I_{n}"} if ITEMS.get(n, {}).get("kind") == "issue" else None
        return {"repository": {"issue": issue, "issueFields": {"nodes": []}}}, 1, 1
    for m in SEARCH.finditer(q):
        alias, a = m.group(1) or "search", args_of(m.group(2))
        words = value(a.get("query", '""'), v) or ""
        first = int(value(a.get("first", "50"), v) or 0)
        after = value(a["after"], v) if "after" in a else None
        all_ns = select(**parse_search(words))
        ns = all_ns[:1000]  # GitHub's search: at most 1,000 results.
        data[alias] = connection(ns, first, after, total=len(all_ns))
        conns += 1
        nodes += first
        CALLS["search"] += 1
    in_repo = {}
    for m in LIST.finditer(q):
        alias, kind, a = m.group(1), "pr" if m.group(2) == "pullRequests" else "issue", args_of(m.group(3))
        first = int(value(a.get("first", "50"), v) or 0)
        after = value(a["after"], v) if "after" in a else None
        states = a.get("states", "")
        state = True if "OPEN" in states else None if not states else False
        any_labels = re.findall(r'"([^"]+)"', a.get("labels", ""))
        assignee = v.get("assignee") if "filterBy" in a and "assignee" in a["filterBy"] else None
        ns = select(kind, state, (), any_labels, (), assignee)
        in_repo[alias] = connection(ns, first, after)
        conns += 1
        nodes += first
        CALLS["list"] += 1
    if in_repo:
        data["repository"] = in_repo
    return data, conns, nodes


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

    def body(self):
        return json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")

    def write(self, method):
        """A REST write to an issue: labels, state, assignees."""
        b = self.body()
        m = re.match(rf"/api/repos/{REPO}/issues/(\d+)(/labels(?:/(.+))?|/assignees)?$", urlparse(self.path).path)
        if not m or int(m.group(1)) not in ITEMS:
            return self.answer(404, {"message": "Not Found"})
        with LOCK:
            CALLS["rest"] += 1
            LOG.append([round(time.time(), 3), "rest", method, self.path, None, None, 0])
            refuse = STATE["refuse"]
        time.sleep(REST_DELAY)
        if refuse:
            return self.answer(403, {"message": "Resource not accessible by integration"})
        n = int(m.group(1))
        with LOCK:
            it = ITEMS[n]
            part = m.group(2) or ""
            if part.startswith("/labels"):
                if method == "POST":
                    it["labels"] += [l for l in b.get("labels", []) if l not in it["labels"]]
                elif method == "DELETE" and m.group(3):
                    name = unquote(m.group(3))
                    if name not in it["labels"]:
                        return self.answer(404, {"message": "Label does not exist"})
                    it["labels"].remove(name)
            elif part == "/assignees":
                for login in b.get("assignees", []):
                    if method == "POST" and login not in it["assignees"]:
                        it["assignees"].append(login)
                    if method == "DELETE" and login in it["assignees"]:
                        it["assignees"].remove(login)
            elif method == "PATCH" and "state" in b:
                it["open"] = b["state"] == "open"
            touch(it)
            return self.answer(200, {"number": n, "labels": [{"name": l} for l in it["labels"]]})

    def do_POST(self):
        path = urlparse(self.path).path
        if path != "/api/graphql":
            return self.write("POST")
        b = self.body()
        if STATE["down"]:
            time.sleep(DELAY)
            return self.answer(502, {"message": "Bad Gateway"})
        q, v = b.get("query", ""), b.get("variables", {}) or {}
        with LOCK:
            data, conns, nodes = graphql(q, v)
            points = max(1, nodes // 100)
            CALLS["graphql"] += 1
            CALLS["connections"] += conns
            CALLS["points"] += points
            what = sorted(k for k in v if k.startswith("q"))
            head = re.sub(r"\s+", " ", q)[:80]
            LOG.append([round(time.time(), 3), "graphql", conns, [v[k] for k in what] or v.get("number") or head,
                        v.get("assignee"), {k: v[k] for k in v if k.startswith("a") and k != "assignee"} or None, points])
        time.sleep(DELAY)
        return self.answer(200, {"data": data})

    def do_PATCH(self):
        return self.write("PATCH")

    def do_DELETE(self):
        return self.write("DELETE")

    def do_GET(self):
        url = urlparse(self.path)
        qs = {k: v[0] for k, v in parse_qs(url.query).items()}
        # The token check of connections/sign-in/token (AGENTS_TEST_GITHUB_URL): any token is sam's (#4092).
        if url.path == "/api/user":
            return self.answer(200, {"login": "sam"})
        if url.path.startswith(f"/api/repos/{REPO}/labels"):
            return self.answer(200, [{"name": l, "color": "0e8a16"} for l in
                                     ["bug", "enhancement", "task", "story", "epic", "priority:high", "priority:medium",
                                      "priority:low"]])
        if url.path == "/truth":
            with LOCK:
                ns = select(qs.get("kind", "issue"), True, [qs["label"]] if qs.get("label") else [], (), (),
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
                touch(it)
                return self.answer(200, {"number": n, "labels": it["labels"]})
        if url.path in ("/down", "/refuse"):
            STATE[url.path[1:]] = qs.get("on") == "1"
            return self.answer(200, STATE)
        if url.path == "/log":
            with LOCK:
                return self.answer(200, [e for e in LOG if e[0] >= float(qs.get("since", 0))])
        if url.path == "/calls":
            with LOCK:
                return self.answer(200, CALLS)
        with LOCK:
            CALLS["rest"] += 1
            LOG.append([round(time.time(), 3), "rest", "GET", self.path, None, None, 0])
        time.sleep(REST_DELAY)
        self.answer(404, {"message": "Not Found"})


def running(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def serve():
    try:
        with open(PORT_FILE) as f:
            old = json.load(f)
        if old.get("pid") != os.getpid() and running(int(old["pid"])):
            sys.exit(f"work6355-github: a fixture already runs in this worktree (pid {old['pid']}, port "
                     f"{old['port']}, {PORT_FILE}). Stop it first: kill {old['pid']}")
    except (OSError, ValueError, KeyError, TypeError):
        pass
    try:
        server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    except OSError as e:
        if e.errno == errno.EADDRINUSE:
            sys.exit(f"work6355-github: port {PORT} is in use, maybe by another lane's fixture. "
                     "Give no port, and the system picks a free one.")
        raise
    port = server.server_address[1]
    os.makedirs(os.path.dirname(PORT_FILE), exist_ok=True)
    tmp = PORT_FILE + ".tmp"
    with open(tmp, "w") as f:
        json.dump({"port": port, "pid": os.getpid(), "url": f"http://127.0.0.1:{port}"}, f)
    os.replace(tmp, PORT_FILE)

    def remove():
        try:
            with open(PORT_FILE) as f:
                if json.load(f).get("pid") == os.getpid():
                    os.remove(PORT_FILE)
        except (OSError, ValueError):
            pass

    atexit.register(remove)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    print(f"work6355-github: {SIZE} on http://127.0.0.1:{port} (pid {os.getpid()}, {PORT_FILE})", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    serve()
