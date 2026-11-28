package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

var meetingNow = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

func newMeeting(t *testing.T, s MeetingStore, loc Locations, started time.Time) wire.Meeting {
	t.Helper()
	m := wire.Meeting{ID: NewUUID(), AgentID: NewUUID(), Folder: FolderURL(t.TempDir()), Title: "Planning",
		State: wire.MeetingStateListening, StartedAt: wire.DateOf(started), Sources: []string{"mic", "call:us.zoom.xos"},
		Speakers: []wire.MeetingSpeaker{}, UpdatedAt: wire.DateOf(started)}
	if err := os.MkdirAll(loc.Agent(string(m.AgentID)), 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func seg(seq int64, source, text string, speaker int64) wire.MeetingSegment {
	return wire.MeetingSegment{Seq: seq, Source: source, AudioStart: float64(seq), AudioEnd: float64(seq) + 0.9, Text: text, SpeakerIndex: new(speaker)}
}

func meetingStore(t *testing.T) (MeetingStore, Locations) {
	t.Helper()
	loc := At(t.TempDir())
	return OpenMeetings(loc), loc
}

func refusedAs(t *testing.T, err error, kind MeetingRefusal) {
	t.Helper()
	if !IsMeetingRefusal(err, kind) {
		t.Fatalf("want a refusal of kind %d, got %v", kind, err)
	}
}

// Segments append in seq order with no gap, and a page after a seq reads only the ones after
// it; a later line for an earlier seq changes its speaker, not its words.
func TestMeetingSegmentsAppendAndPageAfterASeq(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	m := newMeeting(t, s, loc, meetingNow)
	var batch []wire.MeetingSegment
	for i := int64(1); i <= 250; i++ {
		batch = append(batch, seg(i, "mic", fmt.Sprintf("line %d", i), i%2))
		if len(batch) == 100 || i == 250 {
			if _, err := s.Append(m.ID, batch, meetingNow); err != nil {
				t.Fatal(err)
			}
			batch = nil
		}
	}
	page, err := s.Segments(m.ID, 0, 0)
	if err != nil || len(page.Segments) != MeetingSegmentsPage || !page.More || page.Segments[0].Seq != 1 {
		t.Fatalf("%d %v %v", len(page.Segments), page.More, err)
	}
	page, _ = s.Segments(m.ID, 240, 50)
	if len(page.Segments) != 10 || page.More || page.Segments[0].Text != "line 241" {
		t.Fatalf("%+v", page)
	}
	// A gap, a repeat of a new seq, and a source the meeting does not listen to are refused.
	_, err = s.Append(m.ID, []wire.MeetingSegment{seg(252, "mic", "skips one", 0)}, meetingNow)
	refusedAs(t, err, MeetingInvalid)
	_, err = s.Append(m.ID, []wire.MeetingSegment{seg(251, "call:other", "x", 0)}, meetingNow)
	refusedAs(t, err, MeetingInvalid)
	// The diarizer changes its mind about segment 3: the speaker changes, the words do not.
	change := seg(3, "mic", "other words", 5)
	change.SpeakerSettled = true
	got, err := s.Append(m.ID, []wire.MeetingSegment{change}, meetingNow)
	if err != nil || got.LastSeq != 250 {
		t.Fatalf("%+v %v", got, err)
	}
	page, _ = s.Segments(m.ID, 2, 1)
	if p := page.Segments[0]; p.Text != "line 3" || *p.SpeakerIndex != 5 || !p.SpeakerSettled {
		t.Fatalf("%+v", p)
	}
	// Settled is settled.
	_, _ = s.Append(m.ID, []wire.MeetingSegment{seg(3, "mic", "", 7)}, meetingNow)
	page, _ = s.Segments(m.ID, 2, 1)
	if *page.Segments[0].SpeakerIndex != 5 {
		t.Fatalf("%+v", page.Segments[0])
	}
	// A new store (a restart) reads the same through its own index.
	again := OpenMeetings(loc)
	page, _ = again.Segments(m.ID, 2, 1)
	if *page.Segments[0].SpeakerIndex != 5 || page.Segments[0].Text != "line 3" {
		t.Fatalf("%+v", page.Segments[0])
	}
}

// The speakers of two streams do not collide (#7580): slot 0 of the mic and slot 0 of the call
// are two speakers, named apart.
func TestMeetingSpeakersAreKeyedByStreamAndSlot(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	m := newMeeting(t, s, loc, meetingNow)
	if _, err := s.Append(m.ID, []wire.MeetingSegment{seg(1, "mic", "hello", 0), seg(2, "call:us.zoom.xos", "hi", 0)}, meetingNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSpeaker(m.ID, "call:us.zoom.xos", 0, "Priya", meetingNow); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Meeting(m.ID)
	if len(got.Speakers) != 2 || got.Speakers[0].Name != nil || *got.Speakers[1].Name != "Priya" {
		t.Fatalf("%+v", got.Speakers)
	}
	_, err := s.SetSpeaker(m.ID, "call:elsewhere", 0, "Bob", meetingNow)
	refusedAs(t, err, MeetingInvalid)
}

// The limits of the records table.
func TestMeetingLimits(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	m := newMeeting(t, s, loc, meetingNow)
	long := seg(1, "mic", strings.Repeat("a", MeetingSegmentChars+1), 0)
	_, err := s.Append(m.ID, []wire.MeetingSegment{long}, meetingNow)
	refusedAs(t, err, MeetingInvalid)
	_, err = s.SetNotes(m.ID, strings.Repeat("n", MeetingNotesBytes+1), 0, wire.MeetingAuthorAgent, meetingNow)
	refusedAs(t, err, MeetingInvalid)
	_, err = s.Append(m.ID, make([]wire.MeetingSegment, MeetingSegmentsPage+1), meetingNow)
	refusedAs(t, err, MeetingInvalid)
	// At 4 hours of audio the meeting ends, and takes no more words.
	late := seg(1, "mic", "still here", 0)
	late.AudioStart, late.AudioEnd = MeetingAudioCap.Seconds()-1, MeetingAudioCap.Seconds()
	got, err := s.Append(m.ID, []wire.MeetingSegment{late}, meetingNow)
	if err != nil || got.State != wire.MeetingStateEnded || got.EndedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = s.Append(m.ID, []wire.MeetingSegment{seg(2, "mic", "more", 0)}, meetingNow)
	refusedAs(t, err, MeetingInvalid)
	_, err = s.SetState(m.ID, wire.MeetingStateListening, meetingNow)
	refusedAs(t, err, MeetingInvalid)
	// 100 action items, and no more.
	m2 := newMeeting(t, s, loc, meetingNow)
	if _, err := s.Append(m2.ID, []wire.MeetingSegment{seg(1, "mic", "Alex will send the deck by Friday.", 0)}, meetingNow); err != nil {
		t.Fatal(err)
	}
	for i := range MeetingActionItems {
		if _, err := s.SetActionItem(m2.ID, item("send the deck", "send the deck", 1), nil, wire.MeetingAuthorAgent, meetingNow); err != nil {
			t.Fatal(i, err)
		}
	}
	_, err = s.SetActionItem(m2.ID, item("one more", "send the deck", 1), nil, wire.MeetingAuthorAgent, meetingNow)
	refusedAs(t, err, MeetingInvalid)
}

// A session has one meeting, and the index keeps the newest MeetingsKept.
func TestMeetingsListNewestFirstAndKeepTheirMost(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	var ids []wire.UUID
	for i := range 3 {
		ids = append(ids, newMeeting(t, s, loc, meetingNow.Add(time.Duration(i)*time.Minute)).ID)
	}
	page, err := s.List("", 2)
	if err != nil || len(page.Meetings) != 2 || page.Meetings[0].ID != ids[2] || page.Cursor == nil {
		t.Fatalf("%+v %v", page, err)
	}
	page, _ = s.List(*page.Cursor, 2)
	if len(page.Meetings) != 1 || page.Meetings[0].ID != ids[0] || page.Cursor != nil {
		t.Fatalf("%+v", page)
	}
	first, _ := s.Meeting(ids[0])
	again := first
	again.ID = NewUUID()
	_, err = s.Create(again)
	refusedAs(t, err, MeetingInvalid)
}

func item(text, quote string, seqs ...int64) wire.MeetingActionItem {
	return wire.MeetingActionItem{ID: NewUUID(), Text: text, Quote: quote, SegmentSeqs: seqs}
}

// The quote is word for word in its segments, across a segment's end and whatever the
// punctuation and case; anything else is refused.
func TestAnActionItemsQuoteIsWordForWordInItsSegments(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	m := newMeeting(t, s, loc, meetingNow)
	if _, err := s.Append(m.ID, []wire.MeetingSegment{
		seg(1, "mic", "Okay, so Alex will send", 0), seg(2, "mic", "the deck by Friday.", 0), seg(3, "mic", "Thanks.", 1)}, meetingNow); err != nil {
		t.Fatal(err)
	}
	got, err := s.SetActionItem(m.ID, item("Send the deck", "alex will send the deck by friday", 2, 1), nil, wire.MeetingAuthorAgent, meetingNow)
	if err != nil || got.State != wire.MeetingActionItemStateProposed || got.MeetingID != m.ID {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []wire.MeetingActionItem{
		item("Send the deck", "Alex will send the slides by Friday", 1, 2), // a word not said
		item("Send the deck", "Alex will send the deck", 1),                // not in the segments named
		item("Send the deck", "will deck", 1, 2),                           // words between
		item("Send the deck", "", 1),                                       // no quote
		item("Send the deck", "Thanks", 9),                                 // no such segment
	} {
		_, err := s.SetActionItem(m.ID, bad, nil, wire.MeetingAuthorAgent, meetingNow)
		refusedAs(t, err, MeetingInvalid)
	}
	// A person may add one with no quote.
	if _, err := s.SetActionItem(m.ID, item("Book the room", ""), nil, wire.MeetingAuthorPerson, meetingNow); err != nil {
		t.Fatal(err)
	}
}

// The agent does not change a field a person edited; a change made to an older updatedAt is
// refused; the agent may still change the fields the person left alone.
func TestTheAgentDoesNotChangeWhatAPersonEdited(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	m := newMeeting(t, s, loc, meetingNow)
	if _, err := s.Append(m.ID, []wire.MeetingSegment{seg(1, "mic", "Priya will fix the build", 0)}, meetingNow); err != nil {
		t.Fatal(err)
	}
	it, err := s.SetActionItem(m.ID, item("Fix the build", "Priya will fix the build", 1), nil, wire.MeetingAuthorAgent, meetingNow)
	if err != nil {
		t.Fatal(err)
	}
	edit := it
	edit.Text = "Fix the CI build"
	later := meetingNow.Add(time.Second)
	edited, err := s.SetActionItem(m.ID, edit, &it.UpdatedAt, wire.MeetingAuthorPerson, later)
	if err != nil || len(edited.EditedByPerson) != 1 || edited.EditedByPerson[0] != wire.MeetingActionItemFieldText {
		t.Fatalf("%+v %v", edited, err)
	}
	// The person's change was made to the first version: a second one from it is stale.
	_, err = s.SetActionItem(m.ID, edit, &it.UpdatedAt, wire.MeetingAuthorPerson, later)
	refusedAs(t, err, MeetingChanged)
	agent := edited
	agent.Text = "Fix the build"
	_, err = s.SetActionItem(m.ID, agent, nil, wire.MeetingAuthorAgent, later)
	refusedAs(t, err, MeetingPersonEdited)
	agent = edited
	agent.Owner = new("Priya")
	agent.State = wire.MeetingActionItemStateFiled
	agent.Issue = &wire.MeetingActionItemIssue{Forge: "github", Repo: "acme/app", Number: 12, URL: "https://github.com/acme/app/issues/12"}
	got, err := s.SetActionItem(m.ID, agent, nil, wire.MeetingAuthorAgent, later)
	if err != nil || got.Text != "Fix the CI build" || *got.Owner != "Priya" || got.Issue.Number != 12 {
		t.Fatalf("%+v %v", got, err)
	}
	items, _ := s.ActionItems(m.ID)
	if len(items) != 1 || items[0].State != wire.MeetingActionItemStateFiled {
		t.Fatalf("%+v", items)
	}
}

// An item the person approved is filed by the agent, with its issue (#7583); the agent makes
// no other change to the state the person set.
func TestTheAgentFilesAnItemThePersonApproved(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	m := newMeeting(t, s, loc, meetingNow)
	if _, err := s.Append(m.ID, []wire.MeetingSegment{seg(1, "mic", "Priya will fix the build", 0)}, meetingNow); err != nil {
		t.Fatal(err)
	}
	it, err := s.SetActionItem(m.ID, item("Fix the build", "Priya will fix the build", 1), nil, wire.MeetingAuthorAgent, meetingNow)
	if err != nil {
		t.Fatal(err)
	}
	approve := it
	approve.State = wire.MeetingActionItemStateApproved
	approved, err := s.SetActionItem(m.ID, approve, &it.UpdatedAt, wire.MeetingAuthorPerson, meetingNow)
	if err != nil {
		t.Fatal(err)
	}
	agent := approved
	agent.State = wire.MeetingActionItemStateDismissed
	_, err = s.SetActionItem(m.ID, agent, nil, wire.MeetingAuthorAgent, meetingNow)
	refusedAs(t, err, MeetingPersonEdited)
	agent.State = wire.MeetingActionItemStateFiled
	_, err = s.SetActionItem(m.ID, agent, nil, wire.MeetingAuthorAgent, meetingNow)
	refusedAs(t, err, MeetingPersonEdited) // filed with no issue
	agent.Issue = &wire.MeetingActionItemIssue{Forge: "github", Repo: "acme/app", Number: 12, URL: "https://github.com/acme/app/issues/12"}
	if got, err := s.SetActionItem(m.ID, agent, nil, wire.MeetingAuthorAgent, meetingNow); err != nil || got.State != wire.MeetingActionItemStateFiled {
		t.Fatalf("%+v %v", got, err)
	}
	if got, ok, err := s.MeetingOf(m.AgentID); err != nil || !ok || got.ID != m.ID {
		t.Errorf("MeetingOf: %+v %v %v", got, ok, err)
	}
	if _, ok, err := s.MeetingOf(NewUUID()); err != nil || ok {
		t.Errorf("MeetingOf another session: %v %v", ok, err)
	}
}

// Notes are written over the revision they were made from, and keep their last 20 revisions.
func TestNotesAreWrittenOverTheirRevision(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	m := newMeeting(t, s, loc, meetingNow)
	if n, _ := s.Notes(m.ID); n != nil {
		t.Fatalf("%+v", n)
	}
	n, err := s.SetNotes(m.ID, "# Planning", 0, wire.MeetingAuthorAgent, meetingNow)
	if err != nil || n.Revision != 1 {
		t.Fatalf("%+v %v", n, err)
	}
	_, err = s.SetNotes(m.ID, "# Mine", 0, wire.MeetingAuthorPerson, meetingNow)
	refusedAs(t, err, MeetingChanged)
	for i := int64(1); i <= 30; i++ {
		if n, err = s.SetNotes(m.ID, fmt.Sprintf("# Planning %d", i), i, wire.MeetingAuthorAgent, meetingNow); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Notes(m.ID)
	if got.Revision != 31 || got.Markdown != "# Planning 30" {
		t.Fatalf("%+v", got)
	}
	b, _ := os.ReadFile(filepath.Join(loc.Agent(string(m.AgentID)), "meeting", "notes.json"))
	if c := strings.Count(string(b), `"revision"`); c != MeetingNotesRevisions {
		t.Fatalf("%d revisions kept", c)
	}
}

// After 90 days the segments go and the notes and items stay; a deleted session's meeting
// leaves the index.
func TestMeetingSweepAgesOutSegmentsAndForgetsDeletedSessions(t *testing.T) {
	t.Parallel()
	s, loc := meetingStore(t)
	old := newMeeting(t, s, loc, meetingNow.Add(-MeetingSegmentAge-time.Hour))
	fresh := newMeeting(t, s, loc, meetingNow)
	gone := newMeeting(t, s, loc, meetingNow)
	for _, m := range []wire.Meeting{old, fresh} {
		if _, err := s.Append(m.ID, []wire.MeetingSegment{seg(1, "mic", "words", 0)}, meetingNow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetNotes(old.ID, "kept", 0, wire.MeetingAuthorAgent, meetingNow); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(loc.Agent(string(gone.AgentID))); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Sweep(meetingNow); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	m, _ := s.Meeting(old.ID)
	if m.SegmentsRemoved == nil || !*m.SegmentsRemoved {
		t.Fatalf("%+v", m)
	}
	if page, _ := s.Segments(old.ID, 0, 10); len(page.Segments) != 0 {
		t.Fatalf("%+v", page)
	}
	if n, _ := s.Notes(old.ID); n == nil || n.Markdown != "kept" {
		t.Fatalf("%+v", n)
	}
	if page, _ := s.Segments(fresh.ID, 0, 10); len(page.Segments) != 1 {
		t.Fatalf("%+v", page)
	}
	_, err := s.Meeting(gone.ID)
	refusedAs(t, err, MeetingNotFound)
	if page, _ := s.List("", 50); len(page.Meetings) != 2 {
		t.Fatalf("%+v", page)
	}
}
