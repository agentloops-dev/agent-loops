package agentsd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7459: a chat whose allowance ran out continues on another account of the same runtime,
// which the person chose. Only scratch account folders and the fake runtime: never a real
// Claude or Codex home.

// accountsForContinue is a daemon with two added Claude accounts, Work and Personal, a home
// of its own for the runtime's own sign-in, and a chat on Work whose allowance ran out.
func accountsForContinue(t *testing.T) (d *Daemon, c *conn, id wire.UUID, work, personal wire.AccountProfile) {
	t.Helper()
	fakeRuntimes(t)
	home := t.TempDir()
	personalHomeFor(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	testHooksOf(t).change(func(h *testHooks) { h.opts.runtimeEnv = append(h.opts.runtimeEnv, "HOME="+home) })
	d, c = start(t, t.TempDir())
	t.Cleanup(d.Shutdown)
	mustUnmarshal(t, c.call(t, 1, "runtimes/addAccount", map[string]any{"runtimeID": "claude", "name": "Work"}), &work)
	mustUnmarshal(t, c.call(t, 2, "runtimes/addAccount", map[string]any{"runtimeID": "claude", "name": "Personal"}), &personal)
	mustUnmarshal(t, c.call(t, 3, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(), "accountID": work.ID,
		"prompt": "typed limit", "startOptions": map[string]any{"values": map[string]any{}, "extraArguments": []string{}}}), &id)
	c.until(t, "stopped", ended(id, wire.AgentStateStopped))
	if a := d.Store.Agent(id); a.RuntimeSessionID == nil {
		sid := "conversation-1"
		d.change(id, func(a *wire.Agent) { a.RuntimeSessionID = &sid })
	}
	return d, c, id, work, personal
}

func TestContinueOnAccountMovesTheConversationAndGoesOn(t *testing.T) {
	d, c, id, work, personal := accountsForContinue(t)
	sid := *d.Store.Agent(id).RuntimeSessionID
	from, to := accountFolder(work), accountFolder(personal)
	record := filepath.Join("projects", "-scratch-folder", sid+".jsonl")
	results := filepath.Join("projects", "-scratch-folder", sid, "tool-results", "1.txt")
	other := filepath.Join("projects", "-scratch-folder", "another-conversation.jsonl")
	for _, f := range []string{record, results, other, ".credentials.json"} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(from, f)), 0o700))
		must(t, os.WriteFile(filepath.Join(from, f), []byte(f), 0o600))
	}

	req := store.NewUUID()
	var next wire.UUID
	mustUnmarshal(t, c.call(t, 10, "agents/continueOnAccount", map[string]any{"agentID": id, "accountID": personal.ID, "requestID": req}), &next)
	if next != id {
		t.Fatalf("went on in %s, not in the same chat %s", next, id)
	}
	a := d.Store.Agent(id)
	if a.AccountID == nil || *a.AccountID != personal.ID || a.State == wire.AgentStateArchived {
		t.Fatalf("account %v, state %s", a.AccountID, a.State)
	}
	for _, f := range []string{record, results} {
		if b, err := os.ReadFile(filepath.Join(to, f)); err != nil || string(b) != f {
			t.Fatalf("%s not moved: %v", f, err)
		}
		if _, err := os.Stat(filepath.Join(from, f)); !os.IsNotExist(err) {
			t.Fatalf("%s still in the old account: %v", f, err)
		}
	}
	// Never the sign-in, nor another conversation.
	if _, err := os.Stat(filepath.Join(to, ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("the sign-in was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to, other)); !os.IsNotExist(err) {
		t.Fatalf("another conversation was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(from, ".credentials.json")); err != nil {
		t.Fatalf("the old sign-in went: %v", err)
	}
	waitFor(t, "went on", func() bool { return sessionSaid(d, id, continueOnAccountWords) })
	waitFor(t, "the note", func() bool { return noteSaid(d, id, "Continued on Claude · Personal.") })

	var again wire.UUID
	mustUnmarshal(t, c.call(t, 11, "agents/continueOnAccount", map[string]any{"agentID": id, "accountID": personal.ID, "requestID": req}), &again)
	if again != id {
		t.Fatalf("a retry answered %s", again)
	}
}

func TestContinueOnAccountWithNoRecordStartsANewSessionThere(t *testing.T) {
	d, c, id, _, personal := accountsForContinue(t)
	title := "Login bug"
	d.change(id, func(a *wire.Agent) { a.Title = &title })
	var next wire.UUID
	mustUnmarshal(t, c.call(t, 10, "agents/continueOnAccount", map[string]any{"agentID": id, "accountID": personal.ID}), &next)
	if next == id {
		t.Fatal("went on in the same chat with no record to move")
	}
	made := d.Store.Agent(next)
	if made.RuntimeID != "claude" || made.AccountID == nil || *made.AccountID != personal.ID || made.Title == nil || *made.Title != title {
		t.Fatalf("%s %v %v", made.RuntimeID, made.AccountID, made.Title)
	}
	if d.Store.Agent(id).State != wire.AgentStateArchived {
		t.Fatal(d.Store.Agent(id).State)
	}
	want := `Continue session "Login bug" (` + string(id) + `) by using tool read_session to understand the work. You're in the same folder.`
	waitFor(t, "the prompt", func() bool { return sessionSaid(d, next, want) })
}

func TestContinueOnAccountRefuses(t *testing.T) {
	d, c, id, work, personal := accountsForContinue(t)
	if e := c.callError(t, 10, "agents/continueOnAccount", map[string]any{"agentID": id, "accountID": work.ID}); e == nil || !strings.Contains(e.Message, "already") {
		t.Fatal("the same account was taken", e)
	}
	if e := c.callError(t, 11, "agents/continueOnAccount", map[string]any{"agentID": id, "accountID": "nobody"}); e == nil {
		t.Fatal("an account that is not there was taken")
	}
	d.allowMu.Lock()
	until := wire.DateOf(time.Now().Add(time.Hour).Truncate(time.Second))
	key := accountKey("claude", &personal.ID)
	s := availableState(key)
	s.Status = wire.AllowanceStateStatus{Out: &wire.AllowanceStateStatusOut{Until: &until, Why: wire.AllowanceStateOutReasonAllowanceSpent}}
	d.allowancesLocked()[key] = s
	d.allowMu.Unlock()
	if e := c.callError(t, 12, "agents/continueOnAccount", map[string]any{"agentID": id, "accountID": personal.ID}); e == nil || !strings.Contains(e.Message, "out too") {
		t.Fatal("an account that is out was taken", e)
	}
	done := seeded(t, d, t.TempDir(), nil)
	if e := c.callError(t, 13, "agents/continueOnAccount", map[string]any{"agentID": done.ID, "accountID": "default"}); e == nil || !strings.Contains(e.Message, "allowance ran out") {
		t.Fatal("a finished chat moved", e)
	}
	if a := d.Store.Agent(id); a.AccountID == nil || *a.AccountID != work.ID || a.State != wire.AgentStateStopped {
		t.Fatalf("a refusal changed the chat: %v %s", a.AccountID, a.State)
	}
}

func TestMoveRuntimeFilesCodexKeepsItsDayFolder(t *testing.T) {
	t.Parallel()

	from, to := t.TempDir(), t.TempDir()
	rollout := filepath.Join("sessions", "2026", "11", "26", "rollout-2026-11-26T09-00-00-abc.jsonl")
	must(t, os.MkdirAll(filepath.Dir(filepath.Join(from, rollout)), 0o700))
	must(t, os.WriteFile(filepath.Join(from, rollout), []byte("{}"), 0o600))
	must(t, os.WriteFile(filepath.Join(from, "auth.json"), []byte("{}"), 0o600))
	if err := moveRuntimeFiles("codex", "abc", []string{"CODEX_HOME=" + from}, []string{"CODEX_HOME=" + to}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(to, rollout)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(to, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("auth.json was copied: %v", err)
	}
	if err := moveRuntimeFiles("codex", "missing", []string{"CODEX_HOME=" + from}, []string{"CODEX_HOME=" + to}); err != errNoConversation {
		t.Fatalf("no record: %v", err)
	}
	if err := moveRuntimeFiles("grok", "abc", nil, nil); err != errNoConversation {
		t.Fatalf("a runtime with one account: %v", err)
	}
}

// noteSaid is whether the chat has a runtime note with text.
func noteSaid(d *Daemon, id wire.UUID, text string) bool {
	entries, _, _ := d.Store.Transcript(id, 0)
	for _, e := range entries {
		if n := e.Kind.RuntimeNote; n != nil && n.Value == text {
			return true
		}
	}
	return false
}
