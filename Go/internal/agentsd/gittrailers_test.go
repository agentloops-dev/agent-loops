package agentsd

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// git/trailers (#6371): the block for a session in a folder, by the effective commitTrailers
// rule, with the PR's totals, its epic from lineage, its approvers from the forge, and Claude's
// own Co-authored-by last.
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

	asked := []wire.LineageNode{}
	was := trailerItem
	trailerItem = func(_ context.Context, _ *Daemon, kind string, n wire.LineageNode) (wire.ForgeItemDetail, error) {
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

	// The defaults: Cost off, every other trailer on; Requested-by and Attested have no value.
	got := lines(2, map[string]any{"session": a.ID, "pr": 10})
	want := []string{
		"Made-by: claude-code:claude-opus-5-5",
		"Session: " + string(a.ID),
		"Fixes: #7",
		"Epic: #5",
		"Approved-by: alexec",
		"Tokens: in=412.0k out=38.0k",
		"Duration: 47m",
		"Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if !slices.Equal(asked, []wire.LineageNode{pr}) {
		t.Fatalf("the forge was asked for %v, not only the PR (the epic is lineage's)", asked)
	}

	// The folder turns Cost on and Co-authored-by off; the person turns Tokens off, which is
	// tighter for a spend trailer, so it wins over the default.
	c.call(t, 3, "policy/setFolder", map[string]any{"folder": store.FolderURL(dir),
		"rules": map[string]any{"commitTrailers": map[string]string{"Cost": "on", "Co-authored-by": "off"}}})
	c.call(t, 4, "policy/setYours", wire.SetYourPolicyRequest{Rules: wire.PolicyRules{
		CommitTrailers: wire.CommitTrailers{"Tokens": wire.CommitTrailerValueOff}}, Revision: 1})
	got = lines(5, map[string]any{"session": a.ID, "pr": 10})
	want = []string{
		"Made-by: claude-code:claude-opus-5-5",
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
		"Made-by: claude-code:claude-opus-5-5",
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

// A session that is not there is said, as for every other session method.
func TestGitTrailersForNoSession(t *testing.T) {
	t.Parallel()

	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	if err := c.callError(t, 1, "git/trailers", map[string]any{"session": store.NewUUID()}); err == nil || err.Code != CodeNoSuchAgent {
		t.Fatalf("%+v", err)
	}
}

// Claude Code's own commit attribution is off in every session it starts (#6371), so the
// only Co-authored-by is the one git/trailers writes, when the rule keeps it.
func TestClaudeSessionsHaveNoAttributionOfTheirOwn(t *testing.T) {
	t.Parallel()

	b, _ := json.Marshal(sessionMeta("claude"))
	var meta struct {
		ClaudeCode struct {
			Options struct {
				ExtraArgs map[string]string `json:"extraArgs"`
			} `json:"options"`
		} `json:"claudeCode"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Attribution struct {
			Commit *string `json:"commit"`
		} `json:"attribution"`
		IncludeCoAuthoredBy *bool `json:"includeCoAuthoredBy"`
	}
	if err := json.Unmarshal([]byte(meta.ClaudeCode.Options.ExtraArgs["settings"]), &settings); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if settings.Attribution.Commit == nil || *settings.Attribution.Commit != "" || settings.IncludeCoAuthoredBy == nil || *settings.IncludeCoAuthoredBy {
		t.Fatalf("attribution left on: %s", b)
	}
}
