package agentsd

import (
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// An archive during a turn (#1611): the runtime that only a SIGTERM ends is not taken for
// a crash, nothing asks for the person, the session stays archived, and the next start
// does not pick it back up (#1605).
func TestAnArchiveDuringATurnStaysArchived(t *testing.T) {
	t.Parallel()

	fakeRuntimeProcesses(t)
	root := t.TempDir()
	first, c := start(t, root)
	id := startAgent(t, c, 1, t.TempDir(), "keep going")
	c.until(t, "running", ended(id, wire.AgentStateRunning))
	c.call(t, 2, "agents/archive", map[string]any{"agentID": id})
	settled := func(d *Daemon) {
		t.Helper()
		a := d.Store.Agent(id)
		if a.State != wire.AgentStateArchived || a.ArchivedReason == nil || *a.ArchivedReason != wire.AgentArchivedReasonByUser {
			t.Fatalf("%s %v", a.State, a.ArchivedReason)
		}
		entries, _, _ := d.Store.Transcript(id, 0)
		for _, e := range entries {
			if n := e.Kind.Notice; n != nil && n.Value.Severity == "error" {
				t.Fatalf("notice %q", n.Value.Title)
			}
			if sc := e.Kind.StateChanged; sc != nil && sc.Reason != nil && (*sc.Reason == wire.EndedReasonProcessDied || *sc.Reason == wire.EndedReasonDaemonGone) {
				t.Fatalf("ended %s", *sc.Reason)
			}
		}
		for _, n := range names(d) {
			if n == "agent.failed" || n == "agent.stopped" || n == "agent.needs_you" {
				t.Fatalf("raised %s: %v", n, names(d))
			}
		}
	}
	waitFor(t, "the runtime gone", func() bool {
		l := first.live(id)
		l.mu.Lock()
		defer l.mu.Unlock()
		return !l.inTurn
	})
	time.Sleep(200 * time.Millisecond)
	settled(first)
	first.Shutdown()

	d, _ := start(t, root)
	defer d.Shutdown()
	d.PickUpAfterRestart()
	time.Sleep(500 * time.Millisecond)
	settled(d)
}

// An archive while a card waits for the person: the card's answer does not take the
// session out of the archive (#1611).
func TestAnArchiveWhileACardWaitsStaysArchived(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	var id wire.UUID
	var card wire.PermissionRequest
	mustUnmarshal(t, c.callHearing(t, 1, "agents/start", map[string]any{"runtimeID": "claude", "cwd": "file://" + t.TempDir(),
		"prompt": "Please ask, then write hi.txt"}, "a card", aCard(&card)), &id)
	c.call(t, 2, "agents/archive", map[string]any{"agentID": id})
	waitFor(t, "the turn over", func() bool {
		l := d.live(id)
		l.mu.Lock()
		defer l.mu.Unlock()
		return !l.inTurn
	})
	time.Sleep(200 * time.Millisecond)
	if a := d.Store.Agent(id); a.State != wire.AgentStateArchived {
		t.Fatalf("%s", a.State)
	}
	if n := strings.Join(names(d), " "); strings.Contains(n, "agent.failed") {
		t.Fatal(n)
	}
}

// A change that sets a state from what it read before the archive (a turn's ending, a stop)
// leaves the session archived (#6619): Save would clear its ArchivedAt, and the session would
// come back. Only an unarchive takes it out.
func TestALateChangeKeepsTheSessionArchived(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	id := startAgent(t, c, 1, t.TempDir(), "hello")
	c.until(t, "finished", ended(id, wire.AgentStateFinished))
	c.drain()
	if err := d.archive(id); err != nil {
		t.Fatal(err)
	}
	if got := d.change(id, func(a *wire.Agent) { a.State = wire.AgentStateFinished }); got != nil {
		t.Fatalf("saved %s", got.State)
	}
	if a := d.Store.Agent(id); a.State != wire.AgentStateArchived || a.ArchivedAt == nil {
		t.Fatalf("%s %v", a.State, a.ArchivedAt)
	}
	// A change that keeps it archived still saves.
	if got := d.change(id, func(a *wire.Agent) { title := "renamed"; a.Title = &title }); got == nil || got.ArchivedAt == nil {
		t.Fatalf("%+v", got)
	}
	if err := d.unarchive(id); err != nil {
		t.Fatal(err)
	}
	if a := d.Store.Agent(id); a.State == wire.AgentStateArchived || a.ArchivedAt != nil {
		t.Fatalf("%s %v", a.State, a.ArchivedAt)
	}
}

// An archive while a turn ends (#6619): whichever comes first, the session stays archived,
// with its archive time.
func TestAnArchiveWhileATurnEndsStaysArchived(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	id := startAgent(t, c, 1, t.TempDir(), "hello")
	c.until(t, "finished", ended(id, wire.AgentStateFinished))
	c.drain()
	for i := range 50 {
		d.change(id, func(a *wire.Agent) { a.State = wire.AgentStateRunning })
		ending := make(chan struct{})
		go func() {
			defer close(ending)
			d.end(d.live(id), wire.AgentStateFinished, wire.EndedReasonEndTurn)
		}()
		if err := d.archive(id); err != nil {
			t.Fatal(err)
		}
		<-ending
		if a := d.Store.Agent(id); a.State != wire.AgentStateArchived || a.ArchivedAt == nil {
			t.Fatalf("round %d: %s %v", i, a.State, a.ArchivedAt)
		}
		if err := d.unarchive(id); err != nil {
			t.Fatal(err)
		}
	}
}
