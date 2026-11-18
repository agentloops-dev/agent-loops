//go:build !darwin && !linux

package agentsd

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// processTable is every process's parent and name. A look costs a few milliseconds, so it
// is made once a pass, and only when the pool has a runtime to look at.
func processTable(ctx context.Context) map[int32]procInfo {
	out := map[int32]procInfo{}
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return out
	}
	for _, pid := range pids {
		p, err := process.NewProcessWithContext(ctx, pid)
		if err != nil {
			continue
		}
		parent, err := p.PpidWithContext(ctx)
		if err != nil {
			continue
		}
		name, _ := p.NameWithContext(ctx)
		out[pid] = procInfo{parent: parent, name: name}
	}
	return out
}
