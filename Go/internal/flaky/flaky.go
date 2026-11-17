// Package flaky quarantines a Go test that fails under load, as `.flakyUnderLoad` does for a
// Swift test (Packages/AgentsKit/Tests/AgentsKitTests/Support/FlakyUnderLoad.swift).
//
// The Fix main lane marks a test that failed on main with no cause in its batch (a flake), on
// a pull request of its own, and files a bug to fix it later (.agents/skills/fix-main). A
// rerun is not the test: a rerun that passes leaves the flake in the suite.
//
//	func TestSomething(t *testing.T) {
//		flaky.UnderLoad(t, "#1234")
//		…
//	}
//
// The test is skipped where CI is set, or AGENTS_QUARANTINE=1, unless AGENTS_RUN_FLAKY=1. On a
// Mac, Go/scripts/go.sh runs Go with a clean environment, so the test runs there.
package flaky

import (
	"os"
	"testing"
)

// UnderLoad skips t in CI. bug is the issue that tracks the fix, such as "#1234".
func UnderLoad(t testing.TB, bug string) {
	t.Helper()
	if Quarantined(os.Getenv) {
		t.Skipf("flaky under load; quarantined in CI (%s)", bug)
	}
}

// Quarantined says whether a test marked flaky is skipped, from the environment that getenv
// reads.
func Quarantined(getenv func(string) string) bool {
	if getenv("AGENTS_RUN_FLAKY") == "1" {
		return false
	}
	return getenv("CI") != "" || getenv("AGENTS_QUARANTINE") == "1"
}
