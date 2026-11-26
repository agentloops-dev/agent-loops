package agentsd

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Autopilot's loop for lanes (#5900, epic #5897). After the leads, a folder's run reads forge/next
// and starts a Software Engineer lane for the top ready issue that no epic and no standing epic
// takes: no parent, and no standing epic's query matches it (the standing epics' leads start
// those, #3659; two starters of one issue would race). It starts in the same room as a lead:
// the folder's helper limits, its open-PR limit and the machine's capacity; at most one lane a
// run; a ready child of an open epic with no lead gets a lane too when the epic needs no lead (#5944); in mode high only an issue of priority high. A linux-ok issue goes to a Linux machine
// that is online, else the project's machine that Auto picks when it has several (#5876), else
// this one; a start that fails there runs here.

// autopilotLaneLabel is the label a lane has besides its issue's number.
const autopilotLaneLabel = "lane"

// autopilotLanePersona is the persona of a lane (.agents/personas/software-engineer.md).
const autopilotLanePersona = "software-engineer"

// autopilotLanes is a run's part for lanes in one folder.
func (d *Daemon) autopilotLanes(ctx context.Context, folder string, mode wire.AutopilotMode) {
	next, err := d.forgeNext(ctx, wire.ForgeNextRequest{Folder: folder})
	if err != nil {
		log.Printf("autopilot: %s: %v", folder, err)
		return
	}
	var cands []wire.ForgeNextRow
	for _, r := range next.Rows {
		if !r.OnHold && (mode != wire.AutopilotModeHigh || isHigh(r.Item)) {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return
	}
	standing := d.autopilotStanding(ctx, folder)
	var top *wire.ForgeNextRow
	why := ""
	for _, r := range cands {
		if r.Item.Parent == nil {
			if !slicesAny(standing, func(q wire.ForgeStanding) bool { return standingMatches(q, r.Item) }) {
				top = &r
				break
			}
			continue
		}
		if epic, ok := d.autopilotStray(ctx, r.Item); ok {
			top, why = &r, "child of "+epic+", which has no lead"
			break
		}
	}
	if top == nil {
		return
	}
	room := autopilotRoom{limit: d.helperLimitRefusal(folder) != "", capacity: d.capacityLevel()}
	room.openPRs, room.prLimit = d.autopilotOpenPRs(ctx, folder)
	row := wire.AutopilotRow{Ref: top.Item.Node.Ref, Kind: wire.AutopilotRowKindLane, State: wire.AutopilotRowStateWaiting, Reason: room.wait()}
	if row.Reason != "" && why != "" {
		row.Reason += "; " + why
	}
	if row.Reason == "" {
		id, err := d.autopilotStartLane(ctx, folder, top.Item)
		if err != nil {
			_, row.Reason = errMessage(err)
			row.Reason = "start failed: " + row.Reason
		} else {
			row.State, row.Reason, row.Session = wire.AutopilotRowStateStarted, why, id
		}
	}
	if err := d.autopilotNote(folder, row); err != nil {
		log.Printf("autopilot: %s: %v", folder, err)
	}
}

// autopilotStray says whether a ready child of an open epic gets a lane with no lead (#5944): the
// epic has no lead and does not need one, because the child is its only ready child and no other
// open child waits for it. It returns the epic's ref.
func (d *Daemon) autopilotStray(ctx context.Context, child wire.ForgeItem) (string, bool) {
	if child.Parent == nil || d.leadOf(*child.Parent) != nil {
		return "", false
	}
	epic, children, standing, err := d.forgePlanRead(ctx, "autopilot", *child.Parent)
	if err != nil || standing != nil || epic.State != "open" || !hasLabel(epic, "epic") {
		return "", false
	}
	if !strayOf(children, child.Node) {
		return "", false
	}
	return epic.Node.Ref, true
}

// strayOf is true when child is the epic's only ready open child and blocks no other open child.
func strayOf(children []planChild, child wire.LineageNode) bool {
	ready, found := 0, false
	for _, c := range children {
		if c.item.State != "open" {
			continue
		}
		if c.item.Node == child {
			found = true
		}
		blocked := false
		for _, b := range c.blockedBy {
			if b.node == child && b.open {
				return false
			}
			blocked = blocked || b.open
		}
		if !blocked {
			ready++
		}
	}
	return found && ready == 1
}

func slicesAny[T any](s []T, f func(T) bool) bool {
	for _, x := range s {
		if f(x) {
			return true
		}
	}
	return false
}

// autopilotStanding is the queries of the folder's open standing epics. One whose body does not
// read takes nothing.
func (d *Daemon) autopilotStanding(ctx context.Context, folder string) []wire.ForgeStanding {
	page, err := d.forgeList(ctx, false, wire.ForgeListRequest{Folder: &folder, State: new("open"), Label: new(standingLabel), Limit: new(int64(forgeListMost))})
	if err != nil {
		return nil
	}
	var out []wire.ForgeStanding
	for _, e := range page.Items {
		if !hasLabel(e, "epic") {
			continue
		}
		detail, err := d.forgeItem(ctx, e.Node.Kind, wire.ForgeItemRequest{Node: e.Node})
		if err != nil {
			continue
		}
		if q := standingOf(detail.Body); q.Error == nil {
			out = append(out, q)
		}
	}
	return out
}

// autopilotLaneRequest is a lane's request: Auto, the issue's own prompt (the forge's Start),
// the labels #<n> and lane, the Software Engineer persona, no worktree (the lane moves into one).
func autopilotLaneRequest(folder string, issue wire.ForgeItem, start *wire.ForgeStart) wire.StartRequest {
	prompt, labels := issue.Title, []string{autopilotLaneLabel}
	if _, n, ok := nodeOfRepo(issue.Node); ok {
		prompt = "#" + strconv.Itoa(n) + ": " + issue.Title
		labels = []string{"#" + strconv.Itoa(n), autopilotLaneLabel}
	}
	if start != nil && strings.TrimSpace(start.Prompt) != "" {
		prompt = start.Prompt
	}
	r := helperRequest(autoRuntimeID, folder, prompt, "", nil, nil, labels)
	r.Persona = new(autopilotLanePersona)
	return r
}

// autopilotStartLane starts the lane on the machine its issue calls for, else here.
func (d *Daemon) autopilotStartLane(ctx context.Context, folder string, issue wire.ForgeItem) (*wire.UUID, error) {
	var start *wire.ForgeStart
	if detail, err := d.forgeItem(ctx, issue.Node.Kind, wire.ForgeItemRequest{Node: issue.Node}); err == nil {
		start = detail.Start
	}
	r := autopilotLaneRequest(folder, issue, start)
	if machine, there, ok := d.autopilotMachineFor(folder, hasLabel(issue, "linux-ok"), r.RuntimeID); ok {
		moved := r
		moved.Cwd = store.PathURL(there)
		out, err := d.askWorkspaces("", sessionOnMachine, wire.MachineSessionStart{Host: machine, Start: moved})
		var id wire.UUID
		if err == nil && json.Unmarshal(out, &id) == nil && id != "" {
			return &id, nil
		}
		if err != nil {
			log.Printf("autopilot: %s: start on %s: %v", folder, machine, err)
		}
	}
	id, err := d.startFor(r, startedBy{owner: wire.SessionLabelOwnerAgent})
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// autopilotMachineFor is the other machine a lane goes to, and the folder there: for a linux-ok
// issue, an online Linux machine with the project's one folder and room; else, when the project
// is on more than one machine, the one Auto picks, unless it is this one. ok is false for this machine.
func (d *Daemon) autopilotMachineFor(folder string, linux bool, runtime string) (machine, there string, ok bool) {
	project := projectOfFolder(folder)
	if project == "" || d.askControl.Load() == nil {
		return "", "", false
	}
	out, err := d.askWorkspaces("", projectFoldersMethod, wire.ProjectFoldersRequest{Project: project})
	if err != nil {
		return "", "", false
	}
	var listed wire.ProjectFolders
	if json.Unmarshal(out, &listed) != nil {
		return "", "", false
	}
	folders := map[string]string{}
	for _, m := range listed.Machines {
		if len(m.Folders) == 1 {
			folders[strings.ToLower(m.MachineName)] = m.Folders[0]
		}
	}
	own := strings.ToLower(d.ownName())
	pick := ""
	if linux {
		if machines, err := d.agentWorkspaces(""); err == nil {
			for _, h := range machines.Hosts {
				name := strings.ToLower(h.Name)
				if name != own && h.State == "online" && folders[name] != "" && h.Platform != nil &&
					strings.Contains(strings.ToLower(*h.Platform), "linux") &&
					(h.Capacity == nil || *h.Capacity == wire.CapacityLevelOk) {
					pick = h.Name
					break
				}
			}
		}
	}
	if pick == "" && len(listed.Machines) > 1 {
		if name, _, err := d.autoMachineIn(project, "", runtime); err == nil {
			pick = name
		}
	}
	if pick == "" || strings.ToLower(pick) == own || folders[strings.ToLower(pick)] == "" {
		return "", "", false
	}
	return pick, folders[strings.ToLower(pick)], true
}
