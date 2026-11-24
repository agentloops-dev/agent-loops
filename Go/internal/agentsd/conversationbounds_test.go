package agentsd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A live conversation loses its lines past 90 days, and then past 10 MB (#6706): its pages,
// turns and search agree, and the lines kept have the indexes they had.
func TestTheSweepBoundsALiveConversation(t *testing.T) {
	clock := atTime(t, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) { a.State = wire.AgentStateRunning })
	say := func(ago time.Duration, ask, answer string) {
		t.Helper()
		at := wire.DateOf(clock.now().Add(-ago))
		for _, k := range []wire.TranscriptEntryKind{
			{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: ask}},
			{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: answer}},
		} {
			if _, err := d.Store.Append(a.ID, wire.TranscriptEntry{ID: store.NewUUID(), At: at, Kind: k}); err != nil {
				t.Fatal(err)
			}
		}
	}
	turns := func() wire.TurnsPage {
		t.Helper()
		p, err := d.turns(wire.TurnsRequest{AgentID: a.ID, Limit: 50})
		must(t, err)
		return p
	}
	day := 24 * time.Hour
	say(100*day, "first", "oldest words")
	say(95*day, "second", "older words")
	say(10*day, "third", "newer words")
	say(day, "fourth", "newest words")
	if p := turns(); len(p.Turns) != 3 || p.OpenStart != 6 {
		t.Fatalf("%+v", p)
	}
	if d.saidLine(a.ID, "oldest words") == "" {
		t.Fatal("not found before the sweep")
	}
	d.trimConversations()
	if p := turns(); len(p.Turns) != 1 || p.Turns[0].Start != 4 || p.FirstTurn != 0 || p.OpenStart != 6 {
		t.Fatalf("turns after the sweep: %+v", p)
	}
	page, err := d.transcript(wire.TranscriptRequest{AgentID: a.ID, Limit: 100})
	if err != nil || page.FirstIndex != 4 || page.Total != 8 || len(page.Entries) != 4 {
		t.Fatalf("page after the sweep: %d of %d, %d entries, %v", page.FirstIndex, page.Total, len(page.Entries), err)
	}
	before := int64(2)
	if page, err := d.transcript(wire.TranscriptRequest{AgentID: a.ID, Before: &before, Limit: 100}); err != nil || len(page.Entries) != 0 {
		t.Fatalf("a page of what went: %d entries, %v", len(page.Entries), err)
	}
	if d.saidLine(a.ID, "oldest words") != "" || d.saidLine(a.ID, "newest words") == "" {
		t.Fatal("the search is not the transcript's")
	}

	for range 24 {
		say(time.Hour, "more", strings.Repeat("x", 1<<20))
	}
	select {
	case <-d.largeChat:
	default:
		t.Fatal("no sweep was asked for past twice 10 MB")
	}
	d.trimConversations()
	if info, err := os.Stat(d.Store.Loc.Transcript(string(a.ID))); err != nil || info.Size() > store.TranscriptBytes+1024 {
		t.Fatalf("%d bytes after the sweep, %v", info.Size(), err)
	}
	if p := turns(); p.OpenStart != 54 || len(p.Turns) == 0 {
		t.Fatalf("turns after the size sweep: %d, open at %d", len(p.Turns), p.OpenStart)
	}

	clock.add(91 * day)
	d.trimConversations()
	page, err = d.transcript(wire.TranscriptRequest{AgentID: a.ID, Limit: 100})
	if p := turns(); err != nil || len(p.Turns) != 0 || page.FirstIndex > p.OpenStart || len(page.Entries) != 0 {
		t.Fatalf("after all of it aged out: %+v, %+v, %v", p, page, err)
	}
	if d.Store.Agent(a.ID) == nil {
		t.Fatal("the live agent went with its conversation")
	}
}
