package agentsd

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
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
		d.autopilotLeads(ctx, store.FolderPath(p.Folder), mode)
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
	var epics []autopilotEpic
	for _, r := range next.Rows {
		e := autopilotEpic{row: r}
		e.led = d.leadOf(r.Item.Node) != nil
		x, ok := dismissed[r.Item.Node.Ref]
		p := progressOf(r.Item)
		e.dismissed = ok && x.Done == p.Done && x.Total == p.Total
		epics = append(epics, e)
	}
	room := autopilotRoom{limit: d.helperLimitRefusal(folder) != "", capacity: d.capacityLevel()}
	// The open-PR count is read only when an epic could start.
	if autopilotCanStart(epics, mode) {
		room.openPRs, room.prLimit = d.autopilotOpenPRs(ctx, folder)
	}
	for _, o := range autopilotLeadPlan(epics, mode, room) {
		row := wire.AutopilotRow{Ref: o.epic.Node.Ref, Kind: wire.AutopilotRowKindLead, State: wire.AutopilotRowStateWaiting, Reason: o.why}
		if o.start {
			id, err := d.startFor(autopilotLeadRequest(folder, o.epic), startedBy{owner: wire.SessionLabelOwnerAgent})
			if err != nil {
				_, row.Reason = errMessage(err)
				row.Reason = "start failed: " + row.Reason
			} else {
				row.State, row.Reason, row.Session = wire.AutopilotRowStateStarted, "", &id
			}
		}
		if err := d.autopilotNote(folder, row); err != nil {
			log.Printf("autopilot: %s: %v", folder, err)
		}
	}
}

// autopilotLeadRequest is Start lead's request for an epic: Auto runtime, the lead prompt, the
// labels #<n> and lead, no worktree. The session's priority follows its issue's (#5846).
func autopilotLeadRequest(folder string, epic wire.ForgeItem) wire.StartRequest {
	s := leadStartOf(epic)
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
}

// autopilotRoom is what the folder and the machine allow.
type autopilotRoom struct {
	limit    bool // the helper limits have no place for a lane
	openPRs  int
	prLimit  int
	capacity wire.CapacityLevel
}

// autopilotOutcome is what a run does for one epic: start it, or wait with a reason.
type autopilotOutcome struct {
	epic  wire.ForgeItem
	start bool
	why   string
}

func isHigh(i wire.ForgeItem) bool {
	return strings.EqualFold(strings.TrimSpace(deref(i.Priority)), "high")
}

// autopilotCanStart is whether some epic could start before the limits are looked at.
func autopilotCanStart(epics []autopilotEpic, mode wire.AutopilotMode) bool {
	for _, e := range epics {
		if !e.led && !e.dismissed && !e.row.OnHold && (mode != wire.AutopilotModeHigh || isHigh(e.row.Item)) {
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
		if e.led || e.dismissed {
			continue
		}
		o := autopilotOutcome{epic: e.row.Item}
		switch {
		case e.row.OnHold:
			o.why = autopilotOnHold
		case mode == wire.AutopilotModeHigh && !isHigh(e.row.Item):
			o.why = autopilotModeHigh
		case started:
			o.why = "one lead a run"
		case room.limit:
			o.why = autopilotLimitRunning
		case room.prLimit > 0 && room.openPRs >= room.prLimit:
			o.why = autopilotLimitPRs
		case room.capacity == wire.CapacityLevelNear:
			o.why = "capacity near"
		case room.capacity == wire.CapacityLevelFull:
			o.why = "capacity full"
		default:
			o.start, started = true, true
		}
		out = append(out, o)
	}
	return out
}
