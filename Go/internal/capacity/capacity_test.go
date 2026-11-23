package capacity

import (
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestNextCapacityUsesTwoLooksToFall(t *testing.T) {
	level, last, below := Next(wire.CapacityLevelFull, wire.CapacityLevelNear, "", 0)
	if level != wire.CapacityLevelFull || last != wire.CapacityLevelNear || below != 1 {
		t.Fatalf("first below look: %s %s %d", level, last, below)
	}
	level, last, below = Next(level, wire.CapacityLevelOk, last, below)
	if level != wire.CapacityLevelNear || last != "" || below != 0 {
		t.Fatalf("second below look: %s %s %d", level, last, below)
	}
}
