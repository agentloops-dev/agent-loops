package controlplane

import (
	"sync/atomic"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// Config.Used is told of each client's and machine's socket once its key is proved, for
// the hosted service's idle clock (#6785); a code's exchange alone is not a use.
func TestUsedIsToldOfEachProvedSocket(t *testing.T) {
	t.Parallel()
	var used atomic.Int64
	p := startPlaneWith(t, store.NewMemory(), controllink.NewPrivateKey(), func(c *Config) {
		c.Used = func() { used.Add(1) }
	})
	if _, err := p.s.Codes().Issue(t.Context(), false, false, testCodeLife); err != nil {
		t.Fatal(err)
	}
	if n := used.Load(); n != 0 {
		t.Fatalf("told %d times before any socket", n)
	}
	p.client("Used test")
	p.waitFor(func() bool { return used.Load() >= 1 }, "a client's use")
	before := used.Load()
	p.host("used-host")
	p.waitFor(func() bool { return used.Load() > before }, "a machine's use")
}
