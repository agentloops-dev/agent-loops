package agentsd

import (
	"context"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// shellOrder puts a shell's inputs in the order they were typed (#7147). Each request runs on
// a goroutine of its own, so two inputs sent one after the other can run the other way round.
// A client once kept the order by sending one input at a time and waiting for its answer, which
// on a far machine held each keystroke for a round trip of the one before.
type shellOrder struct {
	mu      sync.Mutex
	streams map[wire.UUID]*shellStream
	// used counts turns, to say which stream was used last.
	used int64
}

type shellStream struct {
	next    int64
	used    int64
	waiting map[int64]chan struct{}
}

const (
	// shellStreams bounds the streams a shell keeps: past it, the one used longest ago goes.
	shellStreams = 64
	// shellAhead bounds how far past the next input of its stream one may be.
	shellAhead = 256
	// shellGap is how long an input waits for the one before it, which comes first on the same
	// connection: past it, that one was lost, and what was typed after it is not written.
	shellGap = 10 * time.Second
)

// turn waits until seq is the next input of stream. The caller calls done when it has written
// it, or failed to; the next one then has its turn.
func (o *shellOrder) turn(ctx context.Context, stream wire.UUID, seq int64) (done func(), err error) {
	lost := &Error{Code: wire.FailureShellNotLive, Message: "Typing before this did not reach the shell, so this was not typed."}
	o.mu.Lock()
	s := o.stream(stream)
	if seq < s.next || seq > s.next+shellAhead || s.waiting[seq] != nil {
		o.mu.Unlock()
		return nil, Invalid("shell/input: %d is not a place in its stream that can be typed now.", seq)
	}
	done = func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		delete(s.waiting, seq)
		s.next = seq + 1
		if c := s.waiting[s.next]; c != nil {
			close(c)
		}
	}
	if seq == s.next {
		// Taken: the same place again is refused.
		s.waiting[seq] = closedTurn
		o.mu.Unlock()
		return done, nil
	}
	ready := make(chan struct{})
	s.waiting[seq] = ready
	o.mu.Unlock()
	t := time.NewTimer(shellGap)
	defer t.Stop()
	select {
	case <-ready:
		return done, nil
	case <-t.C:
	case <-ctx.Done():
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if s.next == seq {
		// Its turn came as it gave up.
		return done, nil
	}
	delete(s.waiting, seq)
	return nil, lost
}

var closedTurn = make(chan struct{})

// stream is the stream of that name, with mu held.
func (o *shellOrder) stream(name wire.UUID) *shellStream {
	o.used++
	s := o.streams[name]
	if s == nil {
		if o.streams == nil {
			o.streams = map[wire.UUID]*shellStream{}
		}
		if len(o.streams) >= shellStreams {
			var oldest wire.UUID
			for n, have := range o.streams {
				if oldest == "" || have.used < o.streams[oldest].used {
					oldest = n
				}
			}
			delete(o.streams, oldest)
		}
		s = &shellStream{waiting: map[int64]chan struct{}{}}
		o.streams[name] = s
	}
	s.used = o.used
	return s
}
