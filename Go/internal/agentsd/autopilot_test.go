package agentsd

import (
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/citimings"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Autopilot (#5898): absent is off with no rows; the mode is kept; the status keeps the newest 100 rows.
func TestAutopilotSettingAndStatus(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	url := store.FolderURL(dir)

	var st wire.AutopilotStatus
	mustUnmarshal(t, c.call(t, 2, "autopilot/status", map[string]any{"folder": url}), &st)
	if st.Mode != wire.AutopilotModeOff || len(st.Rows) != 0 {
		t.Fatalf("%+v", st)
	}
	mustUnmarshal(t, c.call(t, 3, "autopilot/set", map[string]any{"folder": url, "mode": "high"}), &st)
	if st.Mode != wire.AutopilotModeHigh {
		t.Fatalf("%+v", st)
	}
	if p, _, _ := d.folderRecord(dir); p.Autopilot == nil || p.Autopilot.Mode != wire.AutopilotModeHigh || p.Autopilot.SetBy == "" {
		t.Fatalf("%+v", p.Autopilot)
	}
	for i := range 120 {
		ref := "#" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		if err := d.autopilotNote(dir, wire.AutopilotRow{Ref: ref, Kind: wire.AutopilotRowKindLane, State: wire.AutopilotRowStateWaiting, Reason: "on hold"}); err != nil {
			t.Fatal(err)
		}
	}
	mustUnmarshal(t, c.call(t, 4, "autopilot/status", map[string]any{"folder": url}), &st)
	if len(st.Rows) != 100 || st.LastRunAt == nil {
		t.Fatalf("%d rows", len(st.Rows))
	}
	mustUnmarshal(t, c.call(t, 5, "autopilot/set", map[string]any{"folder": url, "mode": "off"}), &st)
	if st.Mode != wire.AutopilotModeOff || len(st.Rows) != 0 {
		t.Fatalf("%+v", st)
	}
	if p, _, _ := d.folderRecord(dir); p.Autopilot != nil {
		t.Fatalf("%+v", p.Autopilot)
	}
}

func epicRow(ref, priority string, hold bool) wire.ForgeNextRow {
	i := wire.ForgeItem{Node: wire.LineageNode{Ref: ref}}
	if priority != "" {
		i.Priority = &priority
	}
	return wire.ForgeNextRow{Item: i, OnHold: hold}
}

// The loop starts the top epic with no lead, one a run, and says why each other waits (#5899).
func TestAutopilotLeadPlan(t *testing.T) {
	t.Parallel()
	epics := []autopilotEpic{
		{row: epicRow("#1", "", false), led: true},
		{row: epicRow("#2", "", true)},
		{row: epicRow("#3", "", false), dismissed: true},
		{row: epicRow("#4", "medium", false)},
		{row: epicRow("#5", "high", false)},
	}
	why := func(o []autopilotOutcome) []string {
		var s []string
		for _, x := range o {
			if x.start {
				s = append(s, x.epic.Node.Ref+" start")
			} else {
				s = append(s, x.epic.Node.Ref+" "+x.why)
			}
		}
		return s
	}
	eq := func(got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%q, want %q", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%q, want %q", got, want)
			}
		}
	}
	ok := autopilotRoom{openPRs: 1, prLimit: 5, capacity: wire.CapacityLevelOk}
	eq(why(autopilotLeadPlan(epics, wire.AutopilotModeOn, ok)), []string{"#2 on hold", "#4 start", "#5 one lead a run"})
	eq(why(autopilotLeadPlan(epics, wire.AutopilotModeHigh, ok)), []string{"#2 on hold", "#4 mode high", "#5 start"})
	full := ok
	full.limit = true
	eq(why(autopilotLeadPlan(epics, wire.AutopilotModeOn, full)), []string{"#2 on hold", "#4 limit running", "#5 limit running"})
	full = ok
	full.openPRs = 5
	eq(why(autopilotLeadPlan(epics[3:4], wire.AutopilotModeOn, full)), []string{"#4 limit open-prs"})
	full = ok
	full.capacity = wire.CapacityLevelNear
	eq(why(autopilotLeadPlan(epics[3:4], wire.AutopilotModeOn, full)), []string{"#4 capacity near"})
	full.capacity = wire.CapacityLevelFull
	eq(why(autopilotLeadPlan(epics[3:4], wire.AutopilotModeOn, full)), []string{"#4 capacity full"})
	if autopilotCanStart(epics[:3], wire.AutopilotModeOn) {
		t.Fatal("none can start")
	}
}

// The lead's request is Start lead's: Auto, the lead prompt, the labels, no worktree.
func TestAutopilotLeadRequest(t *testing.T) {
	t.Parallel()
	r := autopilotLeadRequest("/x", wire.ForgeItem{Node: wire.LineageNode{Kind: "issue", Ref: "o/r#7"}, Title: "Epic"}, "")
	if r.Worktree != nil || r.RuntimeID != autoRuntimeID || len(r.Labels) == 0 || r.Labels[len(r.Labels)-1] != "lead" {
		t.Fatalf("%+v", r)
	}
}

// A hear wakes the loop once, for the events of autopilotWakes only.
func TestAutopilotHear(t *testing.T) {
	t.Parallel()
	d := &Daemon{autopilotKick: make(chan struct{}, 1)}
	d.autopilotHear(wire.Event{Name: "agent.started"})
	d.autopilotHear(wire.Event{Name: "pr.merged"})
	d.autopilotHear(wire.Event{Name: "agent.ended"})
	if len(d.autopilotKick) != 1 {
		t.Fatal("one wake")
	}
}

func ciRun(ref string, made int64, conclusion string, attempt int) citimings.Run {
	return citimings.Run{Ref: ref, Attempt: attempt, Workflow: "ci.yml", Default: true, Made: made, Ended: 600, Conclusion: conclusion}
}

// Main is red from the end of the first failed run that runs on to the newest (#5902).
func TestMainRedSince(t *testing.T) {
	t.Parallel()
	if _, red := mainRedSince(nil, "ci.yml"); red {
		t.Fatal("no runs")
	}
	green := []citimings.Run{ciRun("a", 100, "failure", 1), ciRun("b", 200, "success", 1)}
	if _, red := mainRedSince(green, "ci.yml"); red {
		t.Fatal("latest is green")
	}
	runs := []citimings.Run{ciRun("a", 100, "success", 1), ciRun("b", 200, "failure", 1), ciRun("c", 300, "cancelled", 1), ciRun("d", 400, "timed_out", 1)}
	runs = append(runs, citimings.Run{Ref: "e", Attempt: 1, Workflow: "ci.yml", Made: 500, Conclusion: "success"}, // a PR run
		citimings.Run{Ref: "f", Attempt: 1, Workflow: "other.yml", Default: true, Made: 600, Conclusion: "success"})
	since, red := mainRedSince(runs, "ci.yml")
	if !red || since.Unix() != 800 {
		t.Fatalf("%v %v", since, red)
	}
	// A re-run that passes makes the run green.
	runs = append(runs, ciRun("d", 400, "success", 2))
	if _, red := mainRedSince(runs, "ci.yml"); red {
		t.Fatal("re-run passed")
	}
}

// An epic at 2× its estimate is paused; the plan puts it ahead of the started lead, with its lead (#5902).
func TestAutopilotPausePlan(t *testing.T) {
	t.Parallel()
	lead := &wire.ForgeLead{Session: "x"}
	epics := []autopilotEpic{
		{row: epicRow("#1", "", false), led: true, over: "over budget", lead: lead},
		{row: epicRow("#2", "", false)},
	}
	ok := autopilotRoom{prLimit: 5, capacity: wire.CapacityLevelOk}
	out := autopilotLeadPlan(epics, wire.AutopilotModeOn, ok)
	if len(out) != 2 || !out[0].over || out[0].lead != lead || !out[1].start {
		t.Fatalf("%+v", out)
	}
	red := ok
	red.mainRed = true
	out = autopilotLeadPlan(epics, wire.AutopilotModeOn, red)
	if len(out) != 2 || !out[0].over || out[1].start || out[1].why != "main red" {
		t.Fatalf("%+v", out)
	}
	if autopilotCanStart(epics[:1], wire.AutopilotModeOn) {
		t.Fatal("over budget cannot start")
	}
}

// A pause notes a paused row once, and a resume says so (#5902).
func TestAutopilotPauseResume(t *testing.T) {
	t.Parallel()
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	d.autopilotPause(dir, "o/r#9", wire.AutopilotRowKindLead, "over budget", nil)
	if d.autopilotRowState(dir, "o/r#9") != wire.AutopilotRowStatePaused {
		t.Fatal("paused")
	}
	d.autopilotPause(dir, "o/r#9", wire.AutopilotRowKindLead, "over budget", nil)
	d.autopilotResume(dir, "o/r#9", wire.AutopilotRowKindLead, "back under its estimate")
	if d.autopilotRowState(dir, "o/r#9") != wire.AutopilotRowStateWaiting {
		t.Fatal("resumed")
	}
}

// A lead that stopped or ended for 10 minutes is replaced, except one a person stopped; an epic
// restarted twice in a day is paused (#5901).
func TestAutopilotStalledAndCapped(t *testing.T) {
	t.Parallel()
	at := wire.Date(10000)
	mk := func(s wire.AgentState, why *wire.EndedReason, ago float64) wire.Agent {
		return wire.Agent{State: s, EndedReason: why, LastActivityAt: at - wire.Date(ago)}
	}
	died, byPerson := wire.EndedReasonProcessDied, wire.EndedReasonCancelled
	for name, c := range map[string]struct {
		a    wire.Agent
		want bool
	}{
		"stopped 11m":     {mk(wire.AgentStateStopped, &died, 660), true},
		"stopped 5m":      {mk(wire.AgentStateStopped, &died, 300), false},
		"finished 11m":    {mk(wire.AgentStateFinished, nil, 660), true},
		"person's stop":   {mk(wire.AgentStateStopped, &byPerson, 6000), false},
		"waiting on user": {mk(wire.AgentStateWaitingOnUser, nil, 6000), false},
		"running":         {mk(wire.AgentStateRunning, nil, 6000), false},
	} {
		if got := autopilotStalled(c.a, at); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	e := autopilotEpic{row: epicRow("#9", "", false), prev: "A", capped: true}
	o := autopilotLeadPlan([]autopilotEpic{e}, wire.AutopilotModeOn, autopilotRoom{capacity: wire.CapacityLevelOk})
	if len(o) != 1 || o[0].start || !o[0].paused || o[0].why != autopilotLeadStopping {
		t.Fatalf("%+v", o)
	}
	e.capped = false
	o = autopilotLeadPlan([]autopilotEpic{e}, wire.AutopilotModeOn, autopilotRoom{capacity: wire.CapacityLevelOk})
	if !o[0].start || o[0].prev != "A" {
		t.Fatalf("%+v", o)
	}
	if p := autopilotLeadRequest("/x", e.row.Item, "A").Prompt; !strings.Contains(p, "read_session") || !strings.Contains(p, "A") {
		t.Fatal(p)
	}
}
