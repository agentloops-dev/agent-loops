//go:build race

package controlplane

// raceEnabled says the race detector is on. It slows each memory access by several
// times, so a test holds no wall-clock budget under it (#5911).
const raceEnabled = true
