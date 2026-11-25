package capacity

import (
	"os"
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

func TestReadPSIFullAvg60(t *testing.T) {
	path := t.TempDir() + "/pressure"
	if err := os.WriteFile(path, []byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=1.00 avg60=16.65 avg300=0.50 total=123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadPSIFullAvg60(path); got != 16.65 {
		t.Fatalf("got %v, want 16.65", got)
	}
}

func TestControlTarget(t *testing.T) {
	for _, tc := range []struct {
		load   float64
		p      Pressure
		level  wire.CapacityLevel
		reason string
	}{
		{2.0, Pressure{IO: 10, Memory: 5}, wire.CapacityLevelOk, ""},
		{2.1, Pressure{}, wire.CapacityLevelNear, "load"},
		{3.1, Pressure{}, wire.CapacityLevelFull, "load"},
		{0, Pressure{IO: 10.1}, wire.CapacityLevelNear, "pressure_io"},
		{0, Pressure{IO: 25.1}, wire.CapacityLevelFull, "pressure_io"},
		{0, Pressure{Memory: 5.1}, wire.CapacityLevelNear, "pressure_memory"},
		{0, Pressure{Memory: 15.1}, wire.CapacityLevelFull, "pressure_memory"},
	} {
		if level, reason := ControlTarget(tc.load, tc.p); level != tc.level || reason != tc.reason {
			t.Errorf("%+v: got %s, %s", tc, level, reason)
		}
	}
}

func TestHigher(t *testing.T) {
	if Higher(wire.CapacityLevelOk, wire.CapacityLevelNear) != wire.CapacityLevelNear ||
		Higher(wire.CapacityLevelFull, wire.CapacityLevelNear) != wire.CapacityLevelFull {
		t.Error("not the higher level")
	}
}
