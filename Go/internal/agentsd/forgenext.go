package agentsd

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// forge/next (#5689, epic #5687): the work that is ready, in one order, with the reason for each
// place, so a lead and Work take the same next item. The order is one for every folder (Alex,
// 2026-11-15): on hold last, then priority, then size (Alex, 2026-11-24, #7034), then the oldest.
// A milestone's due date gives no place (#7033). A standing epic's plan is in the same order (nextCompare), so the two never disagree.

// nextMost is how many rows forge/next gives.
const nextMost = 100

func (d *Daemon) registerForgeNext() {
	On(d, wire.CallForgeNext, func(ctx context.Context, _ *Client, r wire.ForgeNextRequest) (wire.ForgeNext, error) {
		return d.forgeNext(ctx, r)
	})
}

// nextCompare is the one order (#5689, #7034): not on hold first; priority high, medium, none,
// low; size XS, S, M, L, XL, none last; the oldest first (createdAt, none last), then the lowest
// number, then the ref, so the order is total.
func nextCompare(a, b wire.ForgeItem) int {
	return cmp.Or(nextKeys(a, b), compareLast(a.CreatedAt, b.CreatedAt),
		cmp.Compare(refNumber(a.Node.Ref), refNumber(b.Node.Ref)), cmp.Compare(a.Node.Ref, b.Node.Ref))
}

// nextKeys compares on hold, priority and size: the keys before the age.
func nextKeys(a, b wire.ForgeItem) int {
	return cmp.Or(cmp.Compare(boolRank(onHold(a)), boolRank(onHold(b))),
		cmp.Compare(standingRank(a), standingRank(b)), cmp.Compare(sizeRank(a), sizeRank(b)))
}

// nextSizes are the sizes of the points rubric (#4532), the smallest first.
var nextSizes = []string{"XS", "S", "M", "L", "XL"}

// itemSize is the size of an item's size: label (#4532), in any case, as XS to XL: the larger of
// two, and "" for none.
func itemSize(i wire.ForgeItem) string {
	size := -1
	for _, l := range i.Labels {
		name, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(l.Name)), "size:")
		if !ok {
			continue
		}
		if n := slices.Index(nextSizes, strings.ToUpper(strings.TrimSpace(name))); n > size {
			size = n
		}
	}
	if size < 0 {
		return ""
	}
	return nextSizes[size]
}

// sizeRank is an item's size as a rank, XS first and no size last.
func sizeRank(i wire.ForgeItem) int {
	if n := slices.Index(nextSizes, itemSize(i)); n >= 0 {
		return n
	}
	return len(nextSizes)
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// compareLast compares two dates, the earliest first, with no date after every date.
func compareLast(a, b *wire.Date) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return cmp.Compare(*a, *b)
}

// nextOrder sorts items in the one order.
func nextOrder(items []wire.ForgeItem) { slices.SortStableFunc(items, nextCompare) }

// nextReasons are the short words for the keys that put i where it is; aged says its age
// put it above the next row, which ties with it on every key before the age.
func nextReasons(i wire.ForgeItem, aged bool) []string {
	var out []string
	if onHold(i) {
		out = append(out, "on hold")
	}
	if p := deref(i.Priority); p != "" {
		out = append(out, "priority "+p)
	} else {
		out = append(out, "no priority")
	}
	if size := itemSize(i); size != "" {
		out = append(out, "size "+size)
	} else {
		out = append(out, "no size")
	}
	if aged {
		out = append(out, "oldest")
	}
	return out
}

// forgeNext is forge/next: the folder's open issues (or open epics, or one epic's children) that
// are ready, and those on hold that are otherwise ready, in the one order.
func (d *Daemon) forgeNext(ctx context.Context, r wire.ForgeNextRequest) (wire.ForgeNext, error) {
	const method = "forge/next"
	level := strings.ToLower(strings.TrimSpace(deref(r.Level)))
	switch {
	case strings.TrimSpace(r.Folder) == "":
		return wire.ForgeNext{}, Invalid("%s: folder is required", method)
	case level != "" && level != "epic":
		return wire.ForgeNext{}, Invalid("%s: level is epic, or not said", method)
	case level == "epic" && r.Epic != nil:
		return wire.ForgeNext{}, Invalid("%s: level epic gives the open epics; it takes no epic", method)
	}
	if !slices.Contains(d.folderPaths(), r.Folder) {
		return wire.ForgeNext{}, Invalid("%s: %s is not a folder on this machine", method, r.Folder)
	}
	out := wire.ForgeNext{Rows: []wire.ForgeNextRow{}, ReadAt: wire.DateOf(nowFunc())}
	epics := level == "epic"
	taken := d.takenIssues()
	keep := func(i wire.ForgeItem) bool {
		if i.Node.Kind != lineage.KindIssue || i.State != "open" || len(i.Assignees) > 0 || taken[strings.ToLower(i.Node.Ref)] {
			return false
		}
		isEpic := deref(i.Level) == "epic" || hasLabel(i, "epic")
		return isEpic == epics && !d.prTakes(i.Node)
	}

	var items []wire.ForgeItem
	var err error
	if r.Epic != nil {
		items, err = d.forgeNextEpicChildren(ctx, method, *r.Epic, keep)
	} else {
		items, err = d.forgeNextFolderItems(ctx, method, r.Folder, epics, keep, &out)
	}
	if err != nil {
		return wire.ForgeNext{}, err
	}
	items = items[:min(len(items), nextMost)]
	for n, i := range items {
		aged := n+1 < len(items) && nextKeys(i, items[n+1]) == 0
		out.Rows = append(out.Rows, wire.ForgeNextRow{Item: i, Place: int64(n + 1), Reasons: nextReasons(i, aged),
			OnHold: onHold(i)})
	}
	return out, nil
}

// forgeNextEpicChildren is one epic's children that keep keeps and that have
// no open blocker: its plan, with their blockers, as forge/plan reads it. They are in the one order.
func (d *Daemon) forgeNextEpicChildren(ctx context.Context, method string, epic wire.LineageNode, keep func(wire.ForgeItem) bool) ([]wire.ForgeItem, error) {
	var items []wire.ForgeItem
	_, children, _, err := d.forgePlanRead(ctx, method, epic)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		if keep(c.item) && !slices.ContainsFunc(c.blockedBy, func(b planRef) bool { return b.open }) {
			items = append(items, c.item)
		}
	}
	nextOrder(items)
	return items, nil
}

// forgeNextFolderItems is the folder's open issues or epics that keep keeps, as many pages as
// a plan reads, then their blockers in order, a page at a time, only until the rows are full.
// It adds the errors of the forges to out.
func (d *Daemon) forgeNextFolderItems(ctx context.Context, method, folder string, epics bool, keep func(wire.ForgeItem) bool, out *wire.ForgeNext) ([]wire.ForgeItem, error) {
	all, err := d.forgeNextReadOpen(ctx, method, folder, epics, keep, out)
	if err != nil {
		return nil, err
	}
	nextOrder(all)
	var items []wire.ForgeItem
	for len(all) > 0 && len(items) < nextMost {
		page := all[:min(len(all), forgeListMost)]
		all = all[len(page):]
		blocked, err := d.openBlockers(ctx, method, page)
		if err != nil {
			return nil, err
		}
		for _, i := range page {
			if !blocked[i.Node] {
				items = append(items, i)
			}
		}
	}
	return items, nil
}

// forgeNextReadOpen reads the folder's open issues (or epics) that keep keeps, as many pages
// as a plan reads. It adds each new error of the forges to out.
func (d *Daemon) forgeNextReadOpen(ctx context.Context, method, folder string, epics bool, keep func(wire.ForgeItem) bool, out *wire.ForgeNext) ([]wire.ForgeItem, error) {
	q := wire.ForgeListRequest{Folder: &folder, State: new("open"), Limit: new(int64(forgeListMost))}
	if epics {
		q.Label = new("epic")
	}
	var all []wire.ForgeItem
	for range featureEpicPages {
		page, err := d.forgeList(ctx, false, q)
		if err != nil {
			return nil, Invalid("%s: %v", method, strings.TrimPrefix(err.Error(), "forge/issues: "))
		}
		for _, e := range page.Errors {
			if !slices.Contains(out.Errors, e) {
				out.Errors = append(out.Errors, e)
			}
		}
		for _, i := range page.Items {
			if keep(i) {
				all = append(all, i)
			}
		}
		if page.Cursor == nil {
			break
		}
		q.Cursor = page.Cursor
	}
	return all, nil
}

// takenIssues are the issues (by ref, in lower case) that a session on this machine that is not
// archived is labelled with: #n, a key, AB#n, or a P#n whose pull request fixes them.
func (d *Daemon) takenIssues() map[string]bool {
	out := map[string]bool{}
	for _, a := range d.Store.Agents() {
		if a.State == wire.AgentStateArchived {
			continue
		}
		for _, n := range d.labelIssues(a) {
			out[strings.ToLower(n.Ref)] = true
		}
	}
	return out
}

// prTakes says an open pull request fixes or closes the issue: lineage has its link from the PR's
// body, and the link ends when the PR closes without a merge.
func (d *Daemon) prTakes(issue wire.LineageNode) bool {
	return slices.ContainsFunc(d.lineage().Links(issue).Links, func(l wire.LineageLink) bool {
		return l.To == issue && l.From.Kind == lineage.KindPR && l.EndedAt == nil &&
			(l.Rel == lineage.RelFixes || l.Rel == lineage.RelCloses)
	})
}

// openBlockers says which of items have an open blocker: one read on each forge they are on, as
// a standing epic's children are read (#4200). A forge that keeps no blockers blocks none.
func (d *Daemon) openBlockers(ctx context.Context, method string, items []wire.ForgeItem) (map[wire.LineageNode]bool, error) {
	type group struct {
		f     Forge
		refs  []forgeRef
		nodes []wire.LineageNode
	}
	var names []string
	groups := map[string]*group{}
	for _, i := range items {
		f, ref, ok := d.forgeOwning(i.Node)
		if !ok {
			continue
		}
		name := f.Name()
		g := groups[name]
		if g == nil {
			g = &group{f: f}
			groups[name] = g
			names = append(names, name)
		}
		g.refs = append(g.refs, ref)
		g.nodes = append(g.nodes, i.Node)
	}
	out := map[wire.LineageNode]bool{}
	for _, name := range names {
		g := groups[name]
		by, err := g.f.Blockers(ctx, g.refs)
		if c := (*forgeCannot)(nil); errors.As(err, &c) {
			continue
		}
		if err != nil {
			return nil, forgeCannotErr(method, g.f, err)
		}
		for n, ref := range g.refs {
			if slices.ContainsFunc(by[ref], func(b planRef) bool { return b.open }) {
				out[g.nodes[n]] = true
			}
		}
	}
	return out, nil
}

// onHold says the item is on hold (#5747).
func onHold(i wire.ForgeItem) bool { return i.OnHold != nil && *i.OnHold }
