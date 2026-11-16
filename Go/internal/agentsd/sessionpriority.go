package agentsd

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/lineage"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A session's priority (#5846, #5847). The daemon keeps the priority set on a session
// (Agent.prioritySet: by a person with agents/setPriority, an agent with start_session or
// set_session_priority) and works out the one it goes by (Agent.priority), with its source:
//
//   - set: the priority set on it, until it is cleared;
//   - issue: else the highest priority of the issues it is labelled with, #n and the issues a
//     P#n closes, as the forge last said it (ForgeItem.priority, #5688, kept in the lineage
//     cache);
//   - starter: else the priority of the session that started it;
//   - none: else (no priority and no source on the record).
//
// It is worked out again when the session's labels or its set priority change, when a forge
// read changes an issue's priority, when a pull request's fixes links are made, and when its
// starter's changes; agent.updated tells the clients. An issue the cache has not been told of
// yet is asked for in the background, and until it is answered the session keeps what it had,
// so a restart does not drop an issue's priority before the forge is read again.

// priorityLevels are the levels, the highest first.
var priorityLevels = []wire.SessionPriority{wire.SessionPriorityHigh, wire.SessionPriorityMedium, wire.SessionPriorityLow}

// priorityHops bounds how far one refresh follows starters down to the sessions they started.
const priorityHops = 8

// priorityOf is s as a level, or false when it is none of them.
func priorityOf(s string) (wire.SessionPriority, bool) {
	for _, p := range priorityLevels {
		if strings.EqualFold(strings.TrimSpace(s), string(p)) {
			return p, true
		}
	}
	return "", false
}

// priorityOfLabels is the priority labels name, as withPriorityLabels reads them: the higher of
// priority:high, priority:medium and priority:low, any case; "" for none.
func priorityOfLabels(labels []string) string {
	for _, p := range priorityLevels {
		for _, l := range labels {
			if strings.EqualFold(l, "priority:"+string(p)) {
				return string(p)
			}
		}
	}
	return ""
}

// higherPriority is the higher of a and b; "" is lower than any level.
func higherPriority(a, b wire.SessionPriority) wire.SessionPriority {
	for _, p := range priorityLevels {
		if a == p || b == p {
			return p
		}
	}
	return ""
}

// issuePriority is the priority the lineage cache holds for n, and whether the forge has said
// it: a node it does not hold, or whose last ask failed, is asked for in the background.
func (d *Daemon) issuePriority(n wire.LineageNode) (wire.SessionPriority, bool) {
	d.resolveMu.Lock()
	e, ok := d.resolveCache[n]
	d.resolveMu.Unlock()
	if !ok || !e.ok {
		d.resolveSoon(n)
		return "", false
	}
	p, _ := priorityOf(e.priority)
	return p, true
}

// labelIssues are the issues a's labels name: #n, a key (ENG-4), AB#n, and the issues each P#n's
// pull request fixes or closes.
func (d *Daemon) labelIssues(a wire.Agent) []wire.LineageNode {
	folder := folderPath(&a)
	var out []wire.LineageNode
	for _, l := range a.Labels {
		n, ok := d.labelNode(folder, l.Value)
		if !ok {
			continue
		}
		switch n.Kind {
		case lineage.KindIssue:
			out = append(out, n)
		case lineage.KindPR:
			for _, link := range d.lineage().Links(n).Links {
				if link.EndedAt == nil && link.From == n && (link.Rel == lineage.RelFixes || link.Rel == lineage.RelCloses) {
					out = append(out, link.To)
				}
			}
		}
	}
	return out
}

// effectivePriority is the priority a goes by and its source (both nil for none); known is false
// when an issue it is labelled with has not been read yet, and a keeps what it has.
func (d *Daemon) effectivePriority(a wire.Agent) (p *wire.SessionPriority, source *wire.SessionPrioritySource, known bool) {
	src := func(s wire.SessionPrioritySource) *wire.SessionPrioritySource { return &s }
	if a.PrioritySet != nil {
		v := *a.PrioritySet
		return &v, src(wire.SessionPrioritySourceSet), true
	}
	known = true
	var best wire.SessionPriority
	for _, n := range d.labelIssues(a) {
		v, ok := d.issuePriority(n)
		if !ok {
			known = false
		}
		best = higherPriority(best, v)
	}
	if best != "" {
		return &best, src(wire.SessionPrioritySourceIssue), true
	}
	if !known {
		return a.Priority, a.PrioritySource, false
	}
	if a.StartedByAgent != nil {
		if s := d.Store.Agent(*a.StartedByAgent); s != nil && s.Priority != nil {
			v := *s.Priority
			return &v, src(wire.SessionPrioritySourceStarter), true
		}
	}
	return nil, nil, true
}

func samePriority[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// refreshPriorities works out again the priority of the sessions in only (every session when
// nil), then of the sessions those whose priority changed started, priorityHops deep. Not under
// any lock: it reads the folders' git remotes and saves records.
func (d *Daemon) refreshPriorities(only map[wire.UUID]bool) {
	for range priorityHops {
		changed := map[wire.UUID]bool{}
		for _, a := range d.Store.Agents() {
			if only != nil && !only[a.ID] || a.Purpose != nil || a.State == wire.AgentStateArchived {
				continue
			}
			p, source, known := d.effectivePriority(a)
			if !known || samePriority(p, a.Priority) && samePriority(source, a.PrioritySource) {
				continue
			}
			if d.changeAgent(a.ID, false, func(a *wire.Agent) { a.Priority, a.PrioritySource = p, source }) != nil {
				changed[a.ID] = true
			}
		}
		if len(changed) == 0 {
			return
		}
		only = map[wire.UUID]bool{}
		for _, a := range d.Store.Agents() {
			if a.StartedByAgent != nil && changed[*a.StartedByAgent] {
				only[a.ID] = true
			}
		}
		if len(only) == 0 {
			return
		}
	}
}

// refreshPrioritiesSoon is refreshPriorities of every session on the linker's goroutine, after
// the links given before it; once for a burst of calls.
func (d *Daemon) refreshPrioritiesSoon() {
	if !d.priorityQueued.CompareAndSwap(false, true) {
		return
	}
	if !d.lineageDo(func() {
		d.priorityQueued.Store(false)
		d.refreshPriorities(nil)
	}) {
		d.priorityQueued.Store(false)
	}
}

func (d *Daemon) registerSessionPriority() {
	On(d, wire.CallAgentsSetPriority, func(ctx context.Context, c *Client, r wire.SetPriorityRequest) (wire.Agent, error) {
		return d.setPriority(r.AgentID, r.Priority)
	})
}

// setPriority is agents/setPriority: p set on the session, or cleared when nil; the session as it
// is after.
func (d *Daemon) setPriority(id wire.UUID, p *wire.SessionPriority) (wire.Agent, error) {
	if p != nil {
		if _, ok := priorityOf(string(*p)); !ok {
			return wire.Agent{}, Invalid("agents/setPriority: priority is high, medium or low, or none to follow its issue")
		}
	}
	if d.Store.Agent(id) == nil {
		return wire.Agent{}, &Error{Code: CodeNoSuchAgent, Message: "That session no longer exists."}
	}
	if d.changeAgent(id, false, func(a *wire.Agent) { a.PrioritySet = p }) == nil {
		return wire.Agent{}, &Error{Code: CodeNoSuchAgent, Message: "That session no longer exists."}
	}
	d.refreshPriorities(map[wire.UUID]bool{id: true})
	a := d.Store.Agent(id)
	if a == nil {
		return wire.Agent{}, &Error{Code: CodeNoSuchAgent, Message: "That session no longer exists."}
	}
	return *a, nil
}

// setSessionPriority is set_session_priority: the priority of the caller, or of a session it
// started, set or cleared ("issue").
func (d *Daemon) setSessionPriority(caller wire.UUID, args map[string]json.RawMessage) (string, error) {
	word := strings.ToLower(strings.TrimSpace(argString(args, "priority")))
	var p *wire.SessionPriority
	if word != "issue" {
		v, ok := priorityOf(word)
		if !ok {
			return "", Invalid("Nothing changed: priority is high, medium, low, or issue to follow its issue's priority again.")
		}
		p = &v
	}
	target := caller
	if id := strings.TrimSpace(argString(args, "id")); id != "" && !strings.EqualFold(id, string(caller)) {
		t, err := d.helperTarget(caller, wire.UUID(strings.ToUpper(id)), "set the priority of")
		if err != nil {
			return "", err
		}
		target = t.ID
	}
	a, err := d.setPriority(target, p)
	if err != nil {
		return "", err
	}
	who := "This session"
	if target != caller {
		who = quotedTitle(a.Title)
	}
	return who + " " + priorityWords(a) + ".", nil
}

// priorityWords says a's priority and where it comes from, for an agent.
func priorityWords(a wire.Agent) string {
	if a.Priority == nil {
		return "has no priority now: none is set on it, its issues have none, and its starter has none"
	}
	from := map[wire.SessionPrioritySource]string{
		wire.SessionPrioritySourceSet:     "set on it",
		wire.SessionPrioritySourceIssue:   "from its issue",
		wire.SessionPrioritySourceStarter: "from the session that started it",
	}
	words := "is " + string(*a.Priority) + " priority now"
	if a.PrioritySource != nil && from[*a.PrioritySource] != "" {
		words += ", " + from[*a.PrioritySource]
	}
	return words
}
