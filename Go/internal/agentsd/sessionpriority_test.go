package agentsd

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// priorityDaemon is a daemon with a folder on github:alexec/agents whose issues' labels are
// labels's, as the forge says them when asked.
func priorityDaemon(t *testing.T, root string, labels map[int][]string) (*Daemon, *conn, string, *sync.Mutex) {
	t.Helper()
	d, c := start(t, root)
	folder := realTempDir(t)
	c.call(t, 900, "folders/add", map[string]any{"folder": "file://" + folder})
	// A session's folder as the store says it, which on a Mac is not /private/var's.
	stored := folderPath(&wire.Agent{Cwd: store.FolderURL(folder)})
	d.repos = map[string]forgeRepo{folder: {"github", "alexec/agents"}, stored: {"github", "alexec/agents"}}
	var mu sync.Mutex
	d.resolveMu.Lock()
	d.resolveLookup = func(_ context.Context, _ string, _ forgeRepo, _ string, n int) (wire.LineageResolved, error) {
		mu.Lock()
		defer mu.Unlock()
		return wire.LineageResolved{Title: "issue", Labels: labels[n]}, nil
	}
	d.resolveMu.Unlock()
	return d, c, folder, &mu
}

// settlePriorities waits for the links, the forge asks and the refreshes they set off.
func settlePriorities(d *Daemon) {
	for range 3 {
		d.lineageSettle(2 * time.Second)
		d.resolveSettle(2 * time.Second)
	}
}

func wantPriority(t *testing.T, d *Daemon, id wire.UUID, p string, source wire.SessionPrioritySource) {
	t.Helper()
	a := d.Store.Agent(id)
	if a == nil {
		t.Fatal("no session")
	}
	got, src := "", wire.SessionPrioritySourceNone
	if a.Priority != nil {
		got = string(*a.Priority)
	}
	if a.PrioritySource != nil {
		src = *a.PrioritySource
	}
	if got != p || src != source {
		t.Fatalf("priority %q from %q, want %q from %q", got, src, p, source)
	}
}

func labelled(values ...string) []wire.SessionLabel {
	var out []wire.SessionLabel
	for _, v := range values {
		out = append(out, wire.SessionLabel{Value: v, Owner: wire.SessionLabelOwnerAgent})
	}
	return out
}

// #5847: a session labelled with an issue goes by the issue's priority, and follows it when a
// forge read says another.
func TestSessionPriorityFromItsIssue(t *testing.T) {
	t.Parallel()

	d, _, folder, _ := priorityDaemon(t, t.TempDir(), map[int][]string{5846: {"epic", "priority:high"}, 12: {"priority:low"}})
	a := seeded(t, d, folder, func(a *wire.Agent) { a.Labels = labelled("#5846", "#12") })
	d.noteLineage(nil, a)
	settlePriorities(d)
	wantPriority(t, d, a.ID, "high", wire.SessionPrioritySourceIssue)

	// The next forge read says the label changed.
	low := "low"
	d.resolvedFromForge([]wire.ForgeItem{{Node: d.repos[folder].issue(5846), Title: "issue", State: "open", Priority: &low}})
	settlePriorities(d)
	wantPriority(t, d, a.ID, "low", wire.SessionPrioritySourceIssue)

	// Neither has one now: none.
	d.resolvedFromForge([]wire.ForgeItem{{Node: d.repos[folder].issue(5846), Title: "issue", State: "open"},
		{Node: d.repos[folder].issue(12), Title: "issue", State: "open"}})
	settlePriorities(d)
	wantPriority(t, d, a.ID, "", wire.SessionPrioritySourceNone)
}

// #5847: P#n goes by the priority of the issues its pull request fixes.
func TestSessionPriorityFromItsPullRequestsIssue(t *testing.T) {
	t.Parallel()

	d, _, folder, _ := priorityDaemon(t, t.TempDir(), map[int][]string{7: {"priority:medium"}})
	a := seeded(t, d, folder, func(a *wire.Agent) { a.Labels = labelled("P#8") })
	d.noteLineage(nil, a)
	repo := d.repos[folder]
	d.lineageDo(func() { d.link(repo.pr(8), repo.issue(7), lineage.RelFixes, lineage.SourcePRBody) })
	d.refreshPrioritiesSoon()
	settlePriorities(d)
	wantPriority(t, d, a.ID, "medium", wire.SessionPrioritySourceIssue)
}

// #5847: agents/setPriority wins over the issue's; cleared, the session follows its issue again.
func TestSessionPrioritySetWinsAndClears(t *testing.T) {
	t.Parallel()

	d, c, folder, _ := priorityDaemon(t, t.TempDir(), map[int][]string{5846: {"priority:high"}})
	a := seeded(t, d, folder, func(a *wire.Agent) { a.Labels = labelled("#5846") })
	d.noteLineage(nil, a)
	settlePriorities(d)
	var got wire.Agent
	if err := json.Unmarshal(c.call(t, 1, "agents/setPriority", map[string]any{"agentID": a.ID, "priority": "low"}), &got); err != nil {
		t.Fatal(err)
	}
	if got.Priority == nil || *got.Priority != wire.SessionPriorityLow || got.PrioritySet == nil || *got.PrioritySet != wire.SessionPriorityLow {
		t.Fatalf("set low: %+v", got)
	}
	settlePriorities(d)
	wantPriority(t, d, a.ID, "low", wire.SessionPrioritySourceSet)

	var cleared wire.Agent
	if err := json.Unmarshal(c.call(t, 2, "agents/setPriority", map[string]any{"agentID": a.ID, "priority": nil}), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.PrioritySet != nil {
		t.Fatalf("cleared: %+v", cleared)
	}
	wantPriority(t, d, a.ID, "high", wire.SessionPrioritySourceIssue)
}

// #5847: a lane its lead started, with no label yet, goes by the lead's priority, and follows it.
func TestSessionPriorityFromItsStarter(t *testing.T) {
	t.Parallel()

	d, _, folder, _ := priorityDaemon(t, t.TempDir(), nil)
	lead := seeded(t, d, folder, nil)
	if _, err := d.setPriority(lead.ID, new(wire.SessionPriorityHigh)); err != nil {
		t.Fatal(err)
	}
	lane := seeded(t, d, folder, func(a *wire.Agent) { a.StartedByAgent = &lead.ID })
	d.noteLineage(nil, lane)
	settlePriorities(d)
	wantPriority(t, d, lane.ID, "high", wire.SessionPrioritySourceStarter)

	if _, err := d.setPriority(lead.ID, new(wire.SessionPriorityLow)); err != nil {
		t.Fatal(err)
	}
	wantPriority(t, d, lane.ID, "low", wire.SessionPrioritySourceStarter)

	// Cleared, with no issue: none, for both.
	if _, err := d.setPriority(lead.ID, nil); err != nil {
		t.Fatal(err)
	}
	wantPriority(t, d, lead.ID, "", wire.SessionPrioritySourceNone)
	wantPriority(t, d, lane.ID, "", wire.SessionPrioritySourceNone)
}

// #5847: the set priority lasts a restart, and an issue's priority is kept until the forge is
// read again.
func TestSessionPriorityLastsARestart(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	d, c, folder, _ := priorityDaemon(t, root, map[int][]string{3: {"priority:high"}})
	set := seeded(t, d, folder, nil)
	c.call(t, 1, "agents/setPriority", map[string]any{"agentID": set.ID, "priority": "medium"})
	onIssue := seeded(t, d, folder, func(a *wire.Agent) { a.Labels = labelled("#3") })
	d.noteLineage(nil, onIssue)
	settlePriorities(d)
	wantPriority(t, d, onIssue.ID, "high", wire.SessionPrioritySourceIssue)
	d.Shutdown()

	again, _, _, _ := priorityDaemon(t, root, map[int][]string{3: {"priority:high"}})
	stored := folderPath(&wire.Agent{Cwd: store.FolderURL(folder)})
	again.repos = map[string]forgeRepo{folder: {"github", "alexec/agents"}, stored: {"github", "alexec/agents"}}
	wantPriority(t, again, set.ID, "medium", wire.SessionPrioritySourceSet)
	if a := again.Store.Agent(set.ID); a.PrioritySet == nil || *a.PrioritySet != wire.SessionPriorityMedium {
		t.Fatalf("prioritySet after the restart: %+v", a.PrioritySet)
	}
	// Before the forge answers, a refresh keeps the issue's.
	again.resolveMu.Lock()
	again.resolveLookup = func(context.Context, string, forgeRepo, string, int) (wire.LineageResolved, error) {
		return wire.LineageResolved{}, context.Canceled
	}
	again.resolveMu.Unlock()
	again.refreshPriorities(nil)
	wantPriority(t, again, onIssue.ID, "high", wire.SessionPrioritySourceIssue)
	settlePriorities(again)
	wantPriority(t, again, onIssue.ID, "high", wire.SessionPrioritySourceIssue)
}

// #5847: set_session_priority sets the caller's priority or a helper's, and issue clears it;
// start_session's priority is checked.
func TestSetSessionPriorityTool(t *testing.T) {
	t.Parallel()

	d, _, folder, _ := priorityDaemon(t, t.TempDir(), nil)
	me := seeded(t, d, folder, nil)
	helper := seeded(t, d, folder, func(a *wire.Agent) { a.StartedByAgent = &me.ID })
	other := seeded(t, d, folder, nil)
	args := func(kv ...string) map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]], _ = json.Marshal(kv[i+1])
		}
		return m
	}
	if out, err := d.setSessionPriority(me.ID, args("priority", "high")); err != nil || !strings.Contains(out, "high priority now, set on it") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := d.setSessionPriority(me.ID, args("priority", "Low", "id", strings.ToLower(string(helper.ID)))); err != nil || !strings.Contains(out, "low priority") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := d.setSessionPriority(me.ID, args("priority", "high", "id", string(other.ID))); err == nil {
		t.Fatal("set another's own session")
	}
	if _, err := d.setSessionPriority(me.ID, args("priority", "urgent")); err == nil {
		t.Fatal("took urgent")
	}
	if out, err := d.setSessionPriority(me.ID, args("priority", "issue")); err != nil || !strings.Contains(out, "no priority") {
		t.Fatalf("%q %v", out, err)
	}
	if p, err := startPriority(args("priority", "high")); err != nil || p == nil || *p != wire.SessionPriorityHigh {
		t.Fatalf("start high: %v %v", p, err)
	}
	if p, err := startPriority(args()); err != nil || p != nil {
		t.Fatalf("start none: %v %v", p, err)
	}
	if _, err := startPriority(args("priority", "urgent")); err == nil {
		t.Fatal("start took urgent")
	}
}

// #5847: a lookup's labels give the priority; with none, a forge field's priority stays, and a
// label's lost one goes.
func TestLookedUpPriority(t *testing.T) {
	t.Parallel()

	field := resolvedEntry{priority: "high"}
	label := resolvedEntry{priority: "high", resolved: wire.LineageResolved{Labels: []string{"priority:high"}}}
	for _, c := range []struct {
		had    resolvedEntry
		labels []string
		want   string
	}{
		{resolvedEntry{}, []string{"priority:Medium", "priority:low"}, "medium"},
		{field, nil, "high"},
		{label, nil, ""},
		{label, []string{"priority:low"}, "low"},
	} {
		if got := lookedUpPriority(c.had, c.labels); got != c.want {
			t.Errorf("%+v %v: %q, want %q", c.had, c.labels, got, c.want)
		}
	}
}
