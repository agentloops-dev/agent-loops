package agentsd

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/internal/policylayers"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// git/trailers (#6365, #6371): the trailer block of a commit by a session's agent, or of its
// pull request's squash message, by the effective commitTrailers rule of the session's folder
// (policylayers.TrailersOn). The worktree's commit-msg hook and the agents' tool
// commit_trailers ask it; an agent types no trailer by hand.
//
//   - Managed-by is Agent Loops, the app that ran the work (#7174). It does not say that the
//     app wrote the code: the runtime's own Co-authored-by says who did.
//   - Runtime is the runtime's name (claude-code, codex, opencode) and Model the model's id,
//     without Claude's claude- prefix (opus-5-5); another model's id stays as it is.
//   - Cost, Tokens and Duration are the totals of the sessions labelled with the PR (P#<n>) or
//     its issues, from the cost on the link (lineagecost.go): the PR, the issues it fixes, and
//     the session's own #n. With no PR, the session's #n issues; with none, the session itself.
//   - Session is the session's id. Not its web address (#/h/<host>/p/<folder>/s/<id>), which
//     would put the Mac's hardware id and the person's home folder in public history.
//   - Fixes is one line per issue the session is labelled with, and the issues the PR fixes;
//     with no PR it is Refs, as a commit on a branch closes nothing.
//   - Epic is the first issue's parent, from lineage's part-of link (the forge's answer, kept
//     an hour), else the resolve cache's parent (the forge's plan, #3562), else one read of
//     the issue from the forge cache. Approved-by is each login whose newest review of the PR
//     approves it, one read of the PR. At most two forge calls, at the same time (#6530).
//   - Requested-by is the login the person's forge connection has.
//   - A trailer with no value is left out.
//   - Co-authored-by is the runtime's, never ours (#7174): the block has none, and the
//     runtime's own settings (the person's and the project's) decide whether it writes one.

// trailerForgeWait bounds each forge read: a commit waits on it.
const trailerForgeWait = 5 * time.Second

// managedBy is Managed-by's value: the app, with an address that takes no mail.
const managedBy = "Agent Loops <noreply@agentloops.dev>"

// trailerBlockOrder is the order of the block's lines: the records, then the spend (the
// Preview of the gate 2 wireframes).
var trailerBlockOrder = []string{"Managed-by", "Runtime", "Model", "Session", "Fixes", "Epic", "Requested-by",
	"Approved-by", "Cost", "Tokens", "Duration"}

// trailerItem reads an issue or a PR for the block, from the forge cache within its minute.
// Tests replace it. Set in init: its own value reaches back to it through the app tools
// (commit_trailers), which a package variable's initialiser may not.
var trailerItem func(ctx context.Context, d *Daemon, kind string, n wire.LineageNode) (wire.ForgeItemDetail, error)

func init() {
	trailerItem = func(ctx context.Context, d *Daemon, kind string, n wire.LineageNode) (wire.ForgeItemDetail, error) {
		return d.forgeItem(ctx, kind, wire.ForgeItemRequest{Node: n})
	}
}

func (d *Daemon) registerGitTrailers() {
	On(d, wire.CallGitTrailers, func(ctx context.Context, _ *Client, r wire.GitTrailersRequest) (wire.GitTrailers, error) {
		return d.gitTrailers(ctx, r)
	})
}

func (d *Daemon) gitTrailers(ctx context.Context, r wire.GitTrailersRequest) (wire.GitTrailers, error) {
	a := d.Store.Agent(r.Session)
	if a == nil {
		return wire.GitTrailers{}, &Error{Code: CodeNoSuchAgent, Message: "git/trailers: no session " + string(r.Session)}
	}
	if r.Pr != nil && *r.Pr <= 0 {
		return wire.GitTrailers{}, Invalid("git/trailers: pr %d is not a pull request's number", *r.Pr)
	}
	folder := folderPath(a)
	on := policylayers.TrailersOn(d.effective(folder))
	values := d.trailerValues(ctx, a, folder, r.Pr, on)
	lines := []string{}
	for _, key := range trailerBlockOrder {
		if !on[key] {
			continue
		}
		for _, v := range values[key] {
			name := key
			if key == "Fixes" && r.Pr == nil {
				name = "Refs"
			}
			lines = append(lines, name+": "+v)
		}
	}
	return wire.GitTrailers{Lines: lines}, nil
}

// trailerValues is each trailer's values for a, by key; a trailer the rule turns off is not
// worked out, so it costs no forge call.
func (d *Daemon) trailerValues(ctx context.Context, a *wire.Agent, folder string, pr *int64, on map[string]bool) map[string][]string {
	out := map[string][]string{}
	add := func(key, v string) {
		if v != "" {
			out[key] = append(out[key], v)
		}
	}
	repo := d.folderRepo(folder)
	var prNode *wire.LineageNode
	if pr != nil && repo.forge != "" {
		n := repo.pr(int(*pr))
		prNode = &n
	}
	issues := d.sessionIssues(a, folder)
	if prNode != nil {
		issues = appendNew(issues, d.prFixes(*prNode)...)
	}

	if on["Managed-by"] {
		add("Managed-by", managedBy)
	}
	if on["Runtime"] {
		add("Runtime", trailerRuntimeName(a))
	}
	if on["Model"] {
		add("Model", modelID(a))
	}
	if on["Session"] {
		add("Session", string(a.ID))
	}
	// Each ref as the repo's own page names it: #7, or owner/repo#7 in another repo.
	ref := repo.issue(0).Ref
	if prNode != nil {
		ref = prNode.Ref
	}
	if on["Fixes"] {
		for _, n := range issues {
			add("Fixes", shortRef(n.Ref, ref))
		}
	}
	// The two forge reads (Epic's issue, Approved-by's PR) run at the same time: a commit
	// waits for the slower, not the sum (#6530).
	var epic wire.LineageNode
	var hasEpic bool
	var approvers []string
	var wg sync.WaitGroup
	if on["Epic"] && len(issues) > 0 {
		wg.Go(func() { epic, hasEpic = d.issueEpic(ctx, issues[0]) })
	}
	if on["Approved-by"] && prNode != nil {
		wg.Go(func() { approvers = d.prApprovers(ctx, *prNode) })
	}
	wg.Wait()
	if hasEpic {
		add("Epic", shortRef(epic.Ref, issues[0].Ref))
	}
	if on["Requested-by"] {
		if login := d.forgeViewer(repo.forge); login != nil {
			add("Requested-by", *login)
		}
	}
	for _, login := range approvers {
		add("Approved-by", login)
	}
	if on["Cost"] || on["Tokens"] || on["Duration"] {
		for key, v := range spendTrailers(d.trailerTotals(a, prNode, issues)) {
			add(key, v)
		}
	}
	return out
}

// spendTrailers is the Cost, Tokens and Duration of totals c, each absent when it is nothing.
func spendTrailers(c wire.LineageCost) map[string]string {
	out := map[string]string{}
	if sumCost(c.Cost) > 0 {
		out["Cost"] = amountsWords(c.Cost)
	}
	if c.InputTokens > 0 || c.OutputTokens > 0 {
		out["Tokens"] = "in=" + tokenWords(c.InputTokens) + " out=" + tokenWords(c.OutputTokens)
	}
	if c.WorkingSeconds >= 1 {
		out["Duration"] = durationWords(c.WorkingSeconds)
	}
	return out
}

// sessionIssues is the issues a's #n labels name in its folder's repo, in the labels' order.
func (d *Daemon) sessionIssues(a *wire.Agent, folder string) []wire.LineageNode {
	var out []wire.LineageNode
	for _, l := range a.Labels {
		if !issueLabel.MatchString(l.Value) {
			continue
		}
		if n, ok := d.labelNode(folder, l.Value); ok && n.Kind == lineage.KindIssue {
			out = appendNew(out, n)
		}
	}
	return out
}

// prFixes is the issues the cost ledger says pr fixes.
func (d *Daemon) prFixes(pr wire.LineageNode) []wire.LineageNode {
	d.workMu.Lock()
	defer d.workMu.Unlock()
	var out []wire.LineageNode
	for _, ref := range d.costLedgerLocked().Nodes[costKey(pr)].Fixes {
		out = append(out, wire.LineageNode{Kind: lineage.KindIssue, Ref: ref})
	}
	return out
}

// trailerTotals is the cost, tokens and time banked on the PR and issues, or, with neither,
// on the session itself.
func (d *Daemon) trailerTotals(a *wire.Agent, pr *wire.LineageNode, issues []wire.LineageNode) wire.LineageCost {
	nodes := append([]wire.LineageNode{}, issues...)
	if pr != nil {
		nodes = append([]wire.LineageNode{*pr}, nodes...)
	}
	if len(nodes) == 0 {
		nodes = []wire.LineageNode{d.sessionNode(a.ID)}
	}
	total := blankLineageCost()
	d.workMu.Lock()
	defer d.workMu.Unlock()
	f := d.costLedgerLocked()
	for _, n := range nodes {
		lineage.AddCost(&total, f.Nodes[costKey(n)].Banked)
	}
	return total
}

// issueEpic is the issue issue is a sub-issue of: lineage's active part-of link, out of all
// the issue's links (#6530), else the resolve cache's parent, from the forge's plan (#3562),
// else the forge's read of the issue.
func (d *Daemon) issueEpic(ctx context.Context, issue wire.LineageNode) (wire.LineageNode, bool) {
	for _, l := range d.lineage().ActiveFrom(issue, lineage.RelPartOf) {
		if l.To.Kind == lineage.KindIssue {
			return l.To, true
		}
	}
	if p := d.resolvedParent(issue); p != nil {
		return *p, true
	}
	ctx, cancel := context.WithTimeout(ctx, trailerForgeWait)
	defer cancel()
	item, err := trailerItem(ctx, d, lineage.KindIssue, issue)
	if err != nil || item.Item.Parent == nil {
		return wire.LineageNode{}, false
	}
	return *item.Item.Parent, true
}

// resolvedParent is issue's parent as the resolve cache last had it from the forge's plan,
// or nil when it has none.
func (d *Daemon) resolvedParent(issue wire.LineageNode) *wire.LineageNode {
	d.resolveMu.Lock()
	defer d.resolveMu.Unlock()
	if e, ok := d.resolveCache[issue]; ok && e.parent != nil && e.parent.Kind == lineage.KindIssue {
		p := *e.parent
		return &p
	}
	return nil
}

// prApprovers is each login whose newest review of pr approves it.
func (d *Daemon) prApprovers(ctx context.Context, pr wire.LineageNode) []string {
	ctx, cancel := context.WithTimeout(ctx, trailerForgeWait)
	defer cancel()
	item, err := trailerItem(ctx, d, lineage.KindPR, pr)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range item.Reviewers {
		if r.State == wire.ForgeReviewerStateApproved && r.Login != "" {
			out = append(out, r.Login)
		}
	}
	return out
}

// trailerRuntime is a runtime's name in Runtime, where it is not its id: the product's own
// name, as the kernel's Assisted-by takes it.
var trailerRuntime = map[string]string{"claude": "claude-code"}

// trailerRuntimeName is a's runtime as Runtime names it: claude-code, codex, opencode; "" when it
// is not known.
func trailerRuntimeName(a *wire.Agent) string {
	if name, ok := trailerRuntime[a.RuntimeID]; ok {
		return name
	}
	return a.RuntimeID
}

// modelID is a's model as Model names it: the runtime's full id of the model (an alias such
// as "opus" is read through the runtime's choices), without Claude's claude- prefix, as
// opus-5-5. Another runtime's id keeps its prefix (gpt-6-luna), which says whose model it is.
// "" when the model is not known.
func modelID(a *wire.Agent) string {
	model := agentModel(a)
	if model == "" {
		return ""
	}
	if names := modelNames(a.RuntimeID, model, runtimeModelChoices(a.RuntimeID, a.AdvertisedOptions)); len(names) > 0 {
		model = names[len(names)-1]
	}
	return strings.TrimPrefix(bareModel(model), "claude-")
}

func sumCost(m map[string]float64) float64 {
	t := 0.0
	for _, v := range m {
		t += v
	}
	return t
}

// appendNew appends each of add not in list.
func appendNew(list []wire.LineageNode, add ...wire.LineageNode) []wire.LineageNode {
	for _, n := range add {
		if !slices.Contains(list, n) {
			list = append(list, n)
		}
	}
	return list
}
