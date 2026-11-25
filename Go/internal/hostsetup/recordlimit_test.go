package hostsetup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// #7247: a report is cut to its largest, what is running stops at its limit, and how a step ends
// is said after that, up to the file's.
func TestTheLimitsOfTheReports(t *testing.T) {
	t.Parallel()
	l := Layout{Root: t.TempDir()}
	rep, err := NewReporter(l.Root)
	must(t, err)
	step := wire.MachineSetupStepIDProjects
	whole := strings.Repeat("d", detailMost)
	rep.Say(step, wire.MachineSetupStateRunning, whole, "")
	rep.Say(step, wire.MachineSetupStateFailed, whole+"d", strings.Repeat("é", problemMost))
	rs := reports(t, l)
	if len(rs) != 2 || *rs[0].Detail != whole || len(*rs[1].Detail) > detailMost || len(*rs[1].Error) > problemMost ||
		!strings.HasSuffix(*rs[1].Error, "é…") {
		t.Fatalf("%d reports", len(rs))
	}
	// The worst a line can be, escaped.
	rep.Say(step, wire.MachineSetupStateFailed, strings.Repeat("\x01", detailMost), strings.Repeat("\x01", problemMost))
	data, err := os.ReadFile(filepath.Join(l.Root, ReportsFile))
	must(t, err)
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) > 40<<10 {
			t.Fatalf("a line of %d bytes", len(line))
		}
	}
	size := func() int64 {
		info, err := os.Stat(filepath.Join(l.Root, ReportsFile))
		must(t, err)
		return info.Size()
	}
	for i := range 2000 {
		rep.Say(step, wire.MachineSetupStateRunning, strconv.Itoa(i)+whole, "")
	}
	full := size()
	if full > runningMost || full < runningMost-4<<10 {
		t.Fatalf("%d bytes", full)
	}
	rep.Say(step, wire.MachineSetupStateDone, whole, "")
	if size() <= full {
		t.Fatal("the end of the step was not said")
	}
	for i := range 2000 {
		rep.Say(step, wire.MachineSetupStateDone, strconv.Itoa(i)+whole, "")
	}
	if n := size(); n > reportsMost {
		t.Fatalf("%d bytes", n)
	}
}

func TestTheLogOfASetupScriptStopsAtItsLargest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, ".agents"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, SetupScript), []byte("head -c 2000000 /dev/zero | tr '\\0' x | fold -w 1000\n"), 0o600))
	path := filepath.Join(dir, "setup.log")
	f, err := os.Create(path)
	must(t, err)
	_, err = RunScript(context.Background(), dir, "m", false, time.Minute, &scriptLog{file: f, left: scriptLogMost}, nil)
	must(t, err)
	must(t, f.Close())
	info, err := os.Stat(path)
	must(t, err)
	if info.Size() != scriptLogMost {
		t.Fatalf("%d bytes", info.Size())
	}
}

func TestTheSweepOfTheReportsAndTheScriptLogs(t *testing.T) {
	t.Parallel()
	root, now := t.TempDir(), time.Now()
	logs := filepath.Join(root, "logs")
	must(t, os.MkdirAll(logs, 0o700))
	put := func(path string, age time.Duration) {
		must(t, os.WriteFile(path, []byte("x"), 0o600))
		must(t, os.Chtimes(path, now.Add(-age), now.Add(-age)))
	}
	for i := range scriptLogsMost + 1 {
		put(filepath.Join(logs, "setup-"+strconv.Itoa(i)+".log"), time.Duration(i)*time.Second)
	}
	put(filepath.Join(logs, "setup-old.log"), scriptLogAge)
	put(filepath.Join(logs, "daemon.log"), scriptLogAge)
	path := filepath.Join(root, ReportsFile)
	put(path, reportsAge-time.Minute)
	Sweep(root, now)
	if data, _ := os.ReadFile(path); len(data) == 0 {
		t.Fatal("reports inside their age went")
	}
	put(path, reportsAge)
	Sweep(root, now)
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("%q %v", data, err)
	}
	left, _ := filepath.Glob(filepath.Join(logs, "*"))
	if len(left) != scriptLogsMost+1 {
		t.Fatalf("%d logs", len(left))
	}
	for _, gone := range []string{"setup-old.log", "setup-" + strconv.Itoa(scriptLogsMost) + ".log"} {
		if _, err := os.Stat(filepath.Join(logs, gone)); err == nil {
			t.Fatal(gone)
		}
	}
}
