package agentsd

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// setupForwardEvery is how often the host looks for a step the installer has said.
const setupForwardEvery = 2 * time.Second

// setupForwardTries is how often one report is sent again when the control plane did not take
// it, before it is let go: a report refused for good (a step said twice) is not worth more.
const setupForwardTries = 5

// StartSetupReports sends each step `agentsd install --join` says (setup-reports.jsonl in the
// root, #5356) to the control plane as machine/setupStep. The installer starts this host, so
// what it said before the host had joined is sent when the host has.
func (d *Daemon) StartSetupReports() {
	d.resumeUserSetup()
	d.goJob("setup reports", func() {
		d.forwardSetupReports(d.ctx, filepath.Join(d.Store.Loc.Root, hostsetup.ReportsFile), setupForwardEvery)
	})
}

// forwardSetupReports tails file until ctx ends.
func (d *Daemon) forwardSetupReports(ctx context.Context, file string, every time.Duration) {
	// A host that starts again says no line twice (#6896): an old "projects done" must not
	// end the step of a claim.
	offset := sentReports(file)
	tries := 0
	prelude := offset > 0
	watch, next := true, time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		ask := d.askControl.Load()
		if ask == nil {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		before := offset
		if int64(len(data)) < offset {
			// The installer began a new run.
			offset, prelude = 0, false
			watch = true
		}
		offset, tries, prelude = d.forwardSetupReportLines(ctx, *ask, data, offset, tries, prelude)
		if offset != before {
			if err := store.WriteAtomically(file+".sent", []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
				log.Printf("setup: how far the reports were sent could not be kept: %v", err)
			}
		}
		// Asked only once everything said is sent, so the answer knows of it.
		if watch && offset == int64(len(data)) && time.Now().After(next) {
			watch = d.continueSetup(ctx)
			next = time.Now().Add(setupWorkEvery)
		}
	}
}

// sentReports is how much of file an earlier run of this host sent.
func sentReports(file string) int64 {
	b, err := os.ReadFile(file + ".sent")
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// forwardSetupReportLines sends each whole line of data after offset to the control
// plane. It returns the new offset, the count of failed tries and whether the prelude
// was sent. It stops at a part line, or at a line the control plane did not take yet.
func (d *Daemon) forwardSetupReportLines(ctx context.Context, ask askControl, data []byte, offset int64, tries int, prelude bool) (int64, int, bool) {
	for {
		rest := data[offset:]
		end := bytes.IndexByte(rest, '\n')
		if end < 0 {
			return offset, tries, prelude
		}
		var report wire.MachineSetupReport
		if json.Unmarshal(rest[:end], &report) != nil {
			offset += int64(end) + 1
			continue
		}
		// Your set-up and root set-up are the host's own steps (#7155): a machine that joined by
		// hand, with no installer, says them with no create or join before them.
		if !prelude && report.Step != wire.MachineSetupStepIDUser && report.Step != wire.MachineSetupStepIDRoot {
			// This host has joined, so its machine was made and reached: an installer
			// pasted on a machine the control plane did not make says so too. A step said
			// again, or done already, is not an error to the control plane.
			for _, step := range []wire.MachineSetupStepID{wire.MachineSetupStepIDCreate, wire.MachineSetupStepIDJoin} {
				_, _ = ask(ctx, wire.CallMachineSetupStep.Name, wire.MachineSetupReport{Step: step, State: wire.MachineSetupStateDone})
			}
			prelude = true
		}
		if _, err := ask(ctx, wire.CallMachineSetupStep.Name, report); err != nil {
			if tries++; tries < setupForwardTries {
				return offset, tries, prelude // the same line again at the next look
			}
			log.Printf("setup: step %s was not taken by the control plane: %v", report.Step, err)
		}
		tries = 0
		offset += int64(end) + 1
	}
}
