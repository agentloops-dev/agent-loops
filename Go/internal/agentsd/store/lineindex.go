package store

import (
	"bytes"
	"io"
	"os"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A transcript read in part (TranscriptReader, #91): where each line ends, found once
// and extended as the file grows, so a page or a turn decodes only its own lines, and a
// look for a kind of entry reads the bytes without decoding them. The indexes of the
// transcripts read most recently are kept, at most indexedTranscripts of them.

const indexedTranscripts = 32

// Lines is where each kept line of a transcript ends, by the line's number among all that
// were ever written. A trim takes lines off the front (FolderLog) and renumbers none.
type Lines struct {
	// first is the number of the first line kept, and start the place it starts.
	first int
	start int64
	// ends is where each whole line from first on ends, after its newline.
	ends []int64
	// why is what the trims took the lines before first for (#7405).
	why TrimCause
}

// First is the number of the first line kept, and Total one past the last.
func (l Lines) First() int { return l.first }
func (l Lines) Total() int { return l.first + len(l.ends) }

// Removed is what the trims took the lines before First for. Neither, with lines gone, is a
// header written before #7405.
func (l Lines) Removed() TrimCause { return l.why }

// startOf is where a kept line starts, and where the last ends for Total.
func (l Lines) startOf(line int) int64 {
	if line == l.first {
		return l.start
	}
	return l.ends[line-l.first-1]
}

// from is the lines a log with this head still has.
func (l Lines) from(h logHead) Lines {
	if h.lines < l.first || h.lines > l.Total() || l.startOf(h.lines) != h.bytes {
		return Lines{first: h.lines, start: h.bytes, why: h.why}
	}
	return Lines{first: h.lines, start: h.bytes, ends: l.ends[h.lines-l.first:], why: h.why}
}

type lineIndex struct {
	lines Lines
	// used orders the kept indexes: the least recently used goes first.
	used uint64
}

type indexBook struct {
	mu      sync.Mutex
	indexes map[wire.UUID]*lineIndex
	clock   uint64
}

// LineEnds is where each line of the transcript ends, as of now: its total is the
// transcript's count of lines. Its slice is the caller's to read, never to change.
func (s *Store) LineEnds(id wire.UUID) (Lines, error) {
	s.ib.mu.Lock()
	defer s.ib.mu.Unlock()
	if s.ib.indexes == nil {
		s.ib.indexes = map[wire.UUID]*lineIndex{}
	}
	f, head, size, err := s.transcript(id).openLog()
	if os.IsNotExist(err) {
		delete(s.ib.indexes, id)
		return Lines{}, nil
	}
	if err != nil {
		return Lines{}, err
	}
	defer f.Close()
	ix := s.ib.indexes[id]
	if ix == nil {
		ix = &lineIndex{}
		s.ib.indexes[id] = ix
		if len(s.ib.indexes) > indexedTranscripts {
			var oldest wire.UUID
			var at uint64 = ^uint64(0)
			for k, v := range s.ib.indexes {
				if k != id && v.used < at {
					oldest, at = k, v.used
				}
			}
			delete(s.ib.indexes, oldest)
		}
	}
	s.ib.clock++
	ix.used = s.ib.clock
	lines := ix.lines.from(head)
	at := lines.startOf(lines.Total())
	// Shorter than what was indexed: some other transcript now. Look again.
	if size < at {
		lines, at = Lines{first: head.lines, start: head.bytes, why: head.why}, head.bytes
	}
	if size > at {
		err = scanLines(io.NewSectionReader(f, head.at(at), size-at), func(line []byte) bool {
			at += int64(len(line))
			lines.ends = append(lines.ends, at)
			return true
		})
	}
	ix.lines = lines
	return lines, err
}

// EachLine calls see with every line from from to to (not included), its bytes and its number,
// until see says false. A line no longer kept is passed over. The bytes are only see's until
// it returns.
func (s *Store) EachLine(id wire.UUID, lines Lines, from, to int, see func(line []byte, i int) bool) error {
	from, to = max(from, lines.First()), min(to, lines.Total())
	if from >= to {
		return nil
	}
	f, head, _, err := s.transcript(id).openLog()
	if err != nil {
		return err
	}
	defer f.Close()
	// Trimmed since the lines were looked at.
	if from = max(from, head.lines); from >= to {
		return nil
	}
	start := lines.startOf(from)
	i := from
	return scanLines(io.NewSectionReader(f, head.at(start), lines.startOf(to)-start), func(line []byte) bool {
		i++
		return see(line, i-1)
	})
}

// Entries is the entries on lines from from to to (not included) of the transcript as indexed
// by lines, each with its line. A line that will not decode is skipped, as
// TranscriptReader skips one, and still counts as a line.
func (s *Store) Entries(id wire.UUID, lines Lines, from, to int) ([]wire.TranscriptEntry, []int, error) {
	var out []wire.TranscriptEntry
	var at []int
	err := s.EachLine(id, lines, from, to, func(line []byte, i int) bool {
		if len(bytes.TrimSpace(line)) == 0 {
			return true
		}
		var e wire.TranscriptEntry
		if diskjson.Unmarshal(line, &e) == nil {
			out = append(out, e)
			at = append(at, i)
		}
		return true
	})
	return out, at, err
}

// LinesContaining is the lines from from on whose bytes hold what, none of them decoded:
// a key such as "userMessage" in quotes is only ever a key, since a string's quotes are
// escaped.
func (s *Store) LinesContaining(id wire.UUID, lines Lines, what string, from int) ([]int, error) {
	var out []int
	needle := []byte(what)
	err := s.EachLine(id, lines, from, lines.Total(), func(line []byte, i int) bool {
		if bytes.Contains(line, needle) {
			out = append(out, i)
		}
		return true
	})
	return out, err
}

// EntriesFrom calls see with each entry on the whole lines after place from, with where its
// line starts and ends, past the kept line indexes: a reader that keeps its own place (said.json,
// P20261019-1) pushes no open chat's index out. end is where the last whole line ends and size
// where the transcript ends as read; under from, it is some other transcript now, and nothing
// is read.
func (s *Store) EntriesFrom(id wire.UUID, from int64, see func(e wire.TranscriptEntry, start, end int64)) (end, size int64, err error) {
	f, head, size, err := s.transcript(id).openLog()
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	if from = max(from, head.bytes); size <= from {
		return min(from, size), size, nil
	}
	end = from
	err = scanLines(io.NewSectionReader(f, head.at(from), size-from), func(line []byte) bool {
		start := end
		end += int64(len(line))
		if len(bytes.TrimSpace(line)) > 0 {
			var e wire.TranscriptEntry
			if diskjson.Unmarshal(line, &e) == nil {
				see(e, start, end)
			}
		}
		return true
	})
	return end, size, err
}
