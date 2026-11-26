package agentsd

import (
	"strings"
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

// A lane's request (#5900): Auto, the forge's prompt, the labels #<n> and lane, the Software
// Engineer persona, no worktree; with no forge prompt, the issue's title.
func TestAutopilotLaneRequest(t *testing.T) {
	t.Parallel()
	issue := wire.ForgeItem{Node: wire.LineageNode{Kind: "issue", Ref: "github:o/r#7"}, Title: "Fix it"}
	r := autopilotLaneRequest("/x", issue, &wire.ForgeStart{Prompt: "Work on this."})
	if r.Worktree != nil || r.RuntimeID != autoRuntimeID || r.Prompt != "Work on this." || r.Persona == nil || *r.Persona != "software-engineer" {
		t.Fatalf("%+v", r)
	}
	if len(r.Labels) != 2 || r.Labels[0] != "#7" || r.Labels[1] != "lane" {
		t.Fatalf("%q", r.Labels)
	}
	if r = autopilotLaneRequest("/x", issue, nil); r.Prompt != "#7: Fix it" {
		t.Fatalf("%q", r.Prompt)
	}
}

// The room says why it has no place, in the lead's words (#5899, #5900).
func TestAutopilotRoomWait(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		room autopilotRoom
		want string
	}{
		{autopilotRoom{}, ""},
		{autopilotRoom{limit: true}, "limit running"},
		{autopilotRoom{openPRs: 5, prLimit: 5}, "limit open-prs"},
		{autopilotRoom{capacity: wire.CapacityLevelNear}, "capacity near"},
		{autopilotRoom{capacity: wire.CapacityLevelFull}, "capacity full"},
	} {
		if got := c.room.wait(); got != c.want {
			t.Fatalf("%+v: %q, want %q", c.room, got, c.want)
		}
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

// A child is a stray (#5944) only when it is the epic's one ready open child and blocks none.
func TestAutopilotStrayOf(t *testing.T) {
	t.Parallel()
	n := func(ref string) wire.LineageNode { return wire.LineageNode{Ref: ref} }
	c := func(ref, state string, blockers ...planRef) planChild {
		return planChild{item: wire.ForgeItem{Node: n(ref), State: state}, blockedBy: blockers}
	}
	open := func(ref string) planRef { return planRef{node: n(ref), open: true} }
	closed := func(ref string) planRef { return planRef{node: n(ref)} }
	for _, tc := range []struct {
		name     string
		children []planChild
		want     bool
	}{
		{"one open", []planChild{c("#1", "closed"), c("#2", "open", closed("#1"))}, true},
		{"two ready", []planChild{c("#2", "open"), c("#3", "open")}, false},
		{"blocks another", []planChild{c("#2", "open"), c("#3", "open", open("#2"))}, false},
		{"other waits on outside", []planChild{c("#2", "open"), c("#3", "open", open("#9"))}, true},
		{"absent", []planChild{c("#3", "open")}, false},
	} {
		if got := strayOf(tc.children, n("#2")); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
