package agentsd

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Plans in Work (#3265, #3267): work has one hierarchy. A feature is the label feature:<name>; an
// epic is a parent issue, labelled epic; its children are stories, tasks and spikes (labels
// story, task and spike), in the order the forge's "blocked by" links say. forge/plan reads an
// epic's children with their blockers and puts them in steps. The plan is the forge's own sub-issues and links, never a file of ours, so it
// cannot go out of date, and other people and tools read it too.

// forgeLevels are the levels a label can say, as the app says them on every forge.
var forgeLevels = []string{"epic", "story", "task", "spike"}

// featurePrefix starts a feature's label.
const featurePrefix = "feature:"

// Features are read from the newest epics and open issues, at most featureEpicPages pages of
// forgeListMost of each.
const featureEpicPages = 5

func (d *Daemon) registerForgePlans() {
	On(d, wire.CallForgePlan, func(ctx context.Context, _ *Client, r wire.ForgePlanRequest) (wire.ForgePlan, error) {
		return d.forgePlan(ctx, r)
	})
	d.registerForgePlanWrites()
	d.registerForgeNext()
}

// withHierarchy sets an issue's level and feature from its labels, and its parent and progress.
func withHierarchy(i *wire.ForgeItem, parent *wire.LineageNode, total, done int) {
	for _, l := range i.Labels {
		name := strings.TrimSpace(l.Name)
		if level := strings.ToLower(name); i.Level == nil && slices.Contains(forgeLevels, level) {
			i.Level = &level
		}
		if f, ok := featureName(name); ok && i.Feature == nil {
			i.Feature = &f
		}
		if strings.EqualFold(name, standingLabel) {
			i.Standing = new(true)
		}
	}
	i.Parent = parent
	if total > 0 {
		i.Progress = &wire.ForgeProgress{Done: int64(done), Total: int64(total)}
	}
}

// forgeOwn says which of priority and on hold an item's forge has a field of its own for
// (#5747): the labels are not read for those, so the two cannot disagree.
type forgeOwn struct{ priority, hold bool }

// withPlanLabels sets what the forge has no field of its own for, by own, from the item's
// labels (#5688, #5747): its priority from priority:high, priority:medium and priority:low (any
// case; the higher of two), and on hold from the label on hold (any case; on-hold too).
func withPlanLabels(i *wire.ForgeItem, own forgeOwn) {
	if !own.priority && i.Priority == nil {
		for _, p := range []string{"high", "medium", "low"} {
			if hasLabel(*i, "priority:"+p) {
				i.Priority = &p
				break
			}
		}
	}
	if !own.hold && slices.ContainsFunc(i.Labels, func(l wire.ForgeLabel) bool { return isHoldName(l.Name) }) {
		i.OnHold = new(true)
	}
}

// isHoldName says a label or a status names on hold: in any case, with a space, - or _.
func isHoldName(name string) bool {
	return strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(strings.TrimSpace(name))) == "on hold"
}

// featureName is the feature a label names: feature:work is work.
func featureName(label string) (string, bool) {
	if len(label) <= len(featurePrefix) || !strings.EqualFold(label[:len(featurePrefix)], featurePrefix) {
		return "", false
	}
	name := strings.TrimSpace(label[len(featurePrefix):])
	return name, name != ""
}

// planChild is a child as a forge says it, with its blockers, before it has a step.
type planChild struct {
	item      wire.ForgeItem
	blockedBy []planRef
}

type planRef struct {
	node wire.LineageNode
	open bool
}

// forgePlan is an epic's plan: its children in steps, and its lead.
func (d *Daemon) forgePlan(ctx context.Context, r wire.ForgePlanRequest) (wire.ForgePlan, error) {
	epic, children, standing, err := d.forgePlanRead(ctx, "forge/plan", r.Node)
	if err != nil {
		return wire.ForgePlan{}, err
	}
	out := planOf(epic, children, standing != nil)
	out.Standing = standing
	// The lead is this machine's, read each time: it starts and ends more often than a plan changes.
	out.Lead = d.leadOf(r.Node)
	return out, nil
}

// forgePlanRead is an epic and its children with their blockers, as forge/plan reads them (from
// the forge cache within its minute), and a standing epic's query (nil for any other epic).
func (d *Daemon) forgePlanRead(ctx context.Context, method string, node wire.LineageNode) (wire.ForgeItem, []planChild, *wire.ForgeStanding, error) {
	if node.Kind != lineage.KindIssue {
		return wire.ForgeItem{}, nil, nil, Invalid("%s: the node's kind is %s, not issue", method, node.Kind)
	}
	key, _ := json.Marshal([]any{"forge/plan", node.Ref})
	if a, ok := d.forgeCached(string(key)); ok && a.plan != nil {
		return a.plan.epic, a.plan.children, a.plan.standing, nil
	}
	f, ref, ok := d.forgeOwning(node)
	if !ok {
		return wire.ForgeItem{}, nil, nil, Invalid("%s: %s is not an issue on GitHub, GitLab, Forgejo, Linear, Jira or Azure Boards", method, node.Ref)
	}
	a, err := f.Plan(ctx, ref)
	if err != nil {
		return wire.ForgeItem{}, nil, nil, forgeCannotErr(method, f, err)
	}
	epic, children := a.epic, a.children
	var standing *wire.ForgeStanding
	// A standing epic's plan is its query's issues, not its sub-issues (#3661), and it has
	// no "done of total".
	if epic.Standing != nil && *epic.Standing {
		var q wire.ForgeStanding
		if q, children, err = d.standingPlan(ctx, method, epic); err != nil {
			return wire.ForgeItem{}, nil, nil, err
		}
		standing, epic.Progress = &q, nil
	}
	d.forgeRemember(string(key), forgeAnswer{plan: &forgePlanAnswer{epic: epic, children: children, standing: standing}})
	items := []wire.ForgeItem{epic}
	for _, c := range children {
		items = append(items, c.item)
	}
	d.resolvedFromForge(items)
	return epic, children, standing, nil
}

// planOf puts children in steps: a child's step is one more than the last step of the children
// that block it, 1 for a child no other child blocks. A blocker outside the plan adds no step,
// but an open one is said. Children in a loop, and those after them, share the step after the
// rest, so a loop in the forge's links still shows every child. A standing epic's plan is one
// step (#3661, #4200): each child is in step 1, and each open blocker is said, in the plan or not.
func planOf(epic wire.ForgeItem, children []planChild, oneStep bool) wire.ForgePlan {
	in := map[wire.LineageNode]int{}
	for n, c := range children {
		if !oneStep {
			in[c.item.Node] = n
		}
	}
	step := planSteps(children, in)
	out := wire.ForgePlan{Item: epic, Children: []wire.ForgePlanChild{}, Steps: []wire.ForgePlanStep{}}
	for n, c := range children {
		child := wire.ForgePlanChild{Item: c.item, BlockedBy: []wire.LineageNode{}, Step: int64(step[n])}
		for _, b := range c.blockedBy {
			child.BlockedBy = append(child.BlockedBy, b.node)
			if _, ok := in[b.node]; !ok && b.open {
				child.BlockedOutside = append(child.BlockedOutside, b.node)
			}
		}
		out.Children = append(out.Children, child)
	}
	slices.SortStableFunc(out.Children, func(a, b wire.ForgePlanChild) int { return cmp.Compare(a.Step, b.Step) })
	for _, c := range out.Children {
		if len(out.Steps) == 0 || out.Steps[len(out.Steps)-1].Step != c.Step {
			out.Steps = append(out.Steps, wire.ForgePlanStep{Step: c.Step, Nodes: []wire.LineageNode{}})
		}
		s := &out.Steps[len(out.Steps)-1]
		s.Nodes = append(s.Nodes, c.Item.Node)
	}
	return out
}

// planSteps is each child's step in a plan: one more than the last step of the
// children in the plan (in) that block it. Children in a loop, and those after
// them, share the step after the rest.
func planSteps(children []planChild, in map[wire.LineageNode]int) []int {
	step := make([]int, len(children))
	for left := len(children); left > 0; {
		placed := 0
		for n, c := range children {
			if step[n] > 0 {
				continue
			}
			if s, ready := planStepOf(n, c, in, step); ready {
				step[n] = s
				placed++
			}
		}
		left -= placed
		if placed == 0 {
			last := slices.Max(append(slices.Clone(step), 0))
			for n := range step {
				if step[n] == 0 {
					step[n] = last + 1
				}
			}
			break
		}
	}
	return step
}

// planStepOf is the step of child n, c, from the steps of its blockers in the
// plan. ready is false while one of those blockers has no step yet.
func planStepOf(n int, c planChild, in map[wire.LineageNode]int, step []int) (int, bool) {
	s := 1
	for _, b := range c.blockedBy {
		if m, ok := in[b.node]; ok && m != n {
			if step[m] == 0 {
				return s, false
			}
			s = max(s, step[m]+1)
		}
	}
	return s, true
}

// leadOf is the session on this machine that leads the epic: it works on it (from lineage, which
// its #<n> or Linear key label makes) and is labelled lead. A live one before one that ended, and
// the newest of them.
func (d *Daemon) leadOf(epic wire.LineageNode) *wire.ForgeLead {
	var best *wire.Agent
	for _, l := range d.lineage().Links(epic).Links {
		if l.To != epic || l.From.Kind != lineage.KindSession || l.Rel != lineage.RelWorksOn || l.EndedAt != nil {
			continue
		}
		id, ok := lineage.SessionID(l.From.Ref)
		if !ok {
			continue
		}
		a := d.Store.Agent(wire.UUID(id))
		if a == nil || a.State == wire.AgentStateArchived || !slices.ContainsFunc(a.Labels, func(s wire.SessionLabel) bool { return s.Value == "lead" }) {
			continue
		}
		if best == nil || holdsRuntime(a.State) && !holdsRuntime(best.State) ||
			holdsRuntime(a.State) == holdsRuntime(best.State) && a.CreatedAt > best.CreatedAt {
			best = a
		}
	}
	if best == nil {
		return nil
	}
	return &wire.ForgeLead{Session: best.ID, Title: best.Title, State: best.State}
}
