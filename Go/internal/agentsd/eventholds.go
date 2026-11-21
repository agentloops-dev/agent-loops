package agentsd

import (
	"fmt"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A wait_for_event call answers inside its hold (#6310). On 2026-11-18, 46 calls in 14
// sessions ended in the client's "The operation timed out" or "sent no response or
// progress": each was open while the Mac slept. The hold's timer does not count a sleep,
// and the client does, so it gave up at once on waking, before the hold ended. So a sleep
// answers every held call before the Mac sleeps, and a wake answers any still held. A
// server that is slow to say what events it offers cannot hold a call past its hold either.

// answerHeldWaits answers every wait_for_event call held open now with "still waiting".
// Each wait stays, and its event starts the agent again, as after a hold that ran out.
func (d *Daemon) answerHeldWaits() {
	d.waitMu.Lock()
	held := make(map[wire.UUID]wire.UUID, len(d.openWaits))
	for agent, c := range d.openWaits {
		held[agent] = c.waitID
		// A call parked a moment before its record says it waits is not answered below:
		// it answers itself once the record is written (#6495).
		c.answerNow = true
	}
	d.waitMu.Unlock()
	for agent, waitID := range held {
		d.eventHoldReached(agent, waitID)
	}
}

// checkEventWaitServersWithin is checkEventWaitServers with a limit. When the servers have
// not answered within it, the wait is refused, so the call ends inside its hold; the check
// goes on alone, and fills the listing cache for the next call.
func (d *Daemon) checkEventWaitServersWithin(limit time.Duration, serverNames []string, req eventWaitRequest, folder string, appTaken map[string]bool) (mcpWaitCheck, string, error) {
	if len(serverNames) == 0 {
		return d.checkEventWaitServers(serverNames, req, folder, appTaken)
	}
	type checked struct {
		servers mcpWaitCheck
		warning string
		err     error
	}
	done := make(chan checked, 1)
	// Counted, so Shutdown waits for it: stopping ends the connections it may wait on.
	d.goJob("wait's servers", func() {
		servers, warning, err := d.checkEventWaitServers(serverNames, req, folder, appTaken)
		done <- checked{servers, warning, err}
	})
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case c := <-done:
		return c.servers, c.warning, c.err
	case <-timer.C:
		return mcpWaitCheck{}, "", eventRefused(slowServersWords(serverNames, limit))
	case <-d.ctx.Done():
		return mcpWaitCheck{}, "", eventRefused("This machine's host is stopping. Nothing is waiting.")
	}
}

// slowServersWords says a wait's servers did not answer in time.
func slowServersWords(serverNames []string, limit time.Duration) string {
	return fmt.Sprintf("This folder's servers did not say in %s whether they offer %s. Nothing is waiting yet: "+
		"call wait_for_event again.", limit.Round(time.Second), strings.Join(serverNames, ", "))
}
