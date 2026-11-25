package agentsd

import (
	"cmp"
	"encoding/json"
	"log"
	"maps"
	"slices"

	"github.com/agentloops-dev/agent-loops/Go/internal/boundmap"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The largest record of the stores of totals that writeStore writes (#7245). A record is a
// period's rows, or a lineage node. No total is lost: a row with no room goes on "other", and
// of a node only what can be read again is cut.

const (
	// periodRows and runtimeBytes bound a period by runtime (usage, time).
	periodRows   = 32
	runtimeBytes = 128
	// featureRows bounds a day by method; a method's clients are the five of features.go.
	featureRows = 2_000
	// reportRows bounds an hour by reportKey; reportRowsMost has the rows of "other" by runtime
	// and route too, which the allowances of an org account are counted from.
	reportRows     = 200
	reportRowsMost = 300
	reportKeyBytes = 512

	costNodeBytes = 64 << 10
	// Each list or word of a node, as JSON.
	costListBytes   = 12 << 10
	costTitleBytes  = 1 << 10
	costLabelsBytes = 8 << 10
	costWordBytes   = 1 << 10
	costNoticesKept = 1_000
)

// addRow adds v to the row of key in rows, or of "other" when there is no room for it.
func addRow[V any](rows map[string]V, key string, most int, v V, add func(V, V) V) {
	key = boundmap.Key(rows, key, most, runtimeBytes)
	rows[key] = add(rows[key], v)
}

// boundPeriods adds each period's rows up again within the bounds: a file from before them.
func boundPeriods[V any](periods map[string]map[string]V, most int, add func(V, V) V) {
	for at, rows := range periods {
		out := make(map[string]V, min(len(rows), most))
		for _, key := range slices.Sorted(maps.Keys(rows)) {
			addRow(out, key, most, rows[key], add)
		}
		periods[at] = out
	}
}

func boundUsage(u *usageFile) {
	boundPeriods(u.Hours, periodRows, sumUsage)
	boundPeriods(u.Days, periodRows, sumUsage)
}

func boundTime(h *wire.TimeHistory) {
	boundPeriods(h.Hours, periodRows, sumTime)
	boundPeriods(h.Days, periodRows, sumTime)
}

func sumTime(a, b wire.TimeBucket) wire.TimeBucket {
	bucketAdd(&a, b)
	return a
}

func boundSpend(s *spendFile) {
	for day, costs := range s.Days {
		out := map[string]float64{}
		for _, c := range slices.Sorted(maps.Keys(costs)) {
			boundmap.AddMoney(out, c, costs[c])
		}
		s.Days[day] = out
	}
}

func sumUses(a, b map[string]int64) map[string]int64 {
	out := maps.Clone(a)
	if out == nil {
		out = map[string]int64{}
	}
	for client, n := range b {
		out[client] += n
	}
	return out
}

// addReportRow adds to a row of an hour. With no room for it, its tokens and cost go on the row
// of its runtime and route with the folder "other", and past that on "other" alone.
func addReportRow(rows map[string]wire.UsageRollupRow, folder, runtime, workflow, route string, add wire.UsageRollupRow) {
	k := reportKey(folder, runtime, workflow, route)
	if !boundmap.Fits(rows, k, reportRows, reportKeyBytes) {
		folder, workflow = boundmap.Other, ""
		k = reportKey(folder, runtime, workflow, route)
	}
	if !boundmap.Fits(rows, k, reportRowsMost, reportKeyBytes) {
		runtime, route = boundmap.Other, ""
		k = reportKey(folder, runtime, workflow, route)
	}
	row := rows[k]
	row.Folder, row.Runtime = folder, runtime
	if workflow != "" {
		row.Workflow = &workflow
	}
	if route != "" {
		row.Route = &route
	}
	addRollupRow(&row, add)
	rows[k] = row
}

func boundReport(r *reportFile) {
	word := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	for hour, rows := range r.Hours {
		if len(rows) <= reportRowsMost && !slices.ContainsFunc(slices.Collect(maps.Keys(rows)), func(k string) bool { return len(k) > reportKeyBytes }) {
			for k, row := range rows {
				cost := make(map[string]float64, len(row.Cost))
				for _, c := range slices.Sorted(maps.Keys(row.Cost)) {
					boundmap.AddMoney(cost, c, row.Cost[c])
				}
				row.Cost = cost
				rows[k] = row
			}
			continue
		}
		out := make(map[string]wire.UsageRollupRow, min(len(rows), reportRowsMost))
		for _, k := range slices.Sorted(maps.Keys(rows)) {
			row := rows[k]
			addReportRow(out, row.Folder, row.Runtime, word(row.Workflow), word(row.Route), row)
		}
		r.Hours[hour] = out
	}
}

// firstWithin is the first of list that are at most most bytes as JSON.
func firstWithin(list []string, most int) []string {
	size := 2
	for i, s := range list {
		if size += jsonSize(s) + 1; size > most {
			return list[:i:i]
		}
	}
	return list
}

func jsonSize(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// boundCostNode is a node at its largest. Each part has its own bound, so a part that is read
// again is cut to the same. What is banked is never cut.
func boundCostNode(c wire.LineageCostNode) wire.LineageCostNode {
	c.Fixes = firstWithin(c.Fixes, costListBytes)
	c.PartOf = firstWithin(c.PartOf, costListBytes)
	c.Sessions = firstWithin(c.Sessions, costListBytes)
	c.Files = firstWithin(c.Files, costListBytes)
	for _, word := range []**string{&c.Level, &c.Feature, &c.Milestone, &c.Parent, &c.Reverts, &c.Repo, &c.Outcome} {
		if *word != nil && jsonSize(**word) > costWordBytes {
			*word = nil
		}
	}
	if c.Resolved != nil {
		r := *c.Resolved
		r.Title = cutJSON(r.Title, costTitleBytes)
		r.Labels = firstWithin(r.Labels, costLabelsBytes)
		if r.State != nil && jsonSize(*r.State) > costWordBytes || r.Deploy != nil && jsonSize(r.Deploy) > costWordBytes {
			r.State, r.Deploy = nil, nil
		}
		c.Resolved = &r
	}
	// No node is larger than its parts' bounds, but a part that is added later has none.
	if jsonSize(c) > costNodeBytes {
		log.Printf("lineage-cost.json: %.200q is over %d bytes: only its total and its end are kept", c.Node.Ref, costNodeBytes)
		return wire.LineageCostNode{Node: c.Node, Repo: c.Repo, Banked: c.Banked, Outcome: c.Outcome, FailedChecks: c.FailedChecks,
			FirstAt: c.FirstAt, StartedAt: c.StartedAt, EndedAt: c.EndedAt, LastAt: c.LastAt}
	}
	return c
}

// cutJSON is s at most most bytes as JSON, with … where it is cut.
func cutJSON(s string, most int) string {
	if jsonSize(s) <= most {
		return s
	}
	size, end := 2+len("…"), 0
	for i, r := range s {
		if size += jsonSize(string(r)) - 2; size > most {
			break
		}
		end = i
	}
	return s[:end] + "…"
}

// boundNotices holds the notices told to costNoticesKept: those of nodes that are gone go first.
func boundNotices(f *costLedgerFile) {
	keys := slices.SortedFunc(maps.Keys(f.Overruns), func(a, b string) int {
		return cmp.Compare(f.Nodes[a].LastAt, f.Nodes[b].LastAt)
	})
	for _, k := range keys[:max(0, len(keys)-costNoticesKept)] {
		delete(f.Overruns, k)
	}
	late := slices.SortedFunc(maps.Keys(f.MilestonesLate), func(a, b string) int {
		return cmp.Compare(f.MilestonesLate[a], f.MilestonesLate[b])
	})
	for _, k := range late[:max(0, len(late)-costNoticesKept)] {
		delete(f.MilestonesLate, k)
	}
}
