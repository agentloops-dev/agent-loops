package agentsd

import (
	"context"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/process"
)

// TestProcessTableAgreesWithGopsutil: the one read of the table (#6142) gives the parent
// and name that gopsutil's look at each process gave, for this test process and a child.
func TestProcessTableAgreesWithGopsutil(t *testing.T) {
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })

	self, kid := pid32(t, os.Getpid()), pid32(t, child.Process.Pid)
	table := processTable(context.Background())
	for _, pid := range []int32{self, kid} {
		p, err := process.NewProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := p.Ppid()
		if err != nil {
			t.Fatal(err)
		}
		name, err := p.Name()
		if err != nil {
			t.Fatal(err)
		}
		got, ok := table[pid]
		if !ok {
			t.Fatalf("pid %d is not in the table", pid)
		}
		if got.parent != parent {
			t.Errorf("pid %d: parent %d, gopsutil says %d", pid, got.parent, parent)
		}
		// gopsutil lengthens a name of 15 bytes or more from the executable's path.
		if got.name == "" || !strings.HasPrefix(name, got.name) {
			t.Errorf("pid %d: name %q, gopsutil says %q", pid, got.name, name)
		}
	}
	if got := table[kid]; got.parent != self || got.name != "sleep" {
		t.Errorf("child: %+v, want the parent %d and the name sleep", got, self)
	}
	if d := descendantsIn(table, self); !d[kid] {
		t.Errorf("descendants %v leave out the child %d", d, kid)
	}
}

// pid32 is a pid as the table keys it.
func pid32(t *testing.T, pid int) int32 {
	t.Helper()
	if pid >= 0 && pid <= math.MaxInt32 {
		return int32(pid)
	}
	t.Fatalf("pid %d does not fit in an int32", pid)
	return 0
}

func BenchmarkProcessTable(b *testing.B) {
	for b.Loop() {
		processTable(context.Background())
	}
}
