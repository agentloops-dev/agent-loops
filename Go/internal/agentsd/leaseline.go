package agentsd

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A lease's line (#4268): a wait can say `by`, when its step stops mattering, which puts
// it ahead in the line.

// leaseBy is lease_resource's `by`: minutes from now ("5", or 5), a time today ("14:05"),
// or an ISO 8601 time. Nil when none was given or it cannot be read.
func leaseBy(raw json.RawMessage, at time.Time) *wire.Date {
	if len(raw) == 0 {
		return nil
	}
	var n float64
	var s string
	switch {
	case json.Unmarshal(raw, &n) == nil:
	case json.Unmarshal(raw, &s) == nil:
		s = strings.TrimSpace(s)
		if m, err := strconv.ParseFloat(s, 64); err == nil {
			n = m
			break
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			by := wire.DateOf(t)
			return &by
		}
		if t, err := time.ParseInLocation("15:04", s, at.Location()); err == nil {
			y, mo, day := at.Date()
			by := wire.DateOf(time.Date(y, mo, day, t.Hour(), t.Minute(), 0, 0, at.Location()))
			return &by
		}
		return nil
	default:
		return nil
	}
	if n <= 0 {
		return nil
	}
	by := wire.DateOf(at.Add(time.Duration(n * float64(time.Minute))))
	return &by
}

// priorityRank is where a session's effective priority puts it in a line (#5848): high,
// medium, none, then low. A session not on the record goes as none. A queued session whose
// priority set on it has not been worked out yet goes by that.
func priorityRank(a *wire.Agent) int {
	if a == nil {
		return 2
	}
	p := a.Priority
	if p == nil {
		p = a.PrioritySet
	}
	if p == nil {
		return 2
	}
	switch *p {
	case wire.SessionPriorityHigh:
		return 0
	case wire.SessionPriorityMedium:
		return 1
	case wire.SessionPriorityLow:
		return 3
	}
	return 2
}

// lineRank is the waiter's priority rank, read now, so a priority changed while it waits
// moves it.
func (d *Daemon) lineRank(id wire.UUID) int { return priorityRank(d.Store.Agent(id)) }

// lineBefore is the line's order: the higher priority first (#5848), then a wait with a `by`
// before one with none, the earliest `by` first (#4268), then whoever asked first (#4041).
func lineBefore(a, b waiter, rankA, rankB int) bool {
	if rankA != rankB {
		return rankA < rankB
	}
	if (a.By != nil) != (b.By != nil) {
		return a.By != nil
	}
	if a.By != nil && *a.By != *b.By {
		return *a.By < *b.By
	}
	return a.AskedAt < b.AskedAt
}

// orderLine puts e's line in lineBefore's order, with each waiter's priority as it is now,
// and says whether anything moved.
func orderLine(e *leaseEntry, rank func(wire.UUID) int) bool {
	if len(e.Line) < 2 {
		return false
	}
	ranks := make(map[wire.UUID]int, len(e.Line))
	for _, w := range e.Line {
		ranks[w.AgentID] = rank(w.AgentID)
	}
	before := make([]wire.UUID, len(e.Line))
	for i, w := range e.Line {
		before[i] = w.AgentID
	}
	sort.SliceStable(e.Line, func(i, j int) bool {
		return lineBefore(e.Line[i], e.Line[j], ranks[e.Line[i].AgentID], ranks[e.Line[j].AgentID])
	})
	for i, w := range e.Line {
		if before[i] != w.AgentID {
			return true
		}
	}
	return false
}

// joinLine puts a waiter in its place in the line, and says that place (from 1).
func joinLine(e *leaseEntry, w waiter, rank func(wire.UUID) int) int {
	e.Line = append(e.Line, w)
	orderLine(e, rank)
	return lineIndex(e, w.AgentID) + 1
}

// higherAhead is how many ahead of agent in e's line have a higher priority than it.
func higherAhead(e *leaseEntry, agent wire.UUID, rank func(wire.UUID) int) int {
	own := rank(agent)
	n := 0
	for _, w := range e.Line {
		if w.AgentID == agent {
			break
		}
		if rank(w.AgentID) < own {
			n++
		}
	}
	return n
}

// reorderLines puts every line in order again after a session's priority changed (#5848),
// and tells the clients when one moved. A holder is never disturbed: only the line moves.
func (d *Daemon) reorderLines() {
	d.leaseMu.Lock()
	defer d.leaseMu.Unlock()
	if d.book == nil {
		return
	}
	moved := false
	for _, e := range d.book.Entries {
		if orderLine(e, d.lineRank) {
			moved = true
		}
	}
	if moved {
		d.leasesChanged()
	}
}
