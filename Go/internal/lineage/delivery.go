package lineage

import (
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The delivery figures (#4552, epic #4551): the four DORA keys, DORA 2025's rework rate,
// flow efficiency and CI time to green, for a repo, from the cost ledger's nodes and its
// releases' tag times.
//
//   - Lead time for changes: from the first work on an issue (its own start or its PRs') to
//     the first release tag that carries a merged PR that fixes it. Median and P90.
//   - Deployment frequency: release tags a week (#3023).
//   - Change failure rate: failures over merged PRs. A failure is a merged revert (a PR whose
//     title starts with Revert), or a merged PR that fixes a priority:high bug filed within
//     FailureAfterRelease after a release tag.
//   - Failed deployment recovery time: a revert's merge less the merge of the PR it reverts,
//     or a bug's first merged fix less the bug's filing. Median.
//   - Rework rate: the share of merged PRs, of those with their files known, that change a
//     file another PR merged in the ReworkRange before changed.
//   - Flow efficiency: a closed issue's agents' working time (its own and its PRs') over its
//     cycle time (#3562), at most 1. Median.
//   - CI time to green (#4554): a merged PR's last push (the first run made on its last head
//     commit) to the last of that commit's runs passing. Median and P90. The ci-speed skill's
//     figure, and the lane-count rule's (more than 15 minutes means fewer lanes).
//   - Failed rounds: a merged PR's failed CI runs and attempts. Mean.
//   - Approval wait (#4553): from the first review requested from the person on a PR
//     (review.requested, #5803; its opening when no request is known) to its first answer by a
//     person. Median and P90; and the share of merged PRs
//     that had one before their merge.
//
// Each is over the DeliveryWindow before now, with a series of its weeks, 7 days each back
// from now. The band is DORA 2024's cluster, as a reference: those clusters are teams of
// people, and agents merge many small PRs.
//
// Decided (#4552): a PR fixes a bug by any lineage link (the PR body's or a session's), not
// only "Fixes #n" in its body, because every PR an agent opens has one. A bug's filing is
// when the forge says it was made, else when this machine first noted it.

const (
	// DeliveryWindow is the delivery figures' window: 4 weeks.
	DeliveryWindow = 4 * deliveryWeek
	deliveryWeek   = 7 * 24 * time.Hour
	// FailureAfterRelease is how long after a release a priority:high bug filed counts against it.
	FailureAfterRelease = 7 * 24 * time.Hour
	// ReworkRange is how far back a merged PR's files count as recent work.
	ReworkRange = 21 * 24 * time.Hour
	// FilesKept bounds a PR's files on its node; FilesKeep is how long after its merge they
	// are kept: the window and the rework range, with room.
	FilesKept = 100
	FilesKeep = 60 * 24 * time.Hour
)

// The DORA 2024 clusters.
const (
	BandElite  = "elite"
	BandHigh   = "high"
	BandMedium = "medium"
	BandLow    = "low"
)

// revertTitle is the title GitHub's Revert button gives: Revert "<the PR's title>".
var revertTitle = regexp.MustCompile(`^Revert "(.+)"$`)

// RevertsIn is the PR a revert's body says it reverts: "Reverts owner/repo#12", or "Reverts
// #12" in repo (github:owner/repo). "" when it names none.
func RevertsIn(repo, body string) string {
	m := revertsBody.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	if m[1] == "" {
		return repo + "#" + m[2]
	}
	scheme, _, _ := strings.Cut(repo, ":")
	return scheme + ":" + m[1] + "#" + m[2]
}

var revertsBody = regexp.MustCompile(`(?i)\breverts\s+([\w.-]+/[\w.-]+)?#([0-9]{1,9})\b`)

// sample is one figure's value at a time.
type sample struct {
	at wire.Date
	v  float64
}

// DeliveryOf is repo's delivery figures from nodes, which hold each node once, and the
// release tags' times by ref, at now.
func DeliveryOf(nodes []wire.LineageCostNode, repo string, releases map[string]wire.Date, now time.Time) wire.LineageDelivery {
	at := wire.DateOf(now)
	since := wire.DateOf(now.Add(-DeliveryWindow))
	d := indexDelivery(nodes, repo, releases)
	out := wire.LineageDelivery{Since: since, At: at, WindowDays: int64(DeliveryWindow / (24 * time.Hour))}
	out.LeadTime = d.leadTime(since, at)
	out.DeploymentFrequency = tagFrequency(d.tags, since, at)
	out.ChangeFailureRate, out.RecoveryTime = d.failuresAndRecovery(since, at)
	out.ReworkRate = reworkRate(d.merged, since, at)
	out.FlowEfficiency = flowEfficiency(d.mine, since, at)
	out.CiTimeToGreen, out.FailedRounds = ciFigures(d.merged, since, at)
	// The approval wait (#4553), and the share of merged PRs that waited.
	out.ApprovalWait = approvalWaitIn(d.mine, since, at)
	out.ApprovalShare = approvalShare(d.merged, since, at)
	return out
}

// deliveryIndex is one repo's nodes and release tags, indexed for DeliveryOf.
type deliveryIndex struct {
	mine     []wire.LineageCostNode
	releases map[string]wire.Date
	tags     []wire.Date
	byRef    map[string]wire.LineageCostNode
	fixers   map[string][]wire.LineageCostNode
	merged   []wire.LineageCostNode
}

// indexDelivery keeps the nodes and releases of repo, and indexes the nodes by ref, the
// merged PRs, and the merged PRs that fix each issue.
func indexDelivery(nodes []wire.LineageCostNode, repo string, releases map[string]wire.Date) deliveryIndex {
	// The repo, a node's repo and a release's are compared in the full form (#4800), so the
	// old form of any of them still matches.
	repo = FullRepoText(repo)
	d := deliveryIndex{releases: releases, byRef: map[string]wire.LineageCostNode{}, fixers: map[string][]wire.LineageCostNode{}}
	for _, n := range nodes {
		if n.Repo != nil && FullRepoText(*n.Repo) == repo {
			d.mine = append(d.mine, n)
		}
	}
	for ref, t := range releases {
		if RepoOf(wire.LineageNode{Kind: KindRelease, Ref: ref}) == repo {
			d.tags = append(d.tags, t)
		}
	}
	slices.Sort(d.tags)
	for _, n := range d.mine {
		d.byRef[n.Node.Ref] = n
		if n.Node.Kind != KindPR || !mergedPR(n) {
			continue
		}
		d.merged = append(d.merged, n)
		for _, f := range n.Fixes {
			d.fixers[f] = append(d.fixers[f], n)
		}
	}
	return d
}

// leadTime is the lead time for changes, by the tag's time.
func (d deliveryIndex) leadTime(since, at wire.Date) wire.LineageDeliveryFigure {
	var lead []sample
	for _, n := range d.mine {
		if n.Node.Kind != KindIssue || (n.Level != nil && *n.Level == "epic") {
			continue
		}
		start, shipped := d.startAndShipped(n)
		if start != nil && shipped != nil && *shipped >= *start {
			lead = append(lead, sample{*shipped, float64(*shipped - *start)})
		}
	}
	f := figureOf(lead, since, at, median)
	if v := valuesIn(lead, since, at+1); len(v) > 0 {
		p := percentile(v, 0.9)
		f.P90 = &p
	}
	f.Band = band(f.Value, leadBand)
	return f
}

// startAndShipped is when work on an issue started (the issue's or its first fixer's start)
// and the first release that shipped one of its fixers.
func (d deliveryIndex) startAndShipped(issue wire.LineageCostNode) (start, shipped *wire.Date) {
	start = StartOf(issue)
	for _, pr := range d.fixers[issue.Node.Ref] {
		if s := StartOf(pr); s != nil && (start == nil || *s < *start) {
			start = s
		}
		for _, ref := range pr.PartOf {
			if t, ok := d.releases[ref]; ok && (shipped == nil || t < *shipped) {
				shipped = &t
			}
		}
	}
	return start, shipped
}

// tagFrequency is the deployment frequency: tags a week, 0 for none.
func tagFrequency(tags []wire.Date, since, at wire.Date) wire.LineageDeliveryFigure {
	var deploys []sample
	for _, t := range tags {
		deploys = append(deploys, sample{t, 1})
	}
	f := figureOf(deploys, since, at, func(v []float64) float64 { return float64(len(v)) })
	weeks := float64(len(f.Weeks))
	perWeek := float64(f.Count) / weeks
	f.Value = &perWeek
	for i, w := range f.Weeks {
		v := float64(w.Count)
		f.Weeks[i].Value = &v
	}
	f.Band = band(&perWeek, frequencyBand)
	return f
}

// failuresAndRecovery is the change failure rate and the recovery time.
func (d deliveryIndex) failuresAndRecovery(since, at wire.Date) (rate, recoveryTime wire.LineageDeliveryFigure) {
	failureBug := d.failureBugs()
	byTitle := map[string]wire.LineageCostNode{}
	for _, n := range d.merged {
		if n.Resolved != nil && n.Resolved.Title != "" {
			byTitle[n.Resolved.Title] = n
		}
	}
	var failures, recovery []sample
	for _, n := range d.merged {
		failed := n.Outcome != nil && *n.Outcome == OutcomeReverted
		if failed {
			if r, ok := revertedBy(n, d.byRef, byTitle); ok && r.EndedAt != nil && *n.EndedAt >= *r.EndedAt {
				recovery = append(recovery, sample{*n.EndedAt, float64(*n.EndedAt - *r.EndedAt)})
			}
		}
		if slices.ContainsFunc(n.Fixes, func(f string) bool { _, ok := failureBug[f]; return ok }) {
			failed = true
		}
		v := 0.0
		if failed {
			v = 1
		}
		failures = append(failures, sample{*n.EndedAt, v})
	}
	for ref, filed := range failureBug {
		if first := firstEnded(d.fixers[ref]); first != nil && *first >= filed {
			recovery = append(recovery, sample{*first, float64(*first - filed)})
		}
	}
	rate = figureOf(failures, since, at, mean)
	rate.Band = band(rate.Value, failureBand)
	recoveryTime = figureOf(recovery, since, at, median)
	recoveryTime.Band = band(recoveryTime.Value, recoveryBand)
	return rate, recoveryTime
}

// failureBugs is the priority:high bugs filed within a week after a release, by ref, at the
// time each was filed.
func (d deliveryIndex) failureBugs() map[string]wire.Date {
	afterRelease := func(t wire.Date) bool {
		i := sort.Search(len(d.tags), func(i int) bool { return d.tags[i] > t })
		return i > 0 && float64(t-d.tags[i-1]) <= FailureAfterRelease.Seconds()
	}
	failureBug := map[string]wire.Date{}
	for _, n := range d.mine {
		if n.Node.Kind != KindIssue || n.Resolved == nil || !isHighBug(n) {
			continue
		}
		if f := filedAt(n); f != nil && afterRelease(*f) {
			failureBug[n.Node.Ref] = *f
		}
	}
	return failureBug
}

// filedAt is when an issue was filed: its creation on the forge, else its first activity.
func filedAt(issue wire.LineageCostNode) *wire.Date {
	if issue.Resolved != nil && issue.Resolved.CreatedAt != nil {
		return issue.Resolved.CreatedAt
	}
	return issue.FirstAt
}

// firstEnded is the earliest end of the PRs, or nil for none.
func firstEnded(prs []wire.LineageCostNode) *wire.Date {
	var first *wire.Date
	for _, pr := range prs {
		if first == nil || *pr.EndedAt < *first {
			first = pr.EndedAt
		}
	}
	return first
}

// reworkRate is the share of merged PRs that change a file another PR changed in the
// ReworkRange before it.
func reworkRate(merged []wire.LineageCostNode, since, at wire.Date) wire.LineageDeliveryFigure {
	var withFiles []wire.LineageCostNode
	for _, n := range merged {
		if len(n.Files) > 0 {
			withFiles = append(withFiles, n)
		}
	}
	var rework []sample
	for _, n := range withFiles {
		if *n.EndedAt < since {
			continue
		}
		recent := recentFiles(withFiles, n)
		v := 0.0
		if slices.ContainsFunc(n.Files, func(f string) bool { return recent[f] }) {
			v = 1
		}
		rework = append(rework, sample{*n.EndedAt, v})
	}
	return figureOf(rework, since, at, mean)
}

// recentFiles is the files that the other PRs merged in the ReworkRange before n changed.
func recentFiles(withFiles []wire.LineageCostNode, n wire.LineageCostNode) map[string]bool {
	from := *n.EndedAt - wire.Date(ReworkRange.Seconds())
	recent := map[string]bool{}
	for _, o := range withFiles {
		if o.Node != n.Node && *o.EndedAt >= from && *o.EndedAt < *n.EndedAt {
			for _, f := range o.Files {
				recent[f] = true
			}
		}
	}
	return recent
}

// flowEfficiency is the working share of each cycle, by the last merge.
func flowEfficiency(mine []wire.LineageCostNode, since, at wire.Date) wire.LineageDeliveryFigure {
	var flow []sample
	for _, p := range History(mine) {
		if p.Cycle != nil && *p.Cycle > 0 {
			flow = append(flow, sample{p.Merged, min(p.Cost.WorkingSeconds / *p.Cycle, 1)})
		}
	}
	return figureOf(flow, since, at, median)
}

// ciFigures is CI's time to green and the failed rounds, by merge (#4554).
func ciFigures(merged []wire.LineageCostNode, since, at wire.Date) (toGreen, failedRounds wire.LineageDeliveryFigure) {
	var green, rounds []sample
	for _, n := range merged {
		if ci := n.Ci; ci != nil && ci.PushedAt != nil && ci.GreenAt != nil && *ci.GreenAt >= *ci.PushedAt {
			green = append(green, sample{*n.EndedAt, float64(*ci.GreenAt - *ci.PushedAt)})
		}
		v := 0.0
		if n.FailedChecks != nil {
			v = float64(*n.FailedChecks)
		}
		rounds = append(rounds, sample{*n.EndedAt, v})
	}
	toGreen = figureOf(green, since, at, median)
	if v := valuesIn(green, since, at+1); len(v) > 0 {
		p := percentile(v, 0.9)
		toGreen.P90 = &p
	}
	return toGreen, figureOf(rounds, since, at, mean)
}

// approvalShare is the share of merged PRs that waited for an approval.
func approvalShare(merged []wire.LineageCostNode, since, at wire.Date) wire.LineageDeliveryFigure {
	var waited []sample
	for _, n := range merged {
		v := 0.0
		if _, ok := ApprovalWait(n); ok && *n.AnsweredAt <= *n.EndedAt {
			v = 1
		}
		waited = append(waited, sample{*n.EndedAt, v})
	}
	return figureOf(waited, since, at, mean)
}

// ApprovalWait is the seconds a PR waited for a person's answer (#4553): from the review
// requested from the person (#5803), else its opening, to its first answer. Not ok with no answer, no
// start, or an answer before the start.
func ApprovalWait(n wire.LineageCostNode) (float64, bool) {
	if n.Node.Kind != KindPR || n.AnsweredAt == nil {
		return 0, false
	}
	start := n.AskedAt
	if start == nil {
		start = n.OpenedAt
	}
	if start == nil || *n.AnsweredAt < *start {
		return 0, false
	}
	return float64(*n.AnsweredAt - *start), true
}

// ApprovalWaitOf is the approval wait over every node, of any repo, in the DeliveryWindow
// before now: Reports ▸ Time's line (#4553).
func ApprovalWaitOf(nodes []wire.LineageCostNode, now time.Time) wire.LineageDeliveryFigure {
	return approvalWaitIn(nodes, wire.DateOf(now.Add(-DeliveryWindow)), wire.DateOf(now))
}

// approvalWaitIn is the approval wait's figure, the median and P90, by the answer's time.
func approvalWaitIn(nodes []wire.LineageCostNode, since, at wire.Date) wire.LineageDeliveryFigure {
	var waits []sample
	for _, n := range nodes {
		if w, ok := ApprovalWait(n); ok {
			waits = append(waits, sample{*n.AnsweredAt, w})
		}
	}
	f := figureOf(waits, since, at, median)
	if v := valuesIn(waits, since, at+1); len(v) > 0 {
		p := percentile(v, 0.9)
		f.P90 = &p
	}
	return f
}

// mergedPR says a PR merged: a merge, or a merged revert.
func mergedPR(n wire.LineageCostNode) bool {
	return n.Outcome != nil && n.EndedAt != nil && (*n.Outcome == OutcomeMerged || *n.Outcome == OutcomeReverted)
}

// isHighBug says an issue is a bug labelled priority:high.
func isHighBug(n wire.LineageCostNode) bool {
	bug := n.Level != nil && *n.Level == "bug" || TypeOf(n.Resolved.Labels) == "bug"
	return bug && slices.ContainsFunc(n.Resolved.Labels, func(l string) bool {
		return strings.EqualFold(strings.TrimSpace(l), "priority:high")
	})
}

// revertedBy is the merged PR a revert reverts: the one its body names, else the one whose
// title its own title quotes.
func revertedBy(revert wire.LineageCostNode, byRef, byTitle map[string]wire.LineageCostNode) (wire.LineageCostNode, bool) {
	if revert.Reverts != nil {
		if r, ok := byRef[*revert.Reverts]; ok && mergedPR(r) {
			return r, true
		}
	}
	if revert.Resolved == nil {
		return wire.LineageCostNode{}, false
	}
	m := revertTitle.FindStringSubmatch(revert.Resolved.Title)
	if m == nil {
		return wire.LineageCostNode{}, false
	}
	r, ok := byTitle[m[1]]
	return r, ok && r.Node != revert.Node
}

// figureOf is samples' figure over [since, at], and over each of its weeks, by agg.
func figureOf(samples []sample, since, at wire.Date, agg func([]float64) float64) wire.LineageDeliveryFigure {
	week := wire.Date(deliveryWeek.Seconds())
	n := int(math.Round(float64(at-since) / float64(week)))
	f := wire.LineageDeliveryFigure{Weeks: make([]wire.LineageDeliveryWeek, 0, n)}
	// The last week takes at itself.
	all := valuesIn(samples, since, at+1)
	f.Count = int64(len(all))
	if len(all) > 0 {
		v := agg(all)
		f.Value = &v
	}
	for i := range n {
		from := since + wire.Date(i)*week
		to := from + week
		if i == n-1 {
			to = at + 1
		}
		w := wire.LineageDeliveryWeek{Since: from}
		if v := valuesIn(samples, from, to); len(v) > 0 {
			w.Count = int64(len(v))
			x := agg(v)
			w.Value = &x
		}
		f.Weeks = append(f.Weeks, w)
	}
	return f
}

// valuesIn is the values of the samples at from or later and before to, sorted.
func valuesIn(samples []sample, from, to wire.Date) []float64 {
	var v []float64
	for _, s := range samples {
		if s.at >= from && s.at < to {
			v = append(v, s.v)
		}
	}
	sort.Float64s(v)
	return v
}

func mean(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

// band is v's DORA 2024 cluster by of; nil for no value.
func band(v *float64, of func(float64) string) *string {
	if v == nil {
		return nil
	}
	b := of(*v)
	return &b
}

// leadBand: elite under a day, high to a week, medium to a month, else low.
func leadBand(secs float64) string {
	switch day := 24 * time.Hour.Seconds(); {
	case secs < day:
		return BandElite
	case secs <= 7*day:
		return BandHigh
	case secs <= 30*day:
		return BandMedium
	}
	return BandLow
}

// frequencyBand, in tags a week: elite more than daily, high daily to weekly, medium weekly
// to monthly, else low.
func frequencyBand(perWeek float64) string {
	switch {
	case perWeek > 7:
		return BandElite
	case perWeek >= 1:
		return BandHigh
	case perWeek >= 12.0/52:
		return BandMedium
	}
	return BandLow
}

// failureBand is the cluster whose 2024 figure is nearest: elite 5%, medium 10%, high 20%,
// low 40%. DORA 2024 put medium below high on this key.
func failureBand(rate float64) string {
	clusters := []struct {
		band string
		at   float64
	}{{BandElite, 0.05}, {BandMedium, 0.10}, {BandHigh, 0.20}, {BandLow, 0.40}}
	best := clusters[0]
	for _, c := range clusters[1:] {
		if math.Abs(rate-c.at) < math.Abs(rate-best.at) {
			best = c
		}
	}
	return best.band
}

// recoveryBand: elite under an hour, high under a day, medium under a week, else low. DORA
// 2024 put high and medium both under a day, and low from a week to a month.
func recoveryBand(secs float64) string {
	switch hour := time.Hour.Seconds(); {
	case secs < hour:
		return BandElite
	case secs < 24*hour:
		return BandHigh
	case secs < 7*24*hour:
		return BandMedium
	}
	return BandLow
}
