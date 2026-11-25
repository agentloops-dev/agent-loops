package store

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/diskjson"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7244: a session is never dropped, and its save never refused, for a large part.

func TestAnAgentRecordAtTheLargestIsKeptWhole(t *testing.T) {
	_, s, ids := summariesRoot(t)
	a := *s.Agent(ids[0])
	a.AvailableCommands = []wire.SlashCommand{{Name: "c"}}
	b, err := boundedRecord(&a)
	if err != nil || len(b) > AgentRecordMost {
		t.Fatalf("%d bytes, %v", len(b), err)
	}
	if !strings.Contains(string(b), `"name":"c"`) {
		t.Fatal("a record under the largest lost a part")
	}
}

func TestAnAgentRecordOverTheLargestIsSavedWithTheLargePartCut(t *testing.T) {
	root, s, ids := summariesRoot(t)
	a := *s.Agent(ids[0])
	big := strings.Repeat("x", 4096)
	for i := range 400 {
		a.AvailableCommands = append(a.AvailableCommands, wire.SlashCommand{Name: fmt.Sprint("c", i), Description: &big})
	}
	title := "kept"
	a.Title = &title
	var err error
	out := logged(t, func() { err = s.Save(a) })
	if err != nil {
		t.Fatalf("the save is refused: %v", err)
	}
	if !strings.Contains(out, "commands") || !strings.Contains(out, string(a.ID)) {
		t.Fatalf("the log does not say what was cut: %q", out)
	}
	b, err := os.ReadFile(s.Loc.Record(string(a.ID)))
	if err != nil || len(b) > AgentRecordMost {
		t.Fatalf("%d bytes, %v", len(b), err)
	}
	var got wire.Agent
	if err := diskjson.Unmarshal(b, &got); err != nil || got.Title == nil || *got.Title != "kept" || len(got.AvailableCommands) != 0 {
		t.Fatalf("the session is not whole but for its commands: %v %+v", err, got.Title)
	}
	again, _ := reopen(t, root)
	if again.Agent(a.ID) == nil {
		t.Fatal("the session is gone after a start")
	}
}

func TestAnAgentRecordWithOnlyLargeLabelsAndTitleIsCutToo(t *testing.T) {
	_, s, ids := summariesRoot(t)
	a := *s.Agent(ids[0])
	long := strings.Repeat("y", AgentRecordMost)
	a.Title = &long
	if b, err := boundedRecord(&a); err != nil || len(b) > AgentRecordMost {
		t.Fatalf("%d bytes, %v", len(b), err)
	}
}

func TestASummaryOverTheLargestIsLeftOutAndTheRecordIsReadInFull(t *testing.T) {
	root, s, ids := summariesRoot(t)
	a := *s.Agent(ids[1])
	a.ArchiveWhy = ptr(strings.Repeat("z", summaryMost+10))
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	var err error
	out := logged(t, func() { err = s.WriteSummaries() })
	if err != nil || !strings.Contains(out, "left out") {
		t.Fatalf("%v %q", err, out)
	}
	if b, _ := os.ReadFile(s.Loc.Summaries()); len(b) > summaryMost {
		t.Fatalf("the file is %d bytes: it holds the large summary", len(b))
	}
	again, _ := reopen(t, root)
	if got := again.Agent(a.ID); got == nil || got.ArchiveWhy == nil || len(*got.ArchiveWhy) != summaryMost+10 {
		t.Fatal("the record was not read in full")
	}
}

func TestALineOverTheLargestIsCutAndStillDecodes(t *testing.T) {
	line := fmt.Sprintf(`{"event":{"sentence":%q,"details":{"a":%q},"consequences":[]}}`, strings.Repeat("s", 100<<10), "d")
	cut, was := CutLine([]byte(line), 64<<10)
	if !was || len(cut) > 64<<10 {
		t.Fatalf("%v %d", was, len(cut))
	}
	var v struct {
		Event struct {
			Sentence string            `json:"sentence"`
			Details  map[string]string `json:"details"`
		} `json:"event"`
	}
	if err := diskjson.Unmarshal(cut, &v); err != nil || !strings.HasSuffix(v.Event.Sentence, "…") || v.Event.Details["a"] != "d" {
		t.Fatalf("%v %+v", err, v)
	}
	if same, was := CutLine([]byte(line[:100]), 64<<10); was || string(same) != line[:100] {
		t.Fatal("a small line changed")
	}
}

func ptr[T any](v T) *T { return &v }
