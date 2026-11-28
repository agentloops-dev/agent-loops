package controlplane

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// stopWatch is a store that is slow to read a machine's versions, and keeps each write
// made after the test said Stop had returned.
type stopWatch struct {
	store.Store
	mu      sync.Mutex
	stopped bool
	late    []string
}

func (w *stopWatch) Get(ctx context.Context, key string) (*store.Object, error) {
	if strings.HasPrefix(key, machineVersionsPrefix) {
		// As a loaded runner is: the host's read loop is still at its end after Stop.
		time.Sleep(200 * time.Millisecond)
	}
	return w.Store.Get(ctx, key)
}

func (w *stopWatch) Put(ctx context.Context, key string, data []byte, when store.Condition) (string, error) {
	w.mu.Lock()
	if w.stopped {
		w.late = append(w.late, key)
	}
	w.mu.Unlock()
	return w.Store.Put(ctx, key, data, when)
}

func (w *stopWatch) Delete(ctx context.Context, key string) error {
	w.mu.Lock()
	if w.stopped {
		w.late = append(w.late, key)
	}
	w.mu.Unlock()
	return w.Store.Delete(ctx, key)
}

// #7631: Stop waits for each host's read loop, which writes as the host's uplink ends
// (when its version was last seen, its lease's release). A write after Stop went into a
// store that was being removed: t.TempDir's cleanup found a folder "not empty".
func TestStopWaitsForWhatAHostsUplinkEndWrites(t *testing.T) {
	t.Parallel()
	w := &stopWatch{Store: store.NewMemory()}
	p := startPlane(t, w, controllink.NewPrivateKey())
	host := p.host("MAC-A").member.Host
	p.waitFor(func() bool { return p.s.router.Holds(host) }, "the machine held")

	p.s.Stop()
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	// What is still running would write within its read's 200 ms.
	time.Sleep(400 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.late) > 0 {
		t.Fatalf("written after Stop: %v", w.late)
	}
}
