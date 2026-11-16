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
| `go:lint` | golangci-lint, every rule in `Go/.golangci.yml` | a finding not in `Go/.golangci-baseline.txt` | `go` (Linux) |
| `go:lint` | govulncheck | a known vulnerability that the code calls and that `Go/.govulncheck-excluded.txt` does not name | `go` (Linux) |
| `web:lint` | tsc, `Web/test/lint.mjs`, the licence list | an error | `go` (Linux) |
| `web:lint` | Biome, the rules in `Web/biome.json` | an error or a warning | `go` (Linux) |
| `repo-lint:lint` | shellcheck (`-S warning`, `.shellcheckrc`) | a finding | `go` (Linux) |
| `repo-lint:lint` | `zsh -n` | a syntax error | `go` (Linux) |
| `repo-lint:lint` | our text checks (`scripts/check-*.py` and the other self-tests) | a failure | `go` (Linux) |
| `repo-lint:lint` | jscpd (`.jscpd.json`) | a clone not in `.jscpd-baseline.txt` | `go` (Linux) |
| `swift-lint:lint` | SwiftLint (`.swiftlint.yml`) | a finding not in `.swiftlint-baseline.json` | `test` (macOS) |

`repo-lint` and `swift-lint` are projects with no code of their own (`lint/`). `repo-lint` reads
every tracked file, so every change runs it; a change to docs only runs only it. CI's `test` job
runs `swift-lint:lint` only when a Swift file, its rules, its baseline or `scripts/lint.sh`
changes.

## Output

`scripts/nx.sh` streams each task's output as the task runs (`NX_DEFAULT_OUTPUT_STYLE=stream`,
#5887), and CI passes `--output-style=stream`. An `--output-style` flag still wins. With no style,
Nx 24 picks `summary` when an agent runs it, which prints only the path of each failing task's
log, and `static` prints a task's output only when the task ends, so a hung task shows nothing.
`scripts/ci-sample.mjs` keeps `static`, because it keeps the log for a bug, not for a reader.

## Baselines

golangci-lint, SwiftLint and jscpd fail only on a finding that is not in their baseline: the
findings main had when they were made to fail. A finding that only moves stays in its baseline,
as no baseline holds a line number. jscpd's key is the pair of files of a clone, one line per
clone, so a second clone between the same two files is new. Fix an old finding, then run
`scripts/lint.sh --baseline` to drop it from the list. Never add a new finding to a baseline.

## What is left out, and why

- **Biome's `noAssignInExpressions` is off.** `entry ??= make()` inside an expression is how this
  code gets or makes a map's entry, in 17 places when it was made to fail.
- **A finding that is right on purpose** has a `biome-ignore` comment on its line, with the
  reason: a control character in a path regex (refused on purpose), a class merged with an
  interface (the store's parts), and `${NAME}` in a string that a person reads.
- **A vulnerability that stays for a while** is a line in `Go/.govulncheck-excluded.txt`: its
  id, then why it stays and the issue that removes it. Two are there now, fixed by Go 1.26.11,
  which #5704 brings in.
- **govulncheck's database is outside the repository**, so `go:lint`'s inputs hold the date
  (UTC): a pass is good for that day only, and a vulnerability published since runs it again.

## Tools

On a Mac each linter runs on the host, never in a container (#4709): `scripts/lint.sh` gets the
pinned binary into `~/Library/Caches/Agents-build/lint-tools/` the first time. In CI the `go`
job gets them with `scripts/lint.sh --tools` and puts them on the PATH. A linter whose tool
cannot be got fails, rather than pass with nothing run.
