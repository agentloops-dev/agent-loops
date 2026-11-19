# Dead-code review baseline

This file records confirmed false positives. Each entry names the analyzer, declaration
or path, and why the code is live. It is empty until the first complete scan confirms a
false positive. Do not add broad suppressions to make a scan quiet.

Tool pins for the first scan:

- Periphery: pinned in `scripts/lint.sh` since #6336 (3.8.0)
- `golang.org/x/tools/cmd/deadcode` v0.49.0
- Knip 6.42.1

The Web Knip entry points are `src/main.tsx`, `src/sandbox/proxy.ts` and `src/sw.ts` (the
service worker), the JavaScript bundles declared in `Web/build.mjs`. Knip does not infer these API-based
esbuild entries, so `Web/knip.json` names them. The analyzer configuration has no ignores.

## Confirmed false positives

Added 2026-10-27 (first run, `2026-10-27.md`).

### deadcode (Go)

- `Go/internal/agentsd/wake.go` `(*waker).Settings`, `Go/internal/controllink/hostkey.go`
  `HostKeyService`, and in `Go/internal/mac/` `power.go` (`ReadPower`, `HoldAwake`,
  `Hold.Release`), `signin/claude.go` (`NewClaude`, `readKeychain`, `askTheMacsClaude`,
  `isExecutable`, `loginPath`) and `signin/relays.go` (`Relays.SignIn`, `Grant`, `Port`,
  `Stop`, `write`): unreachable only in a linux scan. Their callers are in `_darwin.go`
  files; the `GOOS=darwin` scan reaches them. Scan with `GOOS=darwin` as well before
  reading any `internal/mac` finding.

Added 2026-10-29 (`2026-10-29.md`, the first complete `GOOS=darwin -test` scan):

- `Go/internal/agentsd/procstatus.go` `readProcStatus`: unreachable only in a darwin
  scan. Its caller is `roles_linux.go` (`sshSessions`). The mirror of the entry above:
  read a darwin finding against the linux scan.
- Functions that only the non-test scans list (`deadcode ./...` without `-test`), such as
  the fakes in `s3store/s3fake`, `signin/signintest` and `store.NewMemory`: tests reach
  them, so they are test hooks, not dead. Only a `-test` finding is a candidate.

### Periphery (Swift)

- Since #6336, Periphery is a gate on every PR (`scripts/lint.sh periphery`, in
  `swift-lint:lint`), not this review's: a Swift declaration kept on purpose is a line in
  `.periphery-allowed.txt`, with why. Of the entries that were here, the whole-program scan no
  longer reports `isAntigravityUsageLimit`; `Paper.forgeWaitingWash` and `forgeWaitingRule`
  are in the allowlist; and `run_periphery` runs `xcodegen generate` itself (added 2026-11-19).

### Knip (Web)

- Any export of `Web/src` that a `Web/test/*.test.mjs` file names: the tests bundle the
  source with `test/load.mjs` and read exports by name, which Knip does not follow.
  Knip lists them as unused exports. Not ignored in `knip.json` (it would hide real
  findings); filter them when reading the output.

### Compatibility search

- `Go/internal/agentsd/guarded.go` (the loop after "A file the app no longer keeps"):
  drops a record for any path that is not guarded; the general rule, live.
- `Packages/AgentsKit/Sources/AgentsKitCore/Model/Folder.swift` `unknownFields`: forward
  compatibility between machines on different versions; not migration code.
- "A host (or daemon) too old to know the method": a client and a host of different
  versions, which is live in v0.x; not migration code.
- `App/Sources/Control/ControlConfig.swift` `retiredFileKey` and
  `Remote/Sources/Link/ControlPlaneLink.swift` (the `control-client-key` comment, #1031):
  they delete a key that an older build kept where backups copied it. A secret clean-up,
  live; not migration code (added 2026-10-29).
- The #58 cut-off is v0.13.0 (Alex, 2026-10-30, #2981 D2: 1; it was spec 051). A shim
  for a build older than v0.13.0 is dead: the shims that said "after the #58 cut-off
  (051)" go in #3030 (the daemon's replies), #3031 (records on disk) and #3032 (the #111
  grant). A shim for v0.13.0 or later is live. `WorkOutcome.x-legacy` (#2531) went in
  #2981 (D1: 1).
- `Go/wire/rename.go` `OldMethodName`, read in `Go/internal/agentsd/localpath.go` and
  `Go/internal/controlplane/rpc.go`: receivers accept `machine/*` and `machines/*` under
  their old `host/*` names, step 1 of the two-release rename that the open issue #2653
  tracks. Live: it is the rename, not migration code. It goes in #2653's step 3
  (added 2026-10-30).
- The policy layers' first-start move (#2957 FR-012, research R9):
  `Go/internal/agentsd/policyyours.go` `firstStartMoveLocked` and `moveAsideLocked`.
  They read the v0.14 settings files (`limits.json`, `workflow-limit.json`,
  `display.json`'s notice keys, the archive switch, `runtime-environment.json`) once into
  your policy layer. Migration code by a decision in the spec, because a limit that went
  away without a word would make the rules looser. Live while a v0.14 machine can still
  start v0.15 for the first time; propose its removal once v0.15 is the oldest supported
  release (added 2026-10-31).
- The organisation policy's `modeCeiling` move (#2957 T035):
  `Go/internal/controlplane/policy.go` `migrateModeCeiling`. It reads a stored
  organisation policy's old `modeCeiling` into `permissionMode` once and signs a new
  version, by the same decision as the first-start move above (a limit that went away
  without a word would make the rules looser). Live while the control plane can hold a
  policy written before it; propose its removal with the first-start move
  (added 2026-11-12).
- `Packages/AgentsKit/Sources/AgentsKitCore/Files/Leftovers.swift` and
  `Shared/UI/Voice/KokoroVoice.swift` `link(from:)` (#3747): they delete files that an
  older copy of the app left on the device (TextEdit copies, old voice-model links). A
  clean-up of the device, as `retiredFileKey`; not migration code (added 2026-11-12).
- `Go/internal/controlplane/people.go` `saveForgeLogins` and `peopleList` ("the local
  owner's own record may not exist yet", "before the migration has written the local
  owner", #994): `records.Migrate` runs at every start (`service.go`), so a new control
  plane has no people record until then. The first start of a new store, not a shim for
  an old build; live (added 2026-11-19).
- deadcode needs `GOTOOLCHAIN` set to `go.mod`'s Go (as `Go/scripts/go.sh` sets it) when
  it runs in a clean environment. Without it, the Mac's newer Go runs `go list` for a
  deadcode built with the older one, and the scan fails with "packages contain errors"
  (added 2026-11-19).
