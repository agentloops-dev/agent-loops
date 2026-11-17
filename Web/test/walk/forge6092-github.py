#!/usr/bin/env python3
"""A fixture GitHub for the item page's Priority menu and On hold toggle (#6092): forge5691-github.py's
example/shop (priority:high on #12, priority:medium on #15, on hold on #16), with no Priority issue
field (a GitHub with none, so forge/priority and forge/hold write the labels), and the label writes
(add, remove) kept in memory so the next read shows them. Scratch data only. Run it as forge4391:

    python3 Web/test/walk/forge6092-github.py <port>
"""
import importlib.util
import io
import json
import os
import re
import sys
from http.server import ThreadingHTTPServer
from urllib.parse import unquote

spec = importlib.util.spec_from_file_location("forge5691", os.path.join(os.path.dirname(os.path.abspath(__file__)), "forge5691-github.py"))
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)
base = plan.base
base.LABELS.setdefault("priority:high", "b60205")
LABELS = re.compile(rf"/api/repos/{re.escape(base.REPO)}/issues/(\d+)/labels(?:/(.+))?")


class Handler(base.Handler):
    def do_POST(self):
        path = self.path.split("?")[0]
        if m := LABELS.fullmatch(path):
            b = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
            i = base.ITEMS[int(m[1])]
            i["labels"] += [l for l in b.get("labels", []) if l not in i["labels"]]
            return self.answer(200, [{"name": l, "color": base.LABELS.get(l, "ededed")} for l in i["labels"]])
        if path == "/api/graphql":
            n = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(n)
            q = (json.loads(raw or b"{}")).get("query", "")
            if "issueFields(" in q:
                # A GitHub with no issue fields: forge/priority writes the labels.
                return self.answer(200, {"errors": [{"message": "Field 'issueFields' doesn't exist on type 'Repository'"}]})
            self.rfile = io.BytesIO(raw)
        return super().do_POST()

    def do_DELETE(self):
        if m := LABELS.fullmatch(self.path.split("?")[0]):
            i = base.ITEMS[int(m[1])]
            i["labels"] = [l for l in i["labels"] if l != unquote(m[2] or "")]
            return self.answer(200, [])
        self.answer(404, {"message": "Not Found"})


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
