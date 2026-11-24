// Package capacity contains the shared capacity-level hysteresis.
package capacity

import "github.com/agentloops-dev/agent-loops/Go/wire"

const LooksToFall = 2

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
