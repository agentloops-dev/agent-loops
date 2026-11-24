package agentsd

import (
	"log"
	"sync"
	"time"
)

// A sleep that the Mac's power messages do not say (#6541). With the lid closed, a Mac goes
// from a DarkWake to a Maintenance Sleep and back every few minutes, and IOKit tells
// agents-watch of neither: on 2026-11-21 the Mac did that some 40 times and agentsd heard
// no mac.sleep or mac.wake, so #6310's answer before a sleep and after a wake never ran. A
// dark wake lasts from 10 seconds to 2 minutes here, and the agents run in it, so a call
// held for the full 45 s was open over the next sleep, and its client gave up on waking.
//
// So agentsd sees a sleep itself: the wall clock counts a sleep and Go's monotonic clock
// does not (on a Mac and on Linux), so a look each second that finds the two apart by
// more than sleepGapMin has slept through. It then does what a wake does for the waits and
// leases. A sleep that no wake word follows is a dark wake: until a wake word, the person,
// or darkWakeLasts, a call is held for darkWakeHold, so it is answered before the next
// sleep. Its wait stays, and its event starts the agent again.

var (
	sleepLookEvery = time.Second
	// sleepGapMin is more than the clock's own corrections and a loaded Mac's late tick.
	sleepGapMin   = 5 * time.Second
	darkWakeHold  = 2 * time.Second
	darkWakeLasts = 3 * time.Minute
)

type sleepBook struct {
	sleepMu sync.Mutex
	// darkSince is when a sleep with no wake word after it was seen; zero when awake.
	darkSince time.Time
	// wakeWordAt is when agents-watch last said wake.
	wakeWordAt time.Time
}

// StartSleepWatch looks each second for a sleep the power messages did not say.
func (d *Daemon) StartSleepWatch() {
	d.goJob("sleep watch", func() {
		tick := time.NewTicker(sleepLookEvery)
		defer tick.Stop()
		last := time.Now()
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-tick.C:
			}
			now := time.Now()
			if slept := sleptBetween(last, now); slept >= sleepGapMin {
				d.sleptThrough(slept, last)
			}
			last = now
		}
	})
}

// sleptBetween is how much of the time from last to now the process slept: the wall
// clock's time less the monotonic clock's. Both must come from time.Now().
func sleptBetween(last, now time.Time) time.Duration {
	return now.Round(0).Sub(last.Round(0)) - now.Sub(last)
}

// sleptThrough acts on a sleep seen by the clocks, which ended after lastLook. With no wake
// word since lastLook, the Mac is in a dark wake.
func (d *Daemon) sleptThrough(slept time.Duration, lastLook time.Time) {
	d.sleepMu.Lock()
	heard := d.wakeWordAt.Round(0).After(lastLook.Round(0))
	if !heard {
		d.darkSince = time.Now()
	}
	d.sleepMu.Unlock()
	if !heard {
		log.Printf("sleep watch: slept %s with no wake from the power messages (a dark wake): calls are held %s", slept.Round(time.Second), darkWakeHold)
	}
	d.afterSleep()
}

// awakeForSure ends a dark wake: the power messages said wake, or the person is here.
func (d *Daemon) awakeForSure(wakeWord bool) {
	d.sleepMu.Lock()
	defer d.sleepMu.Unlock()
	d.darkSince = time.Time{}
	if wakeWord {
		d.wakeWordAt = time.Now()
	}
}

// waitHold is how long a wait_for_event call may be held now.
func (d *Daemon) waitHold() time.Duration {
	d.sleepMu.Lock()
	defer d.sleepMu.Unlock()
	if !d.darkSince.IsZero() && time.Since(d.darkSince) < darkWakeLasts {
		return min(darkWakeHold, eventHoldLimit)
	}
	return eventHoldLimit
}

// afterSleep is what a wake does for the waits and the leases.
func (d *Daemon) afterSleep() {
	// A timer does not count the time the Mac slept (#6190): what came due while it slept,
	// a lease's end or a wait's deadline, is acted on now, not that much later.
	d.goJob("leases after wake", d.lapseLeasesNow)
	// A call held over the sleep is answered now (#6310): its client counted the sleep and
	// ends it at once, while the hold's timer did not count it. A wait whose time came in
	// the sleep is told that first.
	d.goJob("waits after wake", func() {
		d.waitDeadlinesPassed()
		d.answerHeldWaits()
	})
}
