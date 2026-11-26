package locks

import (
	"encoding/json"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7249: the largest entry that the lock's check takes fits the largest that is kept, so no entry
// that a device signed is refused for its size, and one over the body's largest is refused.
func TestTheLargestLockEntryFits(t *testing.T) {
	data, err := json.Marshal(wire.LockEntry{Body: make([]byte, machinelock.MostBody), Sig: make([]byte, 64)})
	if err != nil || len(data) > mostEntryBytes {
		t.Fatalf("%d bytes: %v", len(data), err)
	}
}
