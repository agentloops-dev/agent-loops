// Quarantines a web test that fails under load, as `.flakyUnderLoad` does for a Swift test
// (Packages/AgentsKit/Tests/AgentsKitTests/Support/FlakyUnderLoad.swift) and flaky.UnderLoad
// for a Go test (Go/internal/flaky).
//
// The Fix main lane marks a test that failed on main with no cause in its batch (a flake), on
// a pull request of its own, and files a bug to fix it later (.agents/skills/fix-main). A
// rerun is not the test: a rerun that passes leaves the flake in the suite.
//
//   test("it does a thing", { skip: flakyUnderLoad("#1234") }, async () => { … });
//
// Skipped where CI is set, or AGENTS_QUARANTINE=1, unless AGENTS_RUN_FLAKY=1. The value is the
// reason node:test prints, or false to run the test.
export function flakyUnderLoad(bug, env = process.env) {
  return quarantined(env) ? `flaky under load; quarantined in CI (${bug})` : false;
}

export function quarantined(env = process.env) {
  if (env.AGENTS_RUN_FLAKY === "1") return false;
  return Boolean(env.CI) || env.AGENTS_QUARANTINE === "1";
}
