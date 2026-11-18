package lineage

import (
	"cmp"
	"slices"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The DORA figures of one target (#5245, spec 5239-deploy-model, "What it gives us"): a
// component in one environment, from its deploys, over a range.
//
//   - Deployment frequency: the deploys that succeeded, a week.
//   - Lead time for changes: a PR's merge to the first succeeded deploy that brings it. The
//     median, and p90.
//   - Change failure rate: the failed deploys and the rollbacks, over the deploys that ended
//     (succeeded or failed; a cancelled one is neither).
//   - Failed deployment recovery time: a failed deploy's end, or a health signal seen in a
//     deploy, to the next deploy that succeeded. The median.
//
// Each is counted at the deploy's end, with a series of 7-day weeks back from at, and DORA
// 2024's band, as DeliveryOf's are.

// DeployFact is one deploy of a target, as the figures need it.
type DeployFact struct {
	State wire.DeployState
	// At is when it ended: its finish, else its start.
	At wire.Date
	// Rollback is a succeeded deploy that put an older version back.
	Rollback bool
	// Merges are the merge times of the PRs it brings, for a succeeded deploy.
	Merges []wire.Date
	// Health are the times health signals were seen in it.
	Health []wire.Date
}

// DeployFigures is a target's four figures.
type DeployFigures struct {
	Ended, Failed, Rollbacks                                       int
	DeploymentFrequency, LeadTime, ChangeFailureRate, RecoveryTime wire.LineageDeliveryFigure
}

// DeployFiguresOf is the four figures of one target's deploys, over [since, at].
func DeployFiguresOf(deploys []DeployFact, since, at wire.Date) DeployFigures {
	ds := slices.Clone(deploys)
	slices.SortFunc(ds, func(a, b DeployFact) int {
		switch {
		case a.At < b.At:
			return -1
		case a.At > b.At:
			return 1
		}
		return 0
	})
	acc := deployAccumulator{ds: ds, since: since, at: at}
	for _, d := range ds {
		acc.add(d)
	}
	out := acc.out
	out.DeploymentFrequency = figureOf(acc.went, since, at, func(v []float64) float64 { return float64(len(v)) })
	if weeks := float64(len(out.DeploymentFrequency.Weeks)); weeks > 0 {
		perWeek := float64(out.DeploymentFrequency.Count) / weeks
		out.DeploymentFrequency.Value = &perWeek
		out.DeploymentFrequency.Band = band(&perWeek, frequencyBand)
	}
	for i, w := range out.DeploymentFrequency.Weeks {
		v := float64(w.Count)
		out.DeploymentFrequency.Weeks[i].Value = &v
	}
	out.LeadTime = figureOf(acc.lead, since, at, median)
	if v := valuesIn(acc.lead, since, at+1); len(v) > 0 {
		p := percentile(v, 0.9)
		out.LeadTime.P90 = &p
	}
	out.LeadTime.Band = band(out.LeadTime.Value, leadBand)
	out.ChangeFailureRate = figureOf(acc.failures, since, at, mean)
	out.ChangeFailureRate.Band = band(out.ChangeFailureRate.Value, failureBand)
	out.RecoveryTime = figureOf(acc.recovery, since, at, median)
	out.RecoveryTime.Band = band(out.RecoveryTime.Value, recoveryBand)
	return out
}

// deployAccumulator collects the counts and samples of DeployFiguresOf, one deploy at a
// time, over the deploys ds in time order.
type deployAccumulator struct {
	ds                             []DeployFact
	since, at                      wire.Date
	out                            DeployFigures
	went, lead, failures, recovery []sample
}

func (a *deployAccumulator) in(t wire.Date) bool { return t >= a.since && t <= a.at }

// nextGood is the first deploy that succeeded after t, at its end.
func (a *deployAccumulator) nextGood(t wire.Date) (wire.Date, bool) {
	for _, d := range a.ds {
		if d.State == wire.DeployStateSucceeded && d.At > t {
			return d.At, true
		}
	}
	return 0, false
}

// add counts one deploy: its end and failure, the recovery after it, and, for a success in
// the range, the deploy and the lead time of its merges.
func (a *deployAccumulator) add(d DeployFact) {
	if (d.State == wire.DeployStateSucceeded || d.State == wire.DeployStateFailed) && a.in(d.At) {
		a.addEnded(d)
	}
	if d.State == wire.DeployStateFailed {
		a.addRecovery(d.At)
	}
	for _, h := range d.Health {
		a.addRecovery(h)
	}
	if d.State != wire.DeployStateSucceeded || !a.in(d.At) {
		return
	}
	a.went = append(a.went, sample{d.At, 1})
	for _, m := range d.Merges {
		if m <= d.At {
			a.lead = append(a.lead, sample{d.At, float64(d.At - m)})
		}
	}
}

// addEnded counts a deploy that ended in the range, and whether it failed or rolled back.
func (a *deployAccumulator) addEnded(d DeployFact) {
	a.out.Ended++
	failed := d.State == wire.DeployStateFailed
	if failed {
		a.out.Failed++
	}
	if d.Rollback && !failed {
		a.out.Rollbacks++
	}
	v := 0.0
	if failed || d.Rollback {
		v = 1
	}
	a.failures = append(a.failures, sample{d.At, v})
}

// addRecovery adds the time from a failure at t to the next good deploy, when that is in
// the range.
func (a *deployAccumulator) addRecovery(t wire.Date) {
	if good, ok := a.nextGood(t); ok && a.in(good) {
		a.recovery = append(a.recovery, sample{good, float64(good - t)})
	}
}

// The answer of deploys/delivery and deploys/deliveryAll (#5496): the machine counts the
// deploys it keeps, and the control plane the copies its machines send (deploys/sync), with
// the same code, so both say the same of the same deploys.

// DeliveryDays is a range's days: the last 7 for a day, as for a week.
func DeliveryDays(r wire.ReportRange) int {
	switch r {
	case wire.ReportRangeMonth:
		return 30
	case wire.ReportRangeThreeMonths:
		return 90
	}
	return 7
}

// DeliveryFactDays is how far back a machine works out what its deploys bring for its copy:
// the longest range.
const DeliveryFactDays = 90

// tierOrder puts production first, then staging, testing, development and other.
var tierOrder = map[wire.EnvironmentTier]int{
	wire.EnvironmentTierProduction: 0, wire.EnvironmentTierStaging: 1, wire.EnvironmentTierTesting: 2,
	wire.EnvironmentTierDevelopment: 3, wire.EnvironmentTierOther: 4,
}

// DeploysDeliveryOf is the answer over facts for r's range and tier, at now. deploys is how
// many there are, of every target and age; resolving is left to the caller.
func DeploysDeliveryOf(facts []wire.DeployDeliveryFact, r wire.DeploysDeliveryRequest, now time.Time) wire.DeploysDelivery {
	days := DeliveryDays(r.Range)
	at := wire.DateOf(now)
	since := wire.DateOf(now.Add(-time.Duration(days) * 24 * time.Hour))
	out := wire.DeploysDelivery{Since: since, At: at, WindowDays: int64(days), Deploys: int64(len(facts)),
		Targets: []wire.DeployDeliveryTarget{}}
	type target struct {
		wire.DeployDeliveryTarget
		facts []DeployFact
	}
	targets := map[string]*target{}
	for _, f := range facts {
		fact := DeployFact{State: f.State, At: f.At, Rollback: f.Rollback != nil && *f.Rollback, Merges: f.Merges, Health: f.Health}
		for _, env := range f.Environments {
			key := f.Component + "\x00" + env.Ref
			t := targets[key]
			if t == nil {
				t = &target{DeployDeliveryTarget: wire.DeployDeliveryTarget{Component: f.Component, Environment: env.Name,
					EnvironmentRef: env.Ref, Tier: env.Tier}}
				targets[key] = t
			}
			t.facts = append(t.facts, fact)
		}
	}
	for _, t := range targets {
		if r.Tier != nil && t.Tier != *r.Tier {
			continue
		}
		fig := DeployFiguresOf(t.facts, since, at)
		if fig.Ended == 0 {
			continue
		}
		t.Ended, t.Failed, t.Rollbacks = int64(fig.Ended), int64(fig.Failed), int64(fig.Rollbacks)
		t.DeploymentFrequency, t.LeadTime, t.ChangeFailureRate, t.RecoveryTime = fig.DeploymentFrequency, fig.LeadTime, fig.ChangeFailureRate, fig.RecoveryTime
		out.Targets = append(out.Targets, t.DeployDeliveryTarget)
	}
	slices.SortFunc(out.Targets, func(a, b wire.DeployDeliveryTarget) int {
		return cmp.Or(cmp.Compare(tierOrder[a.Tier], tierOrder[b.Tier]), cmp.Compare(b.Ended, a.Ended),
			cmp.Compare(a.Component, b.Component), cmp.Compare(a.Environment, b.Environment))
	})
	return out
}

// DeployFactsOnce is facts with each deploy once, by its ref: two machines that watch the same
// repository keep the same deploys. The one that says most wins: an ended state over one not
// ended, then the later end, then more of what it brings, then more health signals.
func DeployFactsOnce(facts []wire.DeployDeliveryFact) []wire.DeployDeliveryFact {
	at := map[string]int{}
	var out []wire.DeployDeliveryFact
	for _, f := range facts {
		i, seen := at[f.Ref]
		if !seen {
			at[f.Ref] = len(out)
			out = append(out, f)
			continue
		}
		if factSaysMore(f, out[i]) {
			out[i] = f
		}
	}
	return out
}

func factSaysMore(a, b wire.DeployDeliveryFact) bool {
	ended := func(s wire.DeployState) bool {
		return s == wire.DeployStateSucceeded || s == wire.DeployStateFailed || s == wire.DeployStateCancelled
	}
	if ea, eb := ended(a.State), ended(b.State); ea != eb {
		return ea
	}
	if a.At != b.At {
		return a.At > b.At
	}
	if len(a.Merges) != len(b.Merges) {
		return len(a.Merges) > len(b.Merges)
	}
	return len(a.Health) > len(b.Health)
}
