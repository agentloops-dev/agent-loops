package controlplane

import (
	"slices"
	"strings"
	"testing"
	"time"

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

// A spiky level (#7354: 8 changes in 13 minutes) gives no notice; a level that stays above
// ok gives one control.busy, and only a control.busy is followed by a control.ok (#7367).
func TestCapacityNoticesWaitForALevelThatStays(t *testing.T) {
	ok, near, full := wire.CapacityLevelOk, wire.CapacityLevelNear, wire.CapacityLevelFull
	start := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	var n capacityNotices
	var got []string
	at := start
	feed := func(level wire.CapacityLevel, looks int) {
		for range looks {
			if kind := n.look(level, at); kind != "" {
				got = append(got, at.Sub(start).String()+" "+kind)
			}
			at = at.Add(controlCapacityEvery)
		}
	}
	// 13 minutes of spikes: ok, near, full, near, ok, near, ok … each under 2 minutes.
	for range 4 {
		feed(ok, 4)
		feed(near, 3)
		feed(full, 2)
		feed(near, 3)
	}
	feed(ok, 8)
	if len(got) != 0 {
		t.Fatalf("a spiky level gave notices: %v", got)
	}
	// Above ok for 6 minutes, with the level moving between near and full: one control.busy,
	// 5 minutes after it went above ok.
	feed(near, 8)
	feed(full, 8)
	feed(near, 8)
	// A short return to ok and back gives no control.ok.
	feed(ok, 4)
	feed(near, 4)
	// Back at ok for good: one control.ok, 5 minutes after it went back.
	feed(ok, 30)
	// The spikes end at 14m; above ok from 14m to 20m; ok from 22m.
	if want := []string{"19m0s control.busy", "27m0s control.ok"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
