package agentsd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func conversationID(n int) string { return fmt.Sprintf("01a20bda-e683-7270-8b7d-%012d", n) }

// writeAged writes a file of so many bytes, last written so long before at.
func writeAged(t *testing.T, path, text string, size int, at time.Time, age time.Duration) string {
	t.Helper()
	writeFile(t, path, text+"\n"+string(bytes.Repeat([]byte("x"), size)))
	must(t, os.Chtimes(path, at.Add(-age), at.Add(-age)))
	return path
}

func ageFolder(t *testing.T, path string, at time.Time, age time.Duration) {
	t.Helper()
	must(t, os.Chtimes(path, at.Add(-age), at.Add(-age)))
}

func wantThere(t *testing.T, there bool, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); (err == nil) != there {
			t.Errorf("%s: want there %v (%v)", p, there, err)
		}
	}
}

const accountTestDay = 24 * time.Hour

// A conversation past 90 days goes with its extras; a newer one, one of a session that is not
// archived, and everything else in the home stay (#6707).
func TestAccountHomesAgeOut(t *testing.T) {
	t.Parallel()
	d, c := start(t, t.TempDir())
	dir := folder(t, c, 1)
	at := time.Now()
	old := 91 * accountTestDay
	home := filepath.Join(d.Store.Loc.Root, "runtimes", "accounts", "claude", "a")
	codex := filepath.Join(d.Store.Loc.Root, "runtimes", "accounts", "codex", "b")
	chat := func(n int, age time.Duration) string {
		return writeAged(t, filepath.Join(home, "projects", "-w", conversationID(n)+".jsonl"), "{}", 0, at, age)
	}

	gone := []string{
		chat(1, old),
		writeAged(t, filepath.Join(home, "todos", conversationID(1)+"-agent-"+conversationID(1)+".json"), "", 0, at, old),
		writeAged(t, filepath.Join(home, "debug", conversationID(1)+".txt"), "", 0, at, old),
		chat(4, old), // an archived session's
	}
	subagent := writeAged(t, filepath.Join(home, "projects", "-w", conversationID(1), "subagents", "agent-1.jsonl"), "", 0, at, old)
	ageFolder(t, filepath.Dir(subagent), at, old)
	ageFolder(t, filepath.Dir(filepath.Dir(subagent)), at, old)
	gone = append(gone, filepath.Join(home, "projects", "-w", conversationID(1)))

	needed, archived := conversationID(3), conversationID(4)
	seeded(t, d, dir, func(a *wire.Agent) { a.RuntimeID, a.RuntimeSessionID = "claude", &needed })
	archivedWithSession(t, d, dir, archived)
	unread := conversationID(5)
	writeFile(t, d.Store.Loc.Record("00000000-0000-4000-8000-00000000000F"), `{"runtimeSessionID":"`+unread+`", broken`)
	rollout := func(n int, head string) string {
		return writeAged(t, filepath.Join(codex, "sessions", "2026", "08", "01", "rollout-2026-08-01T06-00-00-"+conversationID(n)+".jsonl"), head, 0, at, old)
	}
	gone = append(gone, rollout(20, "{}"))

	kept := []string{
		chat(2, 89*accountTestDay), chat(3, old), chat(5, old),
		// A helper of the session that is not archived, and one whose first line is not all there.
		rollout(21, `{"payload":{"parent_thread_id":"`+needed+`"}}`),
		writeAged(t, filepath.Join(codex, "archived_sessions", "rollout-2026-08-01T06-00-00-"+conversationID(22)+".jsonl"), string(bytes.Repeat([]byte("x"), rolloutHeadBytes+1)), 0, at, old),
	}
	for _, p := range []string{".claude.json", ".credentials.json", "settings.json", "history.jsonl",
		"projects/-w/memory/MEMORY.md", "projects/-w/notes.jsonl", "plugins/p/x", "skills/" + conversationID(1) + ".jsonl"} {
		kept = append(kept, writeAged(t, filepath.Join(home, p), "", 0, at, old))
	}
	for _, p := range []string{"auth.json", "config.toml", "history.jsonl", "state_5.sqlite", "sessions/2026/08/01/rollout-x.jsonl"} {
		kept = append(kept, writeAged(t, filepath.Join(codex, p), "", 0, at, old))
	}
	// What a link leads to is the person's own.
	own := writeAged(t, filepath.Join(t.TempDir(), conversationID(6)+".jsonl"), "", 0, at, old)
	must(t, os.Symlink(filepath.Dir(own), filepath.Join(home, "projects", "-linked")))
	kept = append(kept, own)

	d.sweepHourly()
	wantThere(t, false, gone...)
	wantThere(t, true, kept...)
}

// Over its bytes, a home's oldest conversations go until it is under; one just written, and
// one a session needs, stay.
func TestAccountHomeSize(t *testing.T) {
	t.Parallel()
	home, at := t.TempDir(), time.Now()
	chat := func(n int, age time.Duration) string {
		return writeAged(t, filepath.Join(home, "projects", "-w", conversationID(n)+".jsonl"), "", 999, at, age)
	}
	oldest, needed, newer, busy := chat(1, 9*accountTestDay), chat(2, 8*accountTestDay), chat(3, 7*accountTestDay), chat(4, time.Hour)
	need := func(id string) bool { return id == conversationID(2) }

	if n, left := sweepAccountHome("claude", home, 4000, at, need); n != 0 || left != 4000 {
		t.Fatalf("at the limit: removed %d, left %d", n, left)
	}
	if n, left := sweepAccountHome("claude", home, 3999, at, need); n != 1 || left != 3000 {
		t.Fatalf("just over: removed %d, left %d", n, left)
	}
	wantThere(t, false, oldest)
	if n, left := sweepAccountHome("claude", home, 1, at, need); n != 1 || left != 2000 {
		t.Fatalf("far over: removed %d, left %d", n, left)
	}
	wantThere(t, false, newer)
	wantThere(t, true, needed, busy)
}

// Codex's helpers and forks are one conversation with their session's, however many steps
// away: all stay while one is needed, and go together.
func TestAccountHomeCodexHelpers(t *testing.T) {
	t.Parallel()
	home, at := t.TempDir(), time.Now()
	rollout := func(n int, head string) string {
		return writeAged(t, filepath.Join(home, "sessions", "2026", "08", "01", "rollout-2026-08-01T06-00-00-"+conversationID(n)+".jsonl"), head, 0, at, 100*accountTestDay)
	}
	named := func(n int) string { return `{"payload":{"parent_thread_id":"` + conversationID(n) + `"}}` }
	family := []string{rollout(1, "{}"), rollout(2, named(1)), rollout(3, named(2)), rollout(4, named(3))}
	alone := rollout(9, "{}")

	if n, _ := sweepAccountHome("codex", home, 1<<30, at, func(id string) bool { return id == conversationID(2) }); n != 1 {
		t.Fatalf("removed %d", n)
	}
	wantThere(t, true, family...)
	wantThere(t, false, alone)
	if n, left := sweepAccountHome("codex", home, 1<<30, at, func(string) bool { return false }); n != 1 || left != 0 {
		t.Fatalf("removed %d, left %d", n, left)
	}
	wantThere(t, false, family...)
}
