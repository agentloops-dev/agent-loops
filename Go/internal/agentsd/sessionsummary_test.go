package agentsd

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestSummariseWritesOnlyWhenAskedAndReadsEachTurnOnce(t *testing.T) {
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	a := seeded(t, d, t.TempDir(), func(a *wire.Agent) { a.State = wire.AgentStateRunning })
	var calls atomic.Int32
	var prompts []string
	d.summaryWrite = func(ctx context.Context, _ *Daemon, _ wire.Agent, prompt string) (string, string, error) {
		calls.Add(1)
		prompts = append(prompts, prompt)
		return "  It adds file names to the index.  ", "haiku", nil
	}
	say := func(ask, answer string) {
		t.Helper()
		for _, k := range []wire.TranscriptEntryKind{
			{UserMessage: &wire.TranscriptEntryKindUserMessage{Value: ask}},
			{AgentMessage: &wire.TranscriptEntryKindAgentMessage{Text: answer}},
		} {
			if _, err := d.Store.Append(a.ID, wire.TranscriptEntry{ID: store.NewUUID(), At: now(), Kind: k}); err != nil {
				t.Fatal(err)
			}
		}
	}
	wait := func(want wire.SessionSummaryState) *wire.SessionSummary {
		t.Helper()
		for range 200 {
			if s := d.Store.Agent(a.ID).Summary; s != nil && s.State == want {
				return s
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no summary in state %s: %+v", want, d.Store.Agent(a.ID).Summary)
		return nil
	}

	// Nothing is written until a client asks, and a session with no ended turn has none.
	say("first ask", "first answer")
	if s := d.Store.Agent(a.ID).Summary; s != nil || calls.Load() != 0 {
		t.Fatalf("written before asked: %+v", s)
	}
	got, err := d.summarise(wire.AgentRequest{AgentID: a.ID})
	must(t, err)
	if got.Text != "" || calls.Load() != 0 {
		t.Fatalf("one open turn: %+v, %d calls", got, calls.Load())
	}

	// A second turn closes the first.
	say("second ask", "second answer")
	got, err = d.summarise(wire.AgentRequest{AgentID: a.ID})
	must(t, err)
	if got.State != wire.SessionSummaryStateWriting {
		t.Fatalf("answered at once with %+v", got)
	}
	s := wait(wire.SessionSummaryStateWritten)
	if s.Text != "It adds file names to the index." || s.Model == nil || *s.Model != "haiku" || s.At == nil || s.ThroughTurn == nil {
		t.Fatalf("%+v", s)
	}
	if !strings.Contains(prompts[0], "first ask") || strings.Contains(prompts[0], "second ask") {
		t.Fatalf("prompt: %s", prompts[0])
	}

	// Asked again with no new ended turn: no call.
	if got, err = d.summarise(wire.AgentRequest{AgentID: a.ID}); err != nil || got.State != wire.SessionSummaryStateWritten || calls.Load() != 1 {
		t.Fatalf("%+v %v %d", got, err, calls.Load())
	}

	// A third turn: the last text and only the new turn are read; a failure keeps the text.
	say("third ask", "third answer")
	d.summaryWrite = func(context.Context, *Daemon, wire.Agent, string) (string, string, error) {
		return "", "", context.DeadlineExceeded
	}
	_, err = d.summarise(wire.AgentRequest{AgentID: a.ID})
	must(t, err)
	s = wait(wire.SessionSummaryStateFailed)
	if s.Text != "It adds file names to the index." {
		t.Fatalf("a failure lost the text: %+v", s)
	}
}

func TestSummaryIsCutAndItsInputIsBounded(t *testing.T) {
	if n := len([]rune(cleanSummary(strings.Repeat("é", 2000)))); n != summaryTextLimit {
		t.Fatalf("%d characters", n)
	}
	var turns []wire.TurnSummary
	for range 50 {
		turns = append(turns, wire.TurnSummary{ID: store.NewUUID(), Ask: &wire.TranscriptEntry{Kind: wire.TranscriptEntryKind{
			UserMessage: &wire.TranscriptEntryKindUserMessage{Value: strings.Repeat("x", 3000)}}}})
	}
	if p := summaryPrompt(strings.Repeat("y", 5000), turns); len(p) > summaryInputLimit+len("…") {
		t.Fatalf("%d bytes", len(p))
	}
}
