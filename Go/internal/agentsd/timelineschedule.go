package agentsd

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The Timeline's schedule (#7525, epic #7067; the model of #7523): when each open epic starts and finishes if a
// number of epics, the places, are worked at once. A started epic holds a place from its start until its predicted
// finish, whatever blocks it. The others take the free places in the order of forge/next (level: epic), nextCompare,
// each no earlier than now and the predicted finish of its last open epic blocker. An epic that cannot be dated says
// why. It is derived from the Timeline's own read (the epics, their children, their blockers and the forecast), so a
// folder's schedule costs no read of its own.

// schedulePlaces is how many epics are worked at once when the request does not say (the PoC's smaller choice, #7067).
const schedulePlaces = 3

// schedulePlacesMost bounds a request's places.
const schedulePlacesMost = 20

// Where an epic's observed start came from (#7523): the earliest of its children's.
const (
	startSession     = "session"
	startPullRequest = "pullRequest"
	startClosed      = "closed"
)

// forecastAt is an open epic's forecast if its open work starts at a time, or nil when it cannot be forecast.
type forecastAt func(e timelineEpic, at time.Time) *wire.WorkTimelineForecast

// scheduleEpic is an open epic on its way to a row.
type scheduleEpic struct {
	e       timelineEpic
	started bool
	// holds says it holds a place from its start: it started and is not on hold.
	holds bool
	// release is when its place frees, in seconds of wire.Date; +Inf when that is not known.
	release float64
	// deps are the open epics of the read that block it.
	deps   []int
	row    wire.WorkTimelineScheduleRow
	queued bool
}

// timelineSchedule is the schedule of epics (the open ones; others are left out) with places places, read at now.
// incomplete says that a list was cut short: no row is dated then.
func timelineSchedule(epics []timelineEpic, places int, now time.Time, incomplete bool, at forecastAt) wire.WorkTimelineSchedule {
	out := wire.WorkTimelineSchedule{Places: int64(places), ReadAt: wire.DateOf(now), Rows: []wire.WorkTimelineScheduleRow{}}
	var all []*scheduleEpic
	index := map[wire.LineageNode]int{}
	for _, e := range epics {
		if e.wire.Item.State != "open" || e.wire.ClosedAt != nil {
			continue
		}
		index[e.wire.Item.Node] = len(all)
		s := &scheduleEpic{e: e, release: math.Inf(1), row: wire.WorkTimelineScheduleRow{Epic: e.wire.Item.Node}}
		s.row.StartedAt, s.row.StartSource = e.started()
		s.started = s.row.StartedAt != nil
		s.holds = s.started && !e.held
		for _, b := range e.blockedBy {
			if b.Open {
				s.row.Blockers = append(s.row.Blockers, blockerNode(b))
			}
		}
		all = append(all, s)
	}
	// The one order: the queue, and the ties of the rows.
	slices.SortStableFunc(all, func(a, b *scheduleEpic) int { return nextCompare(a.e.wire.Item, b.e.wire.Item) })
	for n, s := range all {
		index[s.e.wire.Item.Node] = n
	}
	if incomplete {
		out.Incomplete = new(true)
		for _, s := range all {
			s.undated(wire.WorkTimelineScheduleReasonIncomplete)
			out.Rows = append(out.Rows, s.row)
		}
		return out
	}
	for _, s := range all {
		s.classify(all, index, now, at)
	}
	markCycles(all)
	placeEpics(all, places, now, at)
	for _, s := range all {
		out.Rows = append(out.Rows, s.row)
	}
	slices.SortStableFunc(out.Rows, compareRows)
	return out
}

// started is when an epic's work started, the earliest of its children's starts, and where that came from; nil
// while none has started.
func (e timelineEpic) started() (*wire.Date, *string) {
	var at *wire.Date
	var source string
	for n, k := range e.wire.Children {
		if k.StartedAt != nil && (at == nil || *k.StartedAt < *at) {
			at, source = k.StartedAt, e.startSources[n]
		}
	}
	if at == nil {
		return nil, nil
	}
	return at, &source
}

// blockerNode is a blocker's node.
func blockerNode(b githubmcp.TimelineBlocker) wire.LineageNode {
	return forgeRepo{"github", strings.ToLower(b.Repo)}.issue(b.Number)
}

func (s *scheduleEpic) undated(r wire.WorkTimelineScheduleReason) {
	s.row.Reason, s.row.Finish, s.row.PlannedStart = &r, nil, nil
}

// classify settles what an epic's own read says: a started epic's finish, and why one cannot be dated. A not-started
// epic that can be is queued, with the open epics that block it.
func (s *scheduleEpic) classify(all []*scheduleEpic, index map[wire.LineageNode]int, now time.Time, at forecastAt) {
	e := s.e
	switch {
	case e.held:
		// On hold: nobody works it (#7335), so it takes no place.
		s.undated(wire.WorkTimelineScheduleReasonOnHold)
		return
	case e.more:
		s.undated(wire.WorkTimelineScheduleReasonIncomplete)
		return
	case s.started:
		// Its blockers do not move it: its work has begun.
		switch f := at(e, now); {
		case f != nil:
			s.row.Finish, s.release = f, float64(f.Mid)
		case e.allClosed():
			// All its children closed: it finishes when it is closed.
			d := wire.DateOf(now)
			s.row.Finish, s.release = &wire.WorkTimelineForecast{Low: d, Mid: d, High: d}, float64(d)
		default:
			s.undated(wire.WorkTimelineScheduleReasonNoForecast)
		}
		return
	case len(e.wire.Children) == 0:
		s.undated(wire.WorkTimelineScheduleReasonNoIssues)
		return
	}
	var reason wire.WorkTimelineScheduleReason
	worse := func(r wire.WorkTimelineScheduleReason) {
		if reason == "" || blockerRank(r) < blockerRank(reason) {
			reason = r
		}
	}
	for _, b := range e.blockedBy {
		if !b.Open {
			continue
		}
		node := blockerNode(b)
		if n, ok := index[node]; ok {
			if all[n].e.held {
				worse(wire.WorkTimelineScheduleReasonBlockerOnHold)
			} else {
				s.deps = append(s.deps, n)
			}
			continue
		}
		switch {
		case slices.ContainsFunc(b.Labels, isHoldName):
			worse(wire.WorkTimelineScheduleReasonBlockerOnHold)
		case !slices.ContainsFunc(b.Labels, func(l string) bool { return strings.EqualFold(strings.TrimSpace(l), "epic") }):
			worse(wire.WorkTimelineScheduleReasonBlockerNotEpic)
		default:
			worse(wire.WorkTimelineScheduleReasonBlockerNotRead)
		}
	}
	switch {
	case reason != "":
		s.undated(reason)
	case at(e, now) == nil:
		s.undated(wire.WorkTimelineScheduleReasonNoForecast)
	default:
		s.queued = true
	}
}

// blockerRank orders the reasons a blocker gives, the one said first.
func blockerRank(r wire.WorkTimelineScheduleReason) int {
	return slices.Index([]wire.WorkTimelineScheduleReason{wire.WorkTimelineScheduleReasonBlockerOnHold,
		wire.WorkTimelineScheduleReasonBlockerNotEpic, wire.WorkTimelineScheduleReasonBlockerNotRead}, r)
}

// allClosed says every child of the epic is closed.
func (e timelineEpic) allClosed() bool {
	return len(e.wire.Children) > 0 && !slices.ContainsFunc(e.wire.Children, func(k wire.WorkTimelineChild) bool { return k.ClosedAt == nil })
}

// markCycles leaves undated each queued epic whose open epic blockers lead back to it (#7067): no order breaks a cycle.
func markCycles(all []*scheduleEpic) {
	// Tarjan's strongly connected components, on the queued epics' edges to queued epics.
	n := len(all)
	low, num, on := make([]int, n), make([]int, n), make([]bool, n)
	for i := range num {
		num[i] = -1
	}
	var stack []int
	next := 0
	var visit func(v int)
	visit = func(v int) {
		num[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range all[v].deps {
			if !all[w].queued {
				continue
			}
			if num[w] < 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], num[w])
			}
		}
		if low[v] != num[v] {
			return
		}
		var scc []int
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[w] = false
			scc = append(scc, w)
			if w == v {
				break
			}
		}
		if len(scc) > 1 || slices.Contains(all[v].deps, v) {
			for _, w := range scc {
				all[w].queued = false
				all[w].undated(wire.WorkTimelineScheduleReasonCycle)
			}
		}
	}
	for v := range all {
		if all[v].queued && num[v] < 0 {
			visit(v)
		}
	}
}

// placeEpics gives the queued epics their places: at each moment, from now, the free places go to the queued epics
// whose open epic blockers have all finished, in the queue's order; then time moves to the next place that frees or
// blocker that finishes. Places that free at the same moment are filled together. A queued epic left over waits for
// an epic with no date, or for a place that never frees.
func placeEpics(all []*scheduleEpic, places int, now time.Time, at forecastAt) {
	t := float64(wire.DateOf(now))
	// finished is when a blocker finishes, ok false while it has no date.
	finished := func(s *scheduleEpic) (float64, bool) {
		if s.row.Finish == nil {
			return 0, false
		}
		return float64(s.row.Finish.Mid), true
	}
	for {
		busy := 0
		for _, s := range all {
			if (s.holds || s.row.PlannedStart != nil) && s.release > t {
				busy++
			}
		}
		for _, s := range all {
			if busy >= places {
				break
			}
			if !s.queued || !s.ready(all, t, finished) {
				continue
			}
			start := wire.Date(t)
			f := at(s.e, start.Time())
			if f == nil {
				s.queued = false
				s.undated(wire.WorkTimelineScheduleReasonNoForecast)
				continue
			}
			s.queued = false
			s.row.PlannedStart, s.row.Finish, s.release = &start, f, float64(f.Mid)
			busy++
		}
		next := math.Inf(1)
		for _, s := range all {
			if (s.holds || s.row.PlannedStart != nil) && s.release > t {
				next = min(next, s.release)
			}
			if s.queued {
				for _, d := range s.deps {
					if f, ok := finished(all[d]); ok && f > t {
						next = min(next, f)
					}
				}
			}
		}
		if math.IsInf(next, 1) {
			break
		}
		t = next
	}
	for _, s := range all {
		if !s.queued {
			continue
		}
		s.queued = false
		reason := wire.WorkTimelineScheduleReasonNoPlace
		for _, d := range s.deps {
			if _, ok := finished(all[d]); !ok {
				reason = wire.WorkTimelineScheduleReasonBlockerUndated
			}
		}
		s.undated(reason)
	}
}

// ready says every open epic blocker of s has finished by t.
func (s *scheduleEpic) ready(all []*scheduleEpic, t float64, finished func(*scheduleEpic) (float64, bool)) bool {
	for _, d := range s.deps {
		if f, ok := finished(all[d]); !ok || f > t {
			return false
		}
	}
	return true
}

// compareRows is the rows' order (#7067): the started epics by start, then those planned by planned start, then those
// with no date, then those on hold; the sort is stable, so ties keep the order of forge/next.
func compareRows(a, b wire.WorkTimelineScheduleRow) int {
	group := func(r wire.WorkTimelineScheduleRow) (int, wire.Date) {
		switch {
		case r.Reason != nil && *r.Reason == wire.WorkTimelineScheduleReasonOnHold:
			return 3, 0
		case r.StartedAt != nil:
			return 0, *r.StartedAt
		case r.PlannedStart != nil:
			return 1, *r.PlannedStart
		}
		return 2, 0
	}
	ga, da := group(a)
	gb, db := group(b)
	return cmp.Or(cmp.Compare(ga, gb), cmp.Compare(da, db))
}
