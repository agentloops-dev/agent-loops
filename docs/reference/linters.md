---
diataxis: reference
description: Each linter of this repository, its Nx target, what it fails on, its baseline and where CI runs it.
---

# Linters

Each linter in this repository is an Nx target of its language (#5672), and each one fails on a
finding. `scripts/nx.sh affected -t lint` runs the linters of what a branch changes, and skips a
lint that already passed at the same inputs. `scripts/lint.sh` runs one linter by its name, and
is what each target runs.

| Target | Linter | Fails on | Runs in CI |
|---|---|---|---|
| `go:lint` | golangci-lint, every rule in `Go/.golangci.yml` (bugs, security, and gocognit for readability) | a finding not in `Go/.golangci-baseline.txt` | `go` (Linux) |
| `go:lint` | govulncheck | a known vulnerability that the code calls and that `Go/.govulncheck-excluded.txt` does not name | `go` (Linux) |
| `go:lint` | deadcode (`golang.org/x/tools/cmd/deadcode`, whole program) | a function that no main package and no test reaches, in all Go code, and that `Go/.deadcode-allowed.txt` does not name; no baseline | `go` (Linux) |
| `web:lint` | tsc, `Web/test/lint.mjs`, the licence list | an error | `go` (Linux) |
| `web:lint` | Biome, the rules in `Web/biome.json` | an error or a warning | `go` (Linux) |
| `web:lint` | Knip (`Web/knip.config.js`: the bundles of `Web/build.mjs`, the tests and the walks) | an unused file, export, type or dependency of the web app that `Web/knip.config.js` does not allow; no baseline | `go` (Linux) |
| `repo-lint:lint` | shellcheck (`-S warning`, `.shellcheckrc`) | a finding | `go` (Linux) |
| `repo-lint:lint` | `zsh -n` | a syntax error | `go` (Linux) |
| `repo-lint:lint` | our text checks (`scripts/check-*.py` and the other self-tests) | a failure | `go` (Linux) |
| `repo-lint:lint` | jscpd (`.jscpd.json`) | a clone not in `.jscpd-baseline.txt` | `go` (Linux) |
| `swift-lint:lint` | SwiftLint (`.swiftlint.yml`) | a finding not in `.swiftlint-baseline.json` | `swift-lint` (macOS) |

`repo-lint` and `swift-lint` are projects with no code of their own (`lint/`). `repo-lint` reads
every tracked file, so every change runs it; a change to docs only runs only it. CI's `test-swift` job
runs `swift-lint:lint` only when a Swift file, its rules, its baseline or `scripts/lint.sh`
changes.

`Web/test/lint.mjs` also fails when a web test reads a file outside `Web/` that is not an input
of `web:test` in `Web/project.json` (#5780). Without that input, a change to the file alone does
not make `web:test` affected, and Nx's cache gives a false pass. Add the file to
`web-test-reads`.

## Output

`scripts/nx.sh` streams each task's output as the task runs (`NX_DEFAULT_OUTPUT_STYLE=stream`,
#5887), and CI passes `--output-style=stream`. An `--output-style` flag still wins. With no style,
Nx 24 picks `summary` when an agent runs it, which prints only the path of each failing task's
log, and `static` prints a task's output only when the task ends, so a hung task shows nothing.

## Baselines

golangci-lint, SwiftLint and jscpd fail only on a finding that is not in their baseline: the
findings main had when they were made to fail. A finding that only moves stays in its baseline,
as no baseline holds a line number. jscpd's key is the pair of files of a clone, one line per
clone, so a second clone between the same two files is new. Fix an old finding, then run
`scripts/lint.sh --baseline` to drop it from the list. Never add a new finding to a baseline.

## Cognitive complexity

`go:lint` runs gocognit (#6182): it fails on a Go function whose cognitive complexity is over 30,
gocognit's default. Cognitive complexity is how hard a function is to read: each `if`, loop,
`switch`, `&&` and `||` adds to it, and more when it is nested. It is not the function's run time
(big-O); the performance review looks at that. Split a function that is over the limit into
smaller ones, each with a name that says what it does, and keep its behaviour.

When gocognit came, main had 401 functions over 30 (914 over 20, and 1524 over 15). The first PR
split 5 of them. The other 396 are in `Go/.golangci-baseline.txt`, and one `task` under #6182 for
each Go package takes them out. A gocognit finding's key in the baseline has no number: a function
there stays there when an edit changes its complexity, until its task splits it. A new function
over 30 fails.

## No fixed wait in an agentsd test

`go:lint` fails on `time.Sleep` and `time.After` in `Go/internal/agentsd/*_test.go` (forbidigo,
#6128, epic #6126). A fixed wait ends before a loaded `-race` runner answers (spike #5600, cause
B). A test waits on a channel or an event, or for `afterLimit(t)`: the limit from `t.Deadline()`
(`waitLimit`, #5632). A short poll step inside such a wait gets `//nolint:forbidigo // why` on its
line. The waits that were there when the rule came are in `Go/.golangci-baseline.txt`; the lanes
of #6126 take them out.

## What is left out, and why

- **Biome's `noAssignInExpressions` is off.** `entry ??= make()` inside an expression is how this
  code gets or makes a map's entry, in 17 places when it was made to fail.
- **A finding that is right on purpose** has a `biome-ignore` comment on its line, with the
  reason: a control character in a path regex (refused on purpose), a class merged with an
  interface (the store's parts), and `${NAME}` in a string that a person reads.
- **A vulnerability that stays for a while** is a line in `Go/.govulncheck-excluded.txt`: its
  id, then why it stays and the issue that removes it. Two are there now, fixed by Go 1.26.11,
  which #5704 brings in.
- **deadcode is a gate on all Go code, with no baseline (#6334).** It reads every `main`
  package in `Go/` with its tests (`-test`) and its live tests (`-tags live`), once as
  `GOOS=linux` and once as `GOOS=darwin GOARCH=arm64`, and fails on each function that both runs
  say nothing reaches: one that only a `_darwin.go` or a `_linux.go` file calls is live. Remove
  a dead function. One kept on purpose (reflection, `go:linkname`, a method that only something
  outside the program calls) is a line in `Go/.deadcode-allowed.txt`: its path, a colon, its
  name (`Func` or `Type.Method`) and why it stays. golangci-lint's `unused` still finds an
  unexported name that its own package does not use.
- **Knip is a gate on all web code, with no baseline (#6337).** `scripts/lint.sh knip` runs
  Knip at the version `Web/package.json` pins, from the workspace's packages, over `Web/`: from
  the bundles that `Web/build.mjs` makes (`src/main.tsx`, `src/sandbox/proxy.ts`, `src/sw.ts`),
  and from the tests and walks, whose `load("src/…")` it reads as an import. It fails on each
  file, export, type and dependency that none of them reaches: an export that only a test reads
  is live, as a Go function that only a test calls. An export that its own file uses is live
  too; only its `export` word is spare. Remove a dead one. One kept on purpose (the generated
  wire types, a package that the esbuild plugin reads by path, two names for one value) is an
  entry in `Web/knip.config.js`, with why it stays.
- **No dead-code gate for Swift (#6526).** Periphery ran in `swift-lint:lint` from #6336
  until Alex removed it on 2026-11-21: its four index builds took about 8.5 minutes of each
  warm `swift-lint` run, 20 to 30 cold, and a 2.6 GB cache. `swift-lint:lint` is SwiftLint only.
- **govulncheck's database is outside the repository**, so `go:lint`'s inputs hold the date
  (UTC): a pass is good for that day only, and a vulnerability published since runs it again.

## Tools

On a Mac each linter runs on the host, never in a container (#4709): `scripts/lint.sh` gets the
pinned binary into `~/Library/Caches/Agents-build/lint-tools/` the first time. In CI the `go`
job gets them with `scripts/lint.sh --tools` and puts them on the PATH. A linter whose tool
cannot be got fails, rather than pass with nothing run.
