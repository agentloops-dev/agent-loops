package agentsd

import (
	"context"
	"slices"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// forge/priority and forge/hold (#6091, epic #6086): the item page's Priority menu and On hold
// toggle. Each forge writes its own field where it has one, the same map as the read (#5747,
// withPlanLabels): Linear's priority, Jira's priority field and a status named On hold, GitHub's
// Priority issue field, Bitbucket's priority and its state on hold, and Azure Boards' Priority
// field. Elsewhere the labels: priority:high, priority:medium or priority:low (the other two
// taken off), and on hold. They are a person's own clicks, as the other writes (forgewrite.go).

// The actions' names, as forgeCaps.Reasons and errForgeCannot name them.
const (
	actionPriority = "priority"
	actionHold     = "hold"
)

// planPriorities are the priorities forge/priority sets; "" is none.
var planPriorities = []string{"high", "medium", "low"}

// holdLabel is the label a forge with no hold field of its own is put on hold by.
const holdLabel = "on hold"

func (d *Daemon) registerForgePlanFields() {
	On(d, wire.CallForgePriority, func(ctx context.Context, _ *Client, r wire.ForgePriorityRequest) (wire.ForgeDone, error) {
		return d.forgePriority(ctx, r)
	})
	On(d, wire.CallForgeHold, func(ctx context.Context, _ *Client, r wire.ForgeHoldRequest) (wire.ForgeDone, error) {
		return d.forgeHold(ctx, r)
	})
}

func (d *Daemon) forgePriority(ctx context.Context, r wire.ForgePriorityRequest) (wire.ForgeDone, error) {
	const method = "forge/priority"
	p := ""
	if r.Priority != nil {
		p = *r.Priority
	}
	if p != "" && !slices.Contains(planPriorities, p) {
		return wire.ForgeDone{}, Invalid("%s: %q is not high, medium or low", method, p)
	}
	t, err := d.planFieldTarget(ctx, method, r.Node)
	if err != nil {
		return wire.ForgeDone{}, err
	}
	if err := t.forge.SetPriority(ctx, t.ref, p); err != nil {
		return wire.ForgeDone{}, forgeCannotErr(method, t.forge, err)
	}
	d.forgeWrote()
	return wire.ForgeDone{}, nil
}

func (d *Daemon) forgeHold(ctx context.Context, r wire.ForgeHoldRequest) (wire.ForgeDone, error) {
	const method = "forge/hold"
	t, err := d.planFieldTarget(ctx, method, r.Node)
	if err != nil {
		return wire.ForgeDone{}, err
	}
	if err := t.forge.SetHold(ctx, t.ref, r.On); err != nil {
		return wire.ForgeDone{}, forgeCannotErr(method, t.forge, err)
	}
	d.forgeWrote()
	return wire.ForgeDone{}, nil
}

// planFieldTarget is node's forge for a priority or a hold: an issue's only, as forge/next
// orders issues.
func (d *Daemon) planFieldTarget(ctx context.Context, method string, node wire.LineageNode) (forgeTarget, error) {
	if node.Kind != lineage.KindIssue {
		return forgeTarget{}, Invalid("%s: %s is not an issue", method, node.Ref)
	}
	return d.forgeTargetOf(ctx, method, node, false)
}

// isPriorityLabel says a label is priority:high, priority:medium or priority:low, in any case.
func isPriorityLabel(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return slices.ContainsFunc(planPriorities, func(p string) bool { return n == "priority:"+p })
}

// labelNamesOf are an item's labels, from f's own read of it.
func labelNamesOf(ctx context.Context, f Forge, r forgeRef) ([]string, error) {
	d, err := f.Item(ctx, r, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(d.Item.Labels))
	for _, l := range d.Item.Labels {
		out = append(out, l.Name)
	}
	return out, nil
}

// priorityLabelEdit is what makes have the label of priority p ("" for none) and no other
// priority label: the label to add, if any, and the ones to take off, by their own names.
func priorityLabelEdit(have []string, p string) (add, remove []string) {
	want := ""
	if p != "" {
		want = "priority:" + p
	}
	has := false
	for _, l := range have {
		switch {
		case want != "" && strings.EqualFold(strings.TrimSpace(l), want):
			has = true
		case isPriorityLabel(l):
			remove = append(remove, l)
		}
	}
	if want != "" && !has {
		add = []string{want}
	}
	return add, remove
}

// holdLabelEdit is what puts have on hold by label (label, where it has no hold label in any
// spelling), or takes every hold label off.
func holdLabelEdit(have []string, on bool, label string) (add, remove []string) {
	if on {
		if !slices.ContainsFunc(have, isHoldName) {
			add = []string{label}
		}
		return add, nil
	}
	for _, l := range have {
		if isHoldName(l) {
			remove = append(remove, l)
		}
	}
	return nil, remove
}

// setLabelsIf writes add and remove, when there is anything to write.
func setLabelsIf(ctx context.Context, f Forge, r forgeRef, add, remove []string) error {
	if len(add)+len(remove) == 0 {
		return nil
	}
	return f.SetLabels(ctx, r, add, remove)
}

// labelPriority sets an issue's priority by its labels, on a forge with no field for it.
func labelPriority(ctx context.Context, f Forge, r forgeRef, p string) error {
	have, err := labelNamesOf(ctx, f, r)
	if err != nil {
		return err
	}
	add, remove := priorityLabelEdit(have, p)
	return setLabelsIf(ctx, f, r, add, remove)
}

// labelHold puts an issue on hold by the label on hold, or takes it off, on a forge with no
// field for it.
func labelHold(ctx context.Context, f Forge, r forgeRef, on bool) error {
	have, err := labelNamesOf(ctx, f, r)
	if err != nil {
		return err
	}
	add, remove := holdLabelEdit(have, on, holdLabel)
	return setLabelsIf(ctx, f, r, add, remove)
}
