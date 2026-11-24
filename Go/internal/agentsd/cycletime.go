package agentsd

import (
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

type cycleTimeBounds struct {
	firstHour, lastHour string
	firstDay, lastDay   string
}

type cycleFixerIndex struct {
	byRef        map[string]wire.LineageCostNode
	allFixers    map[string][]wire.LineageCostNode
	mergedFixers map[string][]wire.LineageCostNode
}

func (d *Daemon) cycletimeHistory(now time.Time) wire.CycleTimeHistory {
	h := wire.CycleTimeHistory{Hours: map[string]wire.CycleTimeBucket{},
		Days: map[string]wire.CycleTimeBucket{}, Now: hourStamp(now), TimeZone: zoneName()}
	bounds := cycleTimeBounds{
		firstHour: oldestHour(now, usageHoursKept), lastHour: hourStamp(now),
		firstDay: oldestDay(now, usageDaysKept), lastDay: dayStamp(now),
	}

	d.workMu.Lock()
	defer d.workMu.Unlock()
	nodes := ledgerNodes(d.costLedgerLocked())
	index := cycleFixerIndexFrom(nodes)
	covered := map[string]bool{}
	for _, l := range lineage.Landings(nodes) {
		if l.Cycle == nil {
			continue
		}
		covered[l.Ref] = true
		bounds.addSample(&h, l.At, index.sample(l))
	}

	for _, n := range nodes {
		if !cycleNoStart(n) || cycleCovered(n.Fixes, covered) {
			continue
		}
		bounds.addNoStart(&h, n.EndedAt.Time())
	}
	return h
}

func cycleFixerIndexFrom(nodes []wire.LineageCostNode) cycleFixerIndex {
	index := cycleFixerIndex{
		byRef:        map[string]wire.LineageCostNode{},
		allFixers:    map[string][]wire.LineageCostNode{},
		mergedFixers: map[string][]wire.LineageCostNode{},
	}
	for _, n := range nodes {
		index.byRef[n.Node.Ref] = n
		if n.Node.Kind != lineage.KindPR {
			continue
		}
		for _, f := range n.Fixes {
			index.allFixers[f] = append(index.allFixers[f], n)
			if n.Outcome != nil && *n.Outcome == lineage.OutcomeMerged && n.EndedAt != nil {
				index.mergedFixers[f] = append(index.mergedFixers[f], n)
			}
		}
	}
	return index
}

func (index cycleFixerIndex) sample(l lineage.Landing) wire.CycleTimeSample {
	s := wire.CycleTimeSample{Cycle: *l.Cycle, Level: l.Level, Repo: l.Repo}
	if s.Repo == "" {
		s.Repo = lineage.RepoOf(index.byRef[l.Ref].Node)
	}
	starts := []*wire.Date{index.byRef[l.Ref].StartedAt}
	for _, pr := range index.allFixers[l.Ref] {
		starts = append(starts, pr.StartedAt)
	}
	if start := earliestDate(starts...); start != nil {
		opened := index.opened(l.Ref)
		if opened != nil && *opened >= *start && *opened <= l.Merged {
			coding, prTime := float64(*opened-*start), float64(l.Merged-*opened)
			s.Coding, s.Pr = &coding, &prTime
		}
	}
	return s
}

func (index cycleFixerIndex) opened(ref string) *wire.Date {
	var opened *wire.Date
	for _, pr := range index.mergedFixers[ref] {
		opened = earliestDate(opened, pr.OpenedAt)
	}
	return opened
}

func (bounds cycleTimeBounds) addSample(h *wire.CycleTimeHistory, at time.Time, s wire.CycleTimeSample) {
	if hour := hourStamp(at); hour >= bounds.firstHour && hour <= bounds.lastHour {
		addCycleSample(h.Hours, hour, s)
	}
	if day := dayStamp(at); day >= bounds.firstDay && day <= bounds.lastDay {
		addCycleSample(h.Days, day, s)
	}
}

func (bounds cycleTimeBounds) addNoStart(h *wire.CycleTimeHistory, at time.Time) {
	if hour := hourStamp(at); hour >= bounds.firstHour && hour <= bounds.lastHour {
		addCycleNoStart(h.Hours, hour)
	}
	if day := dayStamp(at); day >= bounds.firstDay && day <= bounds.lastDay {
		addCycleNoStart(h.Days, day)
	}
}

func cycleNoStart(n wire.LineageCostNode) bool {
	return n.Node.Kind == lineage.KindPR && n.Outcome != nil && n.EndedAt != nil &&
		(*n.Outcome == lineage.OutcomeMerged || *n.Outcome == lineage.OutcomeReverted)
}

func earliestDate(dates ...*wire.Date) *wire.Date {
	var out *wire.Date
	for _, d := range dates {
		if d != nil && (out == nil || *d < *out) {
			out = d
		}
	}
	return out
}

func cycleCovered(fixes []string, covered map[string]bool) bool {
	for _, f := range fixes {
		if covered[f] {
			return true
		}
	}
	return false
}

func addCycleSample(buckets map[string]wire.CycleTimeBucket, stamp string, s wire.CycleTimeSample) {
	b := buckets[stamp]
	b.Samples = append(b.Samples, s)
	buckets[stamp] = b
}

func addCycleNoStart(buckets map[string]wire.CycleTimeBucket, stamp string) {
	b := buckets[stamp]
	if b.Samples == nil {
		b.Samples = []wire.CycleTimeSample{}
	}
	b.NoStart++
	buckets[stamp] = b
}
