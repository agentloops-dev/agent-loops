package agentsd

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/citimings"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Autopilot stops itself when the work goes bad (#5902, epic #5897). It starts nothing new, and
// says why in autopilot/status with autopilot.paused, while
//   - main is red for more than autopilotMainRedFor: the latest ci.yml runs on the default branch
//     fail, as Fix main reads them. The folder's row "main" is paused; it resumes when main is green.
//   - an epic's loaded spend is at 2× its kept estimate, the point of the overrun notice (#4529):
//     the epic's row is paused, its lead is told to start no new lane, and it resumes when the
//     estimate is raised. A project budget that says Stop (#5518) is not read yet.
//
// Each pause tells the person once, with a notice on a session of the epic (or of the folder)
// that says the reason and what resumes it.

const (
	autopilotMainRedFor = 60 * time.Minute
	autopilotMainRef    = "main"
	autopilotMainFlow   = "ci.yml"
	autopilotNoticeKey  = "autopilot:"
	// autopilotOverAt is the multiple of an epic's estimate at which it is paused.
	autopilotOverAt = 2
	// autopilotCILook is how far back the runs of main are read.
	autopilotCILook = 7 * 24 * time.Hour
)

// mainRedSince is when main went red: the end of the first of the failed runs of the default
// branch's workflow that run on until the newest one, which must itself be failed. A run that
// was cancelled or skipped says nothing; a re-run counts by its newest attempt. False when main
// is green, or no run is known.
func mainRedSince(runs []citimings.Run, workflow string) (time.Time, bool) {
	latest := map[string]citimings.Run{}
	for _, r := range runs {
		if !r.Default || r.Tag || r.Workflow != workflow || r.Event == "pull_request" {
			continue
		}
		switch r.Conclusion {
		case "success", "failure", "timed_out":
		default:
			continue
		}
		if o, ok := latest[r.Ref]; !ok || r.Attempt > o.Attempt {
			latest[r.Ref] = r
		}
	}
	list := make([]citimings.Run, 0, len(latest))
	for _, r := range latest {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Made < list[j].Made })
	var since time.Time
	for i := len(list) - 1; i >= 0 && list[i].Conclusion != "success"; i-- {
		since = list[i].MadeAt().Add(time.Duration(list[i].Ended * float64(time.Second)))
	}
	return since, !since.IsZero()
}

// autopilotMainRed is how long the folder's main has been red, or false when it is green, is not
// read, or has been red for no more than autopilotMainRedFor.
func (d *Daemon) autopilotMainRed(folder string) (since time.Time, red bool) {
	r := d.folderRepo(folder)
	if r.path == "" || !ciReads(r.forge) {
		return time.Time{}, false
	}
	at := nowFunc()
	runs, err := d.ciTimings().Runs(citimings.Repo{Forge: r.forge, Path: strings.ToLower(r.path)}, at.Add(-autopilotCILook), at.Add(time.Hour))
	if err != nil {
		return time.Time{}, false
	}
	since, red = mainRedSince(runs, autopilotMainFlow)
	return since, red && at.Sub(since) > autopilotMainRedFor
}

// autopilotMainWords is the pause's reason for main.
func autopilotMainWords(since time.Time) string {
	return "main has been red since " + since.Local().Format("15:04") + ". Autopilot starts nothing until main is green."
}

// autopilotOverBudget is the reason an epic is paused, or "" when it is not over budget.
func autopilotOverBudget(nodes []wire.LineageCostNode, epic wire.LineageNode) string {
	o, ok := lineage.OverrunOf(nodes, epic)
	if !ok || o.Passed < autopilotOverAt {
		return ""
	}
	return fmt.Sprintf("over budget: %s spent, %.1f× its estimate of %s. Raise the estimate to resume.",
		costWords(o.Spent, o.Currency), o.Spent/o.Estimate, costWords(o.Estimate, o.Currency))
}

// autopilotLedger is the cost ledger's nodes.
func (d *Daemon) autopilotLedger() []wire.LineageCostNode {
	d.workMu.Lock()
	defer d.workMu.Unlock()
	return ledgerNodes(d.costLedgerLocked())
}

// autopilotRowState is the state of the folder's row for ref, "" when it has none.
func (d *Daemon) autopilotRowState(folder, ref string) wire.AutopilotRowState {
	entries, err := d.autopilotFile.Read()
	if err != nil {
		return ""
	}
	for _, r := range entries[folder].Rows {
		if r.Ref == ref {
			return r.State
		}
	}
	return ""
}

// autopilotPause records a paused row. When the row was not paused it also tells the person, once,
// and, for an epic with a lead, tells the lead to start no new lane.
func (d *Daemon) autopilotPause(folder, ref string, kind wire.AutopilotRowKind, why string, lead *wire.ForgeLead) {
	first := d.autopilotRowState(folder, ref) != wire.AutopilotRowStatePaused
	if err := d.autopilotNote(folder, wire.AutopilotRow{Ref: ref, Kind: kind, State: wire.AutopilotRowStatePaused, Reason: why}); err != nil || !first {
		return
	}
	agent, ok := d.autopilotNoticeAgent(folder, lead)
	if ok {
		id := autopilotNoticeKey + folder + "\t" + ref
		d.attentionMu.Lock()
		d.notices = append(slices.DeleteFunc(d.notices, func(n postedNotice) bool { return n.id == id }),
			postedNotice{id: id, agentID: agent, raised: now(), h3: "Autopilot paused " + issueWords(ref) + ". " + why})
		d.attentionMu.Unlock()
		d.reconsider()
	}
	if lead != nil && d.Store.Agent(lead.Session) != nil {
		text := "Autopilot has paused this epic: " + why + " Start no new lane until the person raises the estimate or resumes it."
		_ = d.prompt(wire.PromptRequest{AgentID: lead.Session, Text: text, From: wire.PromptOriginApp})
	}
}

// autopilotResume records that a paused row is not paused now, which raises autopilot.resumed.
func (d *Daemon) autopilotResume(folder, ref string, kind wire.AutopilotRowKind, why string) {
	if d.autopilotRowState(folder, ref) != wire.AutopilotRowStatePaused {
		return
	}
	_ = d.autopilotNote(folder, wire.AutopilotRow{Ref: ref, Kind: kind, State: wire.AutopilotRowStateWaiting, Reason: why})
}

// autopilotNoticeAgent is the session a notice goes on: the epic's lead, else the newest session of
// the folder that is not archived.
func (d *Daemon) autopilotNoticeAgent(folder string, lead *wire.ForgeLead) (wire.UUID, bool) {
	if lead != nil {
		if a := d.Store.Agent(lead.Session); a != nil && a.State != wire.AgentStateArchived {
			return a.ID, true
		}
	}
	var best *wire.Agent
	agents := d.Store.Agents()
	for i := range agents {
		a := &agents[i]
		if a.State == wire.AgentStateArchived || folderPath(a) != folder {
			continue
		}
		if best == nil || a.CreatedAt > best.CreatedAt {
			best = a
		}
	}
	if best == nil {
		return "", false
	}
	return best.ID, true
}
