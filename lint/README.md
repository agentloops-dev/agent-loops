# lint

Two Nx projects with no code of their own (#5672): each has one `lint` target that runs
`scripts/lint.sh` over the whole tree.

- `repo-lint` (this folder): shellcheck, `zsh -n`, our text checks and jscpd. Its inputs are
  every tracked file, so a change anywhere runs it, and the cache skips it at the same tree.
- `swift-lint` (`swift/`): SwiftLint, tag `xcode`, so CI's macOS `swift-lint` job runs it.

The other linters are their languages' targets: `go:lint` (golangci-lint, govulncheck, deadcode) and
`web:lint` (tsc, `test/lint.mjs`, the licence list, Biome, Knip). `docs/reference/linters.md` says
what each one fails on.
