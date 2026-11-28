package agentsd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// listsTool says whether tools/list offers the agent a tool by name.
func listsTool(t *testing.T, m *mcpClient, id int, name string) bool {
	t.Helper()
	_, list := m.post("tools/list", nil, id)
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	mustUnmarshal(t, list["result"], &tools)
	for _, tool := range tools.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// #7583: meeting_notes is offered, briefed and served only in a meeting's session with the
// flag on; read gives the meeting and its words as the prompts' lines; write_notes,
// add_actions, update_action and set_speaker keep their changes, say them as meetings/changed,
// and refuse what the data model refuses.
func TestMeetingNotesToolKeepsTheMeetingsNotes(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	folder := t.TempDir()
	c.call(t, 1, "preferences/set", map[string]any{meetingFlag: true})
	var started wire.MeetingStarted
	mustUnmarshal(t, c.call(t, 2, "meetings/start", map[string]any{"folder": "file://" + folder, "title": "Planning",
		"sources": []string{"mic", "call:us.zoom.xos"}, "runtimeID": "claude"}), &started)
	id := started.Meeting.ID
	segs := []map[string]any{
		{"seq": 1, "source": "mic", "audioStart": 0.0, "audioEnd": 2.5, "text": "Alex will send the deck by Friday.", "speakerIndex": 0, "speakerSettled": true},
		{"seq": 2, "source": "call:us.zoom.xos", "audioStart": 2.6, "audioEnd": 4.0, "text": "Great, thanks.", "speakerIndex": 0, "speakerSettled": true},
	}
	c.call(t, 3, "meetings/append", map[string]any{"meetingID": id, "segments": segs})

	// The runtime's own start takes the tokens before it: the test's comes after it.
	c.until(t, "the starter prompt", func(string, json.RawMessage) bool { return len(userMessages(d, started.AgentID)) == 1 })

	m := meetingNotesGate(t, d, folder, started.AgentID)
	meetingNotesReadAndWrite(t, c, m)
	meetingNotesActions(t, d, m, id)
	meetingNotesSpeakers(t, m)

	// With the flag off, the meeting's session has the tool no more.
	c.call(t, 4, "preferences/set", map[string]any{meetingFlag: false})
	if listsTool(t, m, 19, meetingNotesName) || strings.Contains(d.brief(d.Store.Agent(started.AgentID)), meetingNotesBrief) {
		t.Error("meeting_notes is offered with meeting mode off")
	}
	if _, isErr := m.tool(20, meetingNotesName, map[string]any{"action": "read"}); !isErr {
		t.Error("meeting_notes answered with meeting mode off")
	}
}

// meetingNotesGate: the meeting's session has the tool and its brief line; another has neither.
func meetingNotesGate(t *testing.T, d *Daemon, folder string, agent wire.UUID) *mcpClient {
	t.Helper()
	m := appClient(t, d, agent)
	m.post("initialize", map[string]any{}, 1)
	if !listsTool(t, m, 2, meetingNotesName) {
		t.Fatal("the meeting's session is not offered meeting_notes")
	}
	if b := d.brief(d.Store.Agent(agent)); !strings.Contains(b, meetingNotesBrief) {
		t.Error("the meeting's brief does not name meeting_notes")
	}
	other := seeded(t, d, folder, nil)
	o := appClient(t, d, other.ID)
	o.post("initialize", map[string]any{}, 1)
	if listsTool(t, o, 2, meetingNotesName) || strings.Contains(d.brief(d.Store.Agent(other.ID)), meetingNotesBrief) {
		t.Error("a session with no meeting is offered meeting_notes")
	}
	if text, isErr := o.tool(3, meetingNotesName, map[string]any{"action": "read"}); !isErr || !strings.Contains(text, "only for a meeting's session") {
		t.Errorf("%v %q", isErr, text)
	}

	return m
}

// meetingNotesReadAndWrite: read, then write_notes and its refusals.
func meetingNotesReadAndWrite(t *testing.T, c *conn, m *mcpClient) {
	t.Helper()
	// read: the meeting, its speakers by number, and its words as the prompts give them.
	text, isErr := m.tool(3, meetingNotesName, map[string]any{"action": "read"})
	if isErr || !strings.Contains(text, `"[1] Speaker 1: Alex will send the deck by Friday."`) ||
		!strings.Contains(text, `"[2] Speaker 2: Great, thanks."`) || !strings.Contains(text, `"title": "Planning"`) {
		t.Fatalf("%v %s", isErr, text)
	}
	if text, _ := m.tool(4, meetingNotesName, map[string]any{"action": "read", "afterSeq": 1}); strings.Contains(text, "[1]") || !strings.Contains(text, "[2]") {
		t.Errorf("read after seq 1: %s", text)
	}

	// write_notes: over the revision read, and never over a stale one or 64 KB.
	if text, isErr := m.tool(5, meetingNotesName, map[string]any{"action": "write_notes", "markdown": "# Planning", "baseRevision": 0}); isErr || !strings.Contains(text, "revision 1") {
		t.Fatalf("%v %q", isErr, text)
	}
	c.until(t, "the notes said", func(method string, p json.RawMessage) bool {
		var n wire.MeetingChangedNotification
		return method == "meetings/changed" && json.Unmarshal(p, &n) == nil && n.Notes != nil && n.Notes.UpdatedBy == wire.MeetingAuthorAgent
	})
	if text, isErr := m.tool(6, meetingNotesName, map[string]any{"action": "write_notes", "markdown": "# Stale", "baseRevision": 0}); !isErr || !strings.Contains(text, "read them again") {
		t.Errorf("%v %q", isErr, text)
	}
	if text, isErr := m.tool(7, meetingNotesName, map[string]any{"action": "write_notes", "markdown": strings.Repeat("x", 65<<10), "baseRevision": 1}); !isErr || !strings.Contains(text, "make them shorter") {
		t.Errorf("%v %q", isErr, text)
	}

}

// meetingNotesActions: add_actions and update_action, and their refusals.
func meetingNotesActions(t *testing.T, d *Daemon, m *mcpClient, id wire.UUID) {
	t.Helper()
	// add_actions: each item against its quote; a refused one leaves the others kept.
	text, isErr := m.tool(8, meetingNotesName, map[string]any{"action": "add_actions", "items": []map[string]any{
		{"text": "Send the deck", "owner": "Alex", "due": "by Friday", "quote": "send the deck by Friday", "segmentSeqs": []int{1}},
		{"text": "Send the slides", "quote": "send the slides", "segmentSeqs": []int{1}},
	}})
	if isErr || !strings.Contains(text, "Item 1 (\"Send the deck\") is action item") || !strings.Contains(text, "Item 2 (\"Send the slides\") was refused: the quote is not word for word") {
		t.Fatalf("%v %q", isErr, text)
	}
	if text, isErr := m.tool(9, meetingNotesName, map[string]any{"action": "add_actions", "items": []map[string]any{
		{"text": "Book a room", "quote": "book a room", "segmentSeqs": []int{2}}}}); !isErr || !strings.Contains(text, "was refused") {
		t.Errorf("%v %q", isErr, text)
	}
	items, _ := d.meetings().ActionItems(id)
	if len(items) != 1 || items[0].State != wire.MeetingActionItemStateProposed || *items[0].Owner != "Alex" {
		t.Fatalf("%+v", items)
	}
	item := items[0]

	// update_action: the agent's own change, then none to a field the person edited.
	if text, isErr := m.tool(10, meetingNotesName, map[string]any{"action": "update_action", "id": item.ID, "due": "by Monday"}); isErr || !strings.Contains(text, "is proposed") {
		t.Fatalf("%v %q", isErr, text)
	}
	items, _ = d.meetings().ActionItems(id)
	edited := items[0]
	edited.Text = "Send the deck to Priya"
	edited.State = wire.MeetingActionItemStateApproved
	if _, err := d.meetings().SetActionItem(id, edited, &items[0].UpdatedAt, wire.MeetingAuthorPerson, nowFunc()); err != nil {
		t.Fatal(err)
	}
	if text, isErr := m.tool(11, meetingNotesName, map[string]any{"action": "update_action", "id": item.ID, "text": "Send it"}); !isErr || !strings.Contains(text, "a person edited the text") {
		t.Errorf("%v %q", isErr, text)
	}
	if text, isErr := m.tool(12, meetingNotesName, map[string]any{"action": "update_action", "id": item.ID, "state": "dismissed"}); !isErr || !strings.Contains(text, "a person edited the state") {
		t.Errorf("%v %q", isErr, text)
	}
	// The one change of the person's state the agent makes: approved to filed, with its issue.
	issue := map[string]any{"repo": "acme/app", "number": 12, "url": "https://github.com/acme/app/issues/12"}
	if text, isErr := m.tool(13, meetingNotesName, map[string]any{"action": "update_action", "id": item.ID, "issue": issue}); isErr || !strings.Contains(text, "is filed") {
		t.Fatalf("%v %q", isErr, text)
	}
	items, _ = d.meetings().ActionItems(id)
	if got := items[0]; got.Issue == nil || got.Issue.Forge != "github" || got.Issue.Number != 12 || got.Text != "Send the deck to Priya" {
		t.Errorf("%+v", got)
	}
	if text, isErr := m.tool(14, meetingNotesName, map[string]any{"action": "update_action", "id": "nope", "owner": "Sam"}); !isErr || !strings.Contains(text, "no action item nope") {
		t.Errorf("%v %q", isErr, text)
	}

}

// meetingNotesSpeakers: set_speaker, and an action that is none.
func meetingNotesSpeakers(t *testing.T, m *mcpClient) {
	t.Helper()
	// set_speaker: by the n of "Speaker <n>", for the whole meeting.
	if text, isErr := m.tool(15, meetingNotesName, map[string]any{"action": "set_speaker", "index": 2, "name": "Priya"}); isErr || text != "Speaker 2 is Priya." {
		t.Fatalf("%v %q", isErr, text)
	}
	if text, _ := m.tool(16, meetingNotesName, map[string]any{"action": "read", "afterSeq": 1}); !strings.Contains(text, "[2] Speaker 2 (Priya): Great, thanks.") {
		t.Errorf("%s", text)
	}
	if text, isErr := m.tool(17, meetingNotesName, map[string]any{"action": "set_speaker", "index": 3, "name": "Sam"}); !isErr || !strings.Contains(text, "no Speaker 3") {
		t.Errorf("%v %q", isErr, text)
	}
	if text, isErr := m.tool(18, meetingNotesName, map[string]any{"action": "summarise"}); !isErr || !strings.Contains(text, "not \"summarise\"") {
		t.Errorf("%v %q", isErr, text)
	}

}
