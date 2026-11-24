package controlplane

import (
	"os"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

func TestReadPSIFullAvg60(t *testing.T) {
	path := t.TempDir() + "/pressure"
	if err := os.WriteFile(path, []byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=1.00 avg60=16.65 avg300=0.50 total=123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readPSIFullAvg60(path); got != 16.65 {
		t.Fatalf("got %v, want 16.65", got)
	}
}

func TestControlCapacityWordsNameSignalAndHowTo(t *testing.T) {
	words := controlCapacityWords(wire.CapacityLevelNear, "pressure_io")
	for _, want := range []string{"near capacity", "its disk is busy", "larger machine", "more memory", "faster disk", "store sweep", "https://docs.agentloops.dev/how-to/run-the-control-plane-in-the-cloud/"} {
		if !strings.Contains(words, want) {
			t.Errorf("%q does not contain %q", words, want)
		}
	}
	if got, want := controlCapacityReason(wire.CapacityLevelFull, "pressure_memory"), "the control plane is at capacity: its memory is under pressure"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestControlCapacityTarget(t *testing.T) {
	for _, tc := range []struct {
		sample controlCapacity
		level  wire.CapacityLevel
		reason string
	}{
		{controlCapacity{load1: 2.1}, wire.CapacityLevelNear, "load"},
		{controlCapacity{load1: 3.1}, wire.CapacityLevelFull, "load"},
		{controlCapacity{pressureIO: 10.1}, wire.CapacityLevelNear, "pressure_io"},
		{controlCapacity{pressureIO: 25.1}, wire.CapacityLevelFull, "pressure_io"},
		{controlCapacity{pressureMemory: 5.1}, wire.CapacityLevelNear, "pressure_memory"},
		{controlCapacity{pressureMemory: 15.1}, wire.CapacityLevelFull, "pressure_memory"},
	} {
		level, reason := controlCapacityTarget(tc.sample)
		if level != tc.level || reason != tc.reason {
			t.Errorf("%+v: got %s, %s", tc.sample, level, reason)
		}
	}
}
