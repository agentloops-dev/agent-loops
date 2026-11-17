package agentsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #5919: a chat whose allowance ran out continues in a new session on Auto, in the same
// worktree, and the old chat is archived only after that start is saved.

func TestContinueInNewSessionKeepsTheWorktreeAndTheTitle(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := repo(t)
	c.call(t, 1, "folders/add", map[string]any{"folder": store.FolderURL(dir)})
	var id wire.UUID
	if err := json.Unmarshal(c.call(t, 2, "agents/start", map[string]any{"runtimeID": "claude", "cwd": store.FolderURL(dir),
		"prompt": "typed limit", "worktree": map[string]any{"new": map[string]any{}}}), &id); err != nil {
		t.Fatal(err)
	}
	c.until(t, "stopped", ended(id, wire.AgentStateStopped))
	title := "Login bug"
	yes := true
	d.change(id, func(a *wire.Agent) {
		a.Title, a.TitleIsPersons = &title, &yes
		a.ContinueWhenBack = &wire.ContinueWhenBack{At: wire.DateOf(a.CreatedAt.Time()), ResetKnown: true}
	})
	root := store.FolderPath(d.Store.Agent(id).Worktree.Root)
	req := store.NewUUID()
	var next wire.UUID
	if err := json.Unmarshal(c.call(t, 3, "agents/continueInNewSession", map[string]any{"agentID": id, "requestID": req}), &next); err != nil {
		t.Fatal(err)
	}
	var again wire.UUID
	if err := json.Unmarshal(c.call(t, 4, "agents/continueInNewSession", map[string]any{"agentID": id, "requestID": req}), &again); err != nil {
		t.Fatal(err)
	}
	if again != next {
		t.Fatalf("a retry made %s, not %s", again, next)
	}
	old := d.Store.Agent(id)
	if old.State != wire.AgentStateArchived || old.ContinueWhenBack != nil {
		t.Fatalf("old: %s waiting %+v", old.State, old.ContinueWhenBack)
	}
	made := d.Store.Agent(next)
	want := `Continue session "Login bug" (` + string(id) + `) by using tool read_session to understand the work. You're in the same worktree.`
	if made.Auto == nil || made.Worktree == nil || store.FolderPath(made.Worktree.Root) != root ||
		made.Title == nil || *made.Title != title || made.TitleIsPersons == nil || !*made.TitleIsPersons {
		t.Fatalf("%s %+v %v", made.RuntimeID, made.Worktree, made.Title)
	}
	waitFor(t, "the prompt", func() bool { return sessionSaid(d, next, want) })
	d.sweeps.Wait()
	if !isDir(root) {
		t.Fatal("the worktree went when the old chat was archived")
	}
	if _, kept := d.sweepWorktree(*old, sweepArchived); kept == "" || !isDir(root) {
		t.Fatalf("sweep removed it: %q", kept)
	}
}

func TestContinueInNewSessionInAFolderUsesTheFolderSentence(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	cwd := t.TempDir()
	var id wire.UUID
	if err := json.Unmarshal(c.call(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + cwd, "prompt": "typed limit"}), &id); err != nil {
		t.Fatal(err)
	}
	c.until(t, "stopped", ended(id, wire.AgentStateStopped))
	d.change(id, func(a *wire.Agent) { a.Title, a.TitleIsPersons = nil, nil })
	var next wire.UUID
	if err := json.Unmarshal(c.call(t, 2, "agents/continueInNewSession", map[string]any{"agentID": id}), &next); err != nil {
		t.Fatal(err)
	}
	made := d.Store.Agent(next)
	want := "Continue session " + string(id) + " by using tool read_session to understand the work. You're in the same folder."
	if made.Auto == nil || made.Worktree != nil || made.Title != nil || store.FolderPath(made.Cwd) != cwd {
		t.Fatalf("%+v %v", made.Worktree, made.Title)
	}
	if d.Store.Agent(id).State != wire.AgentStateArchived {
		t.Fatal(d.Store.Agent(id).State)
	}
	waitFor(t, "the prompt", func() bool { return sessionSaid(d, next, want) })
}

func TestContinueInNewSessionRefusesUnlessTheAllowanceRanOut(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	cwd := t.TempDir()
	done := seeded(t, d, cwd, nil)
	if e := c.callError(t, 1, "agents/continueInNewSession", map[string]any{"agentID": done.ID}); !strings.Contains(e.Message, "allowance ran out") || d.Store.Agent(done.ID).State != wire.AgentStateFinished {
		t.Fatal("a finished chat was archived", e)
	}
	stopped := seeded(t, d, cwd, func(a *wire.Agent) {
		a.State, a.EndedReason = wire.AgentStateStopped, reason(wire.EndedReasonEndTurn)
	})
	if e := c.callError(t, 2, "agents/continueInNewSession", map[string]any{"agentID": stopped.ID}); !strings.Contains(e.Message, "allowance ran out") {
		t.Fatal(e)
	}
	if e := c.callError(t, 3, "agents/continueInNewSession", map[string]any{"agentID": store.NewUUID()}); e.Code != CodeNoSuchAgent {
		t.Fatal(e)
	}
	gone := filepath.Join(t.TempDir(), "missing")
	paused := seeded(t, d, gone, func(a *wire.Agent) {
		a.State, a.EndedReason = wire.AgentStateStopped, reason(wire.EndedReasonAllowanceSpent)
	})
	if e := c.callError(t, 4, "agents/continueInNewSession", map[string]any{"agentID": paused.ID}); e.Code != CodeFolderGone {
		t.Fatal(e)
	}
	if got := d.Store.Agent(paused.ID); got.State != wire.AgentStateStopped || got.ArchivedAt != nil {
		t.Fatalf("archived after a failed start: %s", got.State)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// sessionSaid is whether the new session was given text, in its queue or its transcript.
func sessionSaid(d *Daemon, id wire.UUID, text string) bool {
	if a := d.Store.Agent(id); a != nil {
		for _, q := range a.QueuedPrompts {
			if q.Text == text {
				return true
			}
		}
	}
	for _, u := range userMessages(d, id) {
		if u == text {
			return true
		}
	}
	return false
}
