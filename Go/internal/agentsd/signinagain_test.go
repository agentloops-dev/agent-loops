package agentsd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7472: a server's own Claude sign-in that ends (revoked or expired) stops the turn saying
// so, not with a general error, and shows one Sign in again card; signing in again from it
// makes the runtime signed in again.
func TestAServersEndedSignInShowsSignInAgain(t *testing.T) {
	fakeRuntimes(t)
	fakeSignInTools(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asAServer(t)
	must(t, d.keepClaudeSignIn("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-revoked"))

	id := startAgent(t, c, 1, t.TempDir(), "signed out")
	c.until(t, "stopped", ended(id, wire.AgentStateStopped))
	if a := d.Store.Agent(id); a.EndedReason == nil || *a.EndedReason != wire.EndedReasonSignInRefused {
		t.Fatalf("ended %+v", a.EndedReason)
	}
	entries, _, _ := d.Store.Transcript(id, 0)
	var said []string
	for _, e := range entries {
		if n := e.Kind.Notice; n != nil {
			said = append(said, n.Value.Title)
		}
		if e.Kind.RuntimeNote != nil {
			said = append(said, e.Kind.RuntimeNote.Value)
		}
	}
	want := signedOutTitle("Claude", d.serverName())
	if !strings.Contains(strings.Join(said, "\n"), want) || strings.Contains(strings.Join(said, "\n"), "Sign in to continue") ||
		strings.Contains(strings.Join(said, "\n"), "The turn failed") {
		t.Fatalf("said %q", said)
	}
	card := theCard(t, d)
	if card.Mode.Form == nil || card.Mode.Form.Value.Title == nil || *card.Mode.Form.Value.Title != want ||
		card.Message == nil || !strings.Contains(*card.Message, "sign in again") || !strings.Contains(*card.Message, "https://claude.ai/oauth/authorize") {
		t.Fatalf("%+v", card)
	}
	if s := statusFor(t, c, 2, "claude"); s.SignIn == nil || s.SignIn.SignedIn {
		t.Fatalf("status says signed in: %+v", s.SignIn)
	}
	// A second turn that fails the same way: still the one card.
	c.call(t, 3, "agents/prompt", map[string]any{"agentID": id, "text": "signed out", "from": "person"})
	waitFor(t, "stopped again", func() bool {
		users, _ := saidIn(d, id)
		return len(users) == 2 && d.Store.Agent(id).State == wire.AgentStateStopped
	})
	// The person's message does not close it: it is the daemon's card, not the agent's question.
	waitFor(t, "no card being raised", func() bool {
		d.headlessMu.Lock()
		defer d.headlessMu.Unlock()
		return len(d.signInCards.raising) == 0
	})
	if got := d.pendingElicitations(); len(got) != 1 || got[0].ID != card.ID {
		t.Fatalf("%d cards", len(got))
	}

	// Sign in again: the same sign-in as the first, from the card.
	if _, err := d.answerRequest(wire.RequestsAnswer{ID: card.ID, Answer: wire.RequestAnswer{Form: &wire.RequestFormAnswer{
		Action: wire.AnswerElicitationRequestActionAccept, Content: map[string]wire.JSONValue{signInCodeField: wire.JSONValue(`"good"`)}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if d.signedOutHere("claude") || d.storedClaudeSignIn() != "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-abc_DEF-123" {
		t.Fatalf("not signed in again: %q", d.storedClaudeSignIn())
	}
	if s := statusFor(t, c, 4, "claude"); s.SignIn == nil || !s.SignIn.SignedIn {
		t.Fatalf("status: %+v", s.SignIn)
	}
}

// An agent on the Mac's lend, or on an added account, is not this server's own sign-in.
func TestAnEndedLendIsNotSignInAgain(t *testing.T) {
	fakeRuntimes(t)
	fakeSignInTools(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asAServer(t)
	id := startAgent(t, c, 1, t.TempDir(), "")
	c.until(t, "finished", ended(id, wire.AgentStateFinished))
	if d.ownSignInEnded(d.Store.Agent(id)) {
		t.Fatal("no sign-in of its own, and it said it ended")
	}
	must(t, d.keepClaudeSignIn("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-x"))
	a := *d.Store.Agent(id)
	account := "acct"
	a.AccountID = &account
	if d.ownSignInEnded(&a) {
		t.Fatal("an added account's sign-in is its own")
	}
	if d.signedOutHere("claude") {
		t.Fatal("marked signed out")
	}
}

// Claude's token lasts a year and does not renew (spec 7469 Q1): 30 days before it ends the
// card asks for a new one.
func TestClaudeAsksToSignInAgainBeforeItsTokenEnds(t *testing.T) {
	fakeRuntimes(t)
	fakeSignInTools(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	asAServer(t)
	id := startAgent(t, c, 1, t.TempDir(), "")
	must(t, d.keepClaudeSignIn("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-x"))
	d.raiseSignInCard(id, "claude") // returns at once, raising nothing
	if len(d.pendingElicitations()) != 0 {
		t.Fatal("a card for a new token")
	}
	old := time.Now().Add(-340 * 24 * time.Hour)
	must(t, os.Chtimes(d.claudeSignInFile(), old, old))
	d.raiseSignInCard(id, "claude")
	card := theCard(t, d)
	if card.Message == nil || !strings.Contains(*card.Message, "ends on "+old.Add(claudeTokenLife).Format("2 January 2006")) {
		t.Fatalf("%+v", card)
	}
}

// After a restart the server is still signed out, and the card left from before it (its
// sign-in gone with the daemon) is closed when the new one opens.
func TestSignInAgainOutlivesARestart(t *testing.T) {
	fakeRuntimes(t)
	fakeSignInTools(t)
	root := t.TempDir()
	d, c := start(t, root)
	asAServer(t)
	must(t, d.keepClaudeSignIn("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-revoked"))
	id := startAgent(t, c, 1, t.TempDir(), "signed out")
	c.until(t, "stopped", ended(id, wire.AgentStateStopped))
	old := theCard(t, d)
	if old.ElicitationID == nil || *old.ElicitationID != signInElicitation("claude") {
		t.Fatalf("%+v", old)
	}
	d.Shutdown()

	d, _ = start(t, root)
	defer d.Shutdown()
	asAServer(t) // a start on a Mac sets the Mac's tools home again
	if !d.signedOutHere("claude") {
		t.Fatal("the mark went with the restart")
	}
	d.raiseSignInCard(id, "claude")
	waitFor(t, "the new card in the old one's place", func() bool {
		got := d.pendingElicitations()
		return len(got) == 1 && got[0].ID != old.ID
	})
	// The chat says the old card was replaced, so it does not draw it as open (#4990).
	waitFor(t, "the old card answered in the chat", func() bool {
		entries, _, _ := d.Store.Transcript(id, 0)
		for _, e := range entries {
			if a := e.Kind.ElicitationAnswered; a != nil && a.ID == old.ID && a.Summary == signInCardReplaced {
				return true
			}
		}
		return false
	})
	if got := d.pendingElicitations()[0]; got.Mode.Form == nil || got.Mode.Form.Value.Title == nil ||
		*got.Mode.Form.Value.Title != signedOutTitle("Claude", d.serverName()) {
		t.Fatalf("%+v", got)
	}
}

// #7608: the card names the server as the clients show it, the name in the person's lock,
// not the control plane's name that the join code carried.
func TestTheCardNamesTheServerAsTheClientsDo(t *testing.T) {
	d, key, _ := joinedMachine(t)
	defer d.Shutdown()
	must(t, (&controllink.Membership{Host: "h-1", Addresses: []string{}, Name: "run-scratch"}).Save(d.Store.Loc.Root))
	host, _ := os.Hostname()
	if got := d.serverName(); got != host {
		t.Fatalf("with no lock: %q, not the host name %q", got, host)
	}
	mac := newLockKey(t)
	root := lockEntry(t, mac, wire.LockEntryKindRoot, mac, nil)
	h := machinelock.HashOf(root)
	id, name := "h-1", "devbox-1"
	add, err := machinelock.Sign(mac, wire.LockBody{Kind: wire.LockEntryKindAddMachine, Person: "sam", Previous: h[:],
		At: wire.DateOf(time.Now()), Key: key, SealingKey: key, ID: &id, Name: &name})
	must(t, err)
	d.TakeLock(wire.Lock{Person: "sam", Entries: []wire.LockEntry{root, add}})
	if got := d.serverName(); got != name {
		t.Fatalf("%q, not the lock's %q", got, name)
	}
}
