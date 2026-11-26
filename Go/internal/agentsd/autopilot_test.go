package agentsd

import (
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
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
	r := autopilotLeadRequest("/x", wire.ForgeItem{Node: wire.LineageNode{Kind: "issue", Ref: "o/r#7"}, Title: "Epic"})
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
