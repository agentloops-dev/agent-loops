package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Meeting mode (#7581, epic #7327): the methods of the meetings group, behind the flag
// meeting-mode. The records and their rules are store.MeetingStore's; these handlers check
// the flag, make the session of meetings/start, and say each change (meetings/changed). The
// gaps that send the words to the meeting's agent are in meetinggaps.go (#7582); the agent's
// meeting_notes tool is in meetingnotes.go (#7583).

// meetingFlag is the person's flag that turns meeting mode on, in the display settings.
const meetingFlag = "flag:meeting-mode"

type meetingBook struct {
	meetingsOnce sync.Once
	meetingStore store.MeetingStore
	// meetingGapMu orders the appends, the state changes and the gaps of the meetings.
	meetingGapMu sync.Mutex
	// meetingStartMu is held while a meeting is started, so its session's brief and tools/list
	// wait for the meeting to be kept (#7583).
	meetingStartMu sync.RWMutex
	// meetingQueue is each meeting's prompt of words still in its agent's queue (#7582).
	meetingQueue map[wire.UUID]meetingQueued
}

// meetings is this root's store of meetings.
func (d *Daemon) meetings() store.MeetingStore {
	d.meetingsOnce.Do(func() {
		if d.meetingStore == nil {
			d.meetingStore = store.OpenMeetings(d.Store.Loc)
		}
		d.meetingQueue = map[wire.UUID]meetingQueued{}
	})
	return d.meetingStore
}

// meetingModeOn says whether the person turned the flag meeting-mode on.
func (d *Daemon) meetingModeOn() bool {
	var on bool
	v, ok := d.preferences()[meetingFlag]
	return ok && json.Unmarshal(v, &on) == nil && on
}

var errMeetingModeOff = &Error{Code: CodeNotSupported, Message: "meeting mode is off: turn on the flag meeting-mode"}

// meetingError is a store's refusal as the wire's failure.
func meetingError(err error) error {
	var e *store.MeetingError
	if !errors.As(err, &e) {
		return err
	}
	switch e.Kind {
	case store.MeetingChanged:
		return &Error{Code: wire.FailureChangedElsewhere, Message: e.Message}
	case store.MeetingPersonEdited:
		return &Error{Code: CodeNotPermitted, Message: e.Message}
	default:
		return Invalid("%s", e.Message)
	}
}

// onMeeting serves a meetings method only while the flag is on, with the store's refusals
// as failures.
func onMeeting[P, R any](d *Daemon, m wire.Method[P, R], h func(ctx context.Context, c *Client, p P) (R, error)) {
	On(d, m, func(ctx context.Context, c *Client, p P) (R, error) {
		if !d.meetingModeOn() {
			var zero R
			return zero, errMeetingModeOff
		}
		r, err := h(ctx, c, p)
		return r, meetingError(err)
	})
}

// meetingChanged says a change to every client.
func (d *Daemon) meetingChanged(m wire.Meeting, notes *wire.MeetingNotes, items []wire.MeetingActionItem) {
	Broadcast(d, wire.NotifyMeetingsChanged, wire.MeetingChangedNotification{
		MeetingID: m.ID, Meeting: &m, Notes: notes, ActionItems: items, LastSeq: m.LastSeq})
}

func (d *Daemon) registerMeetings() {
	onMeeting(d, wire.CallMeetingsStart, func(ctx context.Context, c *Client, r wire.MeetingStartRequest) (wire.MeetingStarted, error) {
		return d.startMeeting(r, c.actor())
	})
	onMeeting(d, wire.CallMeetingsSetState, func(ctx context.Context, c *Client, r wire.MeetingSetStateRequest) (wire.Meeting, error) {
		m, err := d.meetingSetState(r.MeetingID, r.State)
		if err == nil {
			d.meetingChanged(m, nil, nil)
		}
		return m, err
	})
	onMeeting(d, wire.CallMeetingsAppend, func(ctx context.Context, c *Client, r wire.MeetingAppendRequest) (wire.MeetingAppended, error) {
		m, err := d.meetingAppend(r.MeetingID, r.Segments)
		if err != nil {
			return wire.MeetingAppended{}, err
		}
		d.meetingChanged(m, nil, nil)
		return wire.MeetingAppended{LastSeq: m.LastSeq, SentThroughSeq: m.SentThroughSeq}, nil
	})
	onMeeting(d, wire.CallMeetingsList, func(ctx context.Context, c *Client, r wire.MeetingListRequest) (wire.MeetingPage, error) {
		limit := store.MeetingsPage
		if r.Limit != nil {
			limit = int(*r.Limit)
		}
		cursor := ""
		if r.Cursor != nil {
			cursor = *r.Cursor
		}
		return d.meetings().List(cursor, limit)
	})
	onMeeting(d, wire.CallMeetingsRead, func(ctx context.Context, c *Client, r wire.MeetingRequest) (wire.MeetingRead, error) {
		return d.readMeeting(r.MeetingID)
	})
	onMeeting(d, wire.CallMeetingsSegments, func(ctx context.Context, c *Client, r wire.MeetingSegmentsRequest) (wire.MeetingSegmentsPage, error) {
		limit := store.MeetingSegmentsPage
		if r.Limit != nil {
			limit = int(*r.Limit)
		}
		return d.meetings().Segments(r.MeetingID, r.AfterSeq, limit)
	})
	onMeeting(d, wire.CallMeetingsSetSpeaker, func(ctx context.Context, c *Client, r wire.MeetingSetSpeakerRequest) (wire.Meeting, error) {
		m, err := d.meetings().SetSpeaker(r.MeetingID, r.Source, r.Index, r.Name, nowFunc())
		if err == nil {
			d.meetingChanged(m, nil, nil)
		}
		return m, err
	})
	onMeeting(d, wire.CallMeetingsSetNotes, func(ctx context.Context, c *Client, r wire.MeetingSetNotesRequest) (wire.MeetingNotes, error) {
		n, err := d.meetings().SetNotes(r.MeetingID, r.Markdown, r.BaseRevision, wire.MeetingAuthorPerson, nowFunc())
		if err == nil {
			d.sayMeeting(r.MeetingID, &n, nil)
		}
		return n, err
	})
	onMeeting(d, wire.CallMeetingsSetActionItem, func(ctx context.Context, c *Client, r wire.MeetingSetActionItemRequest) (wire.MeetingActionItem, error) {
		item, err := d.meetings().SetActionItem(r.MeetingID, r.Item, r.BaseUpdatedAt, wire.MeetingAuthorPerson, nowFunc())
		if err == nil {
			d.sayMeeting(r.MeetingID, nil, []wire.MeetingActionItem{item})
		}
		return item, err
	})
}

// sayMeeting says a change of the notes or the items, with the meeting as it is now.
func (d *Daemon) sayMeeting(id wire.UUID, notes *wire.MeetingNotes, items []wire.MeetingActionItem) {
	m, err := d.meetings().Meeting(id)
	if err != nil {
		log.Printf("meeting %s: %v", id, err)
		return
	}
	d.meetingChanged(m, notes, items)
}

func (d *Daemon) readMeeting(id wire.UUID) (wire.MeetingRead, error) {
	m, err := d.meetings().Meeting(id)
	if err != nil {
		return wire.MeetingRead{}, err
	}
	notes, err := d.meetings().Notes(id)
	if err != nil {
		return wire.MeetingRead{}, err
	}
	items, err := d.meetings().ActionItems(id)
	return wire.MeetingRead{Meeting: m, Notes: notes, ActionItems: items}, err
}

// startMeeting makes the meeting's session in the folder, with the meeting's starter prompt,
// then the meeting.
func (d *Daemon) startMeeting(r wire.MeetingStartRequest, actor *wire.AuditActor) (wire.MeetingStarted, error) {
	at := nowFunc()
	title := ""
	if r.Title != nil {
		title = strings.TrimSpace(*r.Title)
	}
	if title == "" {
		title = "Meeting " + at.Local().Format("2 Jan 15:04")
	}
	if len(r.Sources) == 0 || len(r.Sources) > store.MeetingSources {
		return wire.MeetingStarted{}, Invalid("a meeting listens to 1 to %d sources", store.MeetingSources)
	}
	for _, s := range r.Sources {
		if s != "mic" && !strings.HasPrefix(s, "call:") {
			return wire.MeetingStarted{}, Invalid("a meeting's source is mic or call:<bundle id>, not %q", s)
		}
	}
	runtimeID := autoRuntimeID
	if r.RuntimeID != nil && *r.RuntimeID != "" {
		runtimeID = *r.RuntimeID
	}
	id := store.NewUUID()
	d.meetingStartMu.Lock()
	defer d.meetingStartMu.Unlock()
	agentID, err := d.startAs(wire.StartRequest{RuntimeID: runtimeID, Cwd: r.Folder, Prompt: meetingStarter(id, title)}, actor)
	if err != nil {
		return wire.MeetingStarted{}, err
	}
	m := wire.Meeting{ID: id, AgentID: agentID, Folder: r.Folder, Title: title, State: wire.MeetingStateListening,
		StartedAt: wire.DateOf(at), Sources: r.Sources, Speakers: []wire.MeetingSpeaker{}, UpdatedAt: wire.DateOf(at)}
	m, err = d.meetings().Create(m)
	if err != nil {
		return wire.MeetingStarted{}, err
	}
	d.meetingChanged(m, nil, nil)
	return wire.MeetingStarted{Meeting: m, AgentID: agentID}, nil
}

// meetingStarter is the first prompt of a meeting's session: its job, and the tool it does it with.
func meetingStarter(id wire.UUID, title string) string {
	return fmt.Sprintf("You keep the notes of a meeting, %q (meeting %s). The app listens to it and sends you its words "+
		"as prompts at each pause, a line a segment: \"[<seq>] Speaker <n> (<name>): <words>\". Keep the notes and the "+
		"action items with the meeting_notes tool: write short notes in Markdown, add each action item with the words "+
		"it comes from as its quote and the seqs of their lines, and name a speaker when the meeting says who they are. "+
		"Ask with one question card before you file any issue. File each issue with the forge tools you have, then "+
		"record the issue on its action item. A last prompt says when the meeting ended. Until the first words come, "+
		"say nothing more than that you are ready.", title, id)
}

// sweepMeetings ages out the segments of old meetings (90 days).
func (d *Daemon) sweepMeetings(now time.Time) {
	if n, err := d.meetings().Sweep(now); err != nil {
		log.Printf("retention: meetings: %v", err)
	} else if n > 0 {
		log.Printf("retention: the segments of %d meetings aged out", n)
	}
}
