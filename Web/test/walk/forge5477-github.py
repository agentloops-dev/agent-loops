#!/usr/bin/env python3
"""A fixture GitHub for the Files changed page (#5477): forge3130-github.py's repo example/shop, with a pull request's
files for every card the page draws. #21 has six files (a change with changed words, an added file, a removed one, a
rename, a binary with no patch, a long file with a fold); #22's files fail (the error); #23 has none (empty). Each
read of the files waits FILES_DELAY seconds (1.5 by default), so the loading state can be seen. Scratch data only. Point a run-app host at it with
--env AGENTS_TEST_GITHUB_URL=http://127.0.0.1:<port>.

    python3 -I Web/test/walk/forge5477-github.py <port>
"""
import importlib.util
import os
import pathlib
import re
import sys
import time
from http.server import ThreadingHTTPServer

spec = importlib.util.spec_from_file_location("forge3130", pathlib.Path(__file__).with_name("forge3130-github.py"))
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)

REPO = base.REPO
base.ITEMS[23] = {"pr": True, "title": "Empty merge from main", "state": "OPEN", "labels": [], "author": "kim", "head": "0dd0023",
                  "body": "Nothing changed.", "comments": [], "checks": [("build", "SUCCESS")]}

FILES = [
    {"filename": "src/cart.js", "status": "modified", "additions": 4, "deletions": 2,
     "patch": "@@ -1,6 +1,8 @@\n const cart = [];\n-export function load() { return []; }\n+export function load() {\n+  return JSON.parse(localStorage.cart ?? \"[]\");\n+}\n"
              " export function add(item) {\n-  cart.push(item);\n+  cart.push({ ...item, at: Date.now() });\n }\n+export const save = () => { localStorage.cart = JSON.stringify(cart); };\n"},
    {"filename": "test/cart.test.js", "status": "added", "additions": 4, "deletions": 0,
     "patch": "@@ -0,0 +1,4 @@\n+import { load } from \"../src/cart.js\";\n+test(\"an empty cart\", () => {\n+  expect(load()).toEqual([]);\n+});\n"},
    {"filename": "src/legacy/session-cart.js", "status": "removed", "additions": 0, "deletions": 3,
     "patch": "@@ -1,3 +0,0 @@\n-// The cart in the session, before local storage.\n-export const cart = [];\n-export const load = () => cart;\n"},
    {"filename": "src/checkout/total.js", "previous_filename": "src/total.js", "status": "renamed", "additions": 1, "deletions": 1,
     "patch": "@@ -1,2 +1,2 @@\n-import { cart } from \"./cart.js\";\n+import { cart } from \"../cart.js\";\n export const total = () => cart.reduce((n, i) => n + i.price, 0);\n"},
    {"filename": "assets/cart-empty.png", "status": "added", "additions": 0, "deletions": 0},
    {"filename": "README.md", "status": "modified", "additions": 2, "deletions": 0,
     "patch": "@@ -1,19 +1,21 @@\n # Shop\n+\n+The cart is kept in local storage, so a reload keeps it.\n"
              + "".join(f" line {n} of the README\n" for n in range(1, 19))},
]

detail = base.detail


def detail_with_files(n):
    out = detail(n)
    if base.ITEMS[n]["pr"]:
        out["changedFiles"] = len(FILES) if n == 21 else 0 if n == 23 else 3
    return out


base.detail = detail_with_files
get = base.Handler.do_GET


def do_GET(self):
    path = self.path.split("?")[0]
    if m := re.fullmatch(rf"/api/repos/{REPO}/pulls/(\d+)/files", path):
        time.sleep(float(os.environ.get("FILES_DELAY", "1.5")))
        n = int(m[1])
        if n == 22:
            return self.answer(502, {"message": "Server Error"})
        return self.answer(200, FILES if n == 21 and "page=1" in self.path else [])
    return get(self)


base.Handler.do_GET = do_GET

if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), base.Handler).serve_forever()
