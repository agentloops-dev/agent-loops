package agentsd

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/flaky"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestCycletimeHistorySplitsAndCountsMissingStarts(t *testing.T) {
	flaky.UnderLoad(t, "#6927")
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	now := time.Now()
	repo := "github:alexec/agents"
	str := func(s string) *string { return &s }
	date := func(at time.Time) *wire.Date { v := wire.DateOf(at); return &v }
	ref := func(n int) string { return "github:issue:alexec/agents#" + strconv.Itoa(n) }
	closed, merged, level := str("closed"), str(lineage.OutcomeMerged), str("story")
	issue := func(n int, started *wire.Date) wire.LineageCostNode {
		return wire.LineageCostNode{Node: wire.LineageNode{Kind: lineage.KindIssue, Ref: ref(n)},
			Repo: str(repo), Banked: wire.LineageCost{Cost: map[string]float64{}},
			StartedAt: started, Level: level,
			Resolved: &wire.LineageResolved{Title: "Work", State: closed, Labels: []string{"enhancement", "story"}}}
	}
	pr := func(n, fixes int, opened, ended *wire.Date) wire.LineageCostNode {
		return wire.LineageCostNode{Node: wire.LineageNode{Kind: lineage.KindPR,
			Ref: "github:pull:alexec/agents#" + strconv.Itoa(n)},
			Repo: str(repo), Banked: wire.LineageCost{Cost: map[string]float64{}},
			Fixes: []string{ref(fixes)}, Outcome: merged, EndedAt: ended, OpenedAt: opened}
	}
	nodes := []wire.LineageCostNode{
		issue(1, date(now.Add(-10*time.Hour))),
		pr(11, 1, date(now.Add(-8*time.Hour)), date(now.Add(-2*time.Hour))),
		issue(2, date(now.Add(-26*time.Hour))),
		pr(12, 2, nil, date(now.Add(-25*time.Hour))),
		issue(3, nil),
		pr(13, 3, nil, date(now.Add(-time.Hour))),
		pr(14, 0, nil, date(now.Add(-time.Hour))),
		pr(15, 0, nil, date(now.AddDate(0, 0, -100))),
	}

	d.workMu.Lock()
	f := d.costLedgerLocked()
	for _, n := range nodes {
		f.Nodes[costKey(n.Node)] = n
	}
	d.workMu.Unlock()

	answer := c.call(t, 1, "cycletime/history", map[string]any{})
	var h wire.CycleTimeHistory
	if err := json.Unmarshal(answer, &h); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Hours map[string]struct {
			Samples json.RawMessage `json:"samples"`
		} `json:"hours"`
	}
	if err := json.Unmarshal(answer, &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw.Hours[hourStamp(now.Add(-time.Hour))].Samples); got != "[]" {
		t.Fatalf("no-start samples %s, want []", got)
	}
	if h.Now != hourStamp(now) || h.TimeZone == nil {
		t.Fatalf("now %q, zone %v", h.Now, h.TimeZone)
	}
	today := h.Days[dayStamp(now)]
	if len(today.Samples) != 1 || today.NoStart != 2 {
		t.Fatalf("today: %+v", today)
	}
	s := today.Samples[0]
	if s.Cycle != 8*3600 || s.Coding == nil || *s.Coding != 2*3600 || s.Pr == nil || *s.Pr != 6*3600 {
		t.Fatalf("sample: %+v", s)
	}
	if s.Level != "story" || s.Repo != repo {
		t.Fatalf("sample level %q repo %q", s.Level, s.Repo)
	}
	yesterday := h.Days[dayStamp(now.Add(-26*time.Hour))]
	if len(yesterday.Samples) != 1 || yesterday.NoStart != 0 {
		t.Fatalf("yesterday: %+v", yesterday)
	}
	if yesterday.Samples[0].Cycle != 3600 || yesterday.Samples[0].Coding != nil || yesterday.Samples[0].Pr != nil {
		t.Fatalf("yesterday's sample: %+v", yesterday.Samples[0])
	}
	if len(h.Days) != 2 {
		t.Fatalf("days: %+v", h.Days)
	}
	var samples, noStart int
	for _, b := range h.Hours {
		samples += len(b.Samples)
		noStart += int(b.NoStart)
	}
	if samples != 2 || noStart != 2 {
		t.Fatalf("hours: %d samples, %d no start", samples, noStart)
	}
}
