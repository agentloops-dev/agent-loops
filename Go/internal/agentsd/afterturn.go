package agentsd

import (
	"log"
	"sync"
	"time"
)

// afterTurnLimit is how long Shutdown waits for the writes that follow a turn: long enough
// for a stopped turn to record its ending, short enough that a stuck write cannot hang a quit.
const afterTurnLimit = 5 * time.Second

// afterTurnState tracks the goroutines that run turns and write what follows them
// (events.jsonl, workflows.json, time-history.json, achievements.json, guarded-files.json),
// so that Shutdown can wait for them (#1272).
//
// A count and a channel under afterTurnMu, not a sync.WaitGroup (#6269): a WaitGroup
// forbids an Add from zero while a Wait runs, and a wait on a live daemon (a test's, for
// its queue check) raced a folder.changed timer's goAfterTurn.
type afterTurnState struct {
	afterTurnMu     sync.Mutex
	afterTurnClosed bool
	// afterTurnGone closes when afterTurnClosed is set, so a write in line for a client stops
	// waiting (#6603); nil until it is asked for or Shutdown begins.
	afterTurnGone chan struct{}
	// afterTurnsUnderWay is how many have started and not ended.
	afterTurnsUnderWay int
	// afterTurnsIdle closes when afterTurnsUnderWay goes back to 0; nil while it is 0.
	afterTurnsIdle chan struct{}
}

// goAfterTurn is goJob for a turn and what follows it. Once the host is going it starts
// nothing and is false.
func (d *Daemon) goAfterTurn(what string, fn func()) bool {
	d.afterTurnMu.Lock()
	defer d.afterTurnMu.Unlock()
	if d.afterTurnClosed {
		return false
	}
	d.addAfterTurnLocked()
	if !d.goJobAt(what, callerSite(1), func() {
		defer d.doneAfterTurn()
		fn()
	}) {
		d.doneAfterTurnLocked()
		return false
	}
	return true
}

// addAfterTurnLocked counts one more under way; afterTurnMu is held.
func (d *Daemon) addAfterTurnLocked() {
	if d.afterTurnsUnderWay == 0 {
		d.afterTurnsIdle = make(chan struct{})
	}
	d.afterTurnsUnderWay++
}

// doneAfterTurn counts one fewer under way.
func (d *Daemon) doneAfterTurn() {
	d.afterTurnMu.Lock()
	defer d.afterTurnMu.Unlock()
	d.doneAfterTurnLocked()
}

// doneAfterTurnLocked is doneAfterTurn with afterTurnMu held.
func (d *Daemon) doneAfterTurnLocked() {
	d.afterTurnsUnderWay--
	if d.afterTurnsUnderWay == 0 {
		close(d.afterTurnsIdle)
		d.afterTurnsIdle = nil
	}
}

// closeAfterTurns starts no more turns or writes after them.
func (d *Daemon) closeAfterTurns() {
	d.afterTurnMu.Lock()
	defer d.afterTurnMu.Unlock()
	if d.afterTurnClosed {
		return
	}
	d.afterTurnClosed = true
	if d.afterTurnGone == nil {
		d.afterTurnGone = make(chan struct{})
	}
	close(d.afterTurnGone)
}

// shutdownBegun closes when Shutdown begins: shuttingDown, as a channel to wait on.
func (d *Daemon) shutdownBegun() <-chan struct{} {
	d.afterTurnMu.Lock()
	defer d.afterTurnMu.Unlock()
	if d.afterTurnGone == nil {
		d.afterTurnGone = make(chan struct{})
		if d.afterTurnClosed {
			close(d.afterTurnGone)
		}
	}
	return d.afterTurnGone
}

// shuttingDown is whether Shutdown has begun: no turn starts, and nothing more goes to a
// client unasked.
func (d *Daemon) shuttingDown() bool {
	d.afterTurnMu.Lock()
	defer d.afterTurnMu.Unlock()
	return d.afterTurnClosed
}

// going is whether Shutdown has begun, or the daemon's context is done. A SIGTERM ends
// that context, and with it every turn, before main reaches Shutdown (#1822): those turns
// are cut off by the daemon's going too.
func (d *Daemon) going() bool {
	if d.ctx.Err() != nil {
		return true
	}
	return d.shuttingDown()
}

// waitAfterTurns waits until none are under way, at most limit, and is false if they were
// not done. It is safe on a live daemon: one that starts during the wait is waited for too,
// until the count first goes back to 0.
func (d *Daemon) waitAfterTurns(limit time.Duration) bool {
	d.afterTurnMu.Lock()
	idle := d.afterTurnsIdle
	d.afterTurnMu.Unlock()
	if idle == nil {
		return true
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		log.Printf("the host goes before the writes after a turn are done (waited %v)", limit)
		return false
	}
}
