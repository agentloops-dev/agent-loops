// Package capacity contains the capacity level that a host and the control plane share: its lines
// and its hysteresis.
package capacity

import (
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

const LooksToFall = 2

// The load lines, per core (#4583): near is about two builds' load, full about three. A
// machine near or full is ok again only under OkLoad (#6953), well under NearLoad, so a load
// that hovers at the near line does not flap between machine.busy and machine.ok. 1.5 is a
// first value; #4583 has the calibration data.
const (
	OkLoad   = 1.5
	NearLoad = 2.0
	FullLoad = 3.0
)

// Higher is the higher of two levels: a machine with both roles has the higher of its roles'
// levels (#6613).
func Higher(a, b wire.CapacityLevel) wire.CapacityLevel {
	if Rank(b) > Rank(a) {
		return b
	}
	return a
}

// Pressure is Linux's PSI "full avg60" of the memory and the disk: 0 where the kernel has none.
type Pressure struct{ Memory, IO float64 }

// ReadPressure reads the kernel's pressure.
func ReadPressure() Pressure {
	return Pressure{Memory: ReadPSIFullAvg60("/proc/pressure/memory"), IO: ReadPSIFullAvg60("/proc/pressure/io")}
}

// ControlTarget is the level that one look gives the control plane, and the signal that gave
// it ("" for ok). load is per core.
func ControlTarget(load float64, p Pressure) (wire.CapacityLevel, string) {
	switch {
	case load > FullLoad:
		return wire.CapacityLevelFull, "load"
	case p.IO > 25:
		return wire.CapacityLevelFull, "pressure_io"
	case p.Memory > 15:
		return wire.CapacityLevelFull, "pressure_memory"
	case load > NearLoad:
		return wire.CapacityLevelNear, "load"
	case p.IO > 10:
		return wire.CapacityLevelNear, "pressure_io"
	case p.Memory > 5:
		return wire.CapacityLevelNear, "pressure_memory"
	}
	return wire.CapacityLevelOk, ""
}

// Rank orders levels so callers can compare them without duplicating the mapping.
func Rank(level wire.CapacityLevel) int {
	switch level {
	case wire.CapacityLevelNear:
		return 1
	case wire.CapacityLevelFull:
		return 2
	default:
		return 0
	}
}

// Next raises to target at once and falls only after two consecutive looks below level.
func Next(was, target, lastBelow wire.CapacityLevel, below int) (level, nextLastBelow wire.CapacityLevel, nextBelow int) {
	if was == "" {
		was = wire.CapacityLevelOk
	}
	switch {
	case Rank(target) >= Rank(was):
		return target, "", 0
	case below+1 >= LooksToFall:
		if Rank(lastBelow) > Rank(target) {
			target = lastBelow
		}
		return target, "", 0
	default:
		return was, target, below + 1
	}
}

// ReadPSIFullAvg60 is the "full avg60" of one PSI file, 0 when it does not say.
func ReadPSIFullAvg60(path string) float64 {
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
