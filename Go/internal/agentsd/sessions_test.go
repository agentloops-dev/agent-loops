package agentsd

import (
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Ports SessionLookupTests, SessionHistoryTests and AgentMessageTests: the folder's
// sessions listed and read in SessionLookup's words, and a message from one to another
// that wakes it, with the loop guard and the archived refused.

func TestReadingAndMessagingSessions(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	dir := t.TempDir()
	titled := func(title string) func(*wire.Agent) { return func(a *wire.Agent) { a.Title = &title } }
	lead := seeded(t, d, dir, titled("Lead"))
	helper := seeded(t, d, dir, titled("Helper"))
	gone := seeded(t, d, dir, func(a *wire.Agent) {
		title, by, at := "Old", wire.AgentArchivedReasonByUser, now()
		a.Title, a.State, a.ArchivedReason, a.ArchivedAt = &title, wire.AgentStateArchived, &by, &at
	})
	seeded(t, d, t.TempDir(), titled("Elsewhere"))
	person := wire.PromptOriginPerson
	d.record(helper.ID, wire.TranscriptEntryKind{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: "Fix the login", From: &person}})
	id := "m1"
	d.record(helper.ID, wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{MessageID: &id, Text: "I fixed "}})
	d.record(helper.ID, wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{MessageID: &id, Text: "it."}})

	m := appClient(t, d, lead.ID)
	m.post("initialize", map[string]any{}, 1)
	text, _ := m.tool(2, "list_sessions", map[string]any{"limit": 2})
	if !strings.HasPrefix(text, "Sessions in this folder, not archived first, most recent first.") ||
		!strings.Contains(text, string(lead.ID)+": “Lead” (you) — Claude, Done, last active ") ||
		strings.Contains(text, "Elsewhere") || strings.Contains(text, string(gone.ID)) ||
		!strings.Contains(text, "\n\n1 more, all archived. For the next page, call list_sessions with after: \"") {
		t.Fatal(text)
	}
	text, _ = m.tool(3, "read_session", map[string]any{"session": "Helper"})
	if !strings.HasPrefix(text, "# Session “Helper”\n\n- Id: "+string(helper.ID)+"\n- Runtime: Claude\n- Status: Done\n") ||
		!strings.Contains(text, "## The conversation\n\n**The person:** Fix the login\n**Claude:** I fixed it.\n") {
		t.Fatal(text)
	}
	if text, _ := m.tool(4, "read_session", map[string]any{"session": "Nobody"}); text != "There is no session named “Nobody” in this folder." {
		t.Fatal(text)
	}
	if text, isErr := m.tool(5, "message_session", map[string]any{"to": "Old", "message": "hi"}); !isErr || text != "Nothing was sent: “Old” is archived. Only the person can unarchive it." {
		t.Fatal(text)
	}
	if text, isErr := m.tool(6, "message_session", map[string]any{"to": "Lead", "message": "hi"}); !isErr || text != "Nothing was sent: that is this session." {
		t.Fatal(text)
	}
	text, _ = m.tool(7, "message_session", map[string]any{"to": string(helper.ID), "message": "Run the tests too"})
	if text != "Sent to “Helper”, which is working on it now. A reply, if it sends one, comes to this session as a message."+queuedMessageNote {
		t.Fatal(text)
	}
	waitFor(t, "the message in its chat", func() bool {
		u := userMessages(d, helper.ID)
		return u[len(u)-1] == "Run the tests too"
	})
	entries, _, _ := d.Store.Transcript(helper.ID, 0)
	var sender *wire.MessageSender
	for _, e := range entries {
		if e.Kind.UserMessage != nil && e.Kind.UserMessage.Value == "Run the tests too" {
			sender = e.Sender
		}
	}
	if sender == nil || sender.Title != "Lead" {
		t.Fatalf("%+v", sender)
	}
	if n := names(d); !strings.Contains(strings.Join(n, " "), "agent.messaged") {
		t.Fatal(n)
	}
	waitFor(t, "its turn over", func() bool { return d.Store.Agent(helper.ID).State == wire.AgentStateFinished })
	// Once the target's turn has taken the message, the sender may send the next one.
	text, isErr := m.tool(8, "message_session", map[string]any{"to": "Helper", "message": "again"})
	if isErr || !strings.HasPrefix(text, "Sent to “Helper”") {
		t.Fatal(text)
	}
}

// checkToolAnswer fails the test unless the tool's answer is want, and is an error
// only when wantErr is true.
func checkToolAnswer(t *testing.T, text string, isErr, wantErr bool, want string) {
	t.Helper()
	if isErr != wantErr || text != want {
		t.Fatal(text)
	}
}

// checkToolAnswerPrefix fails the test unless the tool's answer is no error and starts with prefix.
func checkToolAnswerPrefix(t *testing.T, text string, isErr bool, prefix string) {
	t.Helper()
	if isErr || !strings.HasPrefix(text, prefix) {
		t.Fatal(text)
	}
}

// checkReplacedQueuedPrompt fails the test unless got has one prompt with the text,
// and that prompt keeps the id, the queue time and the preface of the first one.
func checkReplacedQueuedPrompt(t *testing.T, got []wire.QueuedPrompt, text string, id wire.UUID, queuedAt wire.Date, preface *string) {
	t.Helper()
	if len(got) != 1 || got[0].Text != text || got[0].ID != id || got[0].QueuedAt != queuedAt {
		t.Fatalf("%+v", got)
	}
	if (preface == nil) != (got[0].Preface == nil) || (preface != nil && got[0].Preface != nil && *preface != *got[0].Preface) {
		t.Fatalf("preface %+v %+v", preface, got[0].Preface)
	}
}

// messagesSentCount returns how many messages the sender has sent, under the lock.
func messagesSentCount(d *Daemon, sender wire.UUID) int {
	d.messageMu.Lock()
	defer d.messageMu.Unlock()
	return len(d.messagesSent[sender])
}

// hasQueuedPromptWithText reports whether the queue has the prompt with this id and text.
func hasQueuedPromptWithText(queued []wire.QueuedPrompt, id wire.UUID, text string) bool {
	for _, q := range queued {
		if q.ID == id && q.Text == text {
			return true
		}
	}
	return false
}

// One queued message_session per sender and target: a second send is refused, replace
// swaps the text in place, cancel takes it back, and a message to a stopped session still counts (#2006).
func TestOneQueuedMessagePerTarget(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	c.drain()
	dir := t.TempDir()
	lead := seeded(t, d, dir, titledAs("Lead", inMode("default")))
	other := seeded(t, d, dir, titledAs("Other", inMode("default")))
	helper := seeded(t, d, dir, titledAs("Helper", func(a *wire.Agent) {
		a.State = wire.AgentStateStopped
	}))
	peer := seeded(t, d, dir, titledAs("Peer", func(a *wire.Agent) {
		a.State = wire.AgentStateStopped
	}))
	seeded(t, d, dir, titledAs("Wake"))

	m := appClient(t, d, lead.ID)
	m.post("initialize", map[string]any{}, 1)
	o := appClient(t, d, other.ID)
	o.post("initialize", map[string]any{}, 1)

	text, isErr := m.tool(2, "message_session", map[string]any{"to": "Helper", "message": "first\nwords"})
	if isErr || text != "Sent to “Helper”, which is stopped, so it waits in that chat until it is started again; a message does not start a stopped session."+queuedMessageNote {
		t.Fatal(text)
	}
	first := d.Store.Agent(helper.ID).QueuedPrompts
	if len(first) != 1 || first[0].Text != "first\nwords" {
		t.Fatalf("%+v", first)
	}
	id, queuedAt, preface := first[0].ID, first[0].QueuedAt, first[0].Preface

	text, isErr = m.tool(3, "message_session", map[string]any{"to": "Helper", "message": "second"})
	checkToolAnswer(t, text, isErr, true, "Nothing was sent: you already have a message waiting for “Helper”: “first words”. Replace it with message_session and replace: true, or take it back with cancel_message.")

	text, isErr = m.tool(4, "message_session", map[string]any{"to": "Helper", "message": "replaced", "replace": true})
	checkToolAnswer(t, text, isErr, false, "Replaced the waiting message to “Helper”.")
	checkReplacedQueuedPrompt(t, d.Store.Agent(helper.ID).QueuedPrompts, "replaced", id, queuedAt, preface)

	listed, _ := m.tool(5, "list_sessions", map[string]any{})
	if !strings.Contains(listed, "Your message waits: replaced.") {
		t.Fatal(listed)
	}
	read, _ := m.tool(6, "read_session", map[string]any{"session": "Helper"})
	if !strings.Contains(read, "- Your message waits: replaced\n") {
		t.Fatal(read)
	}

	text, isErr = m.tool(7, "message_session", map[string]any{"to": "Peer", "message": "other target"})
	checkToolAnswerPrefix(t, text, isErr, "Sent to “Peer”")
	if waitingFrom(d.Store.Agent(peer.ID), lead.ID) == nil || waitingFrom(d.Store.Agent(helper.ID), lead.ID) == nil {
		t.Fatal("wanted one waiting on each target")
	}

	text, isErr = o.tool(2, "message_session", map[string]any{"to": "Helper", "message": "from other"})
	checkToolAnswerPrefix(t, text, isErr, "Sent to “Helper”")
	if waitingFrom(d.Store.Agent(helper.ID), other.ID) == nil || waitingFrom(d.Store.Agent(helper.ID), lead.ID) == nil {
		t.Fatal("wanted one waiting from each sender")
	}

	person := wire.QueuedPrompt{ID: store.NewUUID(), Text: "from the person", QueuedAt: now(), From: wire.PromptOriginPerson, Attachments: []wire.Attachment{}}
	d.change(helper.ID, func(a *wire.Agent) {
		a.QueuedPrompts = append(append([]wire.QueuedPrompt{}, a.QueuedPrompts...), person)
	})
	sent := messagesSentCount(d, lead.ID)
	text, isErr = m.tool(8, "message_session", map[string]any{"to": "Helper", "message": "again", "replace": true})
	checkToolAnswer(t, text, isErr, false, "Replaced the waiting message to “Helper”.")
	afterReplace := messagesSentCount(d, lead.ID)
	if afterReplace != sent {
		t.Fatalf("replace counted as a send: %d -> %d", sent, afterReplace)
	}

	text, _ = m.tool(9, "cancel_message", map[string]any{"session": "Helper"})
	if text != "Took back the waiting message to “Helper”." {
		t.Fatal(text)
	}
	if waitingFrom(d.Store.Agent(helper.ID), lead.ID) != nil {
		t.Fatal("lead's message still queued")
	}
	if waitingFrom(d.Store.Agent(helper.ID), other.ID) == nil {
		t.Fatal("other's message was removed")
	}
	if !hasQueuedPromptWithText(d.Store.Agent(helper.ID).QueuedPrompts, person.ID, "from the person") {
		t.Fatal("the person's queued prompt was removed")
	}

	text, _ = m.tool(10, "cancel_message", map[string]any{"session": "Helper"})
	if text != "Nothing is waiting for “Helper”; it has already been read, or nothing was sent." {
		t.Fatal(text)
	}
	text, isErr = m.tool(11, "message_session", map[string]any{"to": "Helper", "message": "fresh", "replace": true})
	checkToolAnswer(t, text, isErr, true, "Nothing was sent: nothing is waiting for “Helper”. Send a new message with message_session.")

	text, isErr = m.tool(12, "message_session", map[string]any{"to": "Wake", "message": "go"})
	checkToolAnswerPrefix(t, text, isErr, "Sent to “Wake”")
}
