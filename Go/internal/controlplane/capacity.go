package controlplane

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	runtimemetrics "runtime/metrics"
	"strconv"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/capacity"
	"github.com/agentloops-dev/agent-loops/Go/wire"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

const controlCapacityEvery = 15 * time.Second

type controlCapacity struct {
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
	s := controlCapacity{level: wire.CapacityLevelOk}
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
	go func() {
		t := time.NewTicker(controlCapacityEvery)
		defer t.Stop()
		previous := wire.CapacityLevelOk
		var lastBelow wire.CapacityLevel
		below := 0
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				sample := sampleControlCapacity(ctx)
				cancel()
				target, reason := controlCapacityTarget(sample)
				level, nextLastBelow, nextBelow := capacity.Next(previous, target, lastBelow, below)
				lastBelow, below = nextLastBelow, nextBelow
				sample.level = level
				if level != target {
					reason = "hysteresis"
				}
				sample.reason = reason
				s.metrics.setCapacity(sample)
				if level != previous {
					previous = level
					s.log(fmt.Sprintf("control capacity: %s (reason %s; load %.2f per core; memory PSI full avg60 %.2f; I/O PSI full avg60 %.2f)",
						level, reason, sample.load1, sample.pressureMemory, sample.pressureIO))
				}
			}
		}
	}()
}
