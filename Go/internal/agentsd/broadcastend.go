package agentsd

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The end of a broadcast (#6955, epic #6951): until:, an event with the grammar of on:; for:,
// at most 7 days; match:, the details a start and an end share; End now (#6956), turning the
// workflow off and archiving it. Nothing is queued, so an unread broadcast that ends is simply
// not given; a session that read it gets the end message once, at its next turn. After a
// restart the disk's state says whether a disk_low broadcast with until: is still active.

const broadcastForMost = 7 * 24 * time.Hour

// Who or what ended a broadcast: endedBy in broadcasts.json.
const (
	endedByEvent    = "event"
	endedByTime     = "time"
	endedByOff      = "off"
	endedByArchived = "archived"
	endedByState    = "state"
)

// broadcastForOf is how long a broadcast of w lasts at most: for:, else 24 h.
func broadcastForOf(w wire.Workflow) time.Duration {
	if w.For == nil || *w.For <= 0 {
		return broadcastFor
	}
	return min(time.Duration(*w.For*float64(time.Second)), broadcastForMost)
}

// broadcastMatchKey is the match: details of an event, such as "volume=/"; "" with no match:
// or no event. A server's event is matched by the server's own details, not the envelope.
func broadcastMatchKey(match []string, e *wire.Event) string {
	if e == nil || len(match) == 0 {
		return ""
	}
	parts := make([]string, 0, len(match))
	for _, k := range match {
		parts = append(parts, k+"="+e.Details[k])
	}
	return strings.Join(parts, ",")
}

// broadcastLevelRank is how bad an event's level is; 0 for one it does not know, which never replaces.
var broadcastLevelRank = map[string]int{"ok": 0, "info": 1, "low": 2, "near": 2, "warning": 2, "high": 3, "critical": 3, "full": 3}

// worseLevel is whether the level now is worse than the level was.
func worseLevel(was *string, now string) bool {
	r := broadcastLevelRank[now]
	if was == nil {
		return r > 0
	}
	return r > broadcastLevelRank[*was]
}

// broadcastEndText is what a session that read it is told once it ends.
func broadcastEndText(w wire.Workflow) string {
	text := "(From the app: the workflow \"" + w.Name + "\" has ended; what it sent no longer holds.)"
	if w.EndMessage != nil {
		text += "\n\n" + *w.EndMessage
	}
	return text
}

// endDue is whether a session is to be told that b ended: b is over, it read b, and it was not
// told yet.
func (b *broadcast) endDue(id wire.UUID, at time.Time) bool {
	if b.active(at) {
		return false
	}
	for _, r := range b.Reads {
		if r.SessionID == id {
			return r.EndToldAt == nil
		}
	}
	return false
}

// endBroadcastsWhere ends each active broadcast for which by gives a reason, and says each on
// the log. It is the broadcasts ended.
func (d *Daemon) endBroadcastsWhere(by func(b *broadcast) string) []broadcast {
	if d.broadcastFile == nil {
		return nil
	}
	at := nowFunc()
	var ended []broadcast
	d.broadcastMu.Lock()
	due := false
	for _, b := range d.broadcastsLocked() {
		if b.EndedAt == nil && by(&b) != "" {
			due = true
			break
		}
	}
	if due {
		_ = d.updateBroadcastsLocked(func(m map[string]broadcast) error {
			for k, b := range m {
				if b.EndedAt != nil {
					continue
				}
				why := by(&b)
				if why == "" {
					continue
				}
				end := wire.DateOf(at)
				if why == endedByTime {
					end = b.EndsAt
				}
				b.EndedAt, b.EndedBy = &end, &why
				m[k] = b
				ended = append(ended, b)
			}
			return nil
		})
	}
	d.broadcastMu.Unlock()
	d.noteBroadcastsEnded(ended)
	return ended
}

// noteBroadcastsEnded raises workflow.completed, with no agent, for each, and tells clients.
func (d *Daemon) noteBroadcastsEnded(ended []broadcast) {
	if len(ended) == 0 {
		return
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].StartedAt.Time().Before(ended[j].StartedAt.Time()) })
	told := map[string]bool{}
	for _, b := range ended {
		by := ""
		if b.EndedBy != nil {
			by = *b.EndedBy
		}
		details := map[string]string{"workflow": b.WorkflowID, "broadcast": string(b.ID), "ended_by": by,
			"read": strconv.Itoa(len(b.Reads))}
		if b.MatchKey != "" {
			details["match"] = b.MatchKey
		}
		d.raise(wire.EventDraft{Name: "workflow.completed", At: now(), Scope: folderScope(b.Folder),
			Sentence: "Workflow " + b.Name + " ended its broadcast" + broadcastEndedWords(by) + ".", Details: details})
		if k := workflowKey(b.Folder, b.WorkflowID); !told[k] {
			told[k] = true
			d.tellWorkflow(b.Folder, b.WorkflowID)
		}
	}
	// An until: of a server's event is subscribed to only while a broadcast is active.
	d.kickMCPEvents()
}

func broadcastEndedWords(by string) string {
	switch by {
	case endedByEvent:
		return " at its until: event"
	case endedByTime:
		return " at the end of its for: time"
	case endedByOff:
		return ", because it was turned off"
	case endedByArchived:
		return ", because it was archived"
	case endedByState:
		return ", because after a restart what started it was over"
	}
	return ""
}

// endDueBroadcasts ends each broadcast whose for: time is up.
func (d *Daemon) endDueBroadcasts() {
	at := nowFunc()
	d.endBroadcastsWhere(func(b *broadcast) string {
		if !at.Before(b.EndsAt.Time()) {
			return endedByTime
		}
		return ""
	})
}

// scheduleBroadcastEnd ends b when its for: time is up.
func (d *Daemon) scheduleBroadcastEnd(b broadcast) {
	wait := time.Until(b.EndsAt.Time())
	if wait < 0 {
		wait = 0
	}
	time.AfterFunc(wait+time.Second, d.jobFunc("broadcast end", d.endDueBroadcasts))
}

// scheduleBroadcastEnds is the daemon's start: a timer for each active broadcast.
func (d *Daemon) scheduleBroadcastEnds() {
	if d.broadcastFile == nil {
		return
	}
	at := nowFunc()
	d.broadcastMu.Lock()
	var active []broadcast
	for _, b := range d.broadcastsLocked() {
		if b.EndedAt == nil {
			active = append(active, b)
		}
	}
	d.broadcastMu.Unlock()
	for _, b := range active {
		if b.active(at) {
			d.scheduleBroadcastEnd(b)
		}
	}
	if len(active) > 0 {
		d.goJob("broadcast end", d.endDueBroadcasts)
	}
}

// broadcastEnd is an event that one of a workflow's until: triggers matches.
type broadcastEnd struct {
	folder, id string
	match      []string
	event      wire.Event
}

// broadcastEndForEventLocked is the end that e is for w, when one of its until: triggers
// matches it. Called with workflowMu held.
func broadcastEndForEventLocked(folder, id string, w wire.Workflow, e wire.Event) (broadcastEnd, bool) {
	if !isLabelled(w) || len(w.Until) == 0 || strings.HasPrefix(e.Name, "workflow.") && e.Details["workflow"] == id {
		return broadcastEnd{}, false
	}
	for _, t := range w.Until {
		if triggerMatches(t, e) {
			return broadcastEnd{folder: folder, id: id, match: w.Match, event: e}, true
		}
	}
	return broadcastEnd{}, false
}

// endBroadcastsForEvent ends the workflow's active broadcasts that the event is about: those
// with its match: details, or each one when the start had none.
func (d *Daemon) endBroadcastsForEvent(end broadcastEnd) {
	key := broadcastMatchKey(end.match, &end.event)
	at := nowFunc()
	d.endBroadcastsWhere(func(b *broadcast) string {
		if b.Folder == end.folder && b.WorkflowID == end.id && b.active(at) && (b.MatchKey == "" || b.MatchKey == key) {
			return endedByEvent
		}
		return ""
	})
}

// endBroadcastsOfStoppedWorkflows ends the broadcasts of each workflow that is off, archived
// or gone.
func (d *Daemon) endBroadcastsOfStoppedWorkflows() {
	if d.broadcastFile == nil {
		return
	}
	d.broadcastMu.Lock()
	keys := map[string][2]string{}
	for _, b := range d.broadcastsLocked() {
		if b.EndedAt == nil {
			keys[workflowKey(b.Folder, b.WorkflowID)] = [2]string{b.Folder, b.WorkflowID}
		}
	}
	d.broadcastMu.Unlock()
	if len(keys) == 0 {
		return
	}
	stopped := map[string]string{}
	d.workflowMu.Lock()
	if d.workflowFiles == nil {
		d.scanLocked()
	}
	for k, fw := range keys {
		f := d.fileLocked(fw[0], fw[1])
		switch {
		case f == nil || !isLabelled(f.wf):
			stopped[k] = endedByOff
		case isArchived(f.wf):
			stopped[k] = endedByArchived
		case isOff(f.wf):
			stopped[k] = endedByOff
		}
	}
	d.workflowMu.Unlock()
	if len(stopped) == 0 {
		return
	}
	d.endBroadcastsWhere(func(b *broadcast) string { return stopped[workflowKey(b.Folder, b.WorkflowID)] })
}

// recheckDiskBroadcasts is the first disk look after the daemon starts: a disk_low broadcast
// with until: whose volume is no longer low ended while the daemon was away. Other events rely
// on for:.
func (d *Daemon) recheckDiskBroadcasts(alarms []wire.DiskAlarm) {
	if d.broadcastFile == nil {
		return
	}
	d.broadcastMu.Lock()
	if d.diskRechecked {
		d.broadcastMu.Unlock()
		return
	}
	d.diskRechecked = true
	d.broadcastMu.Unlock()
	low := map[string]bool{}
	for _, a := range alarms {
		low[a.Reading.Volume] = true
	}
	d.endBroadcastsWhere(func(b *broadcast) string {
		if b.HasUntil && b.StartEvent == "machine.disk_low" && (b.Volume == "" && len(low) == 0 || b.Volume != "" && !low[b.Volume]) {
			return endedByState
		}
		return ""
	})
}

// activeBroadcastLocked is whether the workflow has an active broadcast. Called with
// workflowMu held; it takes broadcastMu, in that order.
func (d *Daemon) activeBroadcastLocked(folder, id string) bool {
	if d.broadcastFile == nil {
		return false
	}
	at := nowFunc()
	d.broadcastMu.Lock()
	defer d.broadcastMu.Unlock()
	for _, b := range d.broadcastsLocked() {
		if b.Folder == folder && b.WorkflowID == id && b.active(at) {
			return true
		}
	}
	return false
}

// untilServerTriggersLocked is the server events of w's until:, while it has an active
// broadcast. Called with workflowMu held.
func (d *Daemon) untilServerTriggersLocked(folder, id string, w wire.Workflow) []mcpTrigger {
	if !isLabelled(w) || len(w.Until) == 0 {
		return nil
	}
	var ts []mcpTrigger
	for _, t := range w.Until {
		if mt, ok := serverTrigger(t); ok {
			ts = append(ts, mt)
		}
	}
	if len(ts) == 0 || !d.activeBroadcastLocked(folder, id) {
		return nil
	}
	return ts
}
