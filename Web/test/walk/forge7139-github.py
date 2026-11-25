#!/usr/bin/env python3
"""A fixture GitHub for blocked images on a Work page (#7139): work6355-github.py's example/shop, with
only an issue for each state. Their images are on public hosts that are on no list, so the daemon reads
them for real once they are shown or allowed. Scratch data only. Point a run-app host at it with
--env AGENTS_TEST_GITHUB_URL=http://127.0.0.1:<port>.

    python3 -I Web/test/walk/forge7139-github.py <port>
"""
import importlib.util
import os
import sys
from datetime import timedelta
from http.server import ThreadingHTTPServer

PORT = int(sys.argv.pop(1))
spec = importlib.util.spec_from_file_location("work6355", os.path.join(os.path.dirname(__file__), "work6355-github.py"))
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)
base.ITEMS.clear()
base.DELAY = base.REST_DELAY = 0
WORDS = {}
node = base.node


def words(n, body=False):
    got = node(n, body)
    got["title"] = WORDS[n][0]
    if body:
        got["body"] = WORDS[n][1]
    return got


base.node = words

CHART = "https://dummyimage.com/600x200/ccddee/223355.png&text=Build+times"
BEFORE, AFTER = "The chart shows the last six builds.", "The last one is the slowest."


def issue(n, title, body):
    WORDS[n] = (title, body)
    base.ITEMS[n] = {"kind": "issue", "open": True, "labels": ["story"], "assignees": [],
                     "updated": base.NEWEST - timedelta(minutes=n)}


def chart(url):
    return f"{BEFORE}\n\n![Build times, last six]({url})\n\n{AFTER}"


issue(71, "Build times went up after the cache change", chart(CHART))
issue(72, "The chart's host redirects", chart("https://picsum.photos/600/200"))
issue(73, "The chart takes a long time", chart("https://httpbin.org/drip?duration=9&numbytes=10"))
issue(74, "The chart is not there", chart("https://httpbin.org/status/404"))
issue(75, "The chart is on a private network", chart("https://localtest.me/chart.png"))
issue(76, "Several charts", f"Before:\n\n![Before the change]({CHART})\n\nAfter:\n\n![After the change]({CHART}+after)\n\n"
          "From another host:\n\n![Memory graph](https://placehold.co/600x200.png)")
issue(77, "A long host", chart("https://a-very-long-tenant-name-0000000000.media-delivery.eu-central.images.example.net/a.png"))
issue(78, "A badge in words, and one in a link", "Status: ![build status](https://raster.shields.io/badge/build-passing-green.png) "
          "on the main branch.\n\n[![Coverage report](https://raster.shields.io/badge/coverage-90-green.png)](https://example.com/coverage)")
issue(79, "A look-alike host", chart("https://exämple.example/chart.png"))

if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", PORT), base.Handler).serve_forever()
