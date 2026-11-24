package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	kinds "github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

var logDay = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// logAt is a log of small bounds and its records' lines.
func logAt(t *testing.T, bounds kinds.Bounds) (*FolderLog, func(id string, at time.Time) []byte) {
	t.Helper()
	l, err := OpenFolderLog(kinds.Spec{Name: "test", Kind: "log", Scope: "one", Bounds: bounds}, filepath.Join(t.TempDir(), "log.jsonl"), &sync.Mutex{}, RecordStamp)
	must(t, err)
	return l, func(id string, at time.Time) []byte {
		return []byte(fmt.Sprintf(`{"id":%q,"at":%q}`, id, diskjson.FormatDate(wire.DateOf(at))))
	}
}

func smallBounds() kinds.Bounds {
	return kinds.Bounds{MaxRecordBytes: 1000, MaxBytes: 1000, MaxAge: ConversationAge}
}

func readIDs(t *testing.T, l *FolderLog, cursor string, limit int) (string, string) {
	t.Helper()
	records, next, err := l.Read(context.Background(), cursor, limit)
	must(t, err)
	var ids []string
	for _, r := range records {
		ids = append(ids, r.ID)
	}
	return strings.Join(ids, ","), next
}

func TestAFolderLogKeepsItsBounds(t *testing.T) {
	ctx := context.Background()
	bounds := smallBounds()
	asked := 0
	bounds.SweepRequested = func(context.Context) { asked++ }
	l, line := logAt(t, bounds)
	if _, err := OpenFolderLog(kinds.Spec{Name: "test", Kind: "log", Scope: "one"}, "x", &sync.Mutex{}, RecordStamp); err == nil {
		t.Fatal("a log with no bounds opened")
	}
	var limit *kinds.LimitError
	if err := l.Append(ctx, "big", logDay, []byte(strings.Repeat("x", 1001))); !errors.As(err, &limit) {
		t.Fatalf("over the record limit: %v", err)
	}
	for i := range 50 {
		id, at := fmt.Sprint(i), logDay.Add(time.Duration(i)*time.Hour)
		must(t, l.Append(ctx, id, at, line(id, at)))
		must(t, l.Append(ctx, id, at, line(id, at))) // a retry is one record
	}
	if asked == 0 {
		t.Fatal("no sweep was asked for past twice the bytes")
	}
	if n := l.count(); n != 50 {
		t.Fatalf("%d lines", n)
	}
	before, _ := l.Size()
	must(t, l.Trim(ctx, time.Time{}))
	if info, err := os.Stat(l.file); err != nil || info.Size() > 1000+maxHead {
		t.Fatalf("trimmed to %v, %v", info.Size(), err)
	}
	// The place and number of a line kept are what they were.
	if after, _ := l.Size(); after != before || l.count() != 50 {
		t.Fatalf("size %d, was %d; %d lines", after, before, l.count())
	}
	must(t, l.Trim(ctx, logDay.Add(48*time.Hour)))
	if ids, _ := readIDs(t, l, "", 10); ids != "48,49" {
		t.Fatalf("kept %s", ids)
	}
	must(t, l.Trim(ctx, logDay.Add(1000*time.Hour)))
	if ids, _ := readIDs(t, l, "", 10); ids != "" {
		t.Fatalf("kept %s", ids)
	}
	must(t, l.Append(ctx, "new", logDay.Add(2000*time.Hour), line("new", logDay.Add(2000*time.Hour))))
	if ids, _ := readIDs(t, l, "", 10); ids != "new" || l.count() != 51 {
		t.Fatalf("after everything went: %s, %d lines", ids, l.count())
	}
}

func TestAFolderLogsCursorHoldsAcrossATrim(t *testing.T) {
	ctx := context.Background()
	l, line := logAt(t, smallBounds())
	for i := range 6 {
		at := logDay.Add(time.Duration(i) * time.Hour)
		must(t, l.Append(ctx, fmt.Sprint(i), at, line(fmt.Sprint(i), at)))
	}
	early := ""
	if ids, next := readIDs(t, l, "", 1); ids != "0" {
		t.Fatal(ids)
	} else {
		early = next
	}
	ids, next := readIDs(t, l, early, 3)
	if ids != "1,2,3" {
		t.Fatal(ids)
	}
	must(t, l.Trim(ctx, logDay.Add(3*time.Hour)))
	if ids, _ := readIDs(t, l, next, 9); ids != "4,5" {
		t.Fatalf("on from a kept record: %s", ids)
	}
	if ids, _ := readIDs(t, l, early, 9); ids != "3,4,5" {
		t.Fatalf("on from a trimmed record: %s", ids)
	}
	if ids, again := readIDs(t, l, next, 9); again == next || ids == "" {
		t.Fatal("the cursor did not move")
	}
	if _, _, err := l.Read(ctx, "bad", 1); err == nil {
		t.Fatal("a bad cursor was read")
	}
}

// Appends made while trims run are all there after, in order.
func TestAppendsDuringTrimsAreKept(t *testing.T) {
	ctx := context.Background()
	bounds := smallBounds()
	bounds.MaxBytes = 1 << 20
	l, line := logAt(t, bounds)
	done := make(chan error)
	go func() {
		for i := range 300 {
			at := logDay.Add(time.Duration(i) * time.Second)
			if err := l.Append(ctx, fmt.Sprint(i), at, line(fmt.Sprint(i), at)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; ; i++ {
		select {
		case err := <-done:
			must(t, err)
			must(t, l.Trim(ctx, logDay.Add(100*time.Second)))
			records, _, err := l.Read(ctx, "", 1000)
			must(t, err)
			if len(records) != 200 || records[0].ID != "100" || records[199].ID != "299" || l.count() != 300 {
				t.Fatalf("%d records, %d lines", len(records), l.count())
			}
			return
		default:
			must(t, l.Trim(ctx, logDay.Add(time.Duration(min(i, 100))*time.Second)))
		}
	}
}

func TestATranscriptsIndexesHoldAcrossATrim(t *testing.T) {
	s, err := Open(t.TempDir())
	must(t, err)
	id := NewUUID()
	now := logDay.Add(ConversationAge)
	for i := range 6 {
		must(t, errOnly(s.Append(id, wire.TranscriptEntry{ID: NewUUID(), At: wire.DateOf(logDay.Add(time.Duration(i-3) * time.Hour)),
			Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: fmt.Sprint(i)}}})))
	}
	was, err := s.LineEnds(id)
	must(t, err)
	must(t, s.AppTools(id).Append(context.Background(), "x", now, []byte(`{"at":"2026-01-01T00:00:00.000Z"}`)))
	changed, err := s.TrimConversation(context.Background(), id, now)
	must(t, err)
	if !changed {
		t.Fatal("nothing was trimmed")
	}
	if records, _, _ := s.AppTools(id).Read(context.Background(), "", 10); len(records) != 0 {
		t.Fatal("an old tool call was kept")
	}
	for name, lines := range map[string]func() Lines{
		"looked at before": func() Lines { return was },
		"looked at after":  func() Lines { l, _ := s.LineEnds(id); return l },
		"on a fresh start": func() Lines { f, _ := Open(s.Loc.Root); l, _ := f.LineEnds(id); return l },
	} {
		entries, at, err := s.Entries(id, lines(), 0, 99)
		if err != nil || len(entries) != 3 || at[0] != 3 || entries[0].Kind.AgentMessage.Text != "3" || lines().Total() != 6 {
			t.Fatalf("%s: %d entries at %v, %v", name, len(entries), at, err)
		}
	}
	n, err := s.Append(id, wire.TranscriptEntry{ID: NewUUID(), At: wire.DateOf(now), Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "6"}}})
	if err != nil || n != 6 {
		t.Fatalf("the next line is %d, %v", n, err)
	}
	fresh, _ := Open(s.Loc.Root)
	if n, err := fresh.Append(id, wire.TranscriptEntry{ID: NewUUID(), At: wire.DateOf(now), Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: "7"}}}); err != nil || n != 7 {
		t.Fatalf("the next line after a start is %d, %v", n, err)
	}
	if changed, _ := s.TrimConversation(context.Background(), id, now); changed {
		t.Fatal("a conversation within its bounds was trimmed")
	}
}

func TestAnEntryOverTheRecordLimitIsKeptAsANotice(t *testing.T) {
	s, err := Open(t.TempDir())
	must(t, err)
	id, big := NewUUID(), wire.TranscriptEntry{ID: NewUUID(), At: wire.DateOf(logDay),
		Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: strings.Repeat("x", transcriptRecord)}}}
	_, got, err := s.AppendOne(id, big)
	if err != nil || got.ID != big.ID || got.At != big.At || got.Kind.Notice == nil {
		t.Fatalf("%v %v", got.ID, err)
	}
	if entries, _, _ := s.Transcript(id, 0); len(entries) != 1 || entries[0].Kind.Notice == nil {
		t.Fatal("the notice was not written")
	}
}
