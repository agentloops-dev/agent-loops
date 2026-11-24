// Package hostsetup is `agentsd install --join` (#5356, epic #5353): the one installer that
// sets a machine up. It runs steps 3 to 6 of the machine set-up spec on the machine (install
// Agents, compare digits, install runtimes, send and prepare projects, 5 and 6 together) and
// says each step as it starts, goes on and ends. It is safe to run again: it does only what
// is missing.
//
// It says each step by appending a line to <root>/setup-reports.jsonl. The host, once it has
// joined, sends each line to the control plane as machine/setupStep (agentsd's
// setupforward.go), so a step said before the host had joined is said when it has.
package hostsetup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// ReportsFile is the file the installer says its steps in, in the host's root.
const ReportsFile = "setup-reports.jsonl"

// Reporter appends steps to the reports file. It is safe for several goroutines (steps 5 and
// 6 run together).
type Reporter struct {
	mu   sync.Mutex
	path string
	last map[wire.MachineSetupStepID]string
	// ctx, when set, ends the run with the host that runs it (Continue): what fails because
	// it ended is not the step's failure, and the next host runs the step again.
	ctx context.Context
}

// NewReporter starts a run's reports afresh: lines of an earlier run are not said again.
func NewReporter(root string) (*Reporter, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ReportsFile)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return nil, err
	}
	return &Reporter{path: path, last: map[wire.MachineSetupStepID]string{}}, nil
}

// moreReports goes on saying steps after those said already, for a run inside the host.
func moreReports(ctx context.Context, root string) *Reporter {
	return &Reporter{path: filepath.Join(root, ReportsFile), last: map[wire.MachineSetupStepID]string{}, ctx: ctx}
}

// Say records one step's state. The same words said twice in a row are said once.
func (r *Reporter) Say(step wire.MachineSetupStepID, state wire.MachineSetupState, detail, problem string) {
	report := wire.MachineSetupReport{Step: step, State: state}
	if detail != "" {
		report.Detail = &detail
	}
	if problem != "" {
		report.Error = &problem
	}
	r.say(report)
}

// Compared says step 4 is done with the digits this machine shows for its join code (#5359,
// #5765): the control plane compares them with the ones it made for a machine it made.
func (r *Reporter) Compared(detail, digits string) {
	report := wire.MachineSetupReport{Step: wire.MachineSetupStepIDCompare, State: wire.MachineSetupStateDone, Detail: &detail}
	if digits != "" {
		report.Code = &digits
	}
	r.say(report)
}

func (r *Reporter) say(report wire.MachineSetupReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx != nil && r.ctx.Err() != nil {
		return
	}
	step := report.Step
	line, err := json.Marshal(report)
	if err != nil || r.last[step] == string(line) {
		return
	}
	r.last[step] = string(line)
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
