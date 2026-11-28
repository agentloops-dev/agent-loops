package agentsd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// seg is a made-up segment of the mic, start to end on the audio clock.
func seg(seq int64, start, end float64) wire.MeetingSegment {
	return wire.MeetingSegment{Seq: seq, Source: "mic", AudioStart: start, AudioEnd: end, Text: "words"}
}

// The gap rule on made-up segment times (#7582): a pause of 2 s after 20 s of new words, or
// 60 s of words with no pause.
func TestMeetingGapRule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		segs []wire.MeetingSegment
		want int64
	}{
		{"no words", nil, 0},
		{"a pause before 20 s of words", []wire.MeetingSegment{seg(1, 0, 10), seg(2, 13, 15)}, 0},
		{"20 s of words, no pause yet", []wire.MeetingSegment{seg(1, 0, 10), seg(2, 10.5, 21)}, 0},
		{"a pause of 2 s after 20 s", []wire.MeetingSegment{seg(1, 0, 10), seg(2, 10.5, 21), seg(3, 23, 25)}, 2},
		{"a pause of 1.9 s after 20 s", []wire.MeetingSegment{seg(1, 0, 10), seg(2, 10.5, 21), seg(3, 22.9, 25)}, 0},
		{"60 s with no pause", []wire.MeetingSegment{seg(1, 0, 20), seg(2, 21, 40), seg(3, 41, 60)}, 3},
		{"59 s with no pause", []wire.MeetingSegment{seg(1, 0, 20), seg(2, 21, 40), seg(3, 41, 59)}, 0},
		{"short pauses do not reset the minute", []wire.MeetingSegment{seg(1, 0, 5), seg(2, 8, 15), seg(3, 15.5, 61)}, 3},
		{"the last of two gaps", []wire.MeetingSegment{seg(1, 100, 121), seg(2, 124, 146), seg(3, 149, 150)}, 2},
		{"words after the minute start again", []wire.MeetingSegment{seg(1, 0, 60), seg(2, 61, 70), seg(3, 75, 76)}, 1},
		{"overlapping voices", []wire.MeetingSegment{seg(1, 0, 22), seg(2, 5, 8), seg(3, 25, 26)}, 2},
	}
	for _, c := range cases {
		if got := meetingGaps.gapThrough(c.segs); got != c.want {
			t.Errorf("%s: through %d, want %d", c.name, got, c.want)
		}
	}
}

// The gap prompt names the meeting, the seq range and each line's speaker, by number and name.
func TestMeetingWords(t *testing.T) {
	t.Parallel()
	zero, one := int64(0), int64(1)
	priya := "Priya"
	m := wire.Meeting{ID: "m1", Speakers: []wire.MeetingSpeaker{{Source: "mic", Index: 1}, {Source: "call:x", Index: 0, Name: &priya}}}
	segs := []wire.MeetingSegment{
		{Seq: 4, Source: "mic", Text: " Hello. ", SpeakerIndex: &one},
		{Seq: 5, Source: "call:x", Text: "Hi.", SpeakerIndex: &zero},
		{Seq: 6, Source: "mic", Text: "Who?"},
	}
	want := "Meeting m1, segments 4 to 6:\n[4] Speaker 1: Hello.\n[5] Speaker 2 (Priya): Hi.\n[6] Unknown speaker: Who?"
	if got := meetingWords(m, 4, 6, segs, false); got != want {
		t.Fatalf("%q", got)
	}
	if got := meetingWords(m, 7, 6, nil, true); !strings.HasPrefix(got, "Meeting m1 ended") {
		t.Fatalf("%q", got)
	}
}

func appendSegs(t *testing.T, c *conn, n int, id wire.UUID, segs ...wire.MeetingSegment) wire.MeetingAppended {
	t.Helper()
	var got wire.MeetingAppended
	mustUnmarshal(t, c.call(t, n, "meetings/append", map[string]any{"meetingID": id, "segments": segs}), &got)
	return got
}

func startMeetingFor(t *testing.T, c *conn, title string) wire.MeetingStarted {
	t.Helper()
	c.call(t, 1, "preferences/set", map[string]any{meetingFlag: true})
	var started wire.MeetingStarted
	mustUnmarshal(t, c.call(t, 2, "meetings/start", map[string]any{"folder": "file://" + t.TempDir(), "title": title,
		"sources": []string{"mic"}, "runtimeID": "claude"}), &started)
	return started
}

// At a gap the words go to an idle agent as a prompt, and sentThroughSeq moves; the end
// sends what is left as a last prompt that says the meeting ended.
func TestMeetingGapPromptsAndEnd(t *testing.T) {
	t.Parallel()
	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	started := startMeetingFor(t, c, "Planning")
	id, agent := started.Meeting.ID, started.AgentID
	c.until(t, "the starter prompt", func(string, json.RawMessage) bool { return len(userMessages(d, agent)) == 1 })

	if got := appendSegs(t, c, 3, id, seg(1, 0, 10), seg(2, 10.5, 21)); got.SentThroughSeq != 0 {
		t.Fatalf("no gap yet, but %+v", got)
	}
	if got := appendSegs(t, c, 4, id, seg(3, 23, 25)); got.SentThroughSeq != 2 {
		t.Fatalf("a gap through 2, but %+v", got)
	}
	c.until(t, "the gap's prompt", func(string, json.RawMessage) bool {
		got := userMessages(d, agent)
		return len(got) == 2 && strings.HasPrefix(got[1], "Meeting "+string(id)+", segments 1 to 2:\n[1] ")
	})
	var m wire.Meeting
	mustUnmarshal(t, c.call(t, 5, "meetings/setState", map[string]any{"meetingID": id, "state": "ended"}), &m)
	if m.SentThroughSeq != 3 {
		t.Fatalf("%+v", m)
	}
	c.until(t, "the end prompt", func(string, json.RawMessage) bool {
		got := userMessages(d, agent)
		return len(got) == 3 && strings.HasPrefix(got[2], "Meeting "+string(id)+", segments 3 to 3:") && strings.Contains(got[2], "ended")
	})
	// An end again sends nothing more.
	c.call(t, 6, "meetings/setState", map[string]any{"meetingID": id, "state": "ended"})
	if a := d.Store.Agent(agent); len(a.QueuedPrompts) != 0 {
		t.Fatalf("%+v", a.QueuedPrompts)
	}
}

// While the agent's turn runs, the next gap's words and the end join the queued prompt, so
// the agent is never more than one prompt behind.
func TestMeetingGapJoinsTheQueuedPrompt(t *testing.T) {
	t.Parallel()
	fakeRuntimes(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	// The title is in the starter prompt, and the fake runtime holds a turn that says "wait for me".
	started := startMeetingFor(t, c, "wait for me")
	id, agent := started.Meeting.ID, started.AgentID
	c.until(t, "the starter's turn", func(string, json.RawMessage) bool { return len(userMessages(d, agent)) == 1 })

	queued := func() []wire.QueuedPrompt { return d.Store.Agent(agent).QueuedPrompts }
	appendSegs(t, c, 3, id, seg(1, 0, 21), seg(2, 24, 25))
	if q := queued(); len(q) != 1 || !strings.HasPrefix(q[0].Text, "Meeting "+string(id)+", segments 1 to 1:") {
		t.Fatalf("%+v", q)
	}
	if got := appendSegs(t, c, 4, id, seg(3, 25, 46), seg(4, 49, 50)); got.SentThroughSeq != 3 {
		t.Fatalf("%+v", got)
	}
	q := queued()
	if len(q) != 1 || !strings.HasPrefix(q[0].Text, "Meeting "+string(id)+", segments 1 to 3:\n[1] ") || !strings.Contains(q[0].Text, "\n[3] ") {
		t.Fatalf("not joined: %+v", q)
	}
	c.call(t, 5, "meetings/setState", map[string]any{"meetingID": id, "state": "ended"})
	q = queued()
	if len(q) != 1 || !strings.HasPrefix(q[0].Text, "Meeting "+string(id)+", segments 1 to 4:") || !strings.Contains(q[0].Text, "ended") {
		t.Fatalf("the end did not join: %+v", q)
	}
}
