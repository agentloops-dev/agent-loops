package agentsd

import (
	"sync"
	"testing"
)

// testHooks is what a test changed in the package's variables, put back only once each of
// its daemons has shut down (#5653). A deferred `v = was` ran when the test function
// returned, before startWith's cleanup shut the daemon down, so the daemon's goroutines read
// the variable while it was written.
type testHooks struct {
	mu       sync.Mutex
	daemons  int
	restores []func()
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
