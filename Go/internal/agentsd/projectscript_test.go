package agentsd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
)

// A send runs the project's .agents/setup.sh there (#7158); the same script does not run
// again, a changed one does, and a failure is in the folder's log and runs again at the
// next send.
func TestSendRunsTheProjectScriptWhenItChanged(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	src := repo(t)
	// The script writes outside the folder: a file it made there would be the machine's own change.
	ran := filepath.Join(t.TempDir(), "ran.txt")
	script := func(text string) {
		must(t, os.MkdirAll(filepath.Join(src, ".agents"), 0o700))
		must(t, os.WriteFile(filepath.Join(src, hostsetup.SetupScript), []byte(text), 0o600)) //nolint:gosec // G703: the test's own folder
		sendGit(t, src, nil, "add", "-A")
		sendGit(t, src, nil, "commit", "-q", "-m", "script")
	}
	dest := filepath.Join(canonical(t.TempDir()), "hello")
	id := 100
	send := func() {
		t.Helper()
		m, _ := macSend(t, c, src, nil, false, "main")
		m["folder"], m["checkout"] = dest, "main"
		id++
		c.call(t, id, "folders/receive", m)
	}
	runs := func() []string {
		b, _ := os.ReadFile(ran) //nolint:gosec // G304: the test's own file
		return strings.Fields(string(b))
	}
	runsAre := func(want ...string) func() bool {
		return func() bool { return strings.Join(runs(), ",") == strings.Join(want, ",") }
	}
	ranWell := func() bool { h, _ := hostsetup.ScriptHash(dest); return hostsetup.LastRan(dest) == h }

	script(fmt.Sprintf("echo \"one-$AGENTS_SETUP-$AGENTS_FROM_POOL\" >> %q\n", ran))
	send()
	waitFor(t, "first run", runsAre("one-1-0"))
	waitFor(t, "mark", ranWell)

	// The same script: nothing runs.
	send()
	if d.afterReceive(dest) {
		t.Fatal("the same script ran again")
	}
	if got := runs(); len(got) != 1 {
		t.Fatalf("the same script ran again: %v", got)
	}

	// A changed script runs.
	script(fmt.Sprintf("echo two >> %q\n", ran))
	send()
	waitFor(t, "second run", runsAre("one-1-0", "two"))
	waitFor(t, "mark", ranWell)

	// A failure: in the log, no mark, and the next send runs it again.
	script(fmt.Sprintf("echo three >> %q\necho broken\nexit 3\n", ran))
	send()
	logFile := filepath.Join(d.Store.Loc.Root, "logs", "setup-hello.log")
	waitFor(t, "failed run", runsAre("one-1-0", "two", "three"))
	waitFor(t, "log", func() bool { b, _ := os.ReadFile(logFile); return strings.Contains(string(b), "broken") }) //nolint:gosec // G304: the test's own file
	if ranWell() {
		t.Fatal("a failed run marked the checkout")
	}
	send()
	waitFor(t, "run after a failure", runsAre("one-1-0", "two", "three", "three"))
}
