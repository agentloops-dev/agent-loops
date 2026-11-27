package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	runtimemetrics "runtime/metrics"
	"sort"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/capacity"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
	"github.com/agentloops-dev/agent-loops/Go/wire"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

const controlCapacityEvery = 15 * time.Second

// Control capacity samples are live telemetry, outside v1/ so a handover or backup never
// treats them as durable store records. A copy that crashed leaves its sample, so the hourly
// sweep deletes one not written again for controlCapacityKept.
// store-bounds: kind=key-value; count=100; size=4KiB a record; age=1d; applied=on write (refused), one a copy, when the copy stops, and the hourly sweep
const controlCapacityPrefix = "runtime/control-capacity/"

const (
	controlCapacityMost   = 100
	controlCapacityRecord = 4 << 10
	controlCapacityKept   = 24 * time.Hour
)

func controlCapacityKey(copy string) string { return controlCapacityPrefix + copy + ".json" }

type controlCapacity struct {
	at                         time.Time
	level                      wire.CapacityLevel
	reason                     string
	load1, load5               float64
	pressureMemory, pressureIO float64
	memoryAvailable, swapUsed  float64
	heapInuse                  float64
	goroutines, openFiles      int
}

// sampleControlCapacity reads local machine and process signals. It makes no network calls.
func sampleControlCapacity(ctx context.Context) controlCapacity {
	s := controlCapacity{level: wire.CapacityLevelOk, at: time.Now().UTC()}
	cores := float64(max(1, runtime.NumCPU()))
	if avg, err := load.AvgWithContext(ctx); err == nil {
		s.load1, s.load5 = avg.Load1/cores, avg.Load5/cores
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		s.memoryAvailable = float64(vm.Available)
	}
	if swap, err := mem.SwapMemoryWithContext(ctx); err == nil {
		s.swapUsed = swap.UsedPercent
	}
	p := capacity.ReadPressure()
	s.pressureMemory, s.pressureIO = p.Memory, p.IO
	var sample [1]runtimemetrics.Sample
	sample[0].Name = "/memory/classes/heap/objects:bytes"
	runtimemetrics.Read(sample[:])
	s.heapInuse = float64(sample[0].Value.Uint64())
	s.goroutines = runtime.NumGoroutine()
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		s.openFiles = len(entries)
	}
	s.at = time.Now().UTC()
	return s
}

func (s *Service) startCapacityMonitor() {
	s.sampling.Add(1)
	go func() {
		defer s.sampling.Done()
		t := time.NewTicker(controlCapacityEvery)
		defer t.Stop()
		previous := wire.CapacityLevelOk
		lastReason := ""
		var lastBelow wire.CapacityLevel
		below := 0
		var notices capacityNotices
		look := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			sample := sampleControlCapacity(ctx)
			cancel()
			target, reason := capacity.ControlTarget(sample.load1, capacity.Pressure{Memory: sample.pressureMemory, IO: sample.pressureIO})
			if reason != "" {
				lastReason = reason
			}
			level, nextLastBelow, nextBelow := capacity.Next(previous, target, lastBelow, below)
			lastBelow, below = nextLastBelow, nextBelow
			sample.level = level
			if level != wire.CapacityLevelOk && reason == "" {
				reason = lastReason
			}
			if level == wire.CapacityLevelOk {
				lastReason = ""
			}
			sample.reason = reason
			s.metrics.setCapacity(sample)
			snapshot := s.keepControlCapacity(sample)
			switch notices.look(level, sample.at) {
			case "control.busy":
				s.noticeAdmins(wire.ControlNotice{Kind: "control.busy", Words: controlCapacityWords(level, reason)})
			case "control.ok":
				s.noticeAdmins(wire.ControlNotice{Kind: "control.ok", Words: "The control plane is back within capacity. See Run the control plane in the cloud: https://docs.agentloops.dev/how-to/run-the-control-plane-in-the-cloud/."})
			}
			if level != previous {
				previous = level
				s.capacityChangedAdmins(snapshot)
				s.log(fmt.Sprintf("control capacity: %s (reason %s; load %.2f per core; memory PSI full avg60 %.2f; I/O PSI full avg60 %.2f)",
					level, reason, sample.load1, sample.pressureMemory, sample.pressureIO))
			}
		}
		look()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				look()
			}
		}
	}()
}

// controlNoticeAfter is how long a level must stay before the admins get a notice of it (#7367).
// The level itself still changes at once (the strip and the rows follow it), but a spiky load
// changed it 8 times in 13 minutes (#7354), so a notice on each change came every few minutes.
const controlNoticeAfter = 5 * time.Minute

// capacityNotices says which notice, if any, one look of the level gives (#7367):
// control.busy when the level has stayed above ok for controlNoticeAfter, and control.ok
// when it has stayed ok for controlNoticeAfter after a control.busy was sent.
type capacityNotices struct {
	since time.Time // when the level last went from ok to above ok, or back; zero at the start
	above bool      // the level is above ok
	busy  bool      // a control.busy was sent and no control.ok after it
}

func (n *capacityNotices) look(level wire.CapacityLevel, at time.Time) string {
	above := level != wire.CapacityLevelOk
	if above != n.above || n.since.IsZero() {
		n.above, n.since = above, at
	}
	if at.Sub(n.since) < controlNoticeAfter {
		return ""
	}
	switch {
	case above && !n.busy:
		n.busy = true
		return "control.busy"
	case !above && n.busy:
		n.busy = false
		return "control.ok"
	}
	return ""
}

func controlCapacityWords(level wire.CapacityLevel, reason string) string {
	why := controlCapacityWhy(reason)
	state := "near capacity"
	if level == wire.CapacityLevelFull {
		state = "at capacity"
	}
	return fmt.Sprintf("The control plane is %s: %s. See Run the control plane in the cloud: https://docs.agentloops.dev/how-to/run-the-control-plane-in-the-cloud/ for steps to use a larger machine, more memory, a faster disk, or a store sweep.", state, why)
}

func controlCapacityReason(level wire.CapacityLevel, reason string) string {
	if level == wire.CapacityLevelOk {
		return "the control plane is within capacity"
	}
	state := "near capacity"
	if level == wire.CapacityLevelFull {
		state = "at capacity"
	}
	return fmt.Sprintf("the control plane is %s: %s", state, controlCapacityWhy(reason))
}

func controlCapacityWhy(reason string) string {
	if why := map[string]string{"load": "its load is high", "pressure_io": "its disk is busy", "pressure_memory": "its memory is under pressure", "hysteresis": "its capacity signal is still high"}[reason]; why != "" {
		return why
	}
	return "one of its system signals is high"
}

func (s *Service) keepControlCapacity(c controlCapacity) wire.ControlCapacityCopy {
	files, bytes := s.metrics.storeSize()
	snapshot := wire.ControlCapacityCopy{Copy: s.copyID, Machine: optional(s.controlMachineID()), Level: c.level, Reason: controlCapacityReason(c.level, c.reason), LoadPerCore: c.load1, LoadPerCore5: c.load5, PressureMemoryFullAvg60: c.pressureMemory, PressureIOFullAvg60: c.pressureIO, MemoryAvailableBytes: c.memoryAvailable, SwapUsedPercent: c.swapUsed, HeapInuseBytes: c.heapInuse, Goroutines: int64(c.goroutines), OpenFiles: int64(c.openFiles), StoreFiles: files, StoreBytes: bytes, At: wire.DateOf(c.at)}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return snapshot
	}
	if len(data) > controlCapacityRecord {
		s.log(fmt.Sprintf("capacity snapshot not kept: it is over %d bytes", controlCapacityRecord))
		return snapshot
	}
	if _, err := s.cfg.Store.Put(context.Background(), controlCapacityKey(s.copyID), data, store.Always); err != nil {
		s.log("capacity snapshot could not be kept: " + err.Error())
	}
	return snapshot
}

// sweepControlCapacity deletes the samples of copies that stopped without deleting them: those
// of controlCapacityKept ago, those it cannot read, and the oldest over controlCapacityMost.
func sweepControlCapacity(ctx context.Context, st store.Store, now time.Time) error {
	keys, err := st.List(ctx, controlCapacityPrefix)
	if err != nil {
		return err
	}
	type sample struct {
		key string
		at  time.Time
	}
	var kept []sample
	for _, k := range keys {
		object, err := st.Get(ctx, k.Key)
		if err != nil {
			return err
		}
		if object == nil {
			continue
		}
		var c wire.ControlCapacityCopy
		if json.Unmarshal(object.Data, &c) != nil || now.Sub(c.At.Time()) > controlCapacityKept {
			if err := st.Delete(ctx, k.Key); err != nil {
				return err
			}
			continue
		}
		kept = append(kept, sample{k.Key, c.At.Time()})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].at.Before(kept[j].at) })
	for _, c := range kept[:max(0, len(kept)-controlCapacityMost)] {
		if err := st.Delete(ctx, c.key); err != nil {
			return err
		}
	}
	return nil
}
