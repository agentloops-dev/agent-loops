package agentsd

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A stall's notice (#6190): a wait or a place in a lease's line that makes no progress for a
// long time is a notice in Needs you, on the waiting session, so a silent stall becomes
// something the person sees. Before, a lane whose server stopped answering, or whose line
// never moved, sat with no error until its until_minutes, and nobody knew why it did nothing.
//
// - A wait on a server's events stalls when no subscription it holds has polled for
//   waitStallAfter: the server does not answer, or its poll stopped. The wait goes on (the
//   server may come back, #2457), and when it times out its words say what the server said.
// - A place in a lease's line stalls when the line has not moved for leaseStallAfter: no
//   lease of the resource was granted since the place was taken.
//
// The notice goes when the stall does (a poll, a grant, the wait's end). It is told once per
// stall, and is not taken from the person's notice bucket, as a disk notice is not.

const (
	// stallNoticePrefix starts a stall notice's id; the rest says what stalled.
	stallNoticePrefix = "stall:"
	// waitStallAfter is three of the slowest polls (mcpPollCeiling, 5 min) missed in a row.
	waitStallAfter = 15 * time.Minute
	// leaseStallAfter is one hour with no grant: longer than the usual lease (30 min) and
	// than the build lease's longest (60 min).
	leaseStallAfter = time.Hour
	// stallCheckEvery is how often the stalls are looked for.
	stallCheckEvery = time.Minute
)

// stall is one wait or place that has made no progress since since.
type stall struct {
	id    string
	agent wire.UUID
	since time.Time
	h3    string
}

// runStallTicker looks for stalls each minute until ctx ends.
func (d *Daemon) runStallTicker(ctx context.Context) {
	t := time.NewTicker(stallCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.checkStalls(now().Time())
		}
	}
}

// checkStalls posts a notice for each stall at at that is not told yet, and takes back the
// notice of each that is no longer stalled.
func (d *Daemon) checkStalls(at time.Time) {
	stalls := append(d.serverWaitStalls(at), d.leaseLineStalls(at)...)
	current := map[string]stall{}
	for _, s := range stalls {
		current[s.id] = s
	}
	changed := false
	d.attentionMu.Lock()
	if d.stallsTold == nil {
		d.stallsTold = map[string]bool{}
	}
	kept := d.notices[:0]
	for _, n := range d.notices {
		if strings.HasPrefix(n.id, stallNoticePrefix) {
			if _, still := current[n.id]; !still {
				changed = true
				continue
			}
		}
		kept = append(kept, n)
	}
	d.notices = kept
	for id := range d.stallsTold {
		if _, still := current[id]; !still {
			delete(d.stallsTold, id)
		}
	}
	for _, s := range stalls {
		if d.stallsTold[s.id] {
			continue
		}
		d.stallsTold[s.id] = true
		d.notices = append(d.notices, postedNotice{id: s.id, agentID: s.agent, raised: wire.DateOf(at), h3: s.h3})
		changed = true
	}
	d.attentionMu.Unlock()
	if changed {
		d.reconsider()
	}
}

// serverWaitStalls is each open wait that hears only servers' events and whose
// subscriptions have all been quiet for waitStallAfter.
func (d *Daemon) serverWaitStalls(at time.Time) []stall {
	var out []stall
	for _, a := range d.Store.LiveAgents() {
		w := a.EventWait
		if w == nil || w.Ending != nil || len(w.ServerEvents) == 0 || a.State == wire.AgentStateArchived {
			continue
		}
		ownToo := false
		for _, p := range waitPatterns(w) {
			ownToo = ownToo || !isServerPattern(p.name)
		}
		if ownToo {
			continue
		}
		quiet, why := d.serverWaitQuietSince(w)
		if at.Sub(quiet) < waitStallAfter {
			continue
		}
		h3 := "Waiting for " + waitLabel(w) + ", but no poll of it worked since " + clock(wire.DateOf(quiet)) +
			", so the wait cannot fire."
		if why != "" {
			h3 += " " + why
		}
		out = append(out, stall{id: stallNoticePrefix + "wait:" + string(w.ID), agent: a.ID, since: quiet, h3: h3})
	}
	return out
}

// serverWaitQuietSince is when w last heard from a server: the latest poll of the
// subscriptions it holds, or when it began when none has polled. why is what a failing
// subscription's server said, if one did.
func (d *Daemon) serverWaitQuietSince(w *wire.EventWait) (time.Time, string) {
	quiet := w.Since.Time()
	var why []string
	d.mcpEvMu.Lock()
	defer d.mcpEvMu.Unlock()
	ids := make([]string, 0, len(d.mcpSubs))
	for id, s := range d.mcpSubs {
		if containsUUID(s.waitIDs, w.ID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if t := d.mcpAliveAt[id]; t.After(quiet) {
			quiet = t
		}
		if l := d.mcpLives[id]; l.failure != nil && l.state != wire.MCPTriggerStatusStateActive {
			why = append(why, strings.TrimSpace(l.failure.Message))
		}
	}
	return quiet, strings.Join(why, " ")
}

// serverTroubleWords is what a wait that times out is told of the servers it held that were
// failing (#6190): with them, "no match" may only mean that nothing could be heard.
func (d *Daemon) serverTroubleWords(w *wire.EventWait) string {
	if len(w.ServerEvents) == 0 {
		return ""
	}
	d.mcpEvMu.Lock()
	defer d.mcpEvMu.Unlock()
	ids := make([]string, 0, len(d.mcpSubs))
	for id, s := range d.mcpSubs {
		if containsUUID(s.waitIDs, w.ID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var words string
	for _, id := range ids {
		l := d.mcpLives[id]
		if l.failure == nil || l.state == wire.MCPTriggerStatusStateActive {
			continue
		}
		words += " " + d.mcpSubs[id].server + " could not be polled since " + clock(l.failure.Since) + ": " +
			strings.TrimSpace(l.failure.Message) + " An event may have come that this wait did not hear."
	}
	return words
}

// leaseLineStalls is each place in a lease's line whose line has not moved for
// leaseStallAfter: no grant of the resource since the place was taken.
func (d *Daemon) leaseLineStalls(at time.Time) []stall {
	d.leaseMu.Lock()
	defer d.leaseMu.Unlock()
	if d.book == nil {
		return nil
	}
	var out []stall
	for name, e := range d.book.Entries {
		var moved time.Time
		var holders []string
		for _, l := range e.Leases {
			if t := l.GrantedAt.Time(); t.After(moved) {
				moved = t
			}
			holders = append(holders, d.holderName(l.Holder)+" holds it until "+clock(l.ExpiresAt))
		}
		for _, w := range e.Line {
			since := w.AskedAt.Time()
			if moved.After(since) {
				since = moved
			}
			if at.Sub(since) < leaseStallAfter {
				continue
			}
			h3 := "In line for " + e.DisplayName + " since " + clock(w.AskedAt) + ", and the line has not moved since " +
				clock(wire.DateOf(since)) + "."
			if len(holders) > 0 {
				h3 += " " + strings.Join(holders, "; ") + "."
			} else {
				h3 += " Nobody holds it: it waits for the machine."
			}
			out = append(out, stall{id: stallNoticePrefix + "lease:" + string(name) + ":" + string(w.AgentID), agent: w.AgentID, since: since, h3: h3})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}
