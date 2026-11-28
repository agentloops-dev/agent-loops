package agentsd

import (
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Meeting mode's gaps (#7582, epic #7327): the daemon, not the client, decides when the
// words of a meeting go to its agent, so the Remote and the web page get the same rule later.
// At a gap the new segments go to the meeting's session as one ordinary prompt; while that
// prompt is still queued (the agent's turn runs), the next gap's words join it, so the agent
// is never more than one prompt behind.

// meetingGapRule is the three numbers of the gap rule, in seconds of the segments' audio
// clock. Stage 7 of the epic tunes them; they are here and nowhere else.
type meetingGapRule struct {
	// Pause is the silence between two segments that makes a gap.
	Pause float64
	// Least is the new words a pause needs before it makes a gap.
	Least float64
	// Most is the new words with no pause that make a gap.
	Most float64
}

var meetingGaps = meetingGapRule{Pause: 2, Least: 20, Most: 60}

// gapThrough is the last seq of the segments that go now, by the rule: segs are the new
// segments, oldest first. It is 0 when no gap has come yet.
func (g meetingGapRule) gapThrough(segs []wire.MeetingSegment) int64 {
	var through int64
	var start, end float64
	open := false
	for _, s := range segs {
		if !open {
			start, end, open = s.AudioStart, s.AudioEnd, true
		} else if s.AudioStart-end >= g.Pause && end-start >= g.Least {
			// A pause after enough words: the words before it go.
			through = s.Seq - 1
			start, end = s.AudioStart, s.AudioEnd
		} else {
			end = max(end, s.AudioEnd)
		}
		if end-start >= g.Most {
			// A minute of words with no pause: they go, this segment too.
			through, open = s.Seq, false
		}
	}
	return through
}

// meetingQueued is the prompt of a meeting's words that waits in its agent's queue.
type meetingQueued struct {
	prompt  wire.UUID
	fromSeq int64
}

// meetingAppend keeps new segments, then sends the words of a gap, and of the end when the
// append took the meeting to its cap.
func (d *Daemon) meetingAppend(id wire.UUID, segs []wire.MeetingSegment) (wire.Meeting, error) {
	d.meetingGapMu.Lock()
	defer d.meetingGapMu.Unlock()
	was, err := d.meetings().Meeting(id)
	if err != nil {
		return was, err
	}
	m, err := d.meetings().Append(id, segs, nowFunc())
	if err != nil {
		return m, err
	}
	if m.State == wire.MeetingStateEnded && was.State != wire.MeetingStateEnded {
		return d.sendMeetingWords(m, m.LastSeq, true), nil
	}
	unsent, err := d.meetingSegments(id, m.SentThroughSeq, m.LastSeq)
	if err != nil {
		log.Printf("meeting %s: %v", id, err)
		return m, nil
	}
	if through := meetingGaps.gapThrough(unsent); through > 0 {
		m = d.sendMeetingWords(m, through, false)
	}
	return m, nil
}

// meetingSetState listens, pauses or ends a meeting. A pause sends what is left, as a gap;
// the end sends what is left as the last prompt, which says the meeting ended.
func (d *Daemon) meetingSetState(id wire.UUID, state wire.MeetingState) (wire.Meeting, error) {
	d.meetingGapMu.Lock()
	defer d.meetingGapMu.Unlock()
	was, err := d.meetings().Meeting(id)
	if err != nil {
		return was, err
	}
	m, err := d.meetings().SetState(id, state, nowFunc())
	if err != nil || m.State == was.State {
		return m, err
	}
	switch m.State {
	case wire.MeetingStateEnded:
		m = d.sendMeetingWords(m, m.LastSeq, true)
	case wire.MeetingStatePaused:
		if m.LastSeq > m.SentThroughSeq {
			m = d.sendMeetingWords(m, m.LastSeq, false)
		}
	}
	return m, nil
}

// sendMeetingWords sends the segments after sentThroughSeq, through through, to the
// meeting's agent: into its queued prompt of the meeting's words when there is one, else as a
// new prompt. It moves sentThroughSeq, and is the meeting as it is then.
func (d *Daemon) sendMeetingWords(m wire.Meeting, through int64, ended bool) wire.Meeting {
	from := m.SentThroughSeq + 1
	prev, joining := d.meetingQueue[m.ID]
	if joining && prev.fromSeq < from {
		from = prev.fromSeq
	}
	segs, err := d.meetingSegments(m.ID, from-1, through)
	if err != nil {
		log.Printf("meeting %s: %v", m.ID, err)
		return m
	}
	// The words for the queued prompt, from its first seq; and for a new prompt, from the
	// first seq that has not gone, when the queued one went out before these words.
	joinText := meetingWords(m, from, through, segs, ended)
	fresh := slices.DeleteFunc(slices.Clone(segs), func(s wire.MeetingSegment) bool { return s.Seq <= m.SentThroughSeq })
	q := wire.QueuedPrompt{ID: store.NewUUID(), Text: meetingWords(m, m.SentThroughSeq+1, through, fresh, ended),
		Attachments: []wire.Attachment{}, QueuedAt: now(), From: wire.PromptOriginApp}
	next := meetingQueued{prompt: q.ID, fromSeq: m.SentThroughSeq + 1}
	a := d.change(m.AgentID, func(a *wire.Agent) {
		queue := slices.Clone(a.QueuedPrompts)
		if i := slices.IndexFunc(queue, func(p wire.QueuedPrompt) bool { return joining && p.ID == prev.prompt }); i >= 0 {
			queue[i].Text, next = joinText, prev
		} else {
			queue = append(queue, q)
		}
		a.QueuedPrompts = queue
	})
	if a == nil {
		log.Printf("meeting %s: its agent %s is not here", m.ID, m.AgentID)
		return m
	}
	if ended {
		delete(d.meetingQueue, m.ID)
	} else {
		d.meetingQueue[m.ID] = next
	}
	d.live(m.AgentID).kick()
	if through > m.SentThroughSeq {
		moved, err := d.meetings().SentThrough(m.ID, through, nowFunc())
		if err != nil {
			log.Printf("meeting %s: %v", m.ID, err)
			return m
		}
		m = moved
	}
	return m
}

// meetingSegments is the segments after afterSeq, through through, page by page.
func (d *Daemon) meetingSegments(id wire.UUID, afterSeq, through int64) ([]wire.MeetingSegment, error) {
	var out []wire.MeetingSegment
	for afterSeq < through {
		page, err := d.meetings().Segments(id, afterSeq, store.MeetingSegmentsPage)
		if err != nil {
			return out, err
		}
		for _, s := range page.Segments {
			if s.Seq > through {
				return out, nil
			}
			out = append(out, s)
			afterSeq = s.Seq
		}
		if !page.More || len(page.Segments) == 0 {
			break
		}
	}
	return out, nil
}

// meetingWords is the prompt of a gap: the meeting, the seq range, and a line a segment,
// "[seq] Speaker <n> (<name>): <words>", so the agent can quote a line and name its seqs.
func meetingWords(m wire.Meeting, from, through int64, segs []wire.MeetingSegment, ended bool) string {
	var b strings.Builder
	if through >= from {
		fmt.Fprintf(&b, "Meeting %s, segments %d to %d:\n", m.ID, from, through)
		for _, s := range segs {
			fmt.Fprintf(&b, "[%d] %s: %s\n", s.Seq, speakerLabel(m.Speakers, s), strings.TrimSpace(s.Text))
		}
	}
	if ended {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Meeting %s ended: no more words will come. Finish the notes and the action items with the meeting_notes tool.", m.ID)
	}
	return strings.TrimRight(b.String(), "\n")
}

// speakerLabel is "Speaker <n>", with the name when the meeting knows it: n counts the
// meeting's voices from 1, in the order they were first heard.
func speakerLabel(speakers []wire.MeetingSpeaker, s wire.MeetingSegment) string {
	if s.SpeakerIndex == nil {
		return "Unknown speaker"
	}
	i := slices.IndexFunc(speakers, func(p wire.MeetingSpeaker) bool { return p.Source == s.Source && p.Index == *s.SpeakerIndex })
	if i < 0 {
		return "Unknown speaker"
	}
	label := fmt.Sprintf("Speaker %d", i+1)
	if n := speakers[i].Name; n != nil && *n != "" {
		label += " (" + *n + ")"
	}
	return label
}
