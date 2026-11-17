package agentsd

import (
	"context"
	"fmt"
	"log"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/safe"
)

// jobBook counts the goroutines the host started (#5653): every one goes through d.goJob (or
// d.jobFunc for a timer's callback), so Shutdown waits for each to end. Before it, a stopped
// host's goroutines wrote into a test's root while t.TempDir removed it, and read package
// variables that the next test changed; eight PRs fixed one goroutine at a time.
type jobBook struct {
	jobMu     sync.Mutex
	jobsDone  bool                // Shutdown waited: nothing more starts
	jobsLive  map[uint64]jobStart // the goroutines still running, by number
	jobNext   uint64
	jobsWait  sync.WaitGroup
	jobCancel context.CancelFunc // ends d.ctx, which every long goroutine of the host waits on
}

// jobStart is where a goroutine started and what it is.
type jobStart struct {
	what string
	site string
}

func (j jobStart) String() string { return j.what + " (" + j.site + ")" }

// jobLimit is how long Shutdown waits for the goroutines the host started.
const jobLimit = 5 * time.Second

// goJob runs fn on a goroutine of its own, with a recover, counted until it returns. After
// Shutdown it does not start.
func (d *Daemon) goJob(what string, fn func()) {
	d.goJobAt(what, callerSite(1), fn)
}

// goJobAt is goJob for a helper that starts goroutines for its callers: site is where it
// was called. It is false when the host has stopped, and fn does not run.
func (d *Daemon) goJobAt(what, site string, fn func()) bool {
	n, ok := d.enterJobAt(what, site)
	if !ok {
		return false
	}
	safe.Go(what, func() {
		defer d.leaveJob(n)
		fn()
	})
	return true
}

// goJobOrNow is goJob for work that must still be done once the host has stopped, such as
// closing what it held: after Shutdown it runs fn at once, on the caller's goroutine.
func (d *Daemon) goJobOrNow(what string, fn func()) {
	if !d.goJobAt(what, callerSite(1), fn) {
		fn()
	}
}

// jobFunc wraps fn (a timer's callback) with a recover, and counts it while it runs. A
// callback that fires after Shutdown does nothing.
func (d *Daemon) jobFunc(what string, fn func()) func() {
	site := callerSite(1)
	return safe.Func(what, func() {
		n, ok := d.enterJobAt(what, site)
		if !ok {
			return
		}
		defer d.leaveJob(n)
		fn()
	})
}

func (d *Daemon) enterJobAt(what, site string) (uint64, bool) {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.jobsDone {
		return 0, false
	}
	if d.jobsLive == nil {
		d.jobsLive = map[uint64]jobStart{}
	}
	d.jobNext++
	d.jobsLive[d.jobNext] = jobStart{what: what, site: site}
	d.jobsWait.Add(1)
	return d.jobNext, true
}

func (d *Daemon) leaveJob(n uint64) {
	d.jobMu.Lock()
	delete(d.jobsLive, n)
	d.jobMu.Unlock()
	d.jobsWait.Done()
}

// liveJobs is where each goroutine still running started, sorted.
func (d *Daemon) liveJobs() []string {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	out := make([]string, 0, len(d.jobsLive))
	for _, j := range d.jobsLive {
		out = append(out, j.String())
	}
	slices.Sort(out)
	return out
}

// waitJobs ends d.ctx, stops new goroutines and waits up to limit for those still running.
// It says which still run at the limit, by where they started.
func (d *Daemon) waitJobs(limit time.Duration) bool {
	d.jobMu.Lock()
	d.jobsDone = true
	cancel := d.jobCancel
	d.jobMu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		d.jobsWait.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		log.Printf("the host goes before its goroutines end (waited %v): %s", limit, strings.Join(d.liveJobs(), "; "))
		return false
	}
}

// hostCtx is d.ctx, which ends when the host stops; a Daemon a test made by hand has none.
func (d *Daemon) hostCtx() context.Context {
	if d.ctx == nil {
		return context.Background()
	}
	return d.ctx
}

// callerSite is file:line of the function skip frames above the one that calls it: 1 is
// its caller's caller.
func callerSite(skip int) string {
	_, file, line, ok := goruntime.Caller(skip + 1)
	if !ok {
		return "?"
	}
	if i := strings.LastIndex(file, "/Go/"); i >= 0 {
		file = file[i+1:]
	}
	return fmt.Sprintf("%s:%d", file, line)
}
