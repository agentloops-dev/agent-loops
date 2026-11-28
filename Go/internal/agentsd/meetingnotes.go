package agentsd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The meeting's agent's own tool, meeting_notes (#7583, epic #7327): it reads the meeting and
// keeps its notes, its action items and its speakers' names. Only a meeting's session has it,
// and only while the flag meeting-mode is on: tools/list, the brief and a call are gated
// alike. Each change says meetings/changed, as the person's own changes do.

const meetingNotesName = "meeting_notes"

// meetingActionsAdded is the most items one add_actions takes.
const meetingActionsAdded = 20

// meetingOfAgent is the meeting whose session agent is, while the flag is on. A meeting being
// started is waited for, so the brief and tools/list of its session, which can come before the
// meeting is kept, see it.
func (d *Daemon) meetingOfAgent(agent wire.UUID) (wire.Meeting, bool) {
	if !d.meetingModeOn() {
		return wire.Meeting{}, false
	}
	meetings := d.meetings()
	d.meetingStartMu.RLock()
	defer d.meetingStartMu.RUnlock()
	m, ok, err := meetings.MeetingOf(agent)
	return m, ok && err == nil
}

// meetingNotesTool is meeting_notes's entry of the app's tools, for tools/list.
func meetingNotesTool() (json.RawMessage, bool) {
	var all []map[string]json.RawMessage
	if json.Unmarshal(wire.AppTools, &all) != nil {
		return nil, false
	}
	for _, t := range all {
		var name string
		if json.Unmarshal(t["name"], &name) == nil && name == meetingNotesName {
			b, err := json.Marshal(t)
			return b, err == nil
		}
	}
	return nil, false
}

// meetingNotesArgs is every argument of meeting_notes; each action reads its own.
type meetingNotesArgs struct {
	Action       string                       `json:"action"`
	AfterSeq     *int64                       `json:"afterSeq"`
	Markdown     *string                      `json:"markdown"`
	BaseRevision *int64                       `json:"baseRevision"`
	Items        []meetingNewAction           `json:"items"`
	ID           string                       `json:"id"`
	State        *wire.MeetingActionItemState `json:"state"`
	Issue        *wire.MeetingActionItemIssue `json:"issue"`
	Text         *string                      `json:"text"`
	Owner        *string                      `json:"owner"`
	Due          *string                      `json:"due"`
	Index        *int64                       `json:"index"`
	Name         *string                      `json:"name"`
}

type meetingNewAction struct {
	Text        string  `json:"text"`
	Owner       *string `json:"owner"`
	Due         *string `json:"due"`
	Quote       string  `json:"quote"`
	SegmentSeqs []int64 `json:"segmentSeqs"`
}

func (d *Daemon) meetingNotes(agent wire.UUID, args map[string]json.RawMessage) (string, error) {
	m, ok := d.meetingOfAgent(agent)
	if !ok {
		return "", Invalid("meeting_notes is only for a meeting's session, with meeting mode on.")
	}
	raw, _ := json.Marshal(args) // a map of raw values always marshals
	var a meetingNotesArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", Invalid("meeting_notes: %v", err)
	}
	var note string
	var err error
	switch a.Action {
	case "read":
		note, err = d.meetingNotesRead(m, a)
	case "write_notes":
		note, err = d.meetingNotesWrite(m, a)
	case "add_actions":
		note, err = d.meetingNotesAdd(m, a)
	case "update_action":
		note, err = d.meetingNotesUpdate(m, a)
	case "set_speaker":
		note, err = d.meetingNotesSpeaker(m, a)
	default:
		return "", Invalid("meeting_notes: action is read, write_notes, add_actions, update_action or set_speaker, not %q.", a.Action)
	}
	if err != nil {
		return "", meetingError(err)
	}
	return note, nil
}

// meetingNotesRead is the meeting as the agent reads it: its speakers by the number its
// prompts give them, the notes, the items, and a page of segments as the prompts' lines.
func (d *Daemon) meetingNotesRead(m wire.Meeting, a meetingNotesArgs) (string, error) {
	r, err := d.readMeeting(m.ID)
	if err != nil {
		return "", err
	}
	after := int64(0)
	if a.AfterSeq != nil {
		after = *a.AfterSeq
	}
	page, err := d.meetings().Segments(m.ID, after, store.MeetingSegmentsPage)
	if err != nil {
		return "", err
	}
	type speaker struct {
		Speaker int     `json:"speaker"`
		Source  string  `json:"source"`
		Name    *string `json:"name,omitempty"`
	}
	speakers := make([]speaker, len(r.Meeting.Speakers))
	for i, s := range r.Meeting.Speakers {
		speakers[i] = speaker{Speaker: i + 1, Source: s.Source, Name: s.Name}
	}
	lines := make([]string, len(page.Segments))
	for i, s := range page.Segments {
		lines[i] = fmt.Sprintf("[%d] %s: %s", s.Seq, speakerLabel(r.Meeting.Speakers, s), strings.TrimSpace(s.Text))
	}
	out := map[string]any{
		"meeting": map[string]any{"id": m.ID, "title": r.Meeting.Title, "state": r.Meeting.State,
			"startedAt": r.Meeting.StartedAt, "endedAt": r.Meeting.EndedAt, "lastSeq": r.Meeting.LastSeq,
			"segmentsRemoved": r.Meeting.SegmentsRemoved},
		"speakers":    speakers,
		"notes":       r.Notes,
		"actionItems": r.ActionItems,
		"segments":    lines,
		"more":        page.More,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	return string(b), err
}

func (d *Daemon) meetingNotesWrite(m wire.Meeting, a meetingNotesArgs) (string, error) {
	if a.Markdown == nil || a.BaseRevision == nil {
		return "", Invalid("write_notes needs markdown and baseRevision, the revision read sees (0 before the first).")
	}
	n, err := d.meetings().SetNotes(m.ID, *a.Markdown, *a.BaseRevision, wire.MeetingAuthorAgent, nowFunc())
	if err != nil {
		if store.IsMeetingRefusal(err, store.MeetingChanged) {
			return "", &Error{Code: wire.FailureChangedElsewhere, Message: err.Error() + ": read them again, then write over the new revision."}
		}
		return "", err
	}
	d.sayMeeting(m.ID, &n, nil)
	return fmt.Sprintf("The notes are at revision %d.", n.Revision), nil
}

// meetingNotesAdd adds each item it can: one refused leaves the others kept, and the reply
// names both.
func (d *Daemon) meetingNotesAdd(m wire.Meeting, a meetingNotesArgs) (string, error) {
	if len(a.Items) == 0 || len(a.Items) > meetingActionsAdded {
		return "", Invalid("add_actions takes 1 to %d items.", meetingActionsAdded)
	}
	var added []wire.MeetingActionItem
	var lines []string
	for i, it := range a.Items {
		item := wire.MeetingActionItem{ID: store.NewUUID(), Text: strings.TrimSpace(it.Text), Owner: it.Owner, Due: it.Due,
			Quote: it.Quote, SegmentSeqs: it.SegmentSeqs, State: wire.MeetingActionItemStateProposed}
		if item.SegmentSeqs == nil {
			item.SegmentSeqs = []int64{}
		}
		kept, err := d.meetings().SetActionItem(m.ID, item, nil, wire.MeetingAuthorAgent, nowFunc())
		if err != nil {
			lines = append(lines, fmt.Sprintf("Item %d (%q) was refused: %s", i+1, it.Text, err.Error()))
			continue
		}
		added = append(added, kept)
		lines = append(lines, fmt.Sprintf("Item %d (%q) is action item %s.", i+1, kept.Text, kept.ID))
	}
	if len(added) == 0 {
		return "", Invalid("%s", strings.Join(lines, "\n"))
	}
	d.sayMeeting(m.ID, nil, added)
	return strings.Join(lines, "\n"), nil
}

func (d *Daemon) meetingNotesUpdate(m wire.Meeting, a meetingNotesArgs) (string, error) {
	if a.ID == "" {
		return "", Invalid("update_action needs the id of the action item.")
	}
	items, err := d.meetings().ActionItems(m.ID)
	if err != nil {
		return "", err
	}
	var item *wire.MeetingActionItem
	for i := range items {
		if string(items[i].ID) == a.ID {
			item = &items[i]
		}
	}
	if item == nil {
		return "", Invalid("the meeting has no action item %s.", a.ID)
	}
	if a.Text != nil {
		item.Text = strings.TrimSpace(*a.Text)
	}
	if a.Owner != nil {
		item.Owner = emptyIsNone(*a.Owner)
	}
	if a.Due != nil {
		item.Due = emptyIsNone(*a.Due)
	}
	if a.Issue != nil {
		if a.Issue.Forge == "" {
			a.Issue.Forge = "github"
		}
		if a.Issue.Repo == "" || a.Issue.Number <= 0 || a.Issue.URL == "" {
			return "", Invalid("an issue needs its repo (owner/name), number and url.")
		}
		item.Issue = a.Issue
		if a.State == nil {
			item.State = wire.MeetingActionItemStateFiled
		}
	}
	if a.State != nil {
		item.State = *a.State
	}
	kept, err := d.meetings().SetActionItem(m.ID, *item, nil, wire.MeetingAuthorAgent, nowFunc())
	if err != nil {
		return "", err
	}
	d.sayMeeting(m.ID, nil, []wire.MeetingActionItem{kept})
	return fmt.Sprintf("Action item %s is %s.", kept.ID, kept.State), nil
}

// meetingNotesSpeaker names a speaker by the number the prompts give it ("Speaker 2").
func (d *Daemon) meetingNotesSpeaker(m wire.Meeting, a meetingNotesArgs) (string, error) {
	if a.Index == nil || a.Name == nil {
		return "", Invalid("set_speaker needs index, the n of \"Speaker <n>\", and name.")
	}
	n := *a.Index
	if n < 1 || n > int64(len(m.Speakers)) {
		return "", Invalid("the meeting has no Speaker %d: it has heard %d.", n, len(m.Speakers))
	}
	s := m.Speakers[n-1]
	named, err := d.meetings().SetSpeaker(m.ID, s.Source, s.Index, *a.Name, nowFunc())
	if err != nil {
		return "", err
	}
	d.meetingChanged(named, nil, nil)
	if strings.TrimSpace(*a.Name) == "" {
		return fmt.Sprintf("Speaker %d has no name now.", n), nil
	}
	return fmt.Sprintf("Speaker %d is %s.", n, strings.TrimSpace(*a.Name)), nil
}

func emptyIsNone(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}
