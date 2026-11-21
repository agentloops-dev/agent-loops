package agentsd

import (
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// testHooks is what a test changed in the package's variables, put back only once each of
// its daemons has shut down (#5653). A deferred `v = was` ran when the test function
// returned, before startWith's cleanup shut the daemon down, so the daemon's goroutines read
// the variable while it was written.
type testHooks struct {
	mu       sync.Mutex
	daemons  int
	restores []func()
	// opts are what startWith gives the test's daemons (#5926): a personal home and fake
	// runtimes of their own, not the process's, so the test can run in parallel.
	opts startOptions
	// homeNamed is whether personalHomeFor named opts.namedHome; without it a daemon reads
	// AGENTS_PERSONAL_HOME, as New does.
	homeNamed bool
	// running are the test's daemons that have not shut down: a change of the options goes to
	// them too.
	running []*Daemon
}

var testHooksBook sync.Map // testing.TB → *testHooks

func testHooksOf(t testing.TB) *testHooks {
	h, loaded := testHooksBook.LoadOrStore(t, &testHooks{})
	if !loaded {
		t.Cleanup(func() { testHooksBook.Delete(t) })
	}
	return h.(*testHooks)
}

func (h *testHooks) started() {
	h.mu.Lock()
	h.daemons++
	h.mu.Unlock()
}

// stopped puts back what the test changed, last first, once its last daemon is down.
func (h *testHooks) stopped() {
	h.mu.Lock()
	h.daemons--
	var run []func()
	if h.daemons == 0 {
		run, h.restores = h.restores, nil
	}
	h.mu.Unlock()
	for i := len(run) - 1; i >= 0; i-- {
		run[i]()
	}
}

// options is what the test's next daemon starts with.
func (h *testHooks) options() startOptions {
	h.mu.Lock()
	defer h.mu.Unlock()
	o := h.opts
	if !h.homeNamed {
		o.namedHome = os.Getenv("AGENTS_PERSONAL_HOME")
	}
	o.diskFree = h.diskFreeLocked()
	return o
}

// diskFreeLocked is the free space the test's daemons read: a disk with room unless the
// test named its own (#6390), so the Mac's own full disk adds no notice to Needs you. Call
// it with mu held.
func (h *testHooks) diskFreeLocked() func(wire.DiskReading) int64 {
	if h.opts.diskFree != nil {
		return h.opts.diskFree
	}
	return roomyDisk
}

// roomyDisk is a disk with nine tenths of it free: far above the low line.
func roomyDisk(r wire.DiskReading) int64 { return r.TotalBytes / 10 * 9 }

// A daemon a test makes with New, not start (a restart on the same root), reads the same
// roomy disk (#6497). init runs before any test, so no daemon reads newDiskFree as it changes.
func init() { newDiskFree = roomyDisk }

// diskFreeFor makes every volume of t's daemons read free bytes free, as a full or a
// roomy disk would (#6390), in place of the Mac's own disk.
func diskFreeFor(t testing.TB, free int64) {
	t.Helper()
	testHooksOf(t).change(func(h *testHooks) { h.opts.diskFree = func(wire.DiskReading) int64 { return free } })
}

// change changes the options of the test's daemons: those it starts next, and those that
// run, as a change of the process's environment once did.
func (h *testHooks) change(f func(*testHooks)) {
	h.mu.Lock()
	f(h)
	running := slices.Clone(h.running)
	named, home, locate, env, attach := h.homeNamed, h.opts.namedHome, h.opts.locate, h.opts.runtimeEnv, h.opts.attach
	diskFree := h.diskFreeLocked()
	h.mu.Unlock()
	for _, d := range running {
		o := d.startOpts()
		if named {
			o.namedHome = home
		}
		o.locate, o.runtimeEnv, o.attach, o.diskFree = locate, env, attach, diskFree
		d.opts.Store(&o)
	}
}

// add and remove keep the test's running daemons.
func (h *testHooks) add(d *Daemon) {
	h.mu.Lock()
	h.running = append(h.running, d)
	h.mu.Unlock()
}

func (h *testHooks) remove(d *Daemon) {
	h.mu.Lock()
	h.running = slices.DeleteFunc(h.running, func(each *Daemon) bool { return each == d })
	h.mu.Unlock()
}

// personalHomeFor names home as the personal home of the daemons t starts, as
// AGENTS_PERSONAL_HOME does for the process's, and "" as none named. Unlike t.Setenv it
// leaves the test free to run in parallel (#5926).
func personalHomeFor(t testing.TB, home string) {
	t.Helper()
	testHooksOf(t).change(func(h *testHooks) { h.opts.namedHome, h.homeNamed = home, true })
}

// setFor sets *v to x for the rest of t, and puts the old value back after t's daemons have
// shut down: use it for any package variable a daemon's goroutine reads.
func setFor[T any](t testing.TB, v *T, x T) {
	t.Helper()
	was := *v
	*v = x
	restore := func() { *v = was }
	h := testHooksOf(t)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.daemons > 0 {
		// A daemon is running: its cleanup, which runs before this test's later ones
		// would, puts the value back after Shutdown.
		h.restores = append(h.restores, restore)
		return
	}
	// No daemon yet: a cleanup registered now runs after those of daemons started later.
	t.Cleanup(restore)
}
