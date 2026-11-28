package agentsd

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func (f *fakeForgeGitHub) Timeline(_ context.Context, repo, _, from string, skip func([]string) bool,
	kept func(int, string) (githubmcp.TimelineEpic, bool)) (githubmcp.Timeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timelines++
	t := f.timeline
	t.Epics = slices.DeleteFunc(slices.Clone(t.Epics), func(e githubmcp.TimelineEpic) bool { return e.State == "open" && skip(e.Labels) })
	var first []githubmcp.TimelineEpic
	f.timelineRead = nil
	for _, e := range t.Epics {
		if e.State == "closed" {
			if k, ok := kept(e.Number, e.UpdatedAt); ok {
				first = append(first, k)
				continue
			}
		}
		if e.ClosedAt != "" && e.ClosedAt < from {
			t.Rest = append(t.Rest, strconv.Itoa(e.Number))
		} else {
			first = append(first, e)
			f.timelineRead = append(f.timelineRead, e.Number)
		}
	}
	t.Epics = first
	return t, nil
}

func (f *fakeForgeGitHub) TimelineRest(_ context.Context, _ string, t githubmcp.Timeline) (githubmcp.Timeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timelineRests++
	t.Epics = slices.Clone(t.Epics)
	for _, e := range f.timeline.Epics {
		if slices.Contains(t.Rest, strconv.Itoa(e.Number)) {
			t.Epics = append(t.Epics, e)
		}
	}
	t.Rest = nil
	return t, nil
}

// checkTimelineFirst asks for the first answer and then all of it (#7027): the window's epics with no forecast, read
// once for two first asks; then the forecast, with only the rest read; then a first ask gets all that is kept.
func checkTimelineFirst(t *testing.T, c *conn, gh *fakeForgeGitHub, req wire.WorkTimelineRequest) {
	t.Helper()
	ask := func(first bool, reads, rests int, partial bool) {
		t.Helper()
		r := req
		if first {
			r.First = new(true)
		}
		var tl wire.WorkTimeline
		mustUnmarshal(t, c.call(t, 50, "work/timeline", r), &tl)
		gh.mu.Lock()
		defer gh.mu.Unlock()
		if gh.timelines != reads || gh.timelineRests != rests || (tl.Partial != nil) != partial || len(tl.Epics) != 2 || len(tl.Releases) != 1 ||
			(tl.Backtest == nil) != partial || (tl.Epics[0].Forecast == nil) != partial || (tl.Schedule == nil) != partial {
			t.Fatalf("first %v: %d reads, %d rests, %+v", first, gh.timelines, gh.timelineRests, tl)
		}
	}
	ask(true, 1, 0, true)
	ask(true, 1, 0, true)
	ask(false, 1, 1, false)
	ask(true, 1, 1, false)
}

// checkTimelineOpenEpic checks the open epic of TestWorkTimelineForecastsTheOpenEpics: its
// forecast, its one segment, its children's starts and the one that waits. firstPR is the
// date of the first child's PR.
func checkTimelineOpenEpic(t *testing.T, e wire.WorkTimelineEpic, now time.Time, firstPR wire.Date) {
	t.Helper()
	if e.Forecast == nil || e.Forecast.Waits == nil || !(e.Forecast.Low <= e.Forecast.Mid && e.Forecast.Mid <= e.Forecast.High) || e.Forecast.Low < wire.DateOf(now) {
		t.Fatalf("forecast %+v", e.Forecast)
	}
	if len(e.Segments) != 1 || e.Segments[0].ClosedAt != nil || *e.Segments[0].StartedAt != wire.DateOf(now.Add(-8*time.Hour)) {
		t.Fatalf("segments %+v", e.Segments)
	}
	kids := e.Children
	if *kids[2].StartedAt != wire.DateOf(now.Add(-8*time.Hour)) || kids[3].StartedAt != nil || *kids[0].StartedAt != firstPR {
		t.Fatalf("starts: %v, %v, %v", kids[2].StartedAt, kids[3].StartedAt, kids[0].StartedAt)
	}
	if kids[4].WaitsForPerson == nil || kids[3].WaitsForPerson != nil {
		t.Fatalf("children: %+v", kids)
	}
}

// #4172: work/timeline gives a project's epics that overlap the window, each child with its
// start (lineage's first session, else the first PR, else its close), the epic's segments, and
// the open one's forecast; children that wait for a person are not forecast; the releases in
// the window; the backtest; and a read again within forgeCacheFor asks GitHub nothing.
func TestWorkTimelineForecastsTheOpenEpics(t *testing.T) {
	t.Parallel()

	d, c, gh, _ := forgeDaemon(t)
	var folder string
	for _, f := range d.folderPaths() {
		if d.folderRepo(f).path == "alexec/agents" {
			folder = f
			break
		}
	}
	must(t, os.MkdirAll(filepath.Join(folder, ".agents"), 0o755))                                 //nolint:gosec // G301: the mode this folder always had; only its error is new
	must(t, os.WriteFile(filepath.Join(folder, folderConfig), []byte(`{"project":"p1"}`), 0o644)) //nolint:gosec // G306: the mode this file always had; only its error is new

	now := time.Now().UTC().Truncate(time.Minute)
	stamp := func(h float64) string { return now.Add(time.Duration(h * float64(time.Hour))).Format(time.RFC3339) }
	number := 100
	child := func(filed, closed float64, labels ...string) githubmcp.TimelineChild {
		number++
		c := githubmcp.TimelineChild{Item: githubmcp.Item{Repo: "alexec/Agents", Number: number, State: "open", Title: "child " + strconv.Itoa(number), Labels: labels,
			CreatedAt: stamp(filed)}}
		if closed != 0 {
			c.State, c.ClosedAt, c.StateReason = "closed", stamp(closed), "completed"
			c.PROpenedAt = []string{stamp(closed - 1)}
		}
		return c
	}
	epic := func(n int, closed float64, kids ...githubmcp.TimelineChild) githubmcp.TimelineEpic {
		e := githubmcp.TimelineEpic{Item: githubmcp.Item{Repo: "alexec/Agents", Number: n, State: "open", Title: "epic " + strconv.Itoa(n), Labels: []string{"epic"},
			CreatedAt: stamp(-24 * 40)}, Children: kids}
		if closed != 0 {
			e.State, e.ClosedAt, e.StateReason = "closed", stamp(closed), "completed"
		}
		return e
	}
	var epics []githubmcp.TimelineEpic
	// Four closed epics in the last 30 days, of 4 children each, to learn from.
	for i := range 4 {
		base := float64(-24 * (20 - 4*i))
		epics = append(epics, epic(10+i, base+9, child(base, base+2), child(base, base+4), child(base, base+6), child(base, base+8)))
	}
	open := epic(20, 0, child(-10, -6), child(-10, -3), child(-10, 0), child(-10, 0), child(-10, 0, "alex-todo"))
	held := epic(25, 0, child(-10, -6), child(-10, 0)) // on hold: never drawn (#7058), so not read (#7026)
	held.Labels = append(held.Labels, "On-Hold")
	epics = append(epics, open, held,
		epic(21, 0),                                 // no children: not drawn
		epic(22, -24*60, child(-24*62, -24*61)),     // ended before the window
		epic(24, -10, child(-24*62, -24*61)),        // closed in the window, but its work ended before it (#7058)
		epic(23, 0, child(-48, -40), child(-20, 0))) // filed after the first line ended: a second line
	// A started epic's blocker does not move it (#7525).
	epics[len(epics)-1].BlockedBy = []githubmcp.TimelineBlocker{{Ref: githubmcp.Ref{Repo: "alexec/Agents", Number: 20, Open: true}, Labels: []string{"epic"}}}
	gh.timeline = githubmcp.Timeline{Epics: epics, Releases: []githubmcp.Release{
		{Tag: "v1", PublishedAt: stamp(-5), URL: "u1"}, {Tag: "v0", PublishedAt: stamp(-24 * 9)}}}
	// A session worked on the first open child: it started then, though no PR closes it yet.
	first := forgeRepo{"github", "alexec/agents"}.issue(open.Children[2].Number)
	if _, err := d.lineage().Link(wire.LineageLinkRequest{From: wire.LineageNode{Kind: lineage.KindSession, Ref: "agents:session:#A"}, To: first,
		Rel: lineage.RelWorksOn, Source: lineage.SourceStart}, now.Add(-8*time.Hour)); err != nil {
		t.Fatal(err)
	}

	req := wire.WorkTimelineRequest{Project: "p1", From: wire.DateOf(now.Add(-24 * time.Hour)), To: wire.DateOf(now.Add(24 * time.Hour))}
	checkTimelineFirst(t, c, gh, req)
	var tl wire.WorkTimeline
	if err := json.Unmarshal(c.call(t, 1, "work/timeline", req), &tl); err != nil {
		t.Fatal(err)
	}
	if len(tl.Errors) > 0 || len(tl.Releases) != 1 || tl.Releases[0].Tag != "v1" || tl.Releases[0].Repo != "alexec/agents" {
		t.Fatalf("errors %v, releases %+v", tl.Errors, tl.Releases)
	}
	if !slices.Equal(tl.Scopes, []string{"github:alexec/agents"}) {
		t.Fatalf("scopes %v", tl.Scopes)
	}
	got := map[int]wire.WorkTimelineEpic{}
	for _, e := range tl.Epics {
		n, _ := strconv.Atoi(e.Item.Node.Ref[len("github:issue:alexec/agents#"):])
		got[n] = e
	}
	if len(got) != 2 || got[20].Item.Title == "" || got[23].Item.Title == "" {
		t.Fatalf("epics in the window: %v", tl.Epics)
	}
	checkTimelineOpenEpic(t, got[20], now, forgeDate(stamp(-7)))
	if s := got[23].Segments; len(s) != 2 || s[0].ClosedAt == nil || s[1].ClosedAt != nil || got[23].Forecast == nil {
		t.Fatalf("a child filed after the line ended starts a second: %+v", got[23])
	}
	if tl.Backtest == nil || tl.Backtest.Cuts == 0 || tl.Backtest.Error < 1 {
		t.Fatalf("backtest %+v", tl.Backtest)
	}
	checkTimelineSchedule(t, tl, got)
	var again wire.WorkTimeline
	mustUnmarshal(t, c.call(t, 2, "work/timeline", req), &again)
	gh.mu.Lock()
	reads := gh.timelines
	gh.mu.Unlock()
	// The same seed gives the same forecast; only the clock moved between the reads.
	if reads != 1 || math.Abs(float64(again.Epics[0].Forecast.Mid-tl.Epics[0].Forecast.Mid)) > 1 {
		t.Fatalf("%d reads; forecasts %v and %v", reads, again.Epics[0].Forecast, tl.Epics[0].Forecast)
	}
	if err := c.callError(t, 3, "work/timeline", wire.WorkTimelineRequest{Project: "p1", From: 10, To: 5}); err == nil {
		t.Fatal("to before from is not refused")
	}
	if err := c.callError(t, 4, "work/timeline", wire.WorkTimelineRequest{Project: "p1", From: 5, To: 10, Places: new(int64(0))}); err == nil {
		t.Fatal("no places is not refused")
	}
}

// checkTimelineSchedule checks the schedule of TestWorkTimelineForecastsTheOpenEpics (#7525): the three open epics
// that are read, the two started ones with their forecasts as finish, the one with no children undated.
func checkTimelineSchedule(t *testing.T, tl wire.WorkTimeline, got map[int]wire.WorkTimelineEpic) {
	t.Helper()
	s := tl.Schedule
	if s == nil || s.Places != schedulePlaces || s.Incomplete != nil || len(s.Rows) != 3 {
		t.Fatalf("schedule %+v", s)
	}
	rows := map[int]wire.WorkTimelineScheduleRow{}
	for _, r := range s.Rows {
		rows[refNumber(r.Epic.Ref)] = r
	}
	for _, n := range []int{20, 23} {
		r := rows[n]
		if r.Reason != nil || r.StartedAt == nil || r.PlannedStart != nil || r.Finish == nil || r.Finish.Mid != got[n].Forecast.Mid {
			t.Fatalf("#%d: %+v, forecast %+v", n, r, got[n].Forecast)
		}
	}
	if r := rows[20]; *r.StartedAt != *got[20].Segments[0].StartedAt || r.StartSource == nil || *r.StartSource != startSession {
		t.Fatalf("#20 started %+v", r)
	}
	if r := rows[23]; len(r.Blockers) != 1 || r.Blockers[0] != got[20].Item.Node {
		t.Fatalf("#23 blockers %+v", r.Blockers)
	}
	if r := rows[21]; r.Reason == nil || *r.Reason != wire.WorkTimelineScheduleReasonNoIssues {
		t.Fatalf("#21 %+v", r)
	}
}

// An epic is one block (#7058): a window in a pause of its work is inside it.
func TestTimelineEpicOverlapsAsOneBlock(t *testing.T) {
	t.Parallel()

	span := func(start, end wire.Date) wire.WorkTimelineSegment {
		s := wire.WorkTimelineSegment{StartedAt: &start}
		if end != 0 {
			s.ClosedAt = &end
		}
		return s
	}
	for name, c := range map[string]struct {
		segments []wire.WorkTimelineSegment
		want     bool
	}{
		"a pause":     {[]wire.WorkTimelineSegment{span(10, 20), span(60, 70)}, true},
		"before":      {[]wire.WorkTimelineSegment{span(10, 20)}, false},
		"after":       {[]wire.WorkTimelineSegment{span(60, 70)}, false},
		"open":        {[]wire.WorkTimelineSegment{span(10, 0)}, true},
		"not started": {[]wire.WorkTimelineSegment{{}}, true},
	} {
		if got := (timelineEpic{wire: wire.WorkTimelineEpic{Segments: c.segments}}).overlaps(30, 50); got != c.want {
			t.Errorf("%s: overlaps %v, want %v", name, got, c.want)
		}
	}
}
