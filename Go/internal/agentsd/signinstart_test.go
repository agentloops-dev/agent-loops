package agentsd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7609: a new Claude or Codex agent on a server with no sign-in of its own (and no lend)
// starts, its first turn waits, and the first sign-in card shows on it. Signing in from the
// card lets that turn go. Before, the start was refused with "needs a key", so there was no
// agent to show the card on.
func TestANewAgentOnAServerWithNoSignInShowsTheFirstCard(t *testing.T) {
	for _, runtimeID := range []string{"codex", "claude"} {
		t.Run(runtimeID, func(t *testing.T) { firstCardOnANewAgent(t, runtimeID) })
	}
}

func firstCardOnANewAgent(t *testing.T, runtimeID string) {
	fakeRuntimes(t)
	home := fakeSignInTools(t)
	setFor(t, &codexFinishWait, 200*time.Millisecond)
	setFor(t, &claudeCheckTurn, func(context.Context, []string) error { return nil })
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asAServer(t)
	d.SetTunneler(&fakeTunnels{})
	if d.needsSignIn(runtimeID) == nil {
		t.Fatal("the server has a sign-in already")
	}

	var id wire.UUID
	mustUnmarshal(t, c.call(t, 1, "agents/start", map[string]any{"runtimeID": runtimeID,
		"cwd": "file://" + t.TempDir(), "prompt": "hello"}), &id)
	card := theCard(t, d)
	if card.AgentID != id || card.Message == nil {
		t.Fatalf("the card %+v is not the new agent's", card)
	}
	if len(d.pendingElicitations()) != 1 {
		t.Fatalf("%d cards", len(d.pendingElicitations()))
	}
	a := d.Store.Agent(id)
	if len(a.QueuedPrompts) != 1 || a.QueuedPrompts[0].Text != "hello" {
		t.Fatalf("the first turn did not wait: %+v", a.QueuedPrompts)
	}
	// Another kick while it waits: the same card, and the wait said once.
	d.live(id).kick()
	if n := waitingNotes(d, id); n != 1 || len(d.pendingElicitations()) != 1 {
		t.Fatalf("%d waiting notes, %d cards", n, len(d.pendingElicitations()))
	}

	answer := wire.RequestsAnswer{ID: card.ID, Answer: wire.RequestAnswer{Form: &wire.RequestFormAnswer{
		Action: wire.AnswerElicitationRequestActionAccept}}}
	if runtimeID == "claude" {
		answer.Answer.Form.Content = map[string]wire.JSONValue{signInCodeField: wire.JSONValue(`"good"`)}
	} else {
		must(t, os.WriteFile(filepath.Join(home, "entered"), nil, 0o600))
	}
	signInFromTheCard(t, d, answer)
	if d.needsSignIn(runtimeID) != nil {
		t.Fatal("not signed in on the server")
	}
	waitFor(t, "the first turn", func() bool { return len(d.Store.Agent(id).QueuedPrompts) == 0 })
	for _, e := range transcriptOf(d, id) {
		if n := e.Kind.Notice; n != nil && n.Value.Detail != nil && strings.Contains(*n.Value.Detail, "needs a key") {
			t.Fatalf("refused: %+v", n.Value)
		}
	}
}

// signInFromTheCard answers the card until the sign-in it shows is made.
func signInFromTheCard(t *testing.T, d *Daemon, answer wire.RequestsAnswer) {
	t.Helper()
	var err error
	waitFor(t, "signed in", func() bool {
		_, err = d.answerRequest(answer, nil)
		return err == nil || !strings.Contains(err.Error(), "still waiting")
	})
	if err != nil {
		t.Fatal(err)
	}
}

// waitingNotes counts the agent's notes that say its turn waits for a sign-in.
func waitingNotes(d *Daemon, id wire.UUID) int {
	n := 0
	for _, e := range transcriptOf(d, id) {
		if v := e.Kind.Notice; v != nil && strings.Contains(v.Value.Title, "is not signed in on") {
			n++
		}
	}
	return n
}

func transcriptOf(d *Daemon, id wire.UUID) []wire.TranscriptEntry {
	entries, _, _ := d.Store.Transcript(id, 0)
	return entries
}
