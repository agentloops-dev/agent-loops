package controlplane

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/providers"
)

// A host's calls on channel 0 run off its link's read loop (#6223). The loop must keep
// reading: coder/websocket answers a ping only while something reads, so one slow
// HostSaid (a store write on a slow disk, say) held every other line and the pong, and
// the host dropped the link after 20 s, sent its hello and its calls again, and stalled
// again.
const (
	// hostCallLanes is how many of one host's calls run at once. Each method has a lane of
	// its own, so one slow method holds only its own calls, and they are answered in the
	// order they came; past this many methods, a method shares a lane.
	hostCallLanes = 16
	// hostCallQueue is how many calls wait in one lane. A call past it is refused at once,
	// and the host asks again later, as it does for a store that does not answer.
	hostCallQueue = 64
	// slowHostCall is a call the log names, so the next slow handler shows by its method.
	slowHostCall = 2 * time.Second
)

type hostCall struct {
	id     json.RawMessage
	method string
	params json.RawMessage
}

// hostCalls is one link's lanes, each started at its first call. They end with the link.
// Only the link's read loop calls take, so it needs no lock.
type hostCalls struct {
	r     *Router
	host  string
	gen   int64
	lanes []chan hostCall
	of    map[string]chan hostCall
	done  chan struct{}
	// parts holds a place for each part of a send that runs off its lane.
	parts chan struct{}
	// refused is how many calls were refused since the last one a lane took. Its log
	// lines go off the read loop (#6569): a write to a log file on a disk that is slow
	// held the loop, and the loop answers the pings.
	refused int
}

// startHostCalls makes the lanes for host's link gen.
func (r *Router) startHostCalls(host string, gen int64) *hostCalls {
	return &hostCalls{r: r, host: host, gen: gen, of: map[string]chan hostCall{}, done: make(chan struct{}),
		parts: make(chan struct{}, providers.SendPartsAtOnce)}
}

// lane is a method's lane: its own while there is room for one more, else one it shares.
func (c *hostCalls) lane(method string) chan hostCall {
	if lane, ok := c.of[method]; ok {
		return lane
	}
	if len(c.lanes) == hostCallLanes {
		h := fnv.New32a()
		_, _ = h.Write([]byte(method))
		return c.lanes[h.Sum32()%hostCallLanes]
	}
	lane := make(chan hostCall, hostCallQueue)
	c.lanes = append(c.lanes, lane)
	c.of[method] = lane
	go func() {
		for {
			select {
			case <-c.done:
				return
			case call := <-lane:
				c.run(call)
			}
		}
	}()
	return lane
}

// run answers a call, a part of a send off the lane when there is a place for it, so the
// machine's next part, and its calls about other things, do not wait for its answer (#7120).
func (c *hostCalls) run(call hostCall) {
	if !providers.IsPart(call.method, call.params) {
		c.r.answerHost(c.host, c.gen, call)
		return
	}
	select {
	case c.parts <- struct{}{}:
		go func() {
			defer func() { <-c.parts }()
			c.r.answerHost(c.host, c.gen, call)
		}()
	default:
		c.r.answerHost(c.host, c.gen, call)
	}
}

// take queues a call in its method's lane; false when that lane is full or the link ended.
func (c *hostCalls) take(call hostCall) bool {
	select {
	case <-c.done:
		return false
	case c.lane(call.method) <- call:
		return true
	default:
		return false
	}
}

func (c *hostCalls) stop() { close(c.done) }

// noteTaken and noteRefused say a call went into a lane or was refused, and log the first
// refusal of a burst and, at its end, how many there were: two lines a burst at most, each
// written off the read loop.
func (c *hostCalls) noteTaken() {
	if c.refused == 0 {
		return
	}
	n := c.refused
	c.refused = 0
	go c.r.log(fmt.Sprintf("host %s: %d calls refused while a lane was %d behind", c.host, n, hostCallQueue))
}

func (c *hostCalls) noteRefused(method string) {
	c.refused++
	if c.refused == 1 {
		go c.r.log(fmt.Sprintf("host %s: %s refused; its calls are %d behind", c.host, method, hostCallQueue))
	}
}

// answerHost runs one call and answers it on the link it came on, not on a newer one.
func (r *Router) answerHost(host string, gen int64, call hostCall) {
	began := time.Now()
	result, err := r.handler.HostSaid(host, call.method, call.params)
	if took := time.Since(began); took > slowHostCall {
		r.log(fmt.Sprintf("host %s: %s took %.1fs", host, call.method, took.Seconds()))
	}
	line := ""
	if err != nil {
		line = replyError(call.id, err)
	} else {
		line = reply(call.id, result)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if h := r.hosts[host]; h != nil && h.gen == gen {
		send := h.conn.Send
		if call.method == providers.MethodHostMachines && len(line) >= controllink.BulkLine {
			send = func(line string) bool { return sendBulk(h.conn, line) }
		}
		send(channelFrame(0, line))
	}
}

// hostBusy is the answer to a call its lane has no room for.
var hostBusy = fail(storeUnavailable, "The control plane is behind on this machine's calls. Ask again in a moment.")
