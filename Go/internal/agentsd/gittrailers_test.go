package agentsd

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// git/trailers (#6371): the block for a session in a folder, by the effective commitTrailers
// rule, with the PR's totals, its epic from lineage, and its approvers from the forge. It has
// no Co-authored-by: that is the runtime's own (#7174).
//
// Not parallel: it replaces trailerItem.
func TestGitTrailersWritesTheBlockByTheRule(t *testing.T) {
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	d.repoMu.Lock()
	d.repos = map[string]forgeRepo{dir: {"github", "alexec/agents"}}
	d.repoMu.Unlock()
	repo := forgeRepo{"github", "alexec/agents"}
	issue, epic, pr := repo.issue(7), repo.issue(5), repo.pr(10)

	model := wire.ConfigOption{ID: "model", Name: "Model", Type: "select", Category: ptr("model"),
		CurrentValue: wire.JSONValue(`"opus"`), Options: wire.JSONValue(`[{"value":"opus","name":"Opus 5.5","description":""}]`)}
	a := seeded(t, d, dir, func(a *wire.Agent) {
		a.Labels = []wire.SessionLabel{{Value: "#7", Owner: wire.SessionLabelOwnerAgent}, {Value: "lane", Owner: wire.SessionLabelOwnerAgent}}
		a.AdvertisedOptions = []wire.ConfigOption{model}
	})

	d.workMu.Lock()
	f := d.costLedgerLocked()
	f.Nodes[costKey(pr)] = wire.LineageCostNode{Node: pr, Fixes: []string{issue.Ref},
		Banked: wire.LineageCost{Cost: map[string]float64{"USD": 2.5}, InputTokens: 400_000, OutputTokens: 30_000, WorkingSeconds: 2400}}
	f.Nodes[costKey(issue)] = wire.LineageCostNode{Node: issue,
		Banked: wire.LineageCost{Cost: map[string]float64{"USD": 0.5}, InputTokens: 12_000, OutputTokens: 8_000, WorkingSeconds: 420}}
	d.workMu.Unlock()
	d.lineageDo(func() { d.link(issue, epic, lineage.RelPartOf, lineage.SourceForge) })
	d.lineageSettle(5 * time.Second)

	var askedMu sync.Mutex
	asked := []wire.LineageNode{}
	was := trailerItem
	trailerItem = func(_ context.Context, _ *Daemon, kind string, n wire.LineageNode) (wire.ForgeItemDetail, error) {
		askedMu.Lock()
		defer askedMu.Unlock()
		asked = append(asked, n)
		return wire.ForgeItemDetail{Reviewers: []wire.ForgeReviewer{
			{Login: "alexec", State: wire.ForgeReviewerStateApproved}, {Login: "bob", State: wire.ForgeReviewerStateCommented}}}, nil
	}
	defer func() { trailerItem = was }()

	lines := func(id int, params map[string]any) []string {
		t.Helper()
		var got wire.GitTrailers
		if err := json.Unmarshal(c.call(t, id, "git/trailers", params), &got); err != nil {
			t.Fatal(err)
		}
		return got.Lines
	}

	// The defaults: Cost off, every other trailer on; Requested-by has no value.
	got := lines(2, map[string]any{"session": a.ID, "pr": 10})
	want := []string{
		"Managed-by: Agent Loops <noreply@agentloops.dev>",
		"Runtime: claude-code",
		"Model: opus-5-5",
		"Session: " + string(a.ID),
		"Fixes: #7",
		"Epic: #5",
		"Approved-by: alexec",
		"Tokens: in=412.0k out=38.0k",
		"Duration: 47m",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if !slices.Equal(asked, []wire.LineageNode{pr}) {
		t.Fatalf("the forge was asked for %v, not only the PR (the epic is lineage's)", asked)
	}

	// The folder turns Cost on and Model off, and has an old layer's Co-authored-by, which has
	// no effect; the person turns Tokens off, which is tighter for a spend trailer, so it wins
	// over the default.
	c.call(t, 3, "policy/setFolder", map[string]any{"folder": store.FolderURL(dir),
		"rules": map[string]any{"commitTrailers": map[string]string{"Cost": "on", "Model": "off", "Co-authored-by": "off"}}})
	c.call(t, 4, "policy/setYours", wire.SetYourPolicyRequest{Rules: wire.PolicyRules{
		CommitTrailers: wire.CommitTrailers{"Tokens": wire.CommitTrailerValueOff}}, Revision: 1})
	got = lines(5, map[string]any{"session": a.ID, "pr": 10})
	want = []string{
		"Managed-by: Agent Loops <noreply@agentloops.dev>",
		"Runtime: claude-code",
		"Session: " + string(a.ID),
		"Fixes: #7",
		"Epic: #5",
		"Approved-by: alexec",
		"Cost: $3.00",
		"Duration: 47m",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}

	// With no PR: Refs, not Fixes; no Approved-by; the totals are the session's issue's.
	got = lines(6, map[string]any{"session": a.ID})
	want = []string{
		"Managed-by: Agent Loops <noreply@agentloops.dev>",
		"Runtime: claude-code",
		"Session: " + string(a.ID),
		"Refs: #7",
		"Epic: #5",
		"Cost: $0.50",
		"Duration: 7m",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// Epic with no part-of link in lineage (#6530): the resolve cache's parent, from the forge's
// plan, with no forge read of the issue; with no parent known either, the read of the issue
// and the read of the PR run at the same time, so a commit waits for one, not both.
//
// Not parallel: it replaces trailerItem.
func TestGitTrailersEpicReadsTheForgeOnceAtMostAndAtTheSameTime(t *testing.T) {
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	dir := folder(t, c, 1)
	d.repoMu.Lock()
	d.repos = map[string]forgeRepo{dir: {"github", "alexec/agents"}}
	d.repoMu.Unlock()
	repo := forgeRepo{"github", "alexec/agents"}
	issue, epic := repo.issue(7), repo.issue(5)
	a := seeded(t, d, dir, func(a *wire.Agent) {
		a.Labels = []wire.SessionLabel{{Value: "#7", Owner: wire.SessionLabelOwnerAgent}}
	})

	var mu sync.Mutex
	asked := []string{}
	both := make(chan struct{})
	was := trailerItem
	trailerItem = func(ctx context.Context, _ *Daemon, kind string, n wire.LineageNode) (wire.ForgeItemDetail, error) {
		mu.Lock()
		asked = append(asked, kind)
		if len(asked) == 2 {
			close(both)
		}
		mu.Unlock()
		if kind == lineage.KindIssue {
			// The issue's read waits for the PR's: it ends only when both run at once.
			select {
			case <-both:
			case <-ctx.Done():
				return wire.ForgeItemDetail{}, ctx.Err()
			}
			p := repo.issue(6)
			return wire.ForgeItemDetail{Item: wire.ForgeItem{Parent: &p}}, nil
		}
		return wire.ForgeItemDetail{Reviewers: []wire.ForgeReviewer{{Login: "alexec", State: wire.ForgeReviewerStateApproved}}}, nil
	}
	defer func() { trailerItem = was }()

	lines := func(id int, params map[string]any) []string {
		t.Helper()
		var got wire.GitTrailers
		if err := json.Unmarshal(c.call(t, id, "git/trailers", params), &got); err != nil {
			t.Fatal(err)
		}
		return got.Lines
	}
	epicLine := func(got []string) string {
		for _, l := range got {
			if strings.HasPrefix(l, "Epic: ") {
				return l
			}
		}
		return ""
	}

	// No parent known: the issue and the PR are read at the same time.
	got := lines(2, map[string]any{"session": a.ID, "pr": 10})
	if epicLine(got) != "Epic: #6" || !slices.Contains(got, "Approved-by: alexec") {
		t.Fatalf("%q", got)
	}
	slices.Sort(asked)
	if !slices.Equal(asked, []string{lineage.KindIssue, lineage.KindPR}) {
		t.Fatalf("the forge was asked for %v", asked)
	}

	// The forge's plan said the parent: no read of the issue.
	d.resolvedFromForgeLocked([]wire.ForgeItem{{Node: issue, Title: "t", State: "open", Parent: &epic}})
	asked = nil
	if got := lines(3, map[string]any{"session": a.ID}); epicLine(got) != "Epic: #5" {
		t.Fatalf("%q", got)
	}
	if len(asked) != 0 {
		t.Fatalf("the forge was asked for %v, with the parent in the resolve cache", asked)
	}
}

// A session that is not there is said, as for every other session method.
func TestGitTrailersForNoSession(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	if err := c.callError(t, 1, "git/trailers", map[string]any{"session": store.NewUUID()}); err == nil || err.Code != CodeNoSuchAgent {
		t.Fatalf("%+v", err)
	}
}

// Claude Code's own commit attribution is the person's and the project's (#7174): a session's
// settings leave it alone, so Claude writes its own Co-authored-by, or none when they turn it
// off.
func TestClaudeSessionsKeepTheirOwnAttribution(t *testing.T) {
	t.Parallel()

	b, _ := json.Marshal(sessionMeta("claude"))
	if strings.Contains(string(b), "attribution") || strings.Contains(string(b), "includeCoAuthoredBy") || strings.Contains(string(b), "settings") {
		t.Fatalf("the session's settings change Claude's attribution: %s", b)
	}
}

// Runtime is the runtime's name and Model the model's id: Claude's without its claude- prefix,
// another runtime's as it is, and none when the model is not known.
func TestTrailerRuntimeAndModel(t *testing.T) {
	t.Parallel()

	opt := func(v string) []wire.ConfigOption {
		return []wire.ConfigOption{{ID: "model", Name: "Model", Type: "select", Category: ptr("model"), CurrentValue: wire.JSONValue(`"` + v + `"`)}}
	}
	for _, c := range []struct {
		a             wire.Agent
		runtime, model string
	}{
		{wire.Agent{RuntimeID: "claude", AdvertisedOptions: opt("claude-sonnet-5-5")}, "claude-code", "sonnet-5-5"},
		{wire.Agent{RuntimeID: "codex", AdvertisedOptions: opt("gpt-6-luna")}, "codex", "gpt-6-luna"},
		{wire.Agent{RuntimeID: "opencode"}, "opencode", ""},
	} {
		if got := trailerRuntimeName(&c.a); got != c.runtime {
			t.Errorf("%s: Runtime %q, not %q", c.a.RuntimeID, got, c.runtime)
		}
		if got := modelID(&c.a); got != c.model {
			t.Errorf("%s: Model %q, not %q", c.a.RuntimeID, got, c.model)
		}
	}
}
