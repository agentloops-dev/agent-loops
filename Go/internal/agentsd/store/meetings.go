package store

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Meetings (#7581, epic #7327): a meeting's records are first-party, on the machine that runs
// its session, in the session's folder (agents/<id>/meeting/), so a delete of the session takes
// them all. The data model is gate 1 of #7330: a meeting record, its segments as an
// append-only JSON-lines file read through a line index, its notes with their last revisions,
// and its action items. Every read and write goes through MeetingStore, so a database can take
// its place later.

// The limits of the records table.
const (
	MeetingRecordBytes    = 8 << 10
	MeetingTitleChars     = 200
	MeetingSources        = 4
	MeetingSpeakers       = 64
	MeetingSegments       = 6000
	MeetingSegmentChars   = 1500
	MeetingSegmentBytes   = 2 << 10
	MeetingAudioCap       = 4 * time.Hour
	MeetingNotesBytes     = 64 << 10
	MeetingNotesRevisions = 20
	MeetingActionItems    = 100
	MeetingActionBytes    = 2 << 10
	MeetingQuoteSeqs      = 20
	MeetingSegmentAge     = 90 * 24 * time.Hour
	MeetingsKept          = 500
	MeetingsPage          = 50
	MeetingSegmentsPage   = 200
	// meetingSegmentLines bounds the segment file: each segment once, and once more for each
	// change of its speaker, at most as many again.
	meetingSegmentLines = 2 * MeetingSegments
	// meetingIndexes is how many meetings' line indexes are kept in memory.
	meetingIndexes = 16
)

// MeetingStore is every read and write of meetings. The rules of the data model are kept
// here, not in a handler: the quote of an action item is word for word in its segments, the
// agent does not change a field a person edited, and notes are written only over the revision
// they were made from.
type MeetingStore interface {
	// Create keeps a new meeting, and is it as kept. A session has at most one.
	Create(m wire.Meeting) (wire.Meeting, error)
	// Meeting is one meeting by its id.
	Meeting(id wire.UUID) (wire.Meeting, error)
	// List is a page of the meetings, newest first.
	List(cursor string, limit int) (wire.MeetingPage, error)
	// SetState listens, pauses or ends a meeting. An ended meeting stays ended.
	SetState(id wire.UUID, state wire.MeetingState, at time.Time) (wire.Meeting, error)
	// Append keeps new segments, and the changed speakers of earlier ones.
	Append(id wire.UUID, segments []wire.MeetingSegment, at time.Time) (wire.Meeting, error)
	// Segments is a page of the segments after afterSeq, oldest first.
	Segments(id wire.UUID, afterSeq int64, limit int) (wire.MeetingSegmentsPage, error)
	// SetSpeaker names a voice for the whole meeting.
	SetSpeaker(id wire.UUID, source string, index int64, name string, at time.Time) (wire.Meeting, error)
	// SentThrough says the segments through seq went to the session's agent.
	SentThrough(id wire.UUID, seq int64, at time.Time) (wire.Meeting, error)
	// Notes is the meeting's notes, nil before the first write.
	Notes(id wire.UUID) (*wire.MeetingNotes, error)
	// SetNotes writes the notes over baseRevision.
	SetNotes(id wire.UUID, markdown string, baseRevision int64, by wire.MeetingAuthor, at time.Time) (wire.MeetingNotes, error)
	// ActionItems is the meeting's action items, in the order they were added.
	ActionItems(id wire.UUID) ([]wire.MeetingActionItem, error)
	// SetActionItem adds an item or changes one. base is the updatedAt the change was made
	// to: nil for a new item, and for an agent's change, which the fields a person edited guard.
	SetActionItem(id wire.UUID, item wire.MeetingActionItem, base *wire.Date, by wire.MeetingAuthor, at time.Time) (wire.MeetingActionItem, error)
	// Sweep takes the segments of the meetings older than MeetingSegmentAge, and the
	// meetings whose session is gone out of the index. It says how many it aged out.
	Sweep(now time.Time) (int, error)
}

// MeetingError is a refusal of the data model's rules.
type MeetingError struct {
	Kind    MeetingRefusal
	Message string
}

// MeetingRefusal is why a write was refused.
type MeetingRefusal int

const (
	// MeetingNotFound: no meeting has the id.
	MeetingNotFound MeetingRefusal = iota
	// MeetingInvalid: a value over its limit, or one the rules refuse (a quote not in its
	// segments, a seq out of order).
	MeetingInvalid
	// MeetingChanged: the record moved since the change was made (the revision, updatedAt).
	MeetingChanged
	// MeetingPersonEdited: the agent changed a field a person edited.
	MeetingPersonEdited
)

func (e *MeetingError) Error() string { return e.Message }

func meetingRefused(kind MeetingRefusal, format string, args ...any) error {
	return &MeetingError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// IsMeetingRefusal says whether err is a refusal of kind.
func IsMeetingRefusal(err error, kind MeetingRefusal) bool {
	var e *MeetingError
	return errors.As(err, &e) && e.Kind == kind
}

// OpenMeetings is the store of the meetings of the root at loc.
func OpenMeetings(loc Locations) MeetingStore {
	return &meetingFiles{loc: loc, indexes: map[wire.UUID]*segmentIndex{}}
}

type meetingFiles struct {
	loc Locations
	// mu is held for every read and write: meetings are few and slow.
	mu      sync.Mutex
	indexes map[wire.UUID]*segmentIndex
	clock   uint64
}

// meetingEntry is a meeting in the index (meetings.json): its session and its start, so a
// list and a read find its folder without reading every session's.
type meetingEntry struct {
	AgentID   wire.UUID `json:"agentID"`
	StartedAt wire.Date `json:"startedAt"`
}

func (s *meetingFiles) indexFile() string { return filepath.Join(s.loc.Root, "meetings.json") }

func (s *meetingFiles) folder(agentID wire.UUID) string {
	return filepath.Join(s.loc.Agent(string(agentID)), "meeting")
}

func (s *meetingFiles) readIndex() (map[wire.UUID]meetingEntry, error) {
	out := map[wire.UUID]meetingEntry{}
	b, err := os.ReadFile(s.indexFile())
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := diskjson.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("meetings.json: %w", err)
	}
	return out, nil
}

func (s *meetingFiles) writeIndex(m map[wire.UUID]meetingEntry) error {
	b, err := diskjson.Marshal(m)
	if err != nil {
		return err
	}
	// store-bounds: kind=key-value; count=500; age=until-deleted; applied=on write, the oldest meeting goes
	return WriteAtomically(s.indexFile(), b, 0o600)
}

func (s *meetingFiles) entry(id wire.UUID) (meetingEntry, error) {
	idx, err := s.readIndex()
	if err != nil {
		return meetingEntry{}, err
	}
	e, ok := idx[id]
	if !ok {
		return meetingEntry{}, meetingRefused(MeetingNotFound, "no meeting %s", id)
	}
	return e, nil
}

// files is the folder of a meeting.
func (s *meetingFiles) files(id wire.UUID) (string, error) {
	e, err := s.entry(id)
	if err != nil {
		return "", err
	}
	return s.folder(e.AgentID), nil
}

func readMeetingFile[T any](path string) (T, bool, error) {
	var v T
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if err := diskjson.Unmarshal(b, &v); err != nil {
		return v, false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return v, true, nil
}

func (s *meetingFiles) read(id wire.UUID) (wire.Meeting, string, error) {
	dir, err := s.files(id)
	if err != nil {
		return wire.Meeting{}, "", err
	}
	m, ok, err := readMeetingFile[wire.Meeting](filepath.Join(dir, "meeting.json"))
	if err == nil && !ok {
		err = meetingRefused(MeetingNotFound, "no meeting %s: its session is gone", id)
	}
	return m, dir, err
}

func writeMeeting(dir string, m wire.Meeting) error {
	b, err := diskjson.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > MeetingRecordBytes {
		return meetingRefused(MeetingInvalid, "the meeting's record would be %d bytes, over %d", len(b), MeetingRecordBytes)
	}
	// store-bounds: kind=document; size=8KiB; count=1; age=until-deleted; applied=refused on write
	return WriteAtomically(filepath.Join(dir, "meeting.json"), b, 0o600)
}

func (s *meetingFiles) Create(m wire.Meeting) (wire.Meeting, error) {
	if m.ID == "" || m.AgentID == "" {
		return m, meetingRefused(MeetingInvalid, "a meeting needs an id and a session")
	}
	if utf8.RuneCountInString(m.Title) > MeetingTitleChars {
		return m, meetingRefused(MeetingInvalid, "a meeting's title is at most %d characters", MeetingTitleChars)
	}
	if len(m.Sources) == 0 || len(m.Sources) > MeetingSources {
		return m, meetingRefused(MeetingInvalid, "a meeting listens to 1 to %d sources", MeetingSources)
	}
	if m.Speakers == nil {
		m.Speakers = []wire.MeetingSpeaker{}
	}
	m.StartedAt, m.UpdatedAt = meetingDate(m.StartedAt.Time()), meetingDate(m.UpdatedAt.Time())
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.readIndex()
	if err != nil {
		return m, err
	}
	if _, ok := idx[m.ID]; ok {
		return m, meetingRefused(MeetingInvalid, "meeting %s is kept already", m.ID)
	}
	for _, e := range idx {
		if e.AgentID == m.AgentID {
			return m, meetingRefused(MeetingInvalid, "session %s has a meeting already", m.AgentID)
		}
	}
	dir := s.folder(m.AgentID)
	if err := writeMeeting(dir, m); err != nil {
		return m, err
	}
	idx[m.ID] = meetingEntry{AgentID: m.AgentID, StartedAt: m.StartedAt}
	// The oldest meetings go past MeetingsKept: their records, not their sessions.
	for len(idx) > MeetingsKept {
		oldest := meetingsNewestFirst(idx)[len(idx)-1]
		if err := os.RemoveAll(s.folder(idx[oldest].AgentID)); err != nil {
			return m, err
		}
		delete(idx, oldest)
		delete(s.indexes, oldest)
	}
	return m, s.writeIndex(idx)
}

// meetingsNewestFirst is the index's ids, the newest meeting first, then by id.
func meetingsNewestFirst(idx map[wire.UUID]meetingEntry) []wire.UUID {
	ids := make([]wire.UUID, 0, len(idx))
	for id := range idx {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b wire.UUID) int {
		if x, y := idx[a].StartedAt, idx[b].StartedAt; x != y {
			if x > y {
				return -1
			}
			return 1
		}
		return strings.Compare(string(a), string(b))
	})
	return ids
}

func (s *meetingFiles) Meeting(id wire.UUID) (wire.Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, _, err := s.read(id)
	return m, err
}

func (s *meetingFiles) List(cursor string, limit int) (wire.MeetingPage, error) {
	if limit <= 0 || limit > MeetingsPage {
		limit = MeetingsPage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.readIndex()
	if err != nil {
		return wire.MeetingPage{}, err
	}
	ids := meetingsNewestFirst(idx)
	from := 0
	if cursor != "" {
		at := slices.Index(ids, wire.UUID(cursor))
		if at < 0 {
			return wire.MeetingPage{}, meetingRefused(MeetingInvalid, "the cursor is no meeting kept")
		}
		from = at + 1
	}
	page := wire.MeetingPage{Meetings: []wire.Meeting{}}
	for i := from; i < len(ids) && len(page.Meetings) < limit; i++ {
		m, ok, err := readMeetingFile[wire.Meeting](filepath.Join(s.folder(idx[ids[i]].AgentID), "meeting.json"))
		if err != nil || !ok {
			continue // its session is gone; the sweep takes it out of the index
		}
		page.Meetings = append(page.Meetings, m)
		if i+1 < len(ids) && len(page.Meetings) == limit {
			page.Cursor = new(string(ids[i]))
		}
	}
	return page, nil
}

// change reads a meeting, lets edit change it, and writes it with updatedAt at.
func (s *meetingFiles) change(id wire.UUID, at time.Time, edit func(m *wire.Meeting, dir string) error) (wire.Meeting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, dir, err := s.read(id)
	if err != nil {
		return m, err
	}
	if err := edit(&m, dir); err != nil {
		return m, err
	}
	m.UpdatedAt = meetingDate(at)
	return m, writeMeeting(dir, m)
}

func (s *meetingFiles) SetState(id wire.UUID, state wire.MeetingState, at time.Time) (wire.Meeting, error) {
	return s.change(id, at, func(m *wire.Meeting, _ string) error {
		switch {
		case m.State == state:
		case m.State == wire.MeetingStateEnded:
			return meetingRefused(MeetingInvalid, "the meeting has ended")
		case state == wire.MeetingStateEnded:
			m.EndedAt = new(meetingDate(at))
		case state != wire.MeetingStateListening && state != wire.MeetingStatePaused:
			return meetingRefused(MeetingInvalid, "no meeting state %q", state)
		}
		m.State = state
		return nil
	})
}

func (s *meetingFiles) SetSpeaker(id wire.UUID, source string, index int64, name string, at time.Time) (wire.Meeting, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 100 {
		return wire.Meeting{}, meetingRefused(MeetingInvalid, "a speaker's name is at most 100 characters")
	}
	return s.change(id, at, func(m *wire.Meeting, _ string) error {
		i := slices.IndexFunc(m.Speakers, func(p wire.MeetingSpeaker) bool { return p.Source == source && p.Index == index })
		if i < 0 {
			if !slices.Contains(m.Sources, source) || index < 0 {
				return meetingRefused(MeetingInvalid, "the meeting has no speaker %d on %s", index, source)
			}
			if len(m.Speakers) >= MeetingSpeakers {
				return meetingRefused(MeetingInvalid, "a meeting has at most %d speakers", MeetingSpeakers)
			}
			m.Speakers = append(m.Speakers, wire.MeetingSpeaker{Source: source, Index: index})
			i = len(m.Speakers) - 1
		}
		m.Speakers[i].Name = nil
		if name != "" {
			m.Speakers[i].Name = &name
		}
		return nil
	})
}

func (s *meetingFiles) SentThrough(id wire.UUID, seq int64, at time.Time) (wire.Meeting, error) {
	return s.change(id, at, func(m *wire.Meeting, _ string) error {
		if seq < m.SentThroughSeq || seq > m.LastSeq {
			return meetingRefused(MeetingInvalid, "seq %d is not between %d and %d", seq, m.SentThroughSeq, m.LastSeq)
		}
		m.SentThroughSeq = seq
		return nil
	})
}

// Segments.

// segmentIndex is where the last line of each seq starts in a meeting's segment file, and
// how many lines and bytes it had when it was read: only this store writes the file, so what
// is past size is new lines.
type segmentIndex struct {
	starts []int64 // starts[seq-1]
	ends   []int64
	lines  int
	size   int64
	used   uint64
}

func segmentFile(dir string) string { return filepath.Join(dir, "segments.jsonl") }

// cached is a meeting's line index in memory, made when first asked for. With s.mu held.
func (s *meetingFiles) cached(id wire.UUID) *segmentIndex {
	ix := s.indexes[id]
	if ix == nil {
		ix = &segmentIndex{}
		s.indexes[id] = ix
		if len(s.indexes) > meetingIndexes {
			var oldest wire.UUID
			at := ^uint64(0)
			for k, v := range s.indexes {
				if k != id && v.used < at {
					oldest, at = k, v.used
				}
			}
			delete(s.indexes, oldest)
		}
	}
	s.clock++
	ix.used = s.clock
	return ix
}

// index is the line index of a meeting's segments, as of now. With s.mu held.
func (s *meetingFiles) index(id wire.UUID, dir string) (*segmentIndex, error) {
	ix := s.cached(id)
	f, err := os.Open(segmentFile(dir))
	if os.IsNotExist(err) {
		*ix = segmentIndex{used: ix.used}
		return ix, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < ix.size {
		*ix = segmentIndex{used: ix.used}
	}
	if info.Size() > ix.size {
		ix.extend(io.NewSectionReader(f, ix.size, info.Size()-ix.size))
	}
	return ix, nil
}

// extend reads the lines written since the index was made. A torn last line is left to the
// next append, which ends it.
func (ix *segmentIndex) extend(from io.Reader) {
	r := bufio.NewReader(from)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			ix.add(line)
		}
		if err != nil {
			return
		}
	}
}

// add is one whole line, at the end of the lines indexed.
func (ix *segmentIndex) add(line []byte) {
	start, end := ix.size, ix.size+int64(len(line))
	ix.size, ix.lines = end, ix.lines+1
	var seg struct {
		Seq int64 `json:"seq"`
	}
	if diskjson.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &seg) != nil || seg.Seq < 1 {
		return
	}
	switch n := int64(len(ix.starts)); {
	case seg.Seq == n+1:
		ix.starts, ix.ends = append(ix.starts, start), append(ix.ends, end)
	case seg.Seq <= n:
		ix.starts[seg.Seq-1], ix.ends[seg.Seq-1] = start, end
	}
}

func readSegment(f io.ReaderAt, start, end int64) (wire.MeetingSegment, error) {
	b := make([]byte, end-start)
	if _, err := f.ReadAt(b, start); err != nil {
		return wire.MeetingSegment{}, err
	}
	var seg wire.MeetingSegment
	err := diskjson.Unmarshal(bytes.TrimSuffix(b, []byte{'\n'}), &seg)
	return seg, err
}

func (s *meetingFiles) Segments(id wire.UUID, afterSeq int64, limit int) (wire.MeetingSegmentsPage, error) {
	if limit <= 0 || limit > MeetingSegmentsPage {
		limit = MeetingSegmentsPage
	}
	afterSeq = max(afterSeq, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, dir, err := s.read(id)
	if err != nil {
		return wire.MeetingSegmentsPage{}, err
	}
	segs, more, err := s.segments(id, dir, afterSeq, afterSeq+int64(limit))
	return wire.MeetingSegmentsPage{Segments: segs, More: more}, err
}

// segments is the segments after from through to, and whether there are more. With s.mu held.
func (s *meetingFiles) segments(id wire.UUID, dir string, from, to int64) ([]wire.MeetingSegment, bool, error) {
	out := []wire.MeetingSegment{}
	ix, err := s.index(id, dir)
	if err != nil || from >= int64(len(ix.starts)) {
		return out, false, err
	}
	f, err := os.Open(segmentFile(dir))
	if err != nil {
		return out, false, err
	}
	defer f.Close()
	to = min(to, int64(len(ix.starts)))
	for seq := from + 1; seq <= to; seq++ {
		seg, err := readSegment(f, ix.starts[seq-1], ix.ends[seq-1])
		if err != nil {
			return nil, false, err
		}
		out = append(out, seg)
	}
	return out, to < int64(len(ix.starts)), nil
}

func (s *meetingFiles) Append(id wire.UUID, segments []wire.MeetingSegment, at time.Time) (wire.Meeting, error) {
	if len(segments) > MeetingSegmentsPage {
		return wire.Meeting{}, meetingRefused(MeetingInvalid, "at most %d segments an append", MeetingSegmentsPage)
	}
	return s.change(id, at, func(m *wire.Meeting, dir string) error {
		if m.SegmentsRemoved != nil && *m.SegmentsRemoved {
			return meetingRefused(MeetingInvalid, "the meeting's segments aged out")
		}
		ix, err := s.index(id, dir)
		if err != nil {
			return err
		}
		f, err := os.Open(segmentFile(dir))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if f != nil {
			defer f.Close()
		}
		var buf bytes.Buffer
		last, lines, speakers, heard := m.LastSeq, ix.lines, slices.Clone(m.Speakers), 0.0
		for _, seg := range segments {
			if seg.Seq > m.LastSeq {
				heard = max(heard, seg.AudioEnd)
			}
			line, err := s.appendLine(m, f, ix, &last, seg)
			if err != nil {
				return err
			}
			if line == nil {
				continue
			}
			if lines++; lines > meetingSegmentLines {
				return meetingRefused(MeetingInvalid, "the meeting has its most changes of segments, %d", meetingSegmentLines)
			}
			buf.Write(line)
			speakers = withSpeaker(speakers, seg)
		}
		if err := appendSegments(dir, buf.Bytes()); err != nil {
			return err
		}
		m.LastSeq, m.Speakers = last, speakers
		// The meeting stops at its cap: 6,000 segments, or 4 hours of audio.
		if last >= MeetingSegments || heard >= MeetingAudioCap.Seconds() {
			m.State, m.EndedAt = wire.MeetingStateEnded, new(meetingDate(at))
		}
		return nil
	})
}

// withSpeaker is speakers with the segment's voice, when it is a new one and there is room.
func withSpeaker(speakers []wire.MeetingSpeaker, seg wire.MeetingSegment) []wire.MeetingSpeaker {
	if seg.SpeakerIndex == nil || len(speakers) >= MeetingSpeakers || slices.ContainsFunc(speakers, func(p wire.MeetingSpeaker) bool {
		return p.Source == seg.Source && p.Index == *seg.SpeakerIndex
	}) {
		return speakers
	}
	return append(speakers, wire.MeetingSpeaker{Source: seg.Source, Index: *seg.SpeakerIndex})
}

// appendSegments writes whole lines at the end of a meeting's segment file.
func appendSegments(dir string, lines []byte) error {
	if len(lines) == 0 {
		return nil
	}
	endTornLine(segmentFile(dir))
	if err := fault(segmentFile(dir), true, lines); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// store-bounds: kind=log; size=24MiB; count=12_000; age=90d; applied=refused on append, the hourly sweep ages out
	w, err := os.OpenFile(segmentFile(dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = w.Write(lines)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

// appendLine is the line one segment of an append writes, nil for none (a repeat), checked
// against the rules. last is the last seq so far, moved on by a new segment.
func (s *meetingFiles) appendLine(m *wire.Meeting, f io.ReaderAt, ix *segmentIndex, last *int64, seg wire.MeetingSegment) ([]byte, error) {
	if seg.Seq <= m.LastSeq {
		// A changed speaker of a kept segment: its words stay as they were.
		if seg.Seq < 1 || f == nil || seg.Seq > int64(len(ix.starts)) {
			return nil, meetingRefused(MeetingInvalid, "no segment %d", seg.Seq)
		}
		kept, err := readSegment(f, ix.starts[seg.Seq-1], ix.ends[seg.Seq-1])
		if err != nil {
			return nil, err
		}
		if kept.SpeakerSettled || (ptrEqual(kept.SpeakerIndex, seg.SpeakerIndex) && kept.SpeakerSettled == seg.SpeakerSettled) {
			return nil, nil
		}
		kept.SpeakerIndex, kept.SpeakerSettled = seg.SpeakerIndex, seg.SpeakerSettled
		seg = kept
	} else {
		if m.State == wire.MeetingStateEnded {
			return nil, meetingRefused(MeetingInvalid, "the meeting has ended")
		}
		if seg.Seq != *last+1 {
			return nil, meetingRefused(MeetingInvalid, "segment %d does not follow %d", seg.Seq, *last)
		}
		if *last >= MeetingSegments {
			return nil, meetingRefused(MeetingInvalid, "a meeting has at most %d segments", MeetingSegments)
		}
		if !slices.Contains(m.Sources, seg.Source) {
			return nil, meetingRefused(MeetingInvalid, "the meeting does not listen to %q", seg.Source)
		}
		if utf8.RuneCountInString(seg.Text) > MeetingSegmentChars {
			return nil, meetingRefused(MeetingInvalid, "a segment's text is at most %d characters", MeetingSegmentChars)
		}
		if seg.AudioStart < 0 || seg.AudioEnd < seg.AudioStart || seg.AudioStart > MeetingAudioCap.Seconds() {
			return nil, meetingRefused(MeetingInvalid, "segment %d's audio is not within 0 to 4 hours, start before end", seg.Seq)
		}
		*last = seg.Seq
	}
	if seg.SpeakerIndex != nil && *seg.SpeakerIndex < 0 {
		return nil, meetingRefused(MeetingInvalid, "a speaker's index is 0 or more")
	}
	b, err := diskjson.Marshal(seg)
	if err != nil {
		return nil, err
	}
	if len(b) > MeetingSegmentBytes {
		return nil, meetingRefused(MeetingInvalid, "segment %d is %d bytes, over %d", seg.Seq, len(b), MeetingSegmentBytes)
	}
	return append(b, '\n'), nil
}

// meetingDate is the date of at as the disk keeps it, to the millisecond, so a date a client
// was given is the date it reads back and sends as its base.
func meetingDate(at time.Time) wire.Date {
	d, err := diskjson.ParseDate(diskjson.FormatDate(wire.DateOf(at)))
	if err != nil {
		return wire.DateOf(at)
	}
	return d
}

func ptrEqual[T comparable](a, b *T) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// Notes.

type notesFile struct {
	// Revisions is the last MeetingNotesRevisions, the newest last.
	Revisions []wire.MeetingNotes `json:"revisions"`
}

func notesPath(dir string) string { return filepath.Join(dir, "notes.json") }

func (s *meetingFiles) Notes(id wire.UUID) (*wire.MeetingNotes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, dir, err := s.read(id)
	if err != nil {
		return nil, err
	}
	n, _, err := readMeetingFile[notesFile](notesPath(dir))
	if err != nil || len(n.Revisions) == 0 {
		return nil, err
	}
	return new(n.Revisions[len(n.Revisions)-1]), nil
}

func (s *meetingFiles) SetNotes(id wire.UUID, markdown string, baseRevision int64, by wire.MeetingAuthor, at time.Time) (wire.MeetingNotes, error) {
	if len(markdown) > MeetingNotesBytes {
		return wire.MeetingNotes{}, meetingRefused(MeetingInvalid, "the notes are %d bytes, over %d: make them shorter", len(markdown), MeetingNotesBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, dir, err := s.read(id)
	if err != nil {
		return wire.MeetingNotes{}, err
	}
	n, _, err := readMeetingFile[notesFile](notesPath(dir))
	if err != nil {
		return wire.MeetingNotes{}, err
	}
	revision := int64(0)
	if len(n.Revisions) > 0 {
		revision = n.Revisions[len(n.Revisions)-1].Revision
	}
	if baseRevision != revision {
		return wire.MeetingNotes{}, meetingRefused(MeetingChanged, "the notes moved to revision %d since revision %d", revision, baseRevision)
	}
	notes := wire.MeetingNotes{MeetingID: id, Markdown: markdown, Revision: revision + 1, UpdatedAt: meetingDate(at), UpdatedBy: by}
	n.Revisions = append(n.Revisions, notes)
	if len(n.Revisions) > MeetingNotesRevisions {
		n.Revisions = n.Revisions[len(n.Revisions)-MeetingNotesRevisions:]
	}
	b, err := diskjson.Marshal(n)
	if err != nil {
		return wire.MeetingNotes{}, err
	}
	// store-bounds: kind=document; size=1344KiB; count=20; age=until-deleted; applied=refused on write over 64KiB, the oldest revision goes
	return notes, WriteAtomically(notesPath(dir), b, 0o600)
}

// Action items.

type actionsFile struct {
	Items []wire.MeetingActionItem `json:"items"`
}

func actionsPath(dir string) string { return filepath.Join(dir, "actions.json") }

func (s *meetingFiles) ActionItems(id wire.UUID) ([]wire.MeetingActionItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, dir, err := s.read(id)
	if err != nil {
		return nil, err
	}
	a, _, err := readMeetingFile[actionsFile](actionsPath(dir))
	if a.Items == nil {
		a.Items = []wire.MeetingActionItem{}
	}
	return a.Items, err
}

func (s *meetingFiles) SetActionItem(id wire.UUID, item wire.MeetingActionItem, base *wire.Date, by wire.MeetingAuthor, at time.Time) (wire.MeetingActionItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, dir, err := s.read(id)
	if err != nil {
		return item, err
	}
	a, _, err := readMeetingFile[actionsFile](actionsPath(dir))
	if err != nil {
		return item, err
	}
	item.MeetingID = id
	i := slices.IndexFunc(a.Items, func(x wire.MeetingActionItem) bool { return x.ID == item.ID })
	if item.ID == "" {
		return item, meetingRefused(MeetingInvalid, "an action item needs an id")
	}
	if i < 0 {
		item, err = newItem(item, len(a.Items), base, by)
	} else {
		item, err = changedItem(a.Items[i], item, base, by)
	}
	if err != nil {
		return item, err
	}
	if err := s.checkItem(m, dir, item, by, i < 0); err != nil {
		return item, err
	}
	item.UpdatedAt = meetingDate(at)
	if b, err := diskjson.Marshal(item); err != nil {
		return item, err
	} else if len(b) > MeetingActionBytes {
		return item, meetingRefused(MeetingInvalid, "the action item is %d bytes, over %d", len(b), MeetingActionBytes)
	}
	if i < 0 {
		a.Items = append(a.Items, item)
	} else {
		a.Items[i] = item
	}
	b, err := diskjson.Marshal(a)
	if err != nil {
		return item, err
	}
	// store-bounds: kind=document; size=200KiB; count=100; age=until-deleted; applied=refused on write
	return item, WriteAtomically(actionsPath(dir), b, 0o600)
}

// newItem is a new item as it is kept, given how many the meeting has.
func newItem(item wire.MeetingActionItem, have int, base *wire.Date, by wire.MeetingAuthor) (wire.MeetingActionItem, error) {
	if base != nil {
		return item, meetingRefused(MeetingChanged, "action item %s is gone", item.ID)
	}
	if have >= MeetingActionItems {
		return item, meetingRefused(MeetingInvalid, "a meeting has at most %d action items", MeetingActionItems)
	}
	if item.State == "" {
		item.State = wire.MeetingActionItemStateProposed
	}
	item.EditedByPerson = nil
	if by == wire.MeetingAuthorPerson {
		item.EditedByPerson = itemFields(wire.MeetingActionItem{}, item)
	}
	return item, nil
}

// changedItem is the change of was to item, as it is kept: the agent changes no field a
// person edited and no quote, and each field a person changes is then theirs.
func changedItem(was, item wire.MeetingActionItem, base *wire.Date, by wire.MeetingAuthor) (wire.MeetingActionItem, error) {
	if base != nil && *base != was.UpdatedAt {
		return item, meetingRefused(MeetingChanged, "action item %s changed since", item.ID)
	}
	changed := itemFields(was, item)
	item.EditedByPerson = was.EditedByPerson
	if by == wire.MeetingAuthorPerson {
		for _, f := range changed {
			if !slices.Contains(item.EditedByPerson, f) {
				item.EditedByPerson = append(item.EditedByPerson, f)
			}
		}
		return item, nil
	}
	if i := slices.IndexFunc(changed, func(f wire.MeetingActionItemField) bool { return slices.Contains(was.EditedByPerson, f) }); i >= 0 {
		return item, meetingRefused(MeetingPersonEdited, "a person edited the %s of action item %s: the agent does not change it", changed[i], item.ID)
	}
	if item.Quote != was.Quote || !slices.Equal(item.SegmentSeqs, was.SegmentSeqs) {
		return item, meetingRefused(MeetingInvalid, "an action item's quote does not change")
	}
	return item, nil
}

// checkItem holds an item to its limits, and a new one to its quote: the agent's quote is word
// for word in the segments it names, and so is a person's when they give one.
func (s *meetingFiles) checkItem(m wire.Meeting, dir string, item wire.MeetingActionItem, by wire.MeetingAuthor, isNew bool) error {
	switch item.State {
	case wire.MeetingActionItemStateProposed, wire.MeetingActionItemStateApproved, wire.MeetingActionItemStateDismissed, wire.MeetingActionItemStateFiled:
	default:
		return meetingRefused(MeetingInvalid, "no action item state %q", item.State)
	}
	if strings.TrimSpace(item.Text) == "" {
		return meetingRefused(MeetingInvalid, "an action item needs its text")
	}
	if len(item.SegmentSeqs) > MeetingQuoteSeqs {
		return meetingRefused(MeetingInvalid, "an action item names at most %d segments", MeetingQuoteSeqs)
	}
	if !isNew || (by == wire.MeetingAuthorPerson && strings.TrimSpace(item.Quote) == "") {
		return nil
	}
	if m.SegmentsRemoved != nil && *m.SegmentsRemoved {
		return meetingRefused(MeetingInvalid, "the meeting's segments aged out: a quote cannot be checked")
	}
	seqs := slices.Clone(item.SegmentSeqs)
	slices.Sort(seqs)
	seqs = slices.Compact(seqs)
	if len(seqs) == 0 {
		return meetingRefused(MeetingInvalid, "an action item names the segments its quote is in")
	}
	ix, err := s.index(m.ID, dir)
	if err != nil {
		return err
	}
	f, err := os.Open(segmentFile(dir))
	if err != nil {
		return meetingRefused(MeetingInvalid, "the meeting has no segments")
	}
	defer f.Close()
	var words []string
	for _, seq := range seqs {
		if seq < 1 || seq > int64(len(ix.starts)) {
			return meetingRefused(MeetingInvalid, "the meeting has no segment %d", seq)
		}
		seg, err := readSegment(f, ix.starts[seq-1], ix.ends[seq-1])
		if err != nil {
			return err
		}
		words = append(words, quoteWords(seg.Text)...)
	}
	if !wordForWord(quoteWords(item.Quote), words) {
		return meetingRefused(MeetingInvalid, "the quote is not word for word in segments %s", seqList(seqs))
	}
	return nil
}

// quoteWords is text's words for the quote check: in lower case, with the punctuation the
// transcriber puts around them taken off, so "Friday." is the word of "friday".
func quoteWords(text string) []string {
	var out []string
	for _, w := range strings.Fields(text) {
		w = strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }))
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// wordForWord says whether quote is a run of words of words, in order and with none between.
func wordForWord(quote, words []string) bool {
	if len(quote) == 0 {
		return false
	}
	for i := 0; i+len(quote) <= len(words); i++ {
		if slices.Equal(words[i:i+len(quote)], quote) {
			return true
		}
	}
	return false
}

func seqList(seqs []int64) string {
	parts := make([]string, len(seqs))
	for i, n := range seqs {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return strings.Join(parts, ", ")
}

// itemFields is the fields a person can change that differ between was and is.
func itemFields(was, is wire.MeetingActionItem) []wire.MeetingActionItemField {
	var out []wire.MeetingActionItemField
	if was.Text != is.Text {
		out = append(out, wire.MeetingActionItemFieldText)
	}
	if !ptrEqual(was.Owner, is.Owner) {
		out = append(out, wire.MeetingActionItemFieldOwner)
	}
	if !ptrEqual(was.Due, is.Due) {
		out = append(out, wire.MeetingActionItemFieldDue)
	}
	if !ptrEqual(was.DueOn, is.DueOn) {
		out = append(out, wire.MeetingActionItemFieldDueOn)
	}
	if was.State != is.State {
		out = append(out, wire.MeetingActionItemFieldState)
	}
	if !ptrEqual(was.Issue, is.Issue) {
		out = append(out, wire.MeetingActionItemFieldIssue)
	}
	return out
}

// Sweep.

func (s *meetingFiles) Sweep(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.readIndex()
	if err != nil {
		return 0, err
	}
	aged, gone := 0, false
	for id, e := range idx {
		dir := s.folder(e.AgentID)
		m, ok, err := readMeetingFile[wire.Meeting](filepath.Join(dir, "meeting.json"))
		if err == nil && !ok {
			if _, err := os.Stat(s.loc.Agent(string(e.AgentID))); os.IsNotExist(err) {
				delete(idx, id) // the session was deleted, and its meeting with it
				delete(s.indexes, id)
				gone = true
			}
			continue
		}
		if err != nil || (m.SegmentsRemoved != nil && *m.SegmentsRemoved) || now.Sub(m.StartedAt.Time()) < MeetingSegmentAge {
			continue
		}
		if err := os.Remove(segmentFile(dir)); err != nil && !os.IsNotExist(err) {
			return aged, err
		}
		delete(s.indexes, id)
		m.SegmentsRemoved = new(true)
		m.UpdatedAt = meetingDate(now)
		if err := writeMeeting(dir, m); err != nil {
			return aged, err
		}
		aged++
	}
	if gone {
		return aged, s.writeIndex(idx)
	}
	return aged, nil
}
