package controllink

import (
	"cmp"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
)

// What the control plane tells this host on channel 0 runs off the link's read loop
// (#6260), as the control plane's side does for a host's calls (#6245). coder/websocket
// reads a pong only inside Read, so a Told handler that waited (on a lock, the disk, the
// event log) held the reads, the pings got no pong, and the link dropped after 20 s.
//
// The notifications go on one queue, handled one at a time in the order they came: their
// handlers share state (machine/orgRoutes reads the identity machine/user sets), so they
// keep the order the read loop gave them. The queue holds at most toldQueue; past that, a
// new one is dropped and logged, never waited for: a handler that far behind is stuck, and
// a new link would not get it unstuck.
const (
	toldQueue = 1024
	// slowHandler is how long a handler, or one line on the read loop, may take before
	// the log names it.
	slowHandler = 2 * time.Second
)

type toldCall struct {
	method string
	params json.RawMessage
}

// toldLine is the Uplink's notifications that wait for Told.
type toldLine struct {
	mu      sync.Mutex
	queue   []toldCall
	running bool
	dropped int
}

// told queues a notification for Told and returns at once.
func (u *Uplink) told(method string, params json.RawMessage) {
	handle := u.Told
	if handle == nil {
		return
	}
	q := &u.toldLine
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) >= toldQueue {
		if q.dropped++; q.dropped == 1 {
			log.Printf("uplink: %d notifications wait for the one before them; %s and the next are dropped until they catch up", toldQueue, method)
		}
		return
	}
	q.queue = append(q.queue, toldCall{method: method, params: params})
	if q.running {
		return
	}
	q.running = true
	safe.Go("uplink told", func() { u.runTold(handle) })
}

// runTold hands the queue's notifications to Told until it is empty.
func (u *Uplink) runTold(handle func(string, json.RawMessage)) {
	q := &u.toldLine
	for {
		q.mu.Lock()
		if len(q.queue) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		call := q.queue[0]
		q.queue[0] = toldCall{}
		q.queue = q.queue[1:]
		if q.dropped > 0 && len(q.queue) < toldQueue/2 {
			log.Printf("uplink: the notifications caught up; %d were dropped", q.dropped)
			q.dropped = 0
		}
		q.mu.Unlock()
		u.timed(call.method, func() {
			// A panic in one handler ends neither the queue nor the link.
			_ = safe.Catch("uplink told "+call.method, func() { handle(call.method, call.params) })
		})
	}
}

// timed runs f and logs it by name when it takes over slowHandler.
func (u *Uplink) timed(what string, f func()) {
	start := time.Now()
	f()
	if took := time.Since(start); took > cmp.Or(u.slowAfter, slowHandler) {
		log.Printf("uplink: %s took %s", what, took.Round(100*time.Millisecond))
	}
}
