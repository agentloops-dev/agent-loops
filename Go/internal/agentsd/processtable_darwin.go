package agentsd

import (
	"context"

	"golang.org/x/sys/unix"
)

// processTable is every process's parent and name (p_comm, at most 16 bytes), from one
// read of kern.proc.all (#6142). It is made once a pass, and only when the pool has a
// runtime to look at. A process's own look, as gopsutil made it, cost about three sysctl
// calls each: 21% of the agentsd tests' CPU on a Mac with 900 processes (#6131).
func processTable(_ context.Context) map[int32]procInfo {
	out := map[int32]procInfo{}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return out
	}
	for i := range procs {
		k := &procs[i]
		out[k.Proc.P_pid] = procInfo{parent: k.Eproc.Ppid, name: unix.ByteSliceToString(k.Proc.P_comm[:])}
	}
	return out
}
