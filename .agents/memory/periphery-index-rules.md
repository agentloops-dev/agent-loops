---
name: periphery-index-rules
description: Periphery gives a different answer each run when an index has two builds of a file; it misses $binding, \( ) and init-as-value uses
metadata:
  type: project
---

Periphery 3.8.0 (`scripts/lint.sh periphery`, #6336) is only deterministic when each index
store holds one build of each file: one scheme, one platform, one arch, one store per scan.
A scan over two stores, or a DerivedData that built both the Mac app and the Remote, or the
Remote for both simulator archs, gave 1739 to 1983 findings on the same index. That is why
lint.sh builds four indexes and `scripts/periphery-report.py` intersects the scans.

It also reports as unused, wrongly: a `@State` read only as `$name`, a static read only in a
string interpolation, an `init` passed as a function value (`map(T.init(x:))`), a type named
only as an enum case's payload or a return type, and some imports a file needs. The compiler
is the check: build after a removal, and put a false positive in `.periphery-allowed.txt`.

**Why:** found while removing 1083 findings on 2026-11-19.
**How to apply:** never trust one scan of a mixed index; when a removal does not compile, restore it and allowlist it with why.
