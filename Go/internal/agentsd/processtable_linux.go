package agentsd

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// processTable is every process's parent and name (comm), from each /proc/<pid>/stat: one
// read a process, where gopsutil made several (#6142). It is made once a pass, and only
// when the pool has a runtime to look at.
func processTable(_ context.Context) map[int32]procInfo {
	out := map[int32]procInfo{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		pid, err := strconv.ParseInt(e.Name(), 10, 32)
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // it ended
		}
		if p, ok := parseProcStat(string(b)); ok {
			out[int32(pid)] = p
		}
	}
	return out
}

// parseProcStat reads "pid (comm) state ppid …". comm can hold spaces and parentheses, so
// it ends at the last ')'.
func parseProcStat(stat string) (procInfo, bool) {
	open, end := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return procInfo{}, false
	}
	rest := strings.Fields(stat[end+1:])
	if len(rest) < 2 {
		return procInfo{}, false
	}
	parent, err := strconv.ParseInt(rest[1], 10, 32)
	if err != nil {
		return procInfo{}, false
	}
	return procInfo{parent: int32(parent), name: stat[open+1 : end]}, true
}
