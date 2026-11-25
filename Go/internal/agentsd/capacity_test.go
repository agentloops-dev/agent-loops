package agentsd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/capacity"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// fakeCapacity puts a sampler that returns what set last gave it, for the life of t.
func fakeCapacity(t *testing.T) (set func(capacitySample)) {
	var mu sync.Mutex
	var now capacitySample
	was := setCapacitySampler(func(context.Context) capacitySample { mu.Lock(); defer mu.Unlock(); return now })
	t.Cleanup(func() { setCapacitySampler(was) })
	return func(s capacitySample) { mu.Lock(); now = s; mu.Unlock() }
}

// The level walks ok → near → full → ok (#4578): up at once, down only after two looks
// under its line, with machine.busy as it leaves ok and machine.ok as it comes back.
func TestCapacityRisesAtOnceAndFallsAfterTwoLooks(t *testing.T) {
	set := fakeCapacity(t)
	d, c := start(t, t.TempDir())
	defer d.Shutdown()
	ctx := context.Background()
	look := func(s capacitySample, want wire.CapacityLevel) {
		t.Helper()
		set(s)
		d.checkCapacity(ctx)
		if got := d.capacityLevel(); got != want {
			t.Fatalf("after %+v: %s, want %s", s, got, want)
		}
	}
	quiet := capacitySample{load1: 0.4, load5: 0.5, pressure: wire.MemoryPressureNormal}
	look(quiet, wire.CapacityLevelOk)
	if n := len(raised(d, "machine.busy")) + len(raised(d, "machine.ok")); n != 0 {
		t.Fatalf("%d events while ok", n)
	}

	look(capacitySample{load1: 2.4, load5: 1.0, pressure: wire.MemoryPressureNormal}, wire.CapacityLevelNear)
	busy := raised(d, "machine.busy")
	if len(busy) != 1 || busy[0].Details["level"] != "near" || busy[0].Details["signal"] != "load" ||
		busy[0].Details["load_per_core"] != "2.40" || busy[0].Sentence != sentenceMachine()+" is near capacity: load 2.4 per core." {
		t.Fatalf("%+v", busy)
	}
	// One look under the line keeps it near.
	look(quiet, wire.CapacityLevelNear)
	// Load above the full line is full, at once; no second machine.busy, as it was not ok.
	look(capacitySample{load1: 3.1, swap: 60}, wire.CapacityLevelFull)
	if n := len(raised(d, "machine.busy")); n != 1 {
		t.Fatalf("%d machine.busy", n)
	}
	// Two looks under full: the higher of the two (near) is where it goes.
	look(capacitySample{load1: 2.1}, wire.CapacityLevelFull)
	look(quiet, wire.CapacityLevelNear)
	if n := len(raised(d, "machine.ok")); n != 0 {
		t.Fatalf("machine.ok at near")
	}
	// A look over the line starts the count again.
	look(quiet, wire.CapacityLevelNear)
	look(capacitySample{load1: 2.2}, wire.CapacityLevelNear)
	look(quiet, wire.CapacityLevelNear)
	look(quiet, wire.CapacityLevelOk)
	ok := raised(d, "machine.ok")
	if len(ok) != 1 || ok[0].Details["was"] != "near" || ok[0].Sentence != sentenceMachine()+" has capacity again: load 0.4 per core." {
		t.Fatalf("%+v", ok)
	}

	// machine/usage carries the last look and the level.
	var s wire.MachineUsageState
	if err := json.Unmarshal(c.call(t, 1, wire.CallMachineUsage.Name, map[string]any{}), &s); err != nil {
		t.Fatal(err)
	}
	m := s.Machine
	if m.Capacity == nil || *m.Capacity != wire.CapacityLevelOk || m.LoadPerCore == nil || *m.LoadPerCore != 0.4 ||
		m.LoadPerCore5 == nil || *m.LoadPerCore5 != 0.5 || m.MemoryPressure == nil || *m.MemoryPressure != wire.MemoryPressureNormal ||
		m.SwapUsedPercent == nil || *m.SwapUsedPercent != 0 {
		t.Fatalf("%+v", m)
	}
}

// Each signal on its own gives its level (#4578).
func TestCapacityTargetTakesTheWorstSignal(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		s      capacitySample
		level  wire.CapacityLevel
		signal string
	}{
		{capacitySample{load1: 2.0}, wire.CapacityLevelOk, ""},
		{capacitySample{load1: 2.01}, wire.CapacityLevelNear, "load"},
		{capacitySample{load1: 3.1}, wire.CapacityLevelFull, "load"},
		// Warn and swap are reported, not a level (#4583).
		{capacitySample{pressure: wire.MemoryPressureWarn}, wire.CapacityLevelOk, ""},
		{capacitySample{pressure: wire.MemoryPressureCritical, load1: 2.5}, wire.CapacityLevelFull, "memory_pressure"},
		{capacitySample{swap: 95}, wire.CapacityLevelOk, ""},
	} {
		if level, signal := capacityTarget(tc.s); level != tc.level || signal != tc.signal {
			t.Errorf("%+v: %s %q, want %s %q", tc.s, level, signal, tc.level, tc.signal)
		}
	}
}

// Linux's PSI text gives the pressure (#4578).
func TestPSIPressure(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]wire.MemoryPressure{
		"some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n":   wire.MemoryPressureNormal,
		"some avg10=12.50 avg60=3.00 avg300=1.00 total=9\nfull avg10=2.00 avg60=0.00 avg300=0.00 total=1\n":  wire.MemoryPressureWarn,
		"some avg10=40.00 avg60=3.00 avg300=1.00 total=9\nfull avg10=10.00 avg60=0.00 avg300=0.00 total=1\n": wire.MemoryPressureCritical,
		"some avg10=11.00 avg60=0.00 avg300=0.00 total=0\n":                                                  wire.MemoryPressureWarn,
		"":          "",
		"garbage\n": "",
	} {
		if got := psiPressure(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

// sampleCapacity reads this machine without failing; what it says depends on the machine.
func TestSampleCapacityReadsThisMachine(t *testing.T) {
	t.Parallel()

	s := sampleCapacity(context.Background())
	if s.load1 < 0 || s.load5 < 0 || s.swap < 0 || s.swap > 100 {
		t.Fatalf("%+v", s)
	}
	t.Logf("%+v", s)
}

// capacity/state says what set the level (#6596): cpu for the load, memory for critical
// pressure, kept while the level waits to fall, and none at ok.
func TestCapacityStateSaysTheReason(t *testing.T) {
	set := fakeCapacity(t)
	d, _ := start(t, t.TempDir())
	defer d.Shutdown()
	ctx := context.Background()
	look := func(s capacitySample, level wire.CapacityLevel, reason wire.CapacityReason) {
		t.Helper()
		set(s)
		d.checkCapacity(ctx)
		st := d.capacityState()
		got := wire.CapacityReason("")
		if st.Reason != nil {
			got = *st.Reason
		}
		if st.Level != level || got != reason {
			t.Fatalf("after %+v: %s for %q, want %s for %q", s, st.Level, got, level, reason)
		}
	}
	quiet := capacitySample{load1: 0.4, pressure: wire.MemoryPressureNormal}
	look(quiet, wire.CapacityLevelOk, "")
	look(capacitySample{load1: 3.2}, wire.CapacityLevelFull, wire.CapacityReasonCpu)
	// Critical pressure with a quiet load is full for memory, at the same level.
	look(capacitySample{load1: 0.4, pressure: wire.MemoryPressureCritical}, wire.CapacityLevelFull, wire.CapacityReasonMemory)
	// A look under the line keeps the level and its reason.
	look(quiet, wire.CapacityLevelFull, wire.CapacityReasonMemory)
	look(quiet, wire.CapacityLevelOk, "")
	look(capacitySample{load1: 2.2}, wire.CapacityLevelNear, wire.CapacityReasonCpu)
}

// With both roles the machine has the higher of the two levels, and keeps one core's load
// free for the control plane (#6613).
func TestCapacityTargetOfAMachineWithBothRoles(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		s      capacitySample
		cores  int
		p      capacity.Pressure
		level  wire.CapacityLevel
		signal string
	}{
		{capacitySample{load1: 1.5}, 2, capacity.Pressure{}, wire.CapacityLevelOk, ""},
		{capacitySample{load1: 1.6}, 2, capacity.Pressure{}, wire.CapacityLevelNear, "load"},
		{capacitySample{load1: 2.6}, 2, capacity.Pressure{}, wire.CapacityLevelFull, "load"},
		{capacitySample{load1: 1.6}, 10, capacity.Pressure{}, wire.CapacityLevelOk, ""},
		{capacitySample{load1: 0.4}, 2, capacity.Pressure{IO: 16}, wire.CapacityLevelNear, "pressure_io"},
		{capacitySample{load1: 1.6}, 2, capacity.Pressure{IO: 26}, wire.CapacityLevelFull, "pressure_io"},
		{capacitySample{load1: 0.4}, 2, capacity.Pressure{Memory: 6}, wire.CapacityLevelNear, "pressure_memory"},
		{capacitySample{pressure: wire.MemoryPressureCritical}, 2, capacity.Pressure{Memory: 6}, wire.CapacityLevelFull, "memory_pressure"},
	} {
		s := tc.s.withControl(tc.cores, tc.p)
		if level, signal := capacityTarget(s); level != tc.level || signal != tc.signal {
			t.Errorf("%+v %+v: %s %q, want %s %q", tc.s, tc.p, level, signal, tc.level, tc.signal)
		}
	}
	s := capacitySample{}.withControl(2, capacity.Pressure{IO: 16, Memory: 6})
	if w := signalWords("pressure_io", s); w != "disk pressure 16%" {
		t.Error(w)
	}
	if w := signalWords("pressure_memory", s); w != "memory pressure 6%" {
		t.Error(w)
	}
}
