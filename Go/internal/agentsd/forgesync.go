package agentsd

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The reads a client cache syncs by (#6349, epic #6348, .agents/skills/epic-lifecycle/
// principles.md): each client keeps its own cursor (the latest updatedAt a read gave) and asks
// forge/changes for what changed since it, so a poll reads a page of what changed, not every
// list again. The daemon keeps no cursor. forge/ids is the full check, from time to time, for
// what a changed-since read does not see (a deleted, moved or hidden item); forge/checks reads
// the checks of many pull requests at once, as they do not move updatedAt; forge/sources says
// which sources this machine reads, so a client asks one machine for each.
//
// A source is a forge and one repository, project or team, in lower case, as the topic of its
// nodes' refs (github:owner/repo, jira:proj, linear:eng). A read goes stream by stream: a
// stream is one forge's list of one kind (issues or pull requests) of one source, or of all the forge's
// sources here when the request names none. A forge whose one query gives at most SearchMost
// items (GitHub's search: 1,000) is read in windows of updatedAt: when a window ends at its
// limit, the next is of the items updated at or before the last one read.
const (
	// syncChangesMost bounds a forge/changes page; syncIdsPage is a forge/ids page.
	syncChangesMost = 100
	syncIdsPage     = 1000
	// syncCallsMost bounds the forge calls of one page, so a page answers in bounded time.
	syncCallsMost = 12
	// syncUnchangedMost bounds forgeUnchanged.
	syncUnchangedMost = 200
)

func (d *Daemon) registerForgeSync() {
	On(d, wire.CallForgeChanges, func(ctx context.Context, _ *Client, r wire.ForgeChangesRequest) (wire.ForgeChanges, error) {
		return d.forgeChanges(ctx, r)
	})
	On(d, wire.CallForgeIds, func(ctx context.Context, _ *Client, r wire.ForgeIdsRequest) (wire.ForgeIds, error) {
		return d.forgeIds(ctx, r)
	})
	On(d, wire.CallForgeChecks, func(ctx context.Context, _ *Client, r wire.ForgeChecksRequest) (wire.ForgeChecks, error) {
		return d.forgePullChecks(ctx, r)
	})
	On(d, wire.CallForgeSources, func(ctx context.Context, _ *Client, _ wire.Empty) (wire.ForgeSources, error) {
		return d.forgeSourceList(ctx), nil
	})
}

// MARK: Sources

// scopeSource is one source of a forge's scope: its name, a folder on it, and the scope
// narrowed to it.
type scopeSource struct {
	name, folder string
	scope        forgeScope
}

// sourcesOf is the sources of a forge's scope, in the scope's order.
func sourcesOf(forge string, scope forgeScope) []scopeSource {
	var out []scopeSource
	each := func(names []string, folderOf map[string]string, narrow func(string) forgeScope) {
		for _, n := range names {
			out = append(out, scopeSource{name: forge + ":" + strings.ToLower(n), folder: folderOf[n], scope: narrow(n)})
		}
	}
	switch s := scope.(type) {
	case githubScope:
		each(s.repos, s.folderOf, func(n string) forgeScope { c := s; c.repos = []string{n}; return c })
	case gitlabScope:
		each(s.projects, s.folderOf, func(n string) forgeScope { c := s; c.projects = []string{n}; return c })
	case forgejoScope:
		each(s.repos, s.folderOf, func(n string) forgeScope { c := s; c.repos = []string{n}; return c })
	case bitbucketScope:
		each(s.repos, s.folderOf, func(n string) forgeScope { c := s; c.repos = []string{n}; return c })
	case azureDevOpsScope:
		each(s.repos, s.folderOf, func(n string) forgeScope { c := s; c.repos = []string{n}; return c })
	case jiraScope:
		each(s.projects, s.projectFolder, func(n string) forgeScope { c := s; c.projects = []string{n}; return c })
	case boardsScope:
		each(s.projects, s.projectFolder, func(n string) forgeScope { c := s; c.projects = []string{n}; return c })
	case linearScope:
		if s.workspace && len(s.teams) == 0 {
			// The whole workspace, as no folder names a team: one source.
			return []scopeSource{{name: forge, folder: s.home, scope: s}}
		}
		each(s.teams, s.teamFolder, func(n string) forgeScope { c := s; c.teams = []string{n}; c.workspace = false; return c })
	}
	return out
}

// syncStream is one forge's list of one kind, of one source or ("" source) of all of the
// forge's sources here; err says why the forge left folders out, or could not be read at all.
type syncStream struct {
	forge  Forge
	prs    bool
	source string
	scope  forgeScope
	err    error
}

// id names the stream in a cursor.
func (s syncStream) id() string {
	return s.forge.Name() + " " + map[bool]string{false: lineage.KindIssue, true: lineage.KindPR}[s.prs] + " " + s.source
}

// syncStreams are the streams of kinds, of source ("" for every source here), in a fixed order:
// by forge, then kind, then source. found says the source is one of this machine's.
func (d *Daemon) syncStreams(ctx context.Context, kinds []string, source string) (streams []syncStream, found bool) {
	folders := d.folderPaths()
	for _, f := range d.forges() {
		for _, prs := range []bool{false, true} {
			if !slices.Contains(kinds, map[bool]string{false: lineage.KindIssue, true: lineage.KindPR}[prs]) || !forgeHas(f, prs) {
				continue
			}
			scope, err := f.Sources(ctx, folders, prs)
			got := streamsOf(f, prs, scope, err, source)
			found = found || source != "" && len(got) > 0
			streams = append(streams, got...)
		}
	}
	return streams, found || source == ""
}

// forgeHas says forge f has pull requests (prs) or issues.
func forgeHas(f Forge, prs bool) bool {
	caps := f.Caps()
	return prs && caps.PRs || !prs && caps.Issues
}

// streamsOf are the streams of one forge's scope of one kind: the whole scope for "" source,
// else the source's, when it is in the scope.
func streamsOf(f Forge, prs bool, scope forgeScope, err error, source string) []syncStream {
	if scope == nil || scope.Empty() {
		if err != nil && source == "" {
			return []syncStream{{forge: f, prs: prs, err: err}}
		}
		return nil
	}
	if source == "" {
		return []syncStream{{forge: f, prs: prs, scope: scope, err: err}}
	}
	var out []syncStream
	for _, s := range sourcesOf(f.Name(), scope) {
		if s.name == source {
			out = append(out, syncStream{forge: f, prs: prs, source: s.name, scope: s.scope})
		}
	}
	return out
}

// forgeSourceList is forge/sources: each source of this machine's folders, once, with the kinds
// it has.
func (d *Daemon) forgeSourceList(ctx context.Context) wire.ForgeSources {
	start := time.Now()
	out := wire.ForgeSources{Sources: []wire.ForgeSource{}}
	at := map[string]int{}
	folders := d.folderPaths()
	for _, f := range d.forges() {
		for _, prs := range []bool{false, true} {
			if !forgeHas(f, prs) {
				continue
			}
			scope, err := f.Sources(ctx, folders, prs)
			if err != nil && !slices.ContainsFunc(out.Errors, func(e wire.ForgeListError) bool { return e.Forge == f.Name() }) {
				out.Errors = append(out.Errors, forgeListError(f.Name(), err))
			}
			if scope == nil || scope.Empty() {
				continue
			}
			kind := map[bool]string{false: lineage.KindIssue, true: lineage.KindPR}[prs]
			for _, s := range sourcesOf(f.Name(), scope) {
				n, ok := at[s.name]
				if !ok {
					n = len(out.Sources)
					at[s.name] = n
					out.Sources = append(out.Sources, wire.ForgeSource{Source: s.name, Forge: f.Name(), Kinds: []string{}, Folder: optional(s.folder)})
				}
				out.Sources[n].Kinds = append(out.Sources[n].Kinds, kind)
			}
		}
	}
	log.Printf("forge/sources: %d sources, no forge calls, in %s", len(out.Sources), time.Since(start).Round(time.Millisecond))
	return out
}

// MARK: The sweep

// syncCursor is where a forge/changes or forge/ids read goes on: the request it is of (Key), the
// stream it is in, that stream's forge cursor, its window (Before, a wire.Date, 0 for none), how
// many items the window gave (N) and the items at Before given already (Seen); and of the read so far, the latest updatedAt (High),
// whether a stream failed, and when it began.
type syncCursor struct {
	Key    string  `json:"k"`
	Stream string  `json:"s,omitempty"`
	At     string  `json:"a,omitempty"`
	Before float64 `json:"b,omitempty"`
	N      int     `json:"n,omitempty"`
	// Seen are the refs given already that were updated at Before, which the window leaves out.
	Seen   []string `json:"seen,omitempty"`
	High   float64  `json:"h,omitempty"`
	Failed bool     `json:"f,omitempty"`
	Began  float64  `json:"t,omitempty"`
}

func (c syncCursor) text() *string {
	b, _ := json.Marshal(c)
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

// syncCursorOf reads a page cursor of the request named key; nil is the read's start.
func syncCursorOf(method string, s *string, key string) (syncCursor, error) {
	c := syncCursor{Key: key}
	if s == nil || *s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(*s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Key != key {
		return syncCursor{}, Invalid("%s: the pageCursor is not one a read of the same request gave", method)
	}
	return c, nil
}

// syncKey names a request in its cursors, so a cursor goes on only the read it came from.
func syncKey(parts ...any) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// syncCall is one forge call of a read, for its log line.
type syncCall struct {
	what  string
	items int
	took  time.Duration
}

func (c syncCall) String() string {
	return fmt.Sprintf("%s %d in %s", c.what, c.items, c.took.Round(time.Millisecond))
}

// syncCalls is a read's log line: the forge calls it made and how long each took.
func syncCalls(calls []syncCall) string {
	if len(calls) == 0 {
		return "no forge calls"
	}
	parts := make([]string, len(calls))
	for n, c := range calls {
		parts[n] = c.String()
	}
	return fmt.Sprintf("%d forge calls (%s)", len(calls), strings.Join(parts, "; "))
}

// sweepPage is one page of a sweep: its items, its forge calls, the errors of the streams that
// failed, and the cursor of the next page (nil at the end).
type sweepPage struct {
	items  []wire.ForgeItem
	calls  []syncCall
	errors []wire.ForgeListError
	next   *syncCursor
	high   float64
	failed bool
}

// sweep reads up to limit items of streams from c, with base as each stream's filter, in at
// most syncCallsMost forge calls. probe, when not nil, may answer a stream's first call with no
// list (forgeChanges's GitHub probe).
func (d *Daemon) sweep(ctx context.Context, streams []syncStream, base forgeFilter, c syncCursor, limit int,
	probe func(ctx context.Context, s syncStream, f forgeFilter) (*sweepProbe, syncCall, bool)) (sweepPage, error) {
	w := sweeper{streams: streams, base: base, c: c, limit: limit, probe: probe, out: sweepPage{high: c.High, failed: c.Failed}}
	if c.Stream != "" {
		w.i = slices.IndexFunc(streams, func(s syncStream) bool { return s.id() == c.Stream })
		if w.i < 0 {
			return sweepPage{}, errors.New("the folders on this machine changed since the read began: start it again")
		}
	}
	for w.i < len(streams) && len(w.out.items) < limit && len(w.out.calls) < syncCallsMost {
		w.step(ctx)
	}
	if w.i < len(streams) {
		w.c.Stream = streams[w.i].id()
		w.c.High, w.c.Failed = w.out.high, w.out.failed
		w.out.next = &w.c
	}
	return w.out, nil
}

// sweeper is a sweep under way: the stream it is in (i), its cursor and the page so far.
type sweeper struct {
	streams []syncStream
	base    forgeFilter
	c       syncCursor
	limit   int
	probe   func(ctx context.Context, s syncStream, f forgeFilter) (*sweepProbe, syncCall, bool)
	i       int
	out     sweepPage
}

// nextStream goes on to the next stream, from its start.
func (w *sweeper) nextStream() {
	w.i++
	w.c.At, w.c.Before, w.c.N, w.c.Seen = "", 0, 0, nil
}

// step makes one read of the stream the sweep is in, and goes on from it.
func (w *sweeper) step(ctx context.Context) {
	s := w.streams[w.i]
	// A forge that left folders out (another GitLab, no sign-in) says so; their items are not
	// read until that changes, so the read's highWater still moves.
	if s.err != nil && !slices.ContainsFunc(w.out.errors, func(e wire.ForgeListError) bool { return e.Forge == s.forge.Name() }) {
		w.out.errors = append(w.out.errors, forgeListError(s.forge.Name(), s.err))
	}
	if s.scope == nil {
		w.nextStream()
		return
	}
	p, ended, err := w.read(ctx, s)
	if err != nil {
		w.out.errors = append(w.out.errors, forgeListError(s.forge.Name(), err))
		w.out.failed = true
		w.nextStream()
		return
	}
	w.take(p.items)
	switch {
	case p.next != "":
		w.c.At = p.next
	case !w.nextWindow(s, p.items, ended):
		w.nextStream()
	}
}

// read is one page of stream s where the cursor is: from the probe when it knows the answer,
// else from the forge's list, cut at the bounds; ended says the bounds ended it.
func (w *sweeper) read(ctx context.Context, s syncStream) (forgePage, bool, error) {
	f := w.base
	f.PRs = s.prs
	if w.c.Before > 0 {
		f.UpdatedBefore = wire.Date(w.c.Before).Time()
	}
	var known *sweepProbe
	if w.probe != nil && w.c.At == "" && w.c.Before == 0 {
		var call syncCall
		var ok bool
		if known, call, ok = w.probe(ctx, s, f); ok {
			w.out.calls = append(w.out.calls, call)
		}
	}
	if known != nil && known.have {
		return forgePage{items: known.items}, false, nil
	}
	start := time.Now()
	p, err := s.forge.List(ctx, s.scope, f, w.c.At, min(w.limit-len(w.out.items), forgeListMost))
	w.out.calls = append(w.out.calls, syncCall{what: s.forge.Name() + " " + map[bool]string{false: "issues", true: "pulls"}[s.prs],
		items: len(p.items), took: time.Since(start)})
	if err != nil {
		return forgePage{}, false, err
	}
	p, ended := sinceBounded(p, f)
	if known != nil && p.next == "" {
		known.keep(p.items)
	}
	return p, ended, nil
}

// take adds a read's items to the page, but those at the window's edge given already.
func (w *sweeper) take(read []wire.ForgeItem) {
	w.c.N += len(read)
	for _, item := range read {
		at := float64(item.UpdatedAt)
		if at == w.c.Before && slices.Contains(w.c.Seen, item.Node.Ref) {
			continue
		}
		w.out.high = max(w.out.high, at)
		w.out.items = append(w.out.items, item)
	}
}

// nextWindow starts the next window of a stream whose window ended at the forge's limit: what
// was updated at or before the last item read, which the forge has not given yet, leaving out
// the items at that time that this window gave. false when the stream is done.
func (w *sweeper) nextWindow(s syncStream, read []wire.ForgeItem, ended bool) bool {
	most := s.forge.Caps().SearchMost
	if most == 0 || ended || w.c.N < most || len(read) == 0 {
		return false
	}
	last := float64(read[len(read)-1].UpdatedAt)
	if w.c.Before != 0 && last >= w.c.Before {
		return false
	}
	var seen []string
	for _, item := range read {
		if float64(item.UpdatedAt) == last {
			seen = append(seen, item.Node.Ref)
		}
	}
	w.c.At, w.c.Before, w.c.N, w.c.Seen = "", last, 0, seen
	return true
}

// sinceBounded keeps the items of p within f's bounds, for a forge whose API does not narrow
// its list by them. A list is the newest first, so an item older than UpdatedSince ends it:
// ended says p ends there. A page with bounds has no total: a forge's count is of its whole list.
func sinceBounded(p forgePage, f forgeFilter) (forgePage, bool) {
	if f.UpdatedSince.IsZero() && f.UpdatedBefore.IsZero() {
		return p, false
	}
	since, before := wire.DateOf(f.UpdatedSince), wire.DateOf(f.UpdatedBefore)
	out := forgePage{forge: p.forge, next: p.next, err: p.err}
	ended := false
	for n, i := range p.items {
		if !f.UpdatedBefore.IsZero() && i.UpdatedAt > before {
			continue
		}
		if !f.UpdatedSince.IsZero() && i.UpdatedAt < since {
			out.next, ended = "", true
			break
		}
		out.items = append(out.items, i)
		if n < len(p.cursors) {
			out.cursors = append(out.cursors, p.cursors[n])
		}
	}
	return out, ended
}

// MARK: forge/changes

// forgeChanges is a page of the items of r.source (every source when not said) that changed at
// or after r.updatedSince, in every state.
func (d *Daemon) forgeChanges(ctx context.Context, r wire.ForgeChangesRequest) (wire.ForgeChanges, error) {
	const method = "forge/changes"
	start := time.Now()
	kinds, err := syncKinds(method, r.Kinds)
	if err != nil {
		return wire.ForgeChanges{}, err
	}
	limit := syncChangesMost
	if r.Limit != nil {
		if *r.Limit < 1 || *r.Limit > syncChangesMost {
			return wire.ForgeChanges{}, Invalid("%s: limit is 1 to %d", method, syncChangesMost)
		}
		limit = int(*r.Limit)
	}
	var since float64
	if r.UpdatedSince != nil {
		since = float64(*r.UpdatedSince)
	}
	source := deref(r.Source)
	c, err := syncCursorOf(method, r.PageCursor, syncKey(method, source, kinds, since))
	if err != nil {
		return wire.ForgeChanges{}, err
	}
	streams, found := d.syncStreams(ctx, kinds, source)
	if !found {
		return wire.ForgeChanges{}, Invalid("%s: %s is not a source on this machine (forge/sources says them)", method, source)
	}
	base := forgeFilter{State: "all"}
	if since > 0 {
		base.UpdatedSince = wire.Date(since).Time()
	}
	page, err := d.sweep(ctx, streams, base, c, limit, d.githubProbe(since))
	if err != nil {
		return wire.ForgeChanges{}, Invalid("%s: %v", method, err)
	}
	out := wire.ForgeChanges{Items: page.items, Errors: page.errors}
	if out.Items == nil {
		out.Items = []wire.ForgeItem{}
	}
	// A failed source's changes are not read: the next read asks again from where this began.
	out.HighWater = wire.Date(since)
	if !page.failed {
		out.HighWater = wire.Date(max(since, page.high))
	}
	if page.next != nil {
		out.NextPageCursor = page.next.text()
	}
	for _, f := range d.forges() {
		if slices.ContainsFunc(out.Items, func(i wire.ForgeItem) bool { _, ok := f.Owns(i.Node); return ok }) {
			out.Forges = append(out.Forges, f.Caps().wire(f.Name()))
		}
	}
	d.resolvedFromForge(out.Items)
	log.Printf("%s: %s, %d items, %s, in %s", method, cmp.Or(source, "every source"), len(out.Items), syncCalls(page.calls),
		time.Since(start).Round(time.Millisecond))
	return out, nil
}

// syncKinds is a request's kinds, checked, in a fixed order.
func syncKinds(method string, kinds []string) ([]string, error) {
	var out []string
	for _, k := range []string{lineage.KindIssue, lineage.KindPR} {
		if slices.Contains(kinds, k) {
			out = append(out, k)
		}
	}
	if len(out) == 0 || slices.ContainsFunc(kinds, func(k string) bool { return k != lineage.KindIssue && k != lineage.KindPR }) {
		return nil, Invalid("%s: kinds is issue, pr or both", method)
	}
	return out, nil
}

// sweepProbe is what a probe knows of a stream: the items of its last read while unchanged, or
// nil items for a read to make and keep.
type sweepProbe struct {
	items []wire.ForgeItem
	have  bool
	keep  func(items []wire.ForgeItem)
}

// unchangedRead is a GitHub source's last changes read and the probe's mark when it was made.
type unchangedRead struct {
	mark  string
	items []wire.ForgeItem
	at    time.Time
}

// forgeGitHubNewest is the part of GitHub that says whether a repo changed since a time, by a
// REST read with its ETag kept (githubmcp.RealGitHub.Newest).
type forgeGitHubNewest interface {
	Newest(ctx context.Context, repo, since string) (string, error)
}

// githubProbe is forge/changes's probe of a GitHub source's stream (one repo, with a since): a
// REST read of the repo's newest item since then, with its ETag, which costs no rate limit
// while nothing changed (a 304). When its mark is the one of the stream's last read, that read
// is the answer, with no search; else the search is made and kept with the mark. A stream of
// many repos, or with no since (a first fill), is not probed.
func (d *Daemon) githubProbe(since float64) func(context.Context, syncStream, forgeFilter) (*sweepProbe, syncCall, bool) {
	if since <= 0 {
		return nil
	}
	return func(ctx context.Context, s syncStream, f forgeFilter) (*sweepProbe, syncCall, bool) {
		scope, ok := s.scope.(githubScope)
		if s.forge.Name() != "github" || s.source == "" || !ok || len(scope.repos) != 1 {
			return nil, syncCall{}, false
		}
		gh, err := d.forgeGitHubKept()
		if err != nil {
			return nil, syncCall{}, false
		}
		newest, ok := gh.(forgeGitHubNewest)
		if !ok {
			return nil, syncCall{}, false
		}
		start := time.Now()
		mark, err := newest.Newest(ctx, scope.repos[0], f.since())
		call := syncCall{what: "github probe", took: time.Since(start)}
		if err != nil {
			call.what += " failed"
			return nil, call, true
		}
		key := fmt.Sprintf("%s %d", s.id(), f.UpdatedSince.UnixNano())
		d.forgeMu.Lock()
		last, had := d.forgeUnchanged[key]
		d.forgeMu.Unlock()
		if had && last.mark == mark {
			call.what, call.items = "github probe unchanged", len(last.items)
			return &sweepProbe{items: slices.Clone(last.items), have: true}, call, true
		}
		return &sweepProbe{keep: func(items []wire.ForgeItem) {
			d.forgeMu.Lock()
			defer d.forgeMu.Unlock()
			if d.forgeUnchanged == nil {
				d.forgeUnchanged = map[string]unchangedRead{}
			}
			if _, ok := d.forgeUnchanged[key]; !ok && len(d.forgeUnchanged) >= syncUnchangedMost {
				var oldest string
				var at time.Time
				for k, e := range d.forgeUnchanged {
					if at.IsZero() || e.at.Before(at) {
						oldest, at = k, e.at
					}
				}
				delete(d.forgeUnchanged, oldest)
			}
			d.forgeUnchanged[key] = unchangedRead{mark: mark, items: slices.Clone(items), at: nowFunc()}
		}}, call, true
	}
}

// MARK: forge/ids

// forgeIds is a page of the nodes of r.source's items now, of r.kinds in r.states. A stream
// that fails fails the read: a short list would have a client remove items that are there.
func (d *Daemon) forgeIds(ctx context.Context, r wire.ForgeIdsRequest) (wire.ForgeIds, error) {
	const method = "forge/ids"
	start := time.Now()
	kinds, err := syncKinds(method, r.Kinds)
	if err != nil {
		return wire.ForgeIds{}, err
	}
	state := "all"
	switch {
	case slices.Contains(r.States, "open") && slices.Contains(r.States, "closed"), len(r.States) == 0:
	case len(r.States) == 1 && (r.States[0] == "open" || r.States[0] == "closed"):
		state = r.States[0]
	default:
		return wire.ForgeIds{}, Invalid("%s: states is open, closed or both", method)
	}
	if r.Source == "" {
		return wire.ForgeIds{}, Invalid("%s: source is a source of this machine (forge/sources says them)", method)
	}
	c, err := syncCursorOf(method, r.PageCursor, syncKey(method, r.Source, kinds, state))
	if err != nil {
		return wire.ForgeIds{}, err
	}
	if c.Began == 0 {
		c.Began = float64(wire.DateOf(nowFunc()))
	}
	streams, found := d.syncStreams(ctx, kinds, r.Source)
	if !found {
		return wire.ForgeIds{}, Invalid("%s: %s is not a source on this machine (forge/sources says them)", method, r.Source)
	}
	page, err := d.sweep(ctx, streams, forgeFilter{State: state}, c, syncIdsPage, nil)
	if err != nil {
		return wire.ForgeIds{}, Invalid("%s: %v", method, err)
	}
	log.Printf("%s: %s, %d items, %s, in %s", method, r.Source, len(page.items), syncCalls(page.calls), time.Since(start).Round(time.Millisecond))
	if len(page.errors) > 0 {
		return wire.ForgeIds{}, fmt.Errorf("%s: %s could not be read: %s", method, page.errors[0].Forge, page.errors[0].Message)
	}
	out := wire.ForgeIds{Nodes: []wire.LineageNode{}, At: wire.Date(c.Began)}
	seen := map[wire.LineageNode]bool{}
	for _, i := range page.items {
		if !seen[i.Node] {
			seen[i.Node] = true
			out.Nodes = append(out.Nodes, i.Node)
		}
	}
	if page.next != nil {
		out.NextPageCursor = page.next.text()
	}
	return out, nil
}

// MARK: forge/checks

// forgeChecksPages bounds the pages of open pull requests that forge/checks reads on a forge
// with no batched read of checks.
const forgeChecksPages = 3

// forgeGitHubChecks is the part of GitHub that reads many pull requests' checks in one call.
type forgeGitHubChecks interface {
	PullChecks(ctx context.Context, repo string, numbers []int) ([]githubmcp.Item, error)
}

// forgePullChecks is the checks summary of r.numbers, pull requests of r.source: on GitHub in one
// GraphQL call; on another forge from its list of open pull requests, which says each one's
// checks, at most forgeChecksPages pages (a pull request that is no longer open is left out).
func (d *Daemon) forgePullChecks(ctx context.Context, r wire.ForgeChecksRequest) (wire.ForgeChecks, error) {
	const method = "forge/checks"
	start := time.Now()
	numbers, err := checksNumbers(method, r.Numbers)
	if err != nil {
		return wire.ForgeChecks{}, err
	}
	streams, found := d.syncStreams(ctx, []string{lineage.KindPR}, r.Source)
	if !found || len(streams) != 1 || r.Source == "" {
		return wire.ForgeChecks{}, Invalid("%s: %s is not a source of pull requests on this machine (forge/sources says them)", method, r.Source)
	}
	out := wire.ForgeChecks{At: wire.DateOf(nowFunc())}
	var calls []syncCall
	out.Pulls, calls, err = d.githubChecks(ctx, streams[0], numbers)
	if out.Pulls == nil && err == nil {
		out.Pulls, calls, err = d.listedChecks(ctx, streams[0], numbers)
	}
	log.Printf("%s: %s, %d of %d pull requests, %s, in %s", method, r.Source, len(out.Pulls), len(numbers), syncCalls(calls),
		time.Since(start).Round(time.Millisecond))
	if err != nil {
		return wire.ForgeChecks{}, err
	}
	return out, nil
}

// checksNumbers are a forge/checks request's numbers, checked, each once.
func checksNumbers(method string, asked []int64) ([]int, error) {
	if len(asked) < 1 || len(asked) > githubmcp.ChecksMost {
		return nil, Invalid("%s: numbers is 1 to %d pull requests", method, githubmcp.ChecksMost)
	}
	var numbers []int
	for _, n := range asked {
		if n < 1 {
			return nil, Invalid("%s: %d is not a pull request's number", method, n)
		}
		if !slices.Contains(numbers, int(n)) {
			numbers = append(numbers, int(n))
		}
	}
	return numbers, nil
}

// githubChecks are the checks of a GitHub stream's pull requests in one call; nil rows for a
// stream of another forge, or a GitHub with no batched read.
func (d *Daemon) githubChecks(ctx context.Context, s syncStream, numbers []int) ([]wire.ForgeChecksRow, []syncCall, error) {
	scope, ok := s.scope.(githubScope)
	if !ok || s.forge.Name() != "github" {
		return nil, nil, nil
	}
	gh, err := d.forgeGitHubKept()
	if err != nil {
		return nil, nil, err
	}
	batch, ok := gh.(forgeGitHubChecks)
	if !ok {
		return nil, nil, nil
	}
	start := time.Now()
	items, err := batch.PullChecks(ctx, scope.repos[0], numbers)
	calls := []syncCall{{what: "github checks", items: len(items), took: time.Since(start)}}
	rows := []wire.ForgeChecksRow{}
	for _, i := range items {
		rows = append(rows, checksRow(forgeItemOf(i, "")))
	}
	return rows, calls, err
}

// listedChecks are the checks of a stream's open pull requests among numbers, from its list.
func (d *Daemon) listedChecks(ctx context.Context, s syncStream, numbers []int) ([]wire.ForgeChecksRow, []syncCall, error) {
	rows := []wire.ForgeChecksRow{}
	var calls []syncCall
	want := slices.Clone(numbers)
	cursor := ""
	for range forgeChecksPages {
		start := time.Now()
		p, err := s.forge.List(ctx, s.scope, forgeFilter{PRs: true, State: "open"}, cursor, forgeListMost)
		calls = append(calls, syncCall{what: s.forge.Name() + " open pulls", items: len(p.items), took: time.Since(start)})
		if err != nil {
			return nil, calls, err
		}
		for _, i := range p.items {
			if _, ref, ok := d.forgeOwning(i.Node); ok && slices.Contains(want, ref.Number) {
				rows = append(rows, checksRow(i))
				want = slices.DeleteFunc(want, func(n int) bool { return n == ref.Number })
			}
		}
		if cursor = p.next; cursor == "" || len(want) == 0 {
			break
		}
	}
	return rows, calls, nil
}

// checksRow is a pull request's checks summary.
func checksRow(i wire.ForgeItem) wire.ForgeChecksRow {
	return wire.ForgeChecksRow{Node: i.Node, State: optional(i.State), Checks: i.Checks, Review: i.Review, Mergeable: i.Mergeable, AutoMerge: i.AutoMerge}
}
