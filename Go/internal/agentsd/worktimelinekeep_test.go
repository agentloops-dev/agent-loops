package agentsd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/connections"
	"github.com/agentloops-dev/agent-loops/Go/internal/githubmcp"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// keptTestEpic is a closed epic with one child.
func keptTestEpic(n int, closedAt time.Time) githubmcp.TimelineEpic {
	at := closedAt.UTC().Format(time.RFC3339)
	item := githubmcp.Item{Repo: "alexec/agents", Number: n, State: "closed", CreatedAt: at, ClosedAt: at, UpdatedAt: at}
	child := item
	child.Number = n + 1000
	return githubmcp.TimelineEpic{Item: item, Children: []githubmcp.TimelineChild{{Item: child}}}
}

// #7028: work/timeline reads a closed epic once: the open ones each time, a closed one again only when it changed or
// another connection asks. A first answer with every closed epic kept is the full one.
func TestWorkTimelineKeepsClosedEpics(t *testing.T) {
	t.Parallel()

	d, c, gh, file := forgeDaemon(t)
	for _, f := range d.folderPaths() {
		if d.folderRepo(f).path == "alexec/agents" {
			must(t, os.MkdirAll(filepath.Join(f, ".agents"), 0o755))                                 //nolint:gosec // G301: the mode this folder always had
			must(t, os.WriteFile(filepath.Join(f, folderConfig), []byte(`{"project":"p1"}`), 0o644)) //nolint:gosec // G306: the mode this file always had
			break
		}
	}
	now := time.Now()
	open := keptTestEpic(1, now.Add(-72*time.Hour))
	open.State, open.ClosedAt = "open", ""
	open.Children[0].State, open.Children[0].ClosedAt = "open", ""
	gh.timeline.Epics = []githubmcp.TimelineEpic{open, keptTestEpic(2, now.Add(-time.Hour)), keptTestEpic(3, now.Add(-240*time.Hour))}
	id := 10
	ask := func(first, partial bool, read ...int) {
		t.Helper()
		id++
		r := wire.WorkTimelineRequest{Project: "p1", From: wire.DateOf(now.Add(-24 * time.Hour)), To: wire.DateOf(now)}
		if first {
			r.First = new(true)
		}
		var tl wire.WorkTimeline
		mustUnmarshal(t, c.call(t, id, "work/timeline", r), &tl)
		gh.mu.Lock()
		defer gh.mu.Unlock()
		if len(tl.Errors) > 0 || (tl.Partial != nil) != partial || len(tl.Epics) != 2 || !slices.Equal(gh.timelineRead, read) {
			t.Fatalf("read %v, want %v: %+v", gh.timelineRead, read, tl)
		}
	}
	ask(true, true, 1, 2)
	ask(false, false, 1, 2) // the rest: epic 3
	d.forgeWrote()          // the minute's copy goes, the kept epics stay
	ask(true, false, 1)
	if gh.timelineRests != 1 {
		t.Fatalf("%d rests", gh.timelineRests)
	}
	d.forgeWrote()
	gh.mu.Lock()
	gh.timeline.Epics[2].UpdatedAt = now.UTC().Format(time.RFC3339)
	gh.mu.Unlock()
	ask(false, false, 1, 3)
	must(t, file.Put(connections.Connection{Provider: connections.GitHub, Login: "sam", Access: "ghu_secret",
		AccessExpiresAt: now.Add(time.Hour), ConnectedAt: now}))
	ask(false, false, 1, 2, 3)

	s, err := d.connectionStore()
	must(t, err)
	must(t, s.Delete(connections.GitHub))
	d.keptEpics.mu.Lock()
	defer d.keptEpics.mu.Unlock()
	if d.keptEpics.bytes != 0 || len(d.keptEpics.repos) != 0 {
		t.Fatalf("kept after the connection went: %+v", d.keptEpics.repos)
	}
}

// The kept epics' bounds: their age, GitHub's list, the 30 days, the repo, the count and the size.
func TestTimelineKeepBounds(t *testing.T) {
	t.Parallel()

	now := time.Now()
	k := &timelineKeep{}
	k.use("a")
	e, old := keptTestEpic(1, now), keptTestEpic(2, now.Add(-31*24*time.Hour))
	has := func(conn string, e githubmcp.TimelineEpic, at time.Time) bool {
		_, ok := k.get(conn, "r", e.Number, e.UpdatedAt, at)
		return ok
	}
	k.put("a", "r", []githubmcp.TimelineEpic{e, old}, now)
	if !has("a", e, now) || !has("a", old, now) || has("b", e, now) || has("a", e, now.Add(timelineKeptFor)) {
		t.Fatal("a kept epic is its connection's, for a day")
	}
	k.trim("a", "r", map[int]bool{1: true, 2: true}, now)
	if !has("a", e, now) || has("a", old, now) {
		t.Fatal("an epic closed over 30 days ago stays")
	}
	k.trim("a", "r", nil, now)
	if has("a", e, now) || k.bytes != 0 {
		t.Fatal("an epic that is not listed stays")
	}
	k.put("b", "r", []githubmcp.TimelineEpic{e}, now)
	if k.bytes != 0 {
		t.Fatal("a read of a connection that went is kept")
	}
	for n := range timelineKeptEpics + 5 {
		k.put("a", "r", []githubmcp.TimelineEpic{keptTestEpic(n+1, now.Add(time.Duration(n)*time.Second))}, now)
	}
	if len(k.repos["r"]) != timelineKeptEpics || k.repos["r"][5].size != 0 || k.repos["r"][6].size == 0 {
		t.Fatalf("%d kept", len(k.repos["r"]))
	}
	k.only([]string{"r"})
	if len(k.repos["r"]) == 0 {
		t.Fatal("a repo that stays is forgotten")
	}
	k.only(nil)
	if len(k.repos) != 0 || k.bytes != 0 {
		t.Fatalf("%d bytes kept of a repo that went", k.bytes)
	}
	for n := range 5 {
		big := keptTestEpic(n+1, now.Add(time.Duration(n)*time.Second))
		big.Title = strings.Repeat("x", timelineKeptBytes/4)
		k.put("a", "r", []githubmcp.TimelineEpic{big}, now)
	}
	if k.bytes > timelineKeptBytes || len(k.repos["r"]) != 3 {
		t.Fatalf("%d epics, %d bytes", len(k.repos["r"]), k.bytes)
	}
}
