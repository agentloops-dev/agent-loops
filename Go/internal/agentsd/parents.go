package agentsd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Session parents (#7538, specs/7535-session-parents/spec.md): a session works under a live
// lead on this host. The parent is one field on the child's record; the store indexes the
// children. A change is told to the child and to its old and new leads on their next turns,
// by notices kept on each recipient's record (an outbox), taken in the same write as the
// turn's words, and cleared when the runtime answers.

const (
	// parentLevelsMost is the most levels a tree of parents has, the top one included.
	parentLevelsMost = 6
	// parentChildrenMost is the most live children a lead has.
	parentChildrenMost = 100
	// parentNoticesMost is the most notices pending on a record; past it, "and N more".
	parentNoticesMost = 32
	// leadLabel is the label a parent has.
	leadLabel = "lead"
	// noticeTitleMost and noticeTextMost bound a notice's words.
	noticeTitleMost = 80
	noticeTextMost  = 2048
	noticeLinesMost = 10
)

func (d *Daemon) registerParents() {
	On(d, wire.CallAgentsSetParent, func(ctx context.Context, c *Client, r wire.SetParentRequest) (wire.Agent, error) {
		d.actedOn(r.AgentID, c.actor())
		return d.setParent(r, wire.SessionParentSetByPerson)
	})
}

func parentRefused(code int, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func hasSessionLabel(a *wire.Agent, value string) bool {
	for _, l := range a.Labels {
		if strings.EqualFold(l.Value, value) {
			return true
		}
	}
	return false
}

// leadOf is the lead a's parent names, when it names one: not the starter.
func leadOf(a *wire.Agent) (wire.UUID, bool) {
	if a.Parent == nil || a.Parent.ID == nil {
		return "", false
	}
	return upperID(*a.Parent.ID), true
}

func upperID(id wire.UUID) wire.UUID { return wire.UUID(strings.ToUpper(string(id))) }

// checkParent is the parent p that child may go under, or why not. child is "" for a session
// not made yet. With parentMu held.
func (d *Daemon) checkParent(child, parentID wire.UUID) (*wire.Agent, error) {
	parentID = upperID(parentID)
	p := d.Store.Agent(parentID)
	if p == nil || d.refuseToRun(p) != nil {
		return nil, parentRefused(wire.FailureParentNotFound, "There is no session %s on this machine.", parentID)
	}
	if p.State == wire.AgentStateArchived {
		return nil, parentRefused(wire.FailureParentNotFound, "%s is archived. Put the session under a live lead.", agentName(p.Title))
	}
	if !hasSessionLabel(p, leadLabel) {
		return nil, parentRefused(wire.FailureParentNotLead, "%s is not a lead. Label it lead first.", agentName(p.Title))
	}
	if child != "" && p.ID == child {
		return nil, parentRefused(wire.FailureParentCycle, "A session cannot work under itself.")
	}
	// Walk p's effective parents: meeting child is a cycle (rename(2)'s EINVAL).
	levels, seen := 1, map[wire.UUID]bool{p.ID: true}
	for cur := p.ID; ; {
		next, ok := d.Store.ParentOf(cur)
		if !ok || seen[next] {
			break
		}
		if child != "" && next == child {
			return nil, parentRefused(wire.FailureParentCycle, "%s works under this session already, so this session cannot work under it.", agentName(p.Title))
		}
		seen[next] = true
		levels++
		if levels > parentLevelsMost {
			break
		}
		cur = next
	}
	height := 0
	if child != "" {
		height = d.Store.SubtreeHeight(child, parentLevelsMost)
	}
	if levels+1+height > parentLevelsMost {
		return nil, parentRefused(wire.FailureParentTooDeep, "That would make the tree deeper than %d levels.", parentLevelsMost)
	}
	n := d.Store.LiveChildren(p.ID)
	if child != "" {
		if at, ok := d.Store.ParentOf(child); ok && at == p.ID {
			if c := d.Store.Agent(child); c != nil && c.State != wire.AgentStateArchived {
				n--
			}
		}
	}
	if n >= parentChildrenMost {
		return nil, parentRefused(wire.FailureParentFull, "%s has %d live sessions under it, the most it can have.", agentName(p.Title), parentChildrenMost)
	}
	return p, nil
}

// setParent is agents/setParent.
func (d *Daemon) setParent(r wire.SetParentRequest, by wire.SessionParentSetBy) (wire.Agent, error) {
	d.parentMu.Lock()
	defer d.parentMu.Unlock()
	c := d.Store.Agent(r.AgentID)
	if c == nil || d.refuseToRun(c) != nil {
		return wire.Agent{}, noSuchAgent()
	}
	if r.ExpectedRevision != nil && (c.Revision == nil || *c.Revision != *r.ExpectedRevision) {
		return wire.Agent{}, &Error{Code: wire.FailureChangedElsewhere, Message: "This session changed since it was read. Read it again."}
	}
	t := now()
	var next *wire.SessionParent
	switch r.Placement {
	case wire.SessionPlacementUnder:
		if r.ParentID == nil {
			return wire.Agent{}, Invalid("placement under needs parentID")
		}
		p, err := d.checkParent(c.ID, *r.ParentID)
		if err != nil {
			return wire.Agent{}, err
		}
		id := p.ID
		next = &wire.SessionParent{ID: &id, SetAt: t, SetBy: by}
	case wire.SessionPlacementTop:
		next = &wire.SessionParent{SetAt: t, SetBy: by}
	case wire.SessionPlacementStarter:
	default:
		return wire.Agent{}, Invalid("placement is under, top or starter")
	}
	if samePlacement(c.Parent, next) {
		return *c, nil
	}
	return d.moveUnder(c.ID, next, t)
}

// samePlacement is whether a parent change would change nothing.
func samePlacement(was, next *wire.SessionParent) bool {
	if (was == nil) != (next == nil) {
		return false
	}
	if was == nil {
		return true
	}
	if (was.ID == nil) != (next.ID == nil) {
		return false
	}
	return was.ID == nil || upperID(*was.ID) == upperID(*next.ID)
}

// moveUnder writes child's new parent with its own notice, in one record write, then the
// notices of its old and new leads. With parentMu held.
func (d *Daemon) moveUnder(child wire.UUID, next *wire.SessionParent, t wire.Date) (wire.Agent, error) {
	var oldLead, newLead wire.UUID
	var hadOld, hasNew bool
	a := d.changeAgent(child, false, func(a *wire.Agent) {
		oldLead, hadOld = leadOf(a)
		a.Parent = next
		newLead, hasNew = leadOf(a)
		if oldLead != newLead || hadOld != hasNew {
			addChildNotice(a, oldLead, hadOld, newLead, hasNew, t)
		}
	})
	if a == nil {
		return wire.Agent{}, &Error{Code: wire.FailureCouldNotSave, Message: "The session's parent could not be saved."}
	}
	if oldLead == newLead && hadOld == hasNew {
		return *a, nil
	}
	if hadOld {
		d.changeAgent(oldLead, false, func(l *wire.Agent) { addLeadNotice(l, child, wire.ParentNoticeChangeLeft, t) })
		d.record(oldLead, runtimeNote(agentName(a.Title)+" no longer works under this session."))
	}
	if hasNew {
		d.changeAgent(newLead, false, func(l *wire.Agent) { addLeadNotice(l, child, wire.ParentNoticeChangeJoined, t) })
		d.record(newLead, runtimeNote(agentName(a.Title)+" now works under this session."))
	}
	if hasNew {
		lead := d.Store.Agent(newLead)
		title := (*string)(nil)
		if lead != nil {
			title = lead.Title
		}
		d.record(child, runtimeNote("Now under "+agentName(title)+"."))
	} else if hadOld {
		d.record(child, runtimeNote("No longer under a lead."))
	}
	return *a, nil
}

// startUnder checks and sets a new session's parent (StartRequest.parentID) on its record,
// not saved yet. The caller holds parentMu until the record is saved, then calls
// startedUnder.
func (d *Daemon) startUnder(a *wire.Agent, parentID wire.UUID, t wire.Date) error {
	p, err := d.checkParent("", parentID)
	if err != nil {
		return err
	}
	id := p.ID
	a.Parent = &wire.SessionParent{ID: &id, SetAt: t, SetBy: wire.SessionParentSetByPerson}
	return nil
}

// startedUnder tells a new session's lead that it works under it. The session itself learns
// it from its brief.
func (d *Daemon) startedUnder(a wire.Agent, t wire.Date) {
	lead, ok := leadOf(&a)
	if !ok {
		return
	}
	d.changeAgent(lead, false, func(l *wire.Agent) { addLeadNotice(l, a.ID, wire.ParentNoticeChangeJoined, t) })
	d.record(lead, runtimeNote(agentName(a.Title)+" now works under this session."))
}

// clearParentsOf takes the parent off each session under a deleted lead.
func (d *Daemon) clearParentsOf(lead wire.UUID, children []wire.UUID) {
	d.parentMu.Lock()
	defer d.parentMu.Unlock()
	for _, c := range children {
		d.changeAgent(c, false, func(a *wire.Agent) {
			if id, ok := leadOf(a); ok && id == upperID(lead) {
				a.Parent = nil
				addChildNotice(a, id, true, "", false, now())
			}
		})
	}
}

func noticesOf(a *wire.Agent) *wire.ParentNotices {
	if a.ParentNotices == nil {
		a.ParentNotices = &wire.ParentNotices{}
	}
	return a.ParentNotices
}

// addChildNotice keeps the child's one notice of where it works now: a run of changes not yet
// told coalesces into one, from the lead it had before the run to the one it has now, and a
// run that ends where it began tells nothing.
func addChildNotice(a *wire.Agent, oldLead wire.UUID, hadOld bool, newLead wire.UUID, hasNew bool, t wire.Date) {
	n := noticesOf(a)
	was, hadWas := oldLead, hadOld
	kept := n.Pending[:0:0]
	for _, p := range n.Pending {
		if p.Change == wire.ParentNoticeChangeParent || p.Change == wire.ParentNoticeChangeNoParent {
			if p.Was != nil {
				was, hadWas = *p.Was, true
			} else {
				was, hadWas = "", false
			}
			continue
		}
		kept = append(kept, p)
	}
	n.Pending = kept
	if hadWas == hasNew && (!hasNew || was == newLead) {
		tidyNotices(a)
		return
	}
	notice := wire.ParentNotice{ID: store.NewUUID(), At: t}
	if hadWas {
		w := was
		notice.Was = &w
	}
	if hasNew {
		notice.Change, notice.About = wire.ParentNoticeChangeParent, newLead
	} else {
		notice.Change, notice.About = wire.ParentNoticeChangeNoParent, was
	}
	pushNotice(n, notice)
}

// addLeadNotice keeps a lead's notice about one child: the latest change wins, and a joined
// and a left of the same child not yet told cancel each other.
func addLeadNotice(a *wire.Agent, child wire.UUID, change wire.ParentNoticeChange, t wire.Date) {
	n := noticesOf(a)
	for i, p := range n.Pending {
		if p.About != child || (p.Change != wire.ParentNoticeChangeJoined && p.Change != wire.ParentNoticeChangeLeft) {
			continue
		}
		n.Pending = append(n.Pending[:i:i], n.Pending[i+1:]...)
		if p.Change != change {
			tidyNotices(a)
			return
		}
		break
	}
	pushNotice(n, wire.ParentNotice{ID: store.NewUUID(), About: child, Change: change, At: t})
}

// pushNotice adds one, dropping the oldest past parentNoticesMost as "and N more".
func pushNotice(n *wire.ParentNotices, p wire.ParentNotice) {
	n.Pending = append(n.Pending, p)
	if over := len(n.Pending) - parentNoticesMost; over > 0 {
		n.Pending = append([]wire.ParentNotice{}, n.Pending[over:]...)
		more := int64(over)
		if n.More != nil {
			more += *n.More
		}
		n.More = &more
	}
}

// tidyNotices drops an empty outbox from the record.
func tidyNotices(a *wire.Agent) {
	n := a.ParentNotices
	if n != nil && len(n.Pending) == 0 && len(n.Sent) == 0 && n.SentWith == nil && (n.More == nil || *n.More == 0) {
		a.ParentNotices = nil
	}
}

// takeNotices moves what is pending, with what an earlier turn took and no runtime answered,
// to sent, with the turn's entry: called in the record write that takes the turn's words. It
// is the notices the turn tells, and the count of those dropped.
func takeNotices(a *wire.Agent, turnEntry wire.UUID) ([]wire.ParentNotice, int64) {
	n := a.ParentNotices
	if n == nil || (len(n.Pending) == 0 && len(n.Sent) == 0) {
		return nil, 0
	}
	all := append(append([]wire.ParentNotice{}, n.Sent...), n.Pending...)
	var more int64
	if n.More != nil {
		more = *n.More
	}
	e := turnEntry
	// The count of those dropped is told with this turn, and forgotten.
	n.Sent, n.Pending, n.More, n.SentWith = all, nil, nil, &e
	return all, more
}

// clearSentNotices is the runtime's answer to the turn that took the notices.
func (d *Daemon) clearSentNotices(id, turnEntry wire.UUID) {
	a := d.Store.Agent(id)
	if a == nil || a.ParentNotices == nil || a.ParentNotices.SentWith == nil || *a.ParentNotices.SentWith != turnEntry {
		return
	}
	d.changeAgent(id, false, func(a *wire.Agent) {
		n := a.ParentNotices
		if n == nil || n.SentWith == nil || *n.SentWith != turnEntry {
			return
		}
		n.Sent, n.SentWith = nil, nil
		tidyNotices(a)
	})
}

// recoverNotices is the restart's part (#7538): a record with notices sent had a turn the
// host cut off. When a runtime entry follows the turn's prompt, the runtime had them and
// they are cleared; else they go with the next turn, said again.
func (d *Daemon) recoverNotices() {
	for _, a := range d.Store.Agents() {
		if a.ParentNotices == nil || a.ParentNotices.SentWith == nil {
			continue
		}
		turn := *a.ParentNotices.SentWith
		entries, _, _ := d.Store.Transcript(a.ID, 0)
		found, answered := false, false
		for _, e := range entries {
			if e.ID == turn {
				found = true
				continue
			}
			if found && runtimeEntry(e.Kind) {
				answered = true
				break
			}
		}
		d.changeAgent(a.ID, false, func(a *wire.Agent) {
			n := a.ParentNotices
			if n == nil || n.SentWith == nil || *n.SentWith != turn {
				return
			}
			if answered {
				n.Sent = nil
			} else if found {
				yes := true
				for i := range n.Sent {
					n.Sent[i].Again = &yes
				}
			}
			n.SentWith = nil
			tidyNotices(a)
		})
	}
}

// runtimeEntry is whether an entry is the runtime's own answer to a prompt.
func runtimeEntry(k wire.TranscriptEntryKind) bool {
	return k.AgentMessage != nil || k.AgentThought != nil || k.ToolCall != nil || k.ToolCallUpdate != nil ||
		k.PlanUpdated != nil || k.PermissionAsked != nil || k.ElicitationAsked != nil || k.UsageRecorded != nil
}

// noticeText is the words of the notices a turn tells, made from the titles now.
func (d *Daemon) noticeText(to wire.UUID, notices []wire.ParentNotice, more int64) string {
	var lines []string
	for _, n := range notices {
		if line := d.noticeLine(to, n); line != "" {
			if n.Again != nil && *n.Again {
				line += " (Said again after a restart.)"
			}
			lines = append(lines, line)
		}
	}
	if len(lines) > noticeLinesMost {
		more += int64(len(lines) - noticeLinesMost)
		lines = lines[len(lines)-noticeLinesMost:]
	}
	if len(lines) == 0 {
		return ""
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("And %d more changes of who works under whom.", more))
	}
	text := strings.Join(lines, "\n")
	if len(text) > noticeTextMost {
		text = strings.ToValidUTF8(text[:noticeTextMost-len("…")], "") + "…"
	}
	return text
}

func (d *Daemon) titleOf(id wire.UUID) string {
	a := d.Store.Agent(id)
	if a == nil {
		return "a session that is gone"
	}
	t := a.Title
	if t != nil {
		if r := []rune(strings.TrimSpace(*t)); len(r) > noticeTitleMost {
			s := string(r[:noticeTitleMost]) + "…"
			t = &s
		}
	}
	return agentName(t)
}

func (d *Daemon) noticeLine(to wire.UUID, n wire.ParentNotice) string {
	switch n.Change {
	case wire.ParentNoticeChangeParent:
		line := fmt.Sprintf("You now work under %s (session %s), a lead. Report to it with message_session; it can read you with read_session.", d.titleOf(n.About), n.About)
		if me := d.Store.Agent(to); me != nil && me.StartedByAgent != nil {
			line += " You were started by " + d.titleOf(*me.StartedByAgent) + "; that does not change."
		}
		return line
	case wire.ParentNoticeChangeNoParent:
		return "You no longer work under " + d.titleOf(n.About) + ". You have no lead now."
	case wire.ParentNoticeChangeJoined:
		c := d.Store.Agent(n.About)
		if c == nil {
			return ""
		}
		where := ""
		if f := folderPath(c); f != "" {
			where = ", in " + filepath.Base(f)
		}
		var labels []string
		for _, l := range c.Labels {
			labels = append(labels, l.Value)
		}
		if len(labels) > 0 {
			where += ", labelled " + strings.Join(labels, ", ")
		}
		return fmt.Sprintf("%s (session %s%s) now works under you. Read it with read_session, and message it with message_session.", d.titleOf(n.About), n.About, where)
	case wire.ParentNoticeChangeLeft:
		line := d.titleOf(n.About) + " no longer works under you"
		if c := d.Store.Agent(n.About); c != nil {
			if lead, ok := leadOf(c); ok && lead != upperID(to) {
				line += ", now under " + d.titleOf(lead)
			}
		}
		return line + "."
	}
	return ""
}
