package agentsd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The meetings methods end to end (#7581): refused while the flag is off; with it on, a start
// makes the session and the meeting, segments append and page after a seq, the notes'
// revision and the person's edits are kept, and each change is said as meetings/changed.
func TestMeetingsMethodsEndToEnd(t *testing.T) {
	t.Parallel()

	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	folder := t.TempDir()
	startParams := map[string]any{"folder": "file://" + folder, "title": "Planning", "sources": []string{"mic", "call:us.zoom.xos"}, "runtimeID": "claude"}

	if e := c.callError(t, 1, "meetings/start", startParams); e.Code != CodeNotSupported {
		t.Fatalf("%+v", e)
	}
	c.call(t, 2, "preferences/set", map[string]any{meetingFlag: true})

	var started wire.MeetingStarted
	mustUnmarshal(t, c.call(t, 3, "meetings/start", startParams), &started)
	m := started.Meeting
	if m.AgentID != started.AgentID || m.State != wire.MeetingStateListening || m.Title != "Planning" {
		t.Fatalf("%+v", started)
	}
	a := d.Store.Agent(started.AgentID)
	if a == nil || store.FolderPath(a.Cwd) != store.FolderPath(store.FolderURL(folder)) || a.Title == nil || *a.Title != "Planning" {
		t.Fatalf("%+v", a)
	}
	c.until(t, "the starter prompt", func(string, json.RawMessage) bool {
		got := userMessages(d, started.AgentID)
		return len(got) == 1 && strings.Contains(got[0], "meeting_notes") && strings.Contains(got[0], string(m.ID))
	})
	// A source is mic or call:<bundle id>.
	if e := c.callError(t, 4, "meetings/start", map[string]any{"folder": "file://" + folder, "sources": []string{"speaker"}}); e.Code != CodeInvalidParams {
		t.Fatalf("%+v", e)
	}

	segs := []map[string]any{
		{"seq": 1, "source": "mic", "audioStart": 0.0, "audioEnd": 2.5, "text": "Alex will send the deck by Friday.", "speakerIndex": 0, "speakerSettled": false},
		{"seq": 2, "source": "call:us.zoom.xos", "audioStart": 2.6, "audioEnd": 4.0, "text": "Great, thanks.", "speakerIndex": 0, "speakerSettled": true},
	}
	var appended wire.MeetingAppended
	mustUnmarshal(t, c.call(t, 5, "meetings/append", map[string]any{"meetingID": m.ID, "segments": segs}), &appended)
	if appended.LastSeq != 2 || appended.SentThroughSeq != 0 {
		t.Fatalf("%+v", appended)
	}
	c.until(t, "meetings/changed", func(method string, p json.RawMessage) bool {
		var n wire.MeetingChangedNotification
		return method == "meetings/changed" && json.Unmarshal(p, &n) == nil && n.MeetingID == m.ID && n.LastSeq == 2 &&
			n.Meeting != nil && len(n.Meeting.Speakers) == 2
	})
	var page wire.MeetingSegmentsPage
	mustUnmarshal(t, c.call(t, 6, "meetings/segments", map[string]any{"meetingID": m.ID, "afterSeq": 1}), &page)
	if len(page.Segments) != 1 || page.Segments[0].Seq != 2 || page.More {
		t.Fatalf("%+v", page)
	}
	if e := c.callError(t, 7, "meetings/append", map[string]any{"meetingID": m.ID, "segments": []map[string]any{
		{"seq": 4, "source": "mic", "audioStart": 5.0, "audioEnd": 6.0, "text": "a gap", "speakerSettled": false}}}); e.Code != CodeInvalidParams {
		t.Fatalf("%+v", e)
	}

	var named wire.Meeting
	mustUnmarshal(t, c.call(t, 8, "meetings/setSpeaker", map[string]any{"meetingID": m.ID, "source": "call:us.zoom.xos", "index": 0, "name": "Priya"}), &named)
	if len(named.Speakers) != 2 || named.Speakers[1].Name == nil || *named.Speakers[1].Name != "Priya" || named.Speakers[0].Name != nil {
		t.Fatalf("%+v", named.Speakers)
	}

	var notes wire.MeetingNotes
	mustUnmarshal(t, c.call(t, 9, "meetings/setNotes", map[string]any{"meetingID": m.ID, "markdown": "# Planning", "baseRevision": 0}), &notes)
	if notes.Revision != 1 || notes.UpdatedBy != wire.MeetingAuthorPerson {
		t.Fatalf("%+v", notes)
	}
	if e := c.callError(t, 10, "meetings/setNotes", map[string]any{"meetingID": m.ID, "markdown": "# Stale", "baseRevision": 0}); e.Code != wire.FailureChangedElsewhere {
		t.Fatalf("%+v", e)
	}

	meetingItemEdited(t, d, c, m.ID)

	var read wire.MeetingRead
	mustUnmarshal(t, c.call(t, 12, "meetings/read", map[string]any{"meetingID": m.ID}), &read)
	if read.Notes == nil || read.Notes.Markdown != "# Planning" || len(read.ActionItems) != 1 || read.ActionItems[0].Text != "Send the deck to Priya" {
		t.Fatalf("%+v", read)
	}
	var list wire.MeetingPage
	mustUnmarshal(t, c.call(t, 13, "meetings/list", map[string]any{}), &list)
	if len(list.Meetings) != 1 || list.Meetings[0].ID != m.ID {
		t.Fatalf("%+v", list)
	}
	var ended wire.Meeting
	mustUnmarshal(t, c.call(t, 14, "meetings/setState", map[string]any{"meetingID": m.ID, "state": "ended"}), &ended)
	if ended.State != wire.MeetingStateEnded || ended.EndedAt == nil {
		t.Fatalf("%+v", ended)
	}

	// Deleting the session deletes its meeting.
	if err := d.Store.Delete(started.AgentID); err != nil {
		t.Fatal(err)
	}
	if e := c.callError(t, 15, "meetings/read", map[string]any{"meetingID": m.ID}); e.Code != CodeInvalidParams {
		t.Fatalf("%+v", e)
	}
	d.sweepMeetings(nowFunc())
	mustUnmarshal(t, c.call(t, 16, "meetings/list", map[string]any{}), &list)
	if len(list.Meetings) != 0 {
		t.Fatalf("%+v", list)
	}
}

// meetingItemEdited: the agent's item (through the store, as the meeting_notes tool will),
// then the person's edit, which the agent may not undo.
func meetingItemEdited(t *testing.T, d *Daemon, c *conn, id wire.UUID) {
	t.Helper()
	itemID := store.NewUUID()
	agentItem, err := d.meetings().SetActionItem(id, wire.MeetingActionItem{ID: itemID, Text: "Send the deck",
		Quote: "will send the deck by Friday", SegmentSeqs: []int64{1}}, nil, wire.MeetingAuthorAgent, nowFunc())
	if err != nil {
		t.Fatal(err)
	}
	edit := map[string]any{"id": itemID, "meetingID": id, "text": "Send the deck to Priya", "quote": agentItem.Quote,
		"segmentSeqs": []int{1}, "state": "approved", "updatedAt": agentItem.UpdatedAt}
	var edited wire.MeetingActionItem
	mustUnmarshal(t, c.call(t, 11, "meetings/setActionItem", map[string]any{"meetingID": id, "item": edit, "baseUpdatedAt": agentItem.UpdatedAt}), &edited)
	if len(edited.EditedByPerson) != 2 || edited.State != wire.MeetingActionItemStateApproved {
		t.Fatalf("%+v", edited)
	}
	again := edited
	again.Text = "Send the deck"
	_, err = d.meetings().SetActionItem(id, again, nil, wire.MeetingAuthorAgent, nowFunc())
	if e := (*Error)(nil); !errors.As(meetingError(err), &e) || e.Code != CodeNotPermitted {
		t.Fatalf("%v", err)
	}
}
