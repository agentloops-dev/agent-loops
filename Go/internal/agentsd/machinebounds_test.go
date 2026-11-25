package agentsd

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// logsOf is what fn logs.
func logsOf(fn func()) string {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	fn()
	return buf.String()
}

// #7244: the largest record of events.jsonl, events-state.json, said.json and the turns cache.

func TestAnEventLineOverTheLargestIsCutAndSaysSo(t *testing.T) {
	msg := strings.Repeat("m", eventLineMost*2)
	e := wire.Event{Position: 1, Name: "x", Sentence: msg, Details: map[string]string{}, Consequences: []wire.Consequence{}}
	var b []byte
	out := logsOf(func() { b = encodeEventLine(eventLine{Event: &e}) })
	if len(b) > eventLineMost+1 || !strings.Contains(out, "kept cut") {
		t.Fatalf("%d bytes; log %q", len(b), out)
	}
}

func TestAnEventLineAtTheLargestIsKeptWhole(t *testing.T) {
	e := wire.Event{Position: 1, Name: "x", Sentence: "short", Details: map[string]string{}, Consequences: []wire.Consequence{}}
	if b := encodeEventLine(eventLine{Event: &e}); !strings.Contains(string(b), `"short"`) {
		t.Fatal(string(b))
	}
}

func TestTheEventStateOverTheLargestLosesEntriesNotThePosition(t *testing.T) {
	s := eventState{NextPosition: 77, DiskLevels: map[string]string{}, CostCrossings: map[string][]string{}}
	for i := range 4000 {
		s.CostCrossings[strings.Repeat("k", 100)+fmt.Sprint(i)] = []string{strings.Repeat("v", 100)}
	}
	var got eventState
	out := logsOf(func() { got = boundEventState(s) })
	if got.NextPosition != 77 || len(got.CostCrossings) == 0 || len(got.CostCrossings) >= 4000 || !strings.Contains(out, "not kept") {
		t.Fatalf("%d entries; log %q", len(got.CostCrossings), out)
	}
	if len(s.CostCrossings) != 4000 {
		t.Fatal("the daemon's own state was cut")
	}
}

func TestTheSaidTailIsWithinTheLargest(t *testing.T) {
	if got := store.CutText(strings.Repeat("é", saidBytes), saidBytes); len(got) > saidBytes {
		t.Fatalf("%d", len(got))
	}
}

func TestATurnLineOverTheLargestIsCut(t *testing.T) {
	big := strings.Repeat("o", turnLineMost*2)
	turn := wire.TurnSummary{ID: "T", Outcome: []wire.TranscriptEntry{{Kind: wire.TranscriptEntryKind{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: big}}}}}
	var b []byte
	logsOf(func() { b = encodeTurns([]wire.TurnSummary{turn}) })
	if len(b) > turnLineMost+1 || len(b) == 0 {
		t.Fatalf("%d", len(b))
	}
}
