package agentsd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A message wakes a session whatever its mode or sandbox and the sender's (#6950). A session another session started takes messages only from it.

func inMode(mode string) func(*wire.Agent) {
	return func(a *wire.Agent) {
		cat := "mode"
		v, _ := json.Marshal(mode)
		a.AdvertisedOptions = []wire.ConfigOption{{ID: "mode", Name: "Mode", Type: "select", Category: &cat, CurrentValue: v}}
	}
}

func titledAs(title string, edits ...func(*wire.Agent)) func(*wire.Agent) {
	return func(a *wire.Agent) {
		a.Title = &title
		for _, e := range edits {
			e(a)
		}
	}
}

func TestMessageWakesASessionInAnyMode(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	// Its many turns would fill what the window is told.
	c.drain()
	dir := t.TempDir()
	sandboxed := func(a *wire.Agent) {
		a.EffectiveSandbox = &wire.EffectiveSandbox{State: wire.SandboxStateOn, Requested: wire.SandboxChoiceOn}
	}
	lead := seeded(t, d, dir, titledAs("Lead", inMode("default")))
	loose := seeded(t, d, dir, titledAs("Loose", inMode("bypassPermissions"), func(a *wire.Agent) {
		wf := "nightly"
		a.StartedByWorkflow = &wf
	}))
	strict := seeded(t, d, dir, titledAs("Strict", inMode("plan")))
	build := seeded(t, d, dir, titledAs("Build", inMode("build")))
	builder := seeded(t, d, dir, titledAs("Builder", inMode("build")))
	boxed := seeded(t, d, dir, titledAs("Boxed", sandboxed))
	open := seeded(t, d, dir, titledAs("Open"))
	manager := seeded(t, d, dir, titledAs("Manager"))
	helper := seeded(t, d, dir, titledAs("Helper", func(a *wire.Agent) { a.StartedByAgent = &manager.ID }))
	sibling := seeded(t, d, dir, titledAs("Sibling", func(a *wire.Agent) { a.StartedByAgent = &manager.ID }))
	peer := seeded(t, d, dir, titledAs("Peer"))

	woken := func(c *mcpClient, n int, to string, target wire.UUID) {
		t.Helper()
		text, isErr := c.tool(n, "message_session", map[string]any{"to": to, "message": "do the work"})
		if isErr || text != "Sent to “"+to+"”, which is working on it now. A reply, if it sends one, comes to this session as a message."+queuedMessageNote {
			t.Fatal(text)
		}
		waitFor(t, "the message in "+to+"'s chat", func() bool {
			u := userMessages(d, target)
			return len(u) > 0 && u[len(u)-1] == "do the work"
		})
		waitFor(t, to+"'s turn over", func() bool { return d.Store.Agent(target).State == wire.AgentStateFinished })
	}

	m := appClient(t, d, lead.ID)
	m.post("initialize", map[string]any{}, 1)
	// A looser mode, a stricter one and one with no rank.
	woken(m, 2, "Loose", loose.ID)
	woken(m, 3, "Strict", strict.ID)
	woken(m, 5, "Build", build.ID)

	// A sandbox not on, while the sender's is.
	b := appClient(t, d, boxed.ID)
	b.post("initialize", map[string]any{}, 1)
	woken(b, 2, "Open", open.ID)

	// A helper wakes its own starter (#1695), and a session that has started none.
	h := appClient(t, d, helper.ID)
	h.post("initialize", map[string]any{}, 1)
	woken(h, 2, "Manager", manager.ID)
	woken(h, 3, "Peer", peer.ID)

	// Another session's helper, a sibling too, is refused, and nothing is queued.
	refused := func(c *mcpClient, n int, to string, target wire.UUID) {
		t.Helper()
		text, isErr := c.tool(n, "message_session", map[string]any{"to": to, "message": "do the work"})
		if !isErr || text != "Nothing was sent: “"+to+"” was started by “Manager”, and a session started by another session takes messages only from it. Send this to “Manager”." {
			t.Fatal(text)
		}
		if a := d.Store.Agent(target); len(a.QueuedPrompts) != 0 || len(userMessages(d, target)) != 0 {
			t.Fatalf("%+v", a)
		}
	}
	refused(m, 4, "Helper", helper.ID)
	refused(h, 4, "Sibling", sibling.ID)

	// A sender with no rank wakes the lead too. It has run no turn: the warm pool takes back
	// the token of a session it releases. The lead's goes the same way, so this is last.
	u := appClient(t, d, builder.ID)
	u.post("initialize", map[string]any{}, 1)
	woken(u, 2, "Lead", lead.ID)

	// A message an older build held, its sender gone since, goes with the next kick.
	gone := wire.MessageSender{AgentID: store.NewUUID(), Title: "Gone"}
	d.change(loose.ID, func(a *wire.Agent) {
		a.QueuedPrompts = []wire.QueuedPrompt{{ID: store.NewUUID(), Text: "held before", From: wire.PromptOriginAgent, Sender: &gone, Attachments: []wire.Attachment{}}}
	})
	d.live(loose.ID).kick()
	waitFor(t, "the held message in Loose's chat", func() bool {
		return strings.Join(userMessages(d, loose.ID), "|") == "do the work|held before"
	})
}
