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
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// ReportsFile is the file the installer says its steps in, in the host's root.
const ReportsFile = "setup-reports.jsonl"

// The limits of the reports (#7247): each text of a line is cut, and past runningMost only how a
// step ends is kept, up to reportsMost.
const (
	detailMost  = 2 << 10
	problemMost = 4 << 10
	digitsMost  = 256
	reportsMost = 2 << 20
	runningMost = 1 << 20
	reportsAge  = 90 * 24 * time.Hour
)

// The limits of the set-up scripts' logs, setup-<project>.log.
const (
	scriptLogMost  = 1 << 20
	scriptLogsMost = 100
	scriptLogAge   = 7 * 24 * time.Hour
)

// cut is s in at most n bytes.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n-len("…")], "") + "…"
}

// scriptLog keeps the first scriptLogMost bytes written to it.
type scriptLog struct {
	file *os.File
	left int
}

func (l *scriptLog) Write(p []byte) (int, error) {
	n := min(len(p), l.left)
	l.left -= n
	if n > 0 {
		if _, err := l.file.Write(p[:n]); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Sweep holds root's set-up reports and its scripts' logs to their age, and the logs to their
// count, the oldest going first. The reports are emptied and not removed: the host that sends
// them begins again at a shorter file.
func Sweep(root string, now time.Time) {
	path := filepath.Join(root, ReportsFile)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 && now.Sub(info.ModTime()) >= reportsAge {
		if err := os.Truncate(path, 0); err != nil {
			log.Printf("retention: %s: %v", ReportsFile, err)
		}
	}
	paths, _ := filepath.Glob(filepath.Join(root, "logs", "setup-*.log")) // the pattern is a good one
	type aged struct {
		path string
		at   time.Time
	}
	var logs []aged
	for _, p := range paths {
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			logs = append(logs, aged{p, info.ModTime()})
		}
	}
	slices.SortFunc(logs, func(a, b aged) int { return b.at.Compare(a.at) })
	for i, l := range logs {
		if i >= scriptLogsMost || now.Sub(l.at) >= scriptLogAge {
			if err := os.Remove(l.path); err != nil {
				log.Printf("retention: %s: %v", filepath.Base(l.path), err)
			}
		}
	}
}

// Reporter appends steps to the reports file. It is safe for several goroutines (steps 5 and
// 6 run together).
type Reporter struct {
	mu   sync.Mutex
	path string
	last map[wire.MachineSetupStepID]string
	full bool
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
	// store-bounds: not a store: starts the reports again, declared where they are appended
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
	detail, problem = cut(detail, detailMost), cut(problem, problemMost)
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
	detail, digits = cut(detail, detailMost), cut(digits, digitsMost)
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
	// store-bounds: kind=log; size=2MiB; age=90d; applied=on write (what is said of a running step stops at 1MiB, 40KiB a line), each run, and hourly sweep
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	limit := int64(reportsMost)
	if report.State == wire.MachineSetupStateRunning {
		limit = runningMost
	}
	if info.Size()+int64(len(line))+1 > limit {
		if !r.full {
			r.full = true
			log.Printf("setup: %s is full; not every step's progress is said", ReportsFile)
		}
		return
	}
	_, _ = f.Write(append(line, '\n'))
}
