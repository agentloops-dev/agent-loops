package agentsd

import (
	"context"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Autopilot's loop for leads (#5899, epic #5897). While a folder's mode is on or high, the daemon
// reads forge/next with level epic and starts a lead for the top epic with no lead, as Start lead
// does (suggestionOf's form: the lead prompt, the labels #<n> and lead, no worktree), within the
// folder's helper limits, its open-PR limit and the machine's capacity. At most one lead starts a
// run. Each epic that waits says why in autopilot/status (autopilotNote).

var (
	// autopilotEvery is the longest between two runs; autopilotLeast the shortest.
	autopilotEvery = 10 * time.Minute
	autopilotLeast = time.Minute
)

// autopilotWakes are the events that start a run (forge changes are the poll's).
var autopilotWakes = map[string]bool{"forge.changed": true, "agent.ended": true, "machine.ok": true, "pr.merged": true}

// These are the Reason of a row that waits, in the words of AutopilotRow.reason.
const (
	autopilotLimitRunning = "limit running"
	autopilotLimitPRs     = "limit open-prs"
	autopilotOnHold       = "on hold"
	autopilotModeHigh     = "mode high"
	autopilotLeadStopping = "lead keeps stopping"
)

// A lead of an open epic that stops or ends is replaced after autopilotStalledFor, at most
// autopilotMostRestarts times for an epic in autopilotRestartDay (#5901).
const (
	autopilotStalledFor   = 10 * time.Minute
	autopilotMostRestarts = 2
	autopilotRestartDay   = 24 * time.Hour
)

// startAutopilot runs the loop until the daemon stops: on an event of autopilotWakes and every
// autopilotEvery, never twice within autopilotLeast.
func (d *Daemon) startAutopilot() {
	d.autopilotKick = make(chan struct{}, 1)
	d.goLoop("autopilot", func(ctx context.Context) {
		tick := time.NewTicker(autopilotEvery)
		defer tick.Stop()
		last := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			case <-d.autopilotKick:
			}
			if wait := autopilotLeast - time.Since(last); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			last = time.Now()
			d.autopilotRun(ctx)
		}
	})
}

// autopilotHear wakes the loop for an event of autopilotWakes.
func (d *Daemon) autopilotHear(e wire.Event) {
	if d.autopilotKick == nil || !autopilotWakes[e.Name] {
		return
	}
	select {
	case d.autopilotKick <- struct{}{}:
	default:
	}
}

// autopilotRun is one run: each folder whose mode is not off.
func (d *Daemon) autopilotRun(ctx context.Context) {
	for _, p := range d.Store.Folders() {
		mode := autopilotModeOf(p)
		if mode == wire.AutopilotModeOff || ctx.Err() != nil {
			continue
		}
		folder := store.FolderPath(p.Folder)
		d.autopilotLeads(ctx, folder, mode)
		d.autopilotLanes(ctx, folder, mode)
	}
}

// autopilotLeads is a run's part for leads in one folder.
func (d *Daemon) autopilotLeads(ctx context.Context, folder string, mode wire.AutopilotMode) {
	next, err := d.forgeNext(ctx, wire.ForgeNextRequest{Folder: folder, Level: new("epic")})
	if err != nil {
		log.Printf("autopilot: %s: %v", folder, err)
		return
	}
	dismissed, _ := d.dismissedLeads.Read()
	red := d.autopilotMainCheck(folder)
	ledger := d.autopilotLedger()
	var epics []autopilotEpic
	at := now()
	restarts := d.autopilotRestarts(folder, at)
	for _, r := range next.Rows {
		epics = append(epics, d.autopilotEpicOf(folder, r, ledger, dismissed, restarts, at))
	}
	room := autopilotRoom{limit: d.helperLimitRefusal(folder) != "", capacity: d.capacityLevel(), mainRed: red}
	// The open-PR count is read only when an epic could start.
	if !red && autopilotCanStart(epics, mode) {
		room.openPRs, room.prLimit = d.autopilotOpenPRs(ctx, folder)
	}
	for _, o := range autopilotLeadPlan(epics, mode, room) {
		if o.over {
			d.autopilotPause(folder, o.epic.Node.Ref, wire.AutopilotRowKindLead, o.why, o.lead)
			continue
		}
		row := wire.AutopilotRow{Ref: o.epic.Node.Ref, Kind: wire.AutopilotRowKindLead, State: wire.AutopilotRowStateWaiting, Reason: o.why}
		if o.paused {
			row.State = wire.AutopilotRowStatePaused
		}
		if o.start {
			id, err := d.startFor(autopilotLeadRequest(folder, o.epic, o.prev), startedBy{owner: wire.SessionLabelOwnerAgent})
			if err != nil {
				_, row.Reason = errMessage(err)
				row.Reason = "start failed: " + row.Reason
			} else {
				row.State, row.Reason, row.Session = wire.AutopilotRowStateStarted, "", &id
				if o.prev != "" {
					d.autopilotRestarted(folder, o.epic.Node.Ref, at)
				}
			}
		}
		if err := d.autopilotNote(folder, row); err != nil {
			log.Printf("autopilot: %s: %v", folder, err)
		}
	}
	// An epic that has a lead and is not over budget now is resumed.
	for _, e := range epics {
		if e.over == "" && (e.led || e.dismissed) {
			d.autopilotResume(folder, e.row.Item.Node.Ref, wire.AutopilotRowKindLead, "back under its estimate")
		}
	}
}

// autopilotMainCheck pauses the folder when its main has been red too long (#5902), and resumes
// it when main is green; it says whether the folder is paused.
func (d *Daemon) autopilotMainCheck(folder string) bool {
	since, red := d.autopilotMainRed(folder)
	if red {
		d.autopilotPause(folder, autopilotMainRef, wire.AutopilotRowKindLead, autopilotMainWords(since), nil)
	} else {
		d.autopilotResume(folder, autopilotMainRef, wire.AutopilotRowKindLead, "main is green")
	}
	return red
}

// autopilotEpicOf is an epic of forge/next with its lead, its spend and its last lead (#5899,
// #5901, #5902). A lead that stopped or ended is archived, to be replaced, unless the epic is
// capped (the person looks) or over budget.
func (d *Daemon) autopilotEpicOf(folder string, r wire.ForgeNextRow, ledger []wire.LineageCostNode,
	dismissed map[string]dismissedLead, restarts map[string][]wire.Date, at wire.Date) autopilotEpic {
	e := autopilotEpic{row: r}
	prev := d.autopilotLastLead(r.Item.Node)
	lead := d.leadOf(r.Item.Node)
	e.led, e.lead = lead != nil, lead
	e.over = autopilotOverBudget(ledger, r.Item.Node)
	if prev != nil {
		e.prev = prev.ID
		e.capped = len(restarts[r.Item.Node.Ref]) >= autopilotMostRestarts
		if e.led && !e.capped && e.over == "" && autopilotStalled(*prev, at) {
			if err := d.archiveBy(prev.ID, wire.AgentArchivedReasonByAgent); err != nil {
				log.Printf("autopilot: %s: lead %s not archived: %v", folder, prev.ID, err)
			} else {
				e.led = false
			}
		}
	}
	x, ok := dismissed[r.Item.Node.Ref]
	p := progressOf(r.Item)
	e.dismissed = ok && x.Done == p.Done && x.Total == p.Total
	return e
}

// autopilotLeadRequest is Start lead's request for an epic: Auto runtime, the lead prompt, the
// labels #<n> and lead, no worktree. The session's priority follows its issue's (#5846). A lead
// that replaces one (before is its id) is told to read the last summary of the one before.
func autopilotLeadRequest(folder string, epic wire.ForgeItem, before wire.UUID) wire.StartRequest {
	s := leadStartOf(epic)
	if before != "" {
		s.Prompt += "\n\nA lead of this epic stopped before you: session " + string(before) +
			". Read its last summary with read_session first, then carry on its plan."
	}
	return helperRequest(autoRuntimeID, folder, s.Prompt, "", nil, nil, s.Labels)
}

// autopilotOpenPRs is the folder's open pull requests and its limit: the folder's most sessions
// not archived (helpers.mostNotArchived), which the open PRs of its lanes follow. -1 for a count
// that could not be read, which holds nothing back: the helper limits still hold.
func (d *Daemon) autopilotOpenPRs(ctx context.Context, folder string) (open, limit int) {
	_, limit64, _ := d.helperLimitsIn(folder)
	limit = int(limit64)
	list, err := d.forgeList(ctx, true, wire.ForgeListRequest{Folder: &folder, State: new("open"), Limit: new(int64(forgeListMost))})
	if err != nil {
		return -1, limit
	}
	for _, i := range list.Items {
		if i.Draft == nil || !*i.Draft {
			open++
		}
	}
	return open, limit
}

// autopilotEpic is an open epic of forge/next with what the plan needs of it.
type autopilotEpic struct {
	row       wire.ForgeNextRow
	led       bool
	dismissed bool
	lead      *wire.ForgeLead
	// over is why the epic is paused for its spend, "" when it is not (#5902).
	over   string
	prev   wire.UUID // the newest lead the epic had, in any state ("" for none)
	capped bool      // it has been restarted autopilotMostRestarts times in a day
}

// autopilotRoom is what the folder and the machine allow.
type autopilotRoom struct {
	limit    bool // the helper limits have no place for a lane
	openPRs  int
	prLimit  int
	capacity wire.CapacityLevel
	mainRed  bool // main has been red for too long (#5902)
}

// wait is why the room has no place for a start: the helper limits, the open-PR limit or the
// machine's capacity; "" when it has one.
func (r autopilotRoom) wait() string {
	switch {
	case r.limit:
		return autopilotLimitRunning
	case r.prLimit > 0 && r.openPRs >= r.prLimit:
		return autopilotLimitPRs
	case r.capacity == wire.CapacityLevelNear:
		return "capacity near"
	case r.capacity == wire.CapacityLevelFull:
		return "capacity full"
	}
	return ""
}

// autopilotOutcome is what a run does for one epic: start it, or wait with a reason.
type autopilotOutcome struct {
	epic   wire.ForgeItem
	start  bool
	paused bool
	why    string
	prev   wire.UUID
	// over: the epic is over budget (#5902); lead is its lead, which is told.
	over bool
	lead *wire.ForgeLead
}

func isHigh(i wire.ForgeItem) bool {
	return strings.EqualFold(strings.TrimSpace(deref(i.Priority)), "high")
}

// autopilotCanStart is whether some epic could start before the limits are looked at.
func autopilotCanStart(epics []autopilotEpic, mode wire.AutopilotMode) bool {
	for _, e := range epics {
		if !e.led && !e.dismissed && e.over == "" && !e.row.OnHold && (mode != wire.AutopilotModeHigh || isHigh(e.row.Item)) {
			return true
		}
	}
	return false
}

// autopilotLeadPlan is one run's outcome for each epic with no lead that is not dismissed, in
// the order of forge/next: the first that may start does, when the room allows; every other
// waits with its reason. An epic with a lead has no row.
func autopilotLeadPlan(epics []autopilotEpic, mode wire.AutopilotMode, room autopilotRoom) []autopilotOutcome {
	var out []autopilotOutcome
	started := false
	for _, e := range epics {
		if e.over != "" {
			out = append(out, autopilotOutcome{epic: e.row.Item, over: true, why: e.over, lead: e.lead})
			continue
		}
		if e.led || e.dismissed {
			continue
		}
		o := autopilotOutcome{epic: e.row.Item, prev: e.prev}
		switch {
		case room.mainRed:
			o.why = "main red"
		case e.prev != "" && e.capped && !e.row.OnHold:
			o.why, o.paused = autopilotLeadStopping, true
		case e.row.OnHold:
			o.why = autopilotOnHold
		case mode == wire.AutopilotModeHigh && !isHigh(e.row.Item):
			o.why = autopilotModeHigh
		case started:
			o.why = "one lead a run"
		case room.wait() != "":
			o.why = room.wait()
		default:
			o.start, started = true, true
		}
		out = append(out, o)
	}
	return out
}

// autopilotLastLead is the newest lead session the epic had, archived or not.
func (d *Daemon) autopilotLastLead(epic wire.LineageNode) *wire.Agent {
	var best *wire.Agent
	seen := map[string]bool{}
	for _, l := range d.lineage().Links(epic).Links {
		if l.To != epic || l.From.Kind != lineage.KindSession || l.Rel != lineage.RelWorksOn || seen[l.From.Ref] {
			continue
		}
		seen[l.From.Ref] = true
		id, ok := lineage.SessionID(l.From.Ref)
		if !ok {
			continue
		}
		a := d.Store.Agent(wire.UUID(id))
		if a == nil || !slices.ContainsFunc(a.Labels, func(s wire.SessionLabel) bool { return s.Value == "lead" }) {
			continue
		}
		if best == nil || a.CreatedAt > best.CreatedAt {
			best = a
		}
	}
	return best
}

// autopilotStalled is whether a lead that is not archived has stopped or ended for
// autopilotStalledFor. One a person stopped (EndedReasonCancelled) is not: the person meant it.
func autopilotStalled(a wire.Agent, at wire.Date) bool {
	switch a.State {
	case wire.AgentStateStopped:
		if a.EndedReason != nil && *a.EndedReason == wire.EndedReasonCancelled {
			return false
		}
	case wire.AgentStateFinished:
	default:
		return false
	}
	return float64(at-a.LastActivityAt) >= autopilotStalledFor.Seconds()
}

// autopilotRestarts is, for each epic of the folder, when Autopilot started a lead again within
// the last autopilotRestartDay.
func (d *Daemon) autopilotRestarts(folder string, at wire.Date) map[string][]wire.Date {
	out := map[string][]wire.Date{}
	m, err := d.autopilotFile.Read()
	if err != nil {
		return out
	}
	for ref, ts := range m[folder].Restarts {
		for _, t := range ts {
			if float64(at-t) < autopilotRestartDay.Seconds() {
				out[ref] = append(out[ref], t)
			}
		}
	}
	return out
}

// autopilotRestarted keeps one restart of an epic's lead, and drops those older than a day.
func (d *Daemon) autopilotRestarted(folder, ref string, at wire.Date) {
	err := d.autopilotFile.Update(func(m map[string]autopilotRecord) error {
		e := m[folder]
		kept := map[string][]wire.Date{}
		for r, ts := range e.Restarts {
			for _, t := range ts {
				if float64(at-t) < autopilotRestartDay.Seconds() {
					kept[r] = append(kept[r], t)
				}
			}
		}
		kept[ref] = append(kept[ref], at)
		e.Restarts = kept
		if e.Updated == 0 {
			e.Updated = at
		}
		m[folder] = e
		return nil
	})
	if err != nil {
		log.Printf("autopilot: %s: restart not kept: %v", folder, err)
	}
}
