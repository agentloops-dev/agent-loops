package controlplane

import (
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

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
