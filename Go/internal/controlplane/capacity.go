package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	runtimemetrics "runtime/metrics"
	"sort"
	"strconv"
	"strings"
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
	s.pressureMemory = readPSIFullAvg60("/proc/pressure/memory")
	s.pressureIO = readPSIFullAvg60("/proc/pressure/io")
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

func readPSIFullAvg60(path string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "full" {
			continue
		}
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if key != "avg60" || !ok {
				continue
			}
			v, err := strconv.ParseFloat(value, 64)
			if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
				return v
			}
		}
	}
	return 0
}

func controlCapacityTarget(s controlCapacity) (wire.CapacityLevel, string) {
	switch {
	case s.load1 > 3:
		return wire.CapacityLevelFull, "load"
	case s.pressureIO > 25:
		return wire.CapacityLevelFull, "pressure_io"
	case s.pressureMemory > 15:
		return wire.CapacityLevelFull, "pressure_memory"
	case s.load1 > 2:
		return wire.CapacityLevelNear, "load"
	case s.pressureIO > 10:
		return wire.CapacityLevelNear, "pressure_io"
	case s.pressureMemory > 5:
		return wire.CapacityLevelNear, "pressure_memory"
	default:
		return wire.CapacityLevelOk, ""
	}
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
		look := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			sample := sampleControlCapacity(ctx)
			cancel()
			target, reason := controlCapacityTarget(sample)
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
			if level != previous {
				wasOK := previous == wire.CapacityLevelOk
				previous = level
				s.capacityChangedAdmins(snapshot)
				if wasOK && level != wire.CapacityLevelOk {
					s.noticeAdmins(wire.ControlNotice{Kind: "control.busy", Words: controlCapacityWords(level, reason)})
				} else if level == wire.CapacityLevelOk {
					s.noticeAdmins(wire.ControlNotice{Kind: "control.ok", Words: "The control plane is back within capacity. See Run the control plane in the cloud: https://docs.agentloops.dev/how-to/run-the-control-plane-in-the-cloud/."})
				}
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
	snapshot := wire.ControlCapacityCopy{Copy: s.copyID, Level: c.level, Reason: controlCapacityReason(c.level, c.reason), LoadPerCore: c.load1, LoadPerCore5: c.load5, PressureMemoryFullAvg60: c.pressureMemory, PressureIOFullAvg60: c.pressureIO, MemoryAvailableBytes: c.memoryAvailable, SwapUsedPercent: c.swapUsed, HeapInuseBytes: c.heapInuse, Goroutines: int64(c.goroutines), OpenFiles: int64(c.openFiles), StoreFiles: files, StoreBytes: bytes, At: wire.DateOf(c.at)}
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
