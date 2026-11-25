package agentsd

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Work ▸ Timeline's data (#4172, specs/time-stream/wireframe.md sections 4, 7 to 9): a project's
// epics that overlap a window, each child with when it started, the epic's lines
// (segments), and the open line's forecast (M10, lineage/forecast.go); and the releases in the
// window. It reads GitHub's epics only for now: GitHub gives the sub-issues and their dates in
// one query. The forecast learns from the epics of the last 30 days, so a read asks for those
// as well as the window's. A client that asks for the first answer gets the window's epics alone, with no forecast,
// and asks again for all of it (#7027).

// timelineWaitLabels mark a child that waits for a person: it is not forecast.
var timelineWaitLabels = []string{"alex-todo", "needs-account"}

// timelineGitHubOnly ends the error that says a project has folders on other forges.
const timelineGitHubOnly = " reads only GitHub's epics for now"

// waitsForPerson is true for an open issue with one of timelineWaitLabels, else nil.
func waitsForPerson(i wire.ForgeItem) *bool {
	if i.State == "open" && slices.ContainsFunc(i.Labels, func(l wire.ForgeLabel) bool {
		return slices.Contains(timelineWaitLabels, strings.ToLower(l.Name))
	}) {
		return new(true)
	}
	return nil
}

// timelineBacktestEvery is how often a project's backtest runs again: at the first read after it.
const timelineBacktestEvery = 24 * time.Hour

type timelineBook struct {
	backtestMu sync.Mutex
	// backtests are each project's last backtest, ok false when it had too few cuts.
	backtests map[string]timelineBacktest
	// keptEpics are the closed epics (#7028).
	keptEpics timelineKeep
}

type timelineBacktest struct {
	at time.Time
	bt lineage.Backtest
	ok bool
}

func (d *Daemon) registerWorkTimeline() {
	On(d, wire.CallWorkTimeline, func(ctx context.Context, _ *Client, r wire.WorkTimelineRequest) (wire.WorkTimeline, error) {
		return d.workTimeline(ctx, r)
	})
}

func (d *Daemon) workTimeline(ctx context.Context, r wire.WorkTimelineRequest) (wire.WorkTimeline, error) {
	const method = "work/timeline"
	if !wire.ValidProjectID(r.Project) {
		return wire.WorkTimeline{}, Invalid("%s: project is a project's id", method)
	}
	if r.To < r.From {
		return wire.WorkTimeline{}, Invalid("%s: to is before from", method)
	}
	out := wire.WorkTimeline{Epics: []wire.WorkTimelineEpic{}, Releases: []wire.WorkTimelineRelease{}, TimeZone: zoneName()}
	repos, folderOf, other := d.workTimelineRepos(r.Project)
	if other {
		out.Errors = append(out.Errors, method+timelineGitHubOnly)
	}
	if len(repos) == 0 {
		return out, nil
	}
	slices.SortFunc(repos, func(a, b forgeRepo) int { return cmp.Compare(a.path, b.path) })
	now := nowFunc()
	since := now.Add(-lineage.ForecastWindow)
	if from := r.From.Time(); from.Before(since) {
		since = from
	}
	since = since.UTC().Truncate(24 * time.Hour)
	// The epics the answer needs: with no from, the history's too.
	var from time.Time
	if r.First != nil && *r.First {
		from = r.From.Time()
	}
	c, ok := d.githubConnection()
	if !ok || !c.Usable(now) {
		out.Errors = append(out.Errors, githubmcp.NotConnected)
		return out, nil
	}
	gh, conn := d.forgeGitHubAs(c.Access), timelineConn(c)
	d.keptEpics.use(conn)
	d.timelineForgetGone()
	var epics []timelineEpic
	scopes := []string{}
	for _, repo := range repos {
		read, err := d.githubTimeline(ctx, gh, conn, repo.path, since, from)
		if err != nil {
			out.Errors = append(out.Errors, "GitHub: "+repo.path+": "+err.Error())
			continue
		}
		scopes = append(scopes, repo.forge+":"+strings.ToLower(repo.path))
		if len(read.Rest) > 0 {
			out.Partial = new(true)
		}
		for _, e := range read.Epics {
			epics = append(epics, d.timelineEpicOf(e, folderOf[repo]))
		}
		for _, rel := range read.Releases {
			at := forgeDate(rel.PublishedAt)
			if at >= r.From && at <= r.To {
				out.Releases = append(out.Releases, wire.WorkTimelineRelease{Tag: rel.Tag, At: at, Repo: repo.path, URL: optional(rel.URL)})
			}
		}
	}
	// With a repo not read, the client asks another machine for them all (#7029).
	if len(scopes) == len(repos) {
		out.Scopes = scopes
	}
	// The forecast draws from the epics in their order, so the order does not depend on which of them were kept.
	slices.SortFunc(epics, func(a, b timelineEpic) int { return cmp.Compare(a.wire.Item.Node.Ref, b.wire.Item.Node.Ref) })
	forecast := func(*timelineEpic) {}
	if out.Partial == nil {
		forecast = d.timelineForecast(r.Project, epics, now, &out)
	}
	for _, e := range epics {
		if len(e.wire.Children) == 0 || !e.overlaps(r.From, r.To) {
			continue
		}
		forecast(&e)
		out.Epics = append(out.Epics, e.wire)
	}
	slices.SortStableFunc(out.Epics, func(a, b wire.WorkTimelineEpic) int {
		return cmp.Or(cmp.Compare(firstStart(a), firstStart(b)), cmp.Compare(a.Item.Node.Ref, b.Item.Node.Ref))
	})
	slices.SortFunc(out.Releases, func(a, b wire.WorkTimelineRelease) int { return cmp.Compare(a.At, b.At) })
	return out, nil
}

// timelineForecast learns from the epics, sets the answer's backtest, and gives what forecasts an epic.
func (d *Daemon) timelineForecast(project string, epics []timelineEpic, now time.Time, out *wire.WorkTimeline) func(*timelineEpic) {
	spans, closes := timelineHistory(epics)
	loc := time.Local
	bt, btOK := d.timelineBacktest(project, spans, closes, now, loc)
	width := 1.0
	if btOK {
		width = bt.Width
		out.Backtest = &wire.WorkTimelineBacktest{Error: bt.Error, Width: bt.Width, Cuts: int64(bt.Cuts), Holds: int64(bt.Holds),
			Epics: int64(bt.Spans), At: wire.DateOf(d.timelineBacktestAt(project))}
	}
	model, modelOK := lineage.Learn(spans, closes, now, loc)
	return func(e *timelineEpic) {
		if modelOK {
			e.forecast(model, now, width)
		}
	}
}

// workTimelineRepos is each repo of a project's folders, once, with the folder it came
// from. other says that a folder's repo is on a forge other than GitHub.
func (d *Daemon) workTimelineRepos(project string) (repos []forgeRepo, folderOf map[forgeRepo]string, other bool) {
	folderOf = map[forgeRepo]string{}
	for _, f := range d.folderPaths() {
		if projectOfFolder(f) != project {
			continue
		}
		repo := d.folderRepo(f)
		if _, seen := folderOf[repo]; seen || repo.path == "" {
			continue
		}
		if repo.forge != "github" {
			other = true
			continue
		}
		folderOf[repo] = f
		repos = append(repos, repo)
	}
	return repos, folderOf, other
}

// epicOnHold says that an epic's labels say on hold: the Timeline never draws it (#7058), so its sub-issues are not
// read (#7026), also not for the forecast.
func epicOnHold(labels []string) bool { return slices.ContainsFunc(labels, isHoldName) }

// githubTimeline is a repo's timeline read, kept for forgeCacheFor as the other forge reads are. With a from, the
// epics closed before it can be left out, in the answer's Rest (#7027); with none, every epic is read, and only the
// rest of a first read that is kept. A closed epic that keptEpics has is not read again (#7028).
func (d *Daemon) githubTimeline(ctx context.Context, gh forgeGitHub, conn, repo string, since, from time.Time) (githubmcp.Timeline, error) {
	key, _ := json.Marshal([]any{"work/timeline", conn, repo, since})
	firstKey := "first " + string(key)
	if a, ok := d.forgeCached(string(key)); ok && a.timeline != nil {
		return *a.timeline, nil
	}
	first, kept := d.forgeCached(firstKey)
	kept = kept && first.timeline != nil
	if kept && !from.IsZero() && !first.timelineFrom.After(from) {
		return *first.timeline, nil
	}
	var t githubmcp.Timeline
	var err error
	now, listed, had := nowFunc(), map[int]bool{}, map[int]bool{}
	rest := kept && from.IsZero()
	if rest {
		t, err = gh.TimelineRest(ctx, repo, *first.timeline)
	} else {
		closedFrom := ""
		if !from.IsZero() {
			closedFrom = from.UTC().Format(time.RFC3339)
		}
		t, err = gh.Timeline(ctx, repo, since.Format(time.RFC3339), closedFrom, epicOnHold, func(number int, updatedAt string) (githubmcp.TimelineEpic, bool) {
			e, ok := d.keptEpics.get(conn, repo, number, updatedAt, now)
			listed[number], had[number] = true, ok
			return e, ok
		})
	}
	if err != nil {
		return githubmcp.Timeline{}, err
	}
	if rest {
		d.keptEpics.put(conn, repo, t.Epics[len(first.timeline.Epics):], now)
	} else {
		d.keptEpics.put(conn, repo, slices.DeleteFunc(slices.Clone(t.Epics), func(e githubmcp.TimelineEpic) bool { return had[e.Number] }), now)
		d.keptEpics.trim(conn, repo, listed, now)
	}
	if len(t.Rest) > 0 {
		d.forgeRemember(firstKey, forgeAnswer{timeline: &t, timelineFrom: from})
	} else {
		d.forgeRemember(string(key), forgeAnswer{timeline: &t})
	}
	return t, nil
}

// timelineEpic is an epic on its way to the wire, with its children's dates as times and its
// segments as the children in each.
type timelineEpic struct {
	wire       wire.WorkTimelineEpic
	notPlanned bool
	segments   [][]int
}

func (d *Daemon) timelineEpicOf(e githubmcp.TimelineEpic, folder string) timelineEpic {
	out := timelineEpic{wire: wire.WorkTimelineEpic{Item: forgeItemOf(e.Item, folder), StateReason: optional(e.StateReason),
		CreatedAt: forgeDate(e.CreatedAt), ClosedAt: forgeDateOptional(e.ClosedAt), Children: []wire.WorkTimelineChild{},
		Segments: []wire.WorkTimelineSegment{}}, notPlanned: e.StateReason == "not_planned"}
	for _, c := range e.Children {
		child := wire.WorkTimelineChild{Item: forgeItemOf(c.Item, d.repoFolder(forgeRepo{"github", strings.ToLower(c.Repo)})),
			StateReason: optional(c.StateReason), CreatedAt: forgeDate(c.CreatedAt), ClosedAt: forgeDateOptional(c.ClosedAt)}
		child.StartedAt = d.childStarted(child.Item.Node, child.ClosedAt, c.PROpenedAt)
		child.WaitsForPerson = waitsForPerson(child.Item)
		out.wire.Children = append(out.wire.Children, child)
	}
	out.segment()
	return out
}

// childStarted is when a child started (section 8): the earliest of the first session linked
// to it in lineage, the opening of the first pull request that closes it, and its close. Nil
// when none is known.
func (d *Daemon) childStarted(node wire.LineageNode, closed *wire.Date, prOpened []string) *wire.Date {
	var start *wire.Date
	earlier := func(t wire.Date) {
		if t > 0 && (start == nil || t < *start) {
			start = &t
		}
	}
	for _, l := range d.lineage().Links(node).Links {
		if l.To == node && l.From.Kind == lineage.KindSession {
			earlier(l.At)
		}
	}
	for _, at := range prOpened {
		earlier(forgeDate(at))
	}
	if closed != nil {
		earlier(*closed)
	}
	return start
}

// segment puts the children in segments (section 7): in the order they were filed, a child
// joins the segment before it unless that segment had ended (all of its children closed)
// before the child was filed. A segment starts at its first child's start and ends at its last
// child's close, with none while one is open.
func (e *timelineEpic) segment() {
	kids := e.wire.Children
	order := make([]int, len(kids))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(kids[a].CreatedAt, kids[b].CreatedAt) })
	var end *wire.Date
	for _, i := range order {
		k := kids[i]
		if len(e.segments) == 0 || end != nil && k.CreatedAt > *end {
			e.segments = append(e.segments, nil)
			end = k.ClosedAt
		} else if end != nil && (k.ClosedAt == nil || *k.ClosedAt > *end) {
			end = k.ClosedAt
		}
		e.segments[len(e.segments)-1] = append(e.segments[len(e.segments)-1], i)
	}
	for _, members := range e.segments {
		var s wire.WorkTimelineSegment
		closed := true
		for _, i := range members {
			k := kids[i]
			if k.StartedAt != nil && (s.StartedAt == nil || *k.StartedAt < *s.StartedAt) {
				s.StartedAt = k.StartedAt
			}
			if k.ClosedAt == nil {
				closed = false
			} else if s.ClosedAt == nil || *k.ClosedAt > *s.ClosedAt {
				s.ClosedAt = k.ClosedAt
			}
		}
		if !closed {
			s.ClosedAt = nil
		}
		e.wire.Segments = append(e.wire.Segments, s)
	}
}

// overlaps says whether the epic's block (#7058: one, from its first child's start to when its work stopped)
// overlaps from to to. An epic with an open child has no end, and one with no start yet (a ghost, from now to
// its predicted finish) always does.
func (e timelineEpic) overlaps(from, to wire.Date) bool {
	var last wire.Date
	started := false
	for _, s := range e.wire.Segments {
		if s.StartedAt != nil && *s.StartedAt <= to {
			started = true
		}
		if s.ClosedAt == nil {
			if s.StartedAt == nil {
				return true
			}
			last = max(last, from)
		} else {
			last = max(last, *s.ClosedAt)
		}
	}
	return started && last >= from
}

// forecast sets an open epic's forecast, its open last segment's: its children that wait for a person are not
// forecast, and the line does not join while one is open.
func (e *timelineEpic) forecast(m lineage.Model, now time.Time, width float64) {
	if e.wire.ClosedAt != nil || len(e.segments) == 0 || e.wire.Segments[len(e.segments)-1].ClosedAt != nil {
		return
	}
	known, done, open, waits := 0, 0, 0, false
	for _, i := range e.segments[len(e.segments)-1] {
		k := e.wire.Children[i]
		switch {
		case k.WaitsForPerson != nil:
			waits = true
		case k.ClosedAt != nil:
			known++
			done++
		default:
			known++
			open++
		}
	}
	f, ok := m.Forecast(now, known, done, open, width, lineage.Rng(e.wire.Item.Node.Ref))
	if !ok {
		return
	}
	e.wire.Forecast = &wire.WorkTimelineForecast{Low: wire.DateOf(f.Low), Mid: wire.DateOf(f.Mid), High: wire.DateOf(f.High)}
	if waits {
		e.wire.Forecast.Waits = new(true)
	}
}

// timelineHistory is what the forecast learns from: each closed segment of an epic not closed as
// not planned, and every child's close.
func timelineHistory(epics []timelineEpic) ([]lineage.Span, []time.Time) {
	var spans []lineage.Span
	var closes []time.Time
	for _, e := range epics {
		for _, k := range e.wire.Children {
			if k.ClosedAt != nil {
				closes = append(closes, k.ClosedAt.Time())
			}
		}
		if e.notPlanned {
			continue
		}
		for n, s := range e.wire.Segments {
			if s.ClosedAt == nil || s.StartedAt == nil {
				continue
			}
			span := lineage.Span{Start: s.StartedAt.Time(), End: s.ClosedAt.Time()}
			for _, i := range e.segments[n] {
				k := e.wire.Children[i]
				span.Children = append(span.Children, lineage.SpanChild{Filed: k.CreatedAt.Time(), Closed: k.ClosedAt.Time()})
			}
			spans = append(spans, span)
		}
	}
	return spans, closes
}

// timelineBacktest is the project's backtest, run again when the last is timelineBacktestEvery
// old: the history it needs is the same read's.
func (d *Daemon) timelineBacktest(project string, spans []lineage.Span, closes []time.Time, now time.Time, loc *time.Location) (lineage.Backtest, bool) {
	d.backtestMu.Lock()
	defer d.backtestMu.Unlock()
	if b, ok := d.backtests[project]; ok && now.Sub(b.at) < timelineBacktestEvery {
		return b.bt, b.ok
	}
	bt, ok := lineage.RunBacktest(spans, closes, now, loc, lineage.BacktestCuts)
	if d.backtests == nil {
		d.backtests = map[string]timelineBacktest{}
	}
	d.backtests[project] = timelineBacktest{at: now, bt: bt, ok: ok}
	return bt, ok
}

func (d *Daemon) timelineBacktestAt(project string) time.Time {
	d.backtestMu.Lock()
	defer d.backtestMu.Unlock()
	return d.backtests[project].at
}

// firstStart is when an epic's first line starts, for the order; one with no start goes last.
func firstStart(e wire.WorkTimelineEpic) wire.Date {
	for _, s := range e.Segments {
		if s.StartedAt != nil {
			return *s.StartedAt
		}
	}
	return wire.Date(1e18)
}

func forgeDateOptional(s string) *wire.Date {
	if t := forgeDate(s); t != 0 {
		return &t
	}
	return nil
}
