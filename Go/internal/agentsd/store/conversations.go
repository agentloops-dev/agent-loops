package store

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	kinds "github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The bounds of a conversation (#6677, #6706): 10 MB a chat and 90 days, live and Forever
// ones too. The oldest go, with no summary.
const (
	ConversationAge  = 90 * 24 * time.Hour
	TranscriptBytes  = 10 << 20
	transcriptRecord = 4 << 20
	// AppToolsBytes bounds a session's record of its app tool calls; each call's arguments
	// and answer are cut short before they are written (#3687).
	AppToolsBytes  = 2 << 20
	appToolsRecord = 1 << 20
)

// transcript is an agent's transcript: every read and write of one is through it.
func (s *Store) transcript(id wire.UUID) *FolderLog {
	return mustLog(OpenFolderLog(kinds.Spec{Name: "machine.transcripts", Kind: "log", Scope: string(id), Bounds: kinds.Bounds{
		MaxRecordBytes: transcriptRecord, MaxBytes: TranscriptBytes, MaxAge: ConversationAge,
		SweepRequested: func(context.Context) {
			if s.OnLarge != nil {
				s.OnLarge()
			}
		},
	}}, s.Loc.Transcript(string(id)), &s.mu, RecordStamp))
}

// appToolsMu is held for an append to any record of app tool calls.
var appToolsMu sync.Mutex

// AppTools is the record of an agent's calls to the app's tools.
func (s *Store) AppTools(id wire.UUID) *FolderLog {
	return mustLog(OpenFolderLog(kinds.Spec{Name: "machine.app-tools", Kind: "log", Scope: string(id), Bounds: kinds.Bounds{
		MaxRecordBytes: appToolsRecord, MaxBytes: AppToolsBytes, MaxAge: ConversationAge,
		SweepRequested: func(context.Context) {
			if s.OnLarge != nil {
				s.OnLarge()
			}
		},
	}}, filepath.Join(s.Loc.Agent(string(id)), "app-tools.jsonl"), &appToolsMu, RecordStamp))
}

// mustLog is a log whose spec is this file's, so it is always whole.
func mustLog(l *FolderLog, err error) *FolderLog {
	if err != nil {
		panic(err)
	}
	return l
}

// TranscriptSize is where a transcript ends, in bytes ever written to it: what a reader
// that keeps its own place compares with (said.json).
func (s *Store) TranscriptSize(id wire.UUID) (int64, error) { return s.transcript(id).Size() }

// OpenTranscript is a transcript's kept lines as they are, to look through and close.
func (s *Store) OpenTranscript(id wire.UUID) (io.ReadCloser, error) {
	f, _, _, err := s.transcript(id).openLog()
	return f, err
}

// TrimConversation applies the bounds to an agent's conversation, and says whether any of
// its transcript went: what is made from the transcript is then out of date.
func (s *Store) TrimConversation(ctx context.Context, id wire.UUID, now time.Time) (bool, error) {
	removeLeftTrims(s.Loc, string(id))
	before := now.Add(-ConversationAge)
	changed, err := s.transcript(id).trim(ctx, before)
	if terr := s.AppTools(id).Trim(ctx, before); err == nil {
		err = terr
	}
	return changed, err
}

// boundEntry is an entry's line within the record limit: an entry over it is kept as a
// notice with the same id and time, so nothing that points at it is lost.
func boundEntry(e *wire.TranscriptEntry, line []byte) ([]byte, error) {
	if len(line) <= transcriptRecord {
		return line, nil
	}
	*e = tooLarge(e.ID, e.At, int64(len(line)))
	return diskjson.Marshal(*e)
}

// tooLarge is the notice in place of an entry of size bytes, with its id and time.
func tooLarge(id wire.UUID, at wire.Date, size int64) wire.TranscriptEntry {
	detail := "The most one entry of a chat can be is 4 MB."
	log.Printf("store: an entry of %d bytes is over the limit of %d, so a notice is kept in its place", size, transcriptRecord)
	return wire.TranscriptEntry{ID: id, At: at, Kind: wire.TranscriptEntryKind{Notice: &wire.TranscriptEntryKindNotice{Value: wire.SessionNotice{
		Severity: "warning", Title: fmt.Sprintf("An entry of %d MB was too large to keep", size>>20), Detail: &detail}}}}
}

// removeLeftTrims removes the temporary files of trims that a stop cut off. One
// sweep runs at a time, so none is in use.
func removeLeftTrims(loc Locations, id string) {
	left, _ := filepath.Glob(filepath.Join(globEscape(loc.Agent(id)), ".*.jsonl.*.tmp"))
	for _, p := range left {
		_ = os.Remove(p) // the next sweep tries again
	}
}

// StartRemoved is the line each client shows where a chat's start was, when its trims took
// any of it (#7405), with why: the 90-day age, the size limit, or either for a trim made
// before the cause was kept. Nil when the chat has all its lines.
func StartRemoved(lines Lines) *string {
	if lines.First() == 0 {
		return nil
	}
	days, mb := int(ConversationAge/(24*time.Hour)), TranscriptBytes>>20
	var why string
	switch r := lines.Removed(); {
	case r.Age && !r.Size:
		why = fmt.Sprintf("it was older than %d days", days)
	case r.Size && !r.Age:
		why = fmt.Sprintf("a chat keeps only its last %d MB", mb)
	default:
		why = fmt.Sprintf("it was older than %d days or the chat was more than %d MB", days, mb)
	}
	return new("The start of this chat was removed because " + why + ".")
}
