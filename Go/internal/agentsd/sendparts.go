package agentsd

import (
	"cmp"
	"context"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// Several parts of a file in flight (#7120). With one at a time, a send on a link with delay
// waits for an answer after each 4 MB. How many is the control plane's partsAtOnce: it says
// none for a machine that takes one part at a time, or when it does itself. It is only the
// most: a part that takes long means the link is full, and then more parts in flight would
// only wait in line ahead of the link's other traffic.

const (
	// sendPartsAtOnce is the most parts this machine has in flight, of all its sends and
	// reads: with sendThereParts, about 22 MB on the link as base64.
	sendPartsAtOnce = 4
	// quickPart is the time a part may take before a send has fewer in flight.
	quickPart = 2 * time.Second
)

type partsBook struct {
	partsOnce  sync.Once
	partsSlots chan struct{}
	// partsQuick is quickPart when 0.
	partsQuick time.Duration
}

// partSlots holds one place for each part in flight.
func (d *Daemon) partSlots() chan struct{} {
	d.partsOnce.Do(func() { d.partsSlots = make(chan struct{}, sendPartsAtOnce) })
	return d.partsSlots
}

func sendStopped() error {
	return &Error{Code: wire.FailureNotSupported, Message: "this machine stopped before the send was done."}
}

// sendPartStep is one machines/sendFolder that carries a part; me asks. The host that stops
// sends no more parts, and waits for none (#6001): Shutdown waits for this job.
func (d *Daemon) sendPartStep(ctx context.Context, me *wire.Agent, step wire.MachineFolderSend) (wire.MachineFolderSent, error) {
	var sent wire.MachineFolderSent
	err := d.askSendCtx(ctx, me, step, &sent)
	if d.hostCtx().Err() != nil {
		return sent, sendStopped()
	}
	return sent, err
}

// sendWindow is how many parts of one file may be in flight, from one to most.
type sendWindow struct {
	now, most int
	quick     time.Duration
}

// took is told how long a part took.
func (w *sendWindow) took(t time.Duration) {
	if t <= w.quick {
		w.now = min(w.now+1, w.most)
	} else {
		w.now = max(w.now/2, 1)
	}
}

// inParts does the parts first..last-1, at most most at once, and starts none after one
// fails. It ends once each it started has ended. done hears each part that went, in turn.
func (d *Daemon) inParts(first, last int64, most int, part func(ctx context.Context, n int64) error, done func(n int64)) error {
	ctx, stop := context.WithCancel(d.hostCtx())
	defer stop()
	type ended struct {
		n    int64
		took time.Duration
		err  error
	}
	ends := make(chan ended, sendPartsAtOnce)
	window := sendWindow{now: 1, most: min(max(most, 1), sendPartsAtOnce), quick: cmp.Or(d.partsQuick, quickPart)}
	var failed error
	for flying, next := 0, first; ; {
		for failed == nil && next < last && flying < window.now {
			n := next
			next++
			flying++
			safe.Go("send part", func() {
				e := ended{n: n, err: &Error{Code: CodeInternal, Message: "a part could not be sent"}}
				defer func() { ends <- e }()
				select {
				case d.partSlots() <- struct{}{}:
				case <-ctx.Done():
					e.err = sendStopped()
					return
				}
				defer func() { <-d.partSlots() }()
				began := time.Now()
				e.err = part(ctx, n)
				e.took = time.Since(began)
			})
		}
		if flying == 0 {
			return failed
		}
		e := <-ends
		flying--
		switch {
		case e.err == nil:
			window.took(e.took)
			done(e.n)
		case failed == nil:
			failed = e.err
			stop()
		}
	}
}

// partsAtOnce is what the control plane said.
func partsAtOnce(sent wire.MachineFolderSent) int {
	if sent.PartsAtOnce == nil {
		return 1
	}
	return int(min(max(*sent.PartsAtOnce, 1), sendPartsAtOnce))
}
