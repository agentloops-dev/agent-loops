package agentsd

import (
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

var scheduleNow = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

const schedDay = 24 * time.Hour

// schedEpic is an open epic numbered n for a schedule test, with one open child and the priority p.
type schedEpic struct {
	n        int
	priority string
	// started is how long ago its work started; 0 for not started.
	started  time.Duration
	labels   []string
	children int
	blockers []githubmcp.TimelineBlocker
	more     bool
}

func epicNode(n int) wire.LineageNode { return forgeRepo{"github", "o/r"}.issue(n) }

func (s schedEpic) epic() timelineEpic {
	item := wire.ForgeItem{Node: epicNode(s.n), State: "open", Labels: []wire.ForgeLabel{{Name: "epic"}}}
	if s.priority != "" {
		item.Priority = new(s.priority)
	}
	created := wire.DateOf(scheduleNow.Add(-30*schedDay + time.Duration(s.n)*time.Hour))
	item.CreatedAt = &created
	for _, l := range s.labels {
		item.Labels = append(item.Labels, wire.ForgeLabel{Name: l})
		if isHoldName(l) {
			item.OnHold = new(true)
		}
	}
	e := timelineEpic{wire: wire.WorkTimelineEpic{Item: item, Children: []wire.WorkTimelineChild{}}, blockedBy: s.blockers, more: s.more}
	e.held = onHold(item)
	kids := s.children
	if kids == 0 {
		kids = 1
	}
	if s.children < 0 {
		kids = 0
	}
	for range kids {
		k := wire.WorkTimelineChild{Item: wire.ForgeItem{State: "open"}}
		source := ""
		if s.started > 0 {
			k.StartedAt, source = new(wire.DateOf(scheduleNow.Add(-s.started))), startSession
		}
		e.wire.Children = append(e.wire.Children, k)
		e.startSources = append(e.startSources, source)
	}
	return e
}

func blocker(n int, open bool, labels ...string) githubmcp.TimelineBlocker {
	return githubmcp.TimelineBlocker{Ref: githubmcp.Ref{Repo: "o/r", Number: n, Open: open}, Labels: labels}
}

// durations forecasts an epic to take its duration from the time it starts; one not in it has no forecast.
func durations(d map[int]time.Duration) forecastAt {
	return func(e timelineEpic, at time.Time) *wire.WorkTimelineForecast {
		dur, ok := d[refNumber(e.wire.Item.Node.Ref)]
		if !ok {
			return nil
		}
		return &wire.WorkTimelineForecast{Low: wire.DateOf(at.Add(dur / 2)), Mid: wire.DateOf(at.Add(dur)), High: wire.DateOf(at.Add(2 * dur))}
	}
}

func schedule(t *testing.T, places int, d map[int]time.Duration, epics ...schedEpic) map[int]wire.WorkTimelineScheduleRow {
	t.Helper()
	var in []timelineEpic
	for _, e := range epics {
		in = append(in, e.epic())
	}
	out := timelineSchedule(in, places, scheduleNow, false, durations(d))
	if out.Places != int64(places) || out.ReadAt != wire.DateOf(scheduleNow) || out.Incomplete != nil || len(out.Rows) != len(epics) {
		t.Fatalf("schedule %+v", out)
	}
	rows := map[int]wire.WorkTimelineScheduleRow{}
	for _, r := range out.Rows {
		rows[refNumber(r.Epic.Ref)] = r
	}
	return rows
}

// planned says that row n starts after from now and finishes after finish from now.
func planned(t *testing.T, rows map[int]wire.WorkTimelineScheduleRow, n int, after, finish time.Duration) {
	t.Helper()
	r := rows[n]
	if r.Reason != nil || r.PlannedStart == nil || *r.PlannedStart != wire.DateOf(scheduleNow.Add(after)) ||
		r.Finish == nil || r.Finish.Mid != wire.DateOf(scheduleNow.Add(finish)) {
		t.Errorf("#%d: %+v, want a start after %v and a finish after %v", n, r, after, finish)
	}
}

func undated(t *testing.T, rows map[int]wire.WorkTimelineScheduleRow, n int, want wire.WorkTimelineScheduleReason) {
	t.Helper()
	if r := rows[n]; r.Reason == nil || *r.Reason != want || r.Finish != nil || r.PlannedStart != nil {
		t.Errorf("#%d: %+v, want no date for %s", n, r, want)
	}
}

// The worked example of #7067's gate 1: two places; A started yesterday and finishes in 2 days; B and C are ready, B
// first in forge/next. B takes the free place now; C takes the place A frees, the first released, in 2 days.
// Decided (#7525): the first place that frees, as rule 2 says; the example's "later of" holds only for one place.
func TestScheduleWorkedExample(t *testing.T) {
	rows := schedule(t, 2, map[int]time.Duration{1: 2 * schedDay, 2: 3 * schedDay, 3: schedDay},
		schedEpic{n: 3}, schedEpic{n: 2, priority: "high"}, schedEpic{n: 1, started: schedDay})
	if r := rows[1]; r.Reason != nil || r.PlannedStart != nil || r.StartedAt == nil || *r.StartedAt != wire.DateOf(scheduleNow.Add(-schedDay)) ||
		r.StartSource == nil || *r.StartSource != startSession || r.Finish == nil || r.Finish.Mid != wire.DateOf(scheduleNow.Add(2*schedDay)) {
		t.Errorf("started epic: %+v", r)
	}
	planned(t, rows, 2, 0, 3*schedDay)
	planned(t, rows, 3, 2*schedDay, 3*schedDay)
	// With one place, C waits for B too.
	rows = schedule(t, 1, map[int]time.Duration{1: 2 * schedDay, 2: 3 * schedDay, 3: schedDay},
		schedEpic{n: 3}, schedEpic{n: 2, priority: "high"}, schedEpic{n: 1, started: schedDay})
	planned(t, rows, 2, 2*schedDay, 5*schedDay)
	planned(t, rows, 3, 5*schedDay, 6*schedDay)
}

// Started epics hold their places, more than the places too: a ready epic waits until fewer than places are held.
func TestScheduleOccupiedPlaces(t *testing.T) {
	rows := schedule(t, 1, map[int]time.Duration{1: 2 * schedDay, 2: 5 * schedDay, 3: schedDay},
		schedEpic{n: 1, started: schedDay}, schedEpic{n: 2, started: 2 * schedDay}, schedEpic{n: 3})
	planned(t, rows, 3, 5*schedDay, 6*schedDay)
	// A started epic with no forecast holds its place with no end: the rest have no place.
	rows = schedule(t, 1, map[int]time.Duration{3: schedDay}, schedEpic{n: 1, started: schedDay}, schedEpic{n: 3})
	undated(t, rows, 1, wire.WorkTimelineScheduleReasonNoForecast)
	if rows[1].StartedAt == nil {
		t.Error("a started epic with no forecast lost its start")
	}
	undated(t, rows, 3, wire.WorkTimelineScheduleReasonNoPlace)
}

// Places that free at the same time are filled together, in the order of forge/next, not the order of the read.
func TestScheduleSimultaneousFinishes(t *testing.T) {
	rows := schedule(t, 2, map[int]time.Duration{1: 2 * schedDay, 2: 2 * schedDay, 3: schedDay, 4: schedDay, 5: schedDay},
		schedEpic{n: 5, priority: "low"}, schedEpic{n: 4}, schedEpic{n: 3, priority: "high"},
		schedEpic{n: 1, started: schedDay}, schedEpic{n: 2, started: schedDay})
	planned(t, rows, 3, 2*schedDay, 3*schedDay)
	planned(t, rows, 4, 2*schedDay, 3*schedDay)
	planned(t, rows, 5, 3*schedDay, 4*schedDay)
}

// An open epic blocker delays an epic to its predicted finish even with a place free; a closed blocker does not; a
// started epic is not moved by its blockers.
func TestScheduleEpicBlockers(t *testing.T) {
	rows := schedule(t, 3, map[int]time.Duration{1: 3 * schedDay, 2: schedDay, 3: schedDay, 4: schedDay},
		schedEpic{n: 1}, schedEpic{n: 2, blockers: []githubmcp.TimelineBlocker{blocker(1, true, "epic"), blocker(9, false)}},
		schedEpic{n: 3, started: schedDay, blockers: []githubmcp.TimelineBlocker{blocker(1, true, "epic")}},
		// The last of two blockers sets the start.
		schedEpic{n: 4, blockers: []githubmcp.TimelineBlocker{blocker(2, true, "epic"), blocker(3, true, "epic")}})
	planned(t, rows, 1, 0, 3*schedDay)
	planned(t, rows, 2, 3*schedDay, 4*schedDay)
	if r := rows[3]; r.Reason != nil || r.Finish == nil || r.Finish.Mid != wire.DateOf(scheduleNow.Add(schedDay)) {
		t.Errorf("a started epic was moved by its blocker: %+v", r)
	}
	planned(t, rows, 4, 4*schedDay, 5*schedDay)
	if r := rows[2]; len(r.Blockers) != 1 || r.Blockers[0] != epicNode(1) {
		t.Errorf("blockers %+v, want the open one", r.Blockers)
	}
}

// A blocker that is not an epic, one on hold (in the read or not), one not read, a cycle, and what waits on an epic
// with no date: each leaves the epic undated with its reason. A held epic takes no place and comes last.
func TestScheduleUndatedReasons(t *testing.T) {
	d := map[int]time.Duration{}
	for n := range 20 {
		d[n] = schedDay
	}
	delete(d, 9)
	rows := schedule(t, 5, d,
		schedEpic{n: 1, blockers: []githubmcp.TimelineBlocker{blocker(50, true)}},
		schedEpic{n: 2, blockers: []githubmcp.TimelineBlocker{blocker(51, true, "epic", "on-hold")}},
		schedEpic{n: 3, blockers: []githubmcp.TimelineBlocker{blocker(10, true, "epic")}},
		schedEpic{n: 4, blockers: []githubmcp.TimelineBlocker{blocker(52, true, "epic")}},
		schedEpic{n: 5, blockers: []githubmcp.TimelineBlocker{blocker(6, true, "epic")}},
		schedEpic{n: 6, blockers: []githubmcp.TimelineBlocker{blocker(5, true, "epic")}},
		schedEpic{n: 7, blockers: []githubmcp.TimelineBlocker{blocker(7, true, "epic")}},
		schedEpic{n: 8, blockers: []githubmcp.TimelineBlocker{blocker(5, true, "epic")}},
		schedEpic{n: 9},
		schedEpic{n: 10, labels: []string{"on hold"}, started: schedDay},
		schedEpic{n: 11, children: -1},
		schedEpic{n: 12, more: true},
		schedEpic{n: 13, blockers: []githubmcp.TimelineBlocker{blocker(9, true, "epic")}},
		// A hidden blocker and an on-hold one: on hold is said first.
		schedEpic{n: 14, blockers: []githubmcp.TimelineBlocker{blocker(50, true), blocker(51, true, "epic", "on hold")}},
		schedEpic{n: 15})
	undated(t, rows, 1, wire.WorkTimelineScheduleReasonBlockerNotEpic)
	undated(t, rows, 2, wire.WorkTimelineScheduleReasonBlockerOnHold)
	undated(t, rows, 3, wire.WorkTimelineScheduleReasonBlockerOnHold)
	undated(t, rows, 4, wire.WorkTimelineScheduleReasonBlockerNotRead)
	undated(t, rows, 5, wire.WorkTimelineScheduleReasonCycle)
	undated(t, rows, 6, wire.WorkTimelineScheduleReasonCycle)
	undated(t, rows, 7, wire.WorkTimelineScheduleReasonCycle)
	undated(t, rows, 8, wire.WorkTimelineScheduleReasonBlockerUndated)
	undated(t, rows, 9, wire.WorkTimelineScheduleReasonNoForecast)
	undated(t, rows, 10, wire.WorkTimelineScheduleReasonOnHold)
	undated(t, rows, 11, wire.WorkTimelineScheduleReasonNoIssues)
	undated(t, rows, 12, wire.WorkTimelineScheduleReasonIncomplete)
	undated(t, rows, 13, wire.WorkTimelineScheduleReasonBlockerUndated)
	undated(t, rows, 14, wire.WorkTimelineScheduleReasonBlockerOnHold)
	// The held epic, started, holds no place: 15 starts now with 5 places.
	planned(t, rows, 15, 0, schedDay)
}

// The rows: started by start, planned by planned start, no date in the order of forge/next, then on hold.
func TestScheduleRowOrder(t *testing.T) {
	var in []timelineEpic
	for _, e := range []schedEpic{{n: 1, labels: []string{"on hold"}}, {n: 2, children: -1}, {n: 3, children: -1, priority: "high"},
		{n: 4, blockers: []githubmcp.TimelineBlocker{blocker(6, true, "epic")}}, {n: 5}, {n: 6, started: schedDay}, {n: 7, started: 2 * schedDay}} {
		in = append(in, e.epic())
	}
	out := timelineSchedule(in, 2, scheduleNow, false, durations(map[int]time.Duration{4: schedDay, 5: schedDay, 6: schedDay, 7: 3 * schedDay}))
	var got []int
	for _, r := range out.Rows {
		got = append(got, refNumber(r.Epic.Ref))
	}
	want := []int{7, 6, 4, 5, 3, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for n := range want {
		if got[n] != want[n] {
			t.Fatalf("rows %v, want %v", got, want)
		}
	}
}

// A list cut short dates no epic, and keeps the observed starts.
func TestScheduleIncomplete(t *testing.T) {
	out := timelineSchedule([]timelineEpic{schedEpic{n: 1, started: schedDay}.epic(), schedEpic{n: 2}.epic()}, 2, scheduleNow, true,
		durations(map[int]time.Duration{1: schedDay, 2: schedDay}))
	if out.Incomplete == nil || len(out.Rows) != 2 {
		t.Fatalf("schedule %+v", out)
	}
	for _, r := range out.Rows {
		if r.Reason == nil || *r.Reason != wire.WorkTimelineScheduleReasonIncomplete || r.Finish != nil || r.PlannedStart != nil {
			t.Errorf("row %+v", r)
		}
	}
	if out.Rows[0].StartedAt == nil {
		t.Error("the started epic lost its start")
	}
}

// A started epic whose children have all closed finishes now, and frees its place now.
func TestScheduleAllChildrenClosed(t *testing.T) {
	e := schedEpic{n: 1, started: schedDay}.epic()
	e.wire.Children[0].ClosedAt = new(wire.DateOf(scheduleNow.Add(-time.Hour)))
	out := timelineSchedule([]timelineEpic{e, schedEpic{n: 2}.epic()}, 1, scheduleNow, false, durations(map[int]time.Duration{2: schedDay}))
	rows := map[int]wire.WorkTimelineScheduleRow{}
	for _, r := range out.Rows {
		rows[refNumber(r.Epic.Ref)] = r
	}
	if r := rows[1]; r.Finish == nil || r.Finish.Mid != wire.DateOf(scheduleNow) {
		t.Errorf("done epic %+v", r)
	}
	planned(t, rows, 2, 0, schedDay)
}
