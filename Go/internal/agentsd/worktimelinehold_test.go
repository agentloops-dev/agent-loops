package agentsd

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7335: with onHold, work/timeline gives the open epics on hold after the others, in the order of forge/next, with
// their children and no forecast, also one with no children; without it, it reads none of them. A closed epic with the
// label is not one, and the forecast of the others is the same either way.
func TestWorkTimelineShowsTheEpicsOnHoldWhenAsked(t *testing.T) {
	t.Parallel()

	d, c, gh, _ := forgeDaemon(t)
	for _, f := range d.folderPaths() {
		if d.folderRepo(f).path == "alexec/agents" {
			must(t, os.MkdirAll(filepath.Join(f, ".agents"), 0o750))
			must(t, os.WriteFile(filepath.Join(f, folderConfig), []byte(`{"project":"p1"}`), 0o600))
		}
	}
	now := time.Now().UTC().Truncate(time.Minute)
	at := func(h float64) string { return now.Add(time.Duration(h * float64(time.Hour))).Format(time.RFC3339) }
	n := 200
	kid := func(filed, closed float64) githubmcp.TimelineChild {
		n++
		k := githubmcp.TimelineChild{Item: githubmcp.Item{Repo: "alexec/Agents", Number: n, State: "open", CreatedAt: at(filed)}}
		if closed != 0 {
			k.State, k.ClosedAt, k.StateReason = "closed", at(closed), "completed"
		}
		return k
	}
	epicOf := func(number int, closed float64, labels []string, kids ...githubmcp.TimelineChild) githubmcp.TimelineEpic {
		e := githubmcp.TimelineEpic{Item: githubmcp.Item{Repo: "alexec/Agents", Number: number, State: "open", Title: "epic " + strconv.Itoa(number),
			Labels: append([]string{"epic"}, labels...), CreatedAt: at(-24 * 40)}, Children: kids}
		if closed != 0 {
			e.State, e.ClosedAt, e.StateReason = "closed", at(closed), "completed"
		}
		return e
	}
	var epics []githubmcp.TimelineEpic
	for i := range 4 {
		base := float64(-24 * (20 - 4*i))
		epics = append(epics, epicOf(10+i, base+9, nil, kid(base, base+2), kid(base, base+4), kid(base, base+6), kid(base, base+8)))
	}
	epics = append(epics,
		epicOf(20, 0, nil, kid(-10, -6), kid(-10, 0)),
		epicOf(30, 0, []string{"on hold", "priority:low"}, kid(-30, -20), kid(-30, 0)), // on hold, with work
		epicOf(31, 0, []string{"On-Hold", "priority:high"}),                            // on hold, no work yet: first of the held
		epicOf(32, -1, []string{"on hold"}, kid(-24*62, -24*61)))                       // closed: not an epic on hold
	gh.timeline = githubmcp.Timeline{Epics: epics}
	req := wire.WorkTimelineRequest{Project: "p1", From: wire.DateOf(now.Add(-24 * time.Hour)), To: wire.DateOf(now.Add(24 * time.Hour))}

	refs := func(tl wire.WorkTimeline) []string {
		var out []string
		for _, e := range tl.Epics {
			out = append(out, e.Item.Node.Ref[len("github:issue:alexec/agents#"):])
		}
		return out
	}
	var off wire.WorkTimeline
	mustUnmarshal(t, c.call(t, 1, "work/timeline", req), &off)
	gh.mu.Lock()
	readOff := slices.Clone(gh.timelineRead)
	gh.mu.Unlock()
	if !slices.Equal(refs(off), []string{"20"}) || slices.Contains(readOff, 30) || slices.Contains(readOff, 31) || off.Epics[0].Forecast == nil {
		t.Fatalf("off: epics %v, read %v", refs(off), readOff)
	}

	req.OnHold = new(true)
	var on wire.WorkTimeline
	mustUnmarshal(t, c.call(t, 2, "work/timeline", req), &on)
	gh.mu.Lock()
	readOn := slices.Clone(gh.timelineRead)
	gh.mu.Unlock()
	if !slices.Equal(refs(on), []string{"20", "31", "30"}) || !slices.Contains(readOn, 30) {
		t.Fatalf("on: epics %v, read %v", refs(on), readOn)
	}
	held, empty := on.Epics[2], on.Epics[1]
	if held.Forecast != nil || len(held.Segments) != 1 || held.Segments[0].StartedAt == nil || held.Item.OnHold == nil {
		t.Fatalf("the held epic with work: %+v", held)
	}
	if empty.Forecast != nil || len(empty.Children) != 0 || empty.Item.OnHold == nil {
		t.Fatalf("the held epic with no work: %+v", empty)
	}
	// The same seed gives the same forecast; only the clock moved between the reads.
	if on.Epics[0].Forecast == nil || math.Abs(float64(on.Epics[0].Forecast.Mid-off.Epics[0].Forecast.Mid)) > 1 {
		t.Fatalf("the forecast changed: %+v, was %+v", on.Epics[0].Forecast, off.Epics[0].Forecast)
	}
}
