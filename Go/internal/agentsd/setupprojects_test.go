package agentsd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// setupPlane is the control plane of a pool machine's test, and the machine that has the
// folder: the steps of the get go to this host's own methods.
type setupPlane struct {
	d       *Daemon
	mu      sync.Mutex
	work    wire.MachineSetupWork
	refused error
	packs   []wire.SendPackRequest
	said    []wire.MachineSetupReport
	// asked counts machine/setupWork.
	asked int
}

func (p *setupPlane) ask(_ context.Context, method string, params any) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch method {
	case wire.CallMachineSetupWork.Name:
		p.asked++
		return json.Marshal(p.work)
	case wire.CallMachineSetupStep.Name:
		p.said = append(p.said, params.(wire.MachineSetupReport))
		return json.RawMessage(`{}`), nil
	}
	call, ok := params.(wire.MachineWorkspaceCall)
	if !ok || call.Method != sendFolderMethod {
		return json.RawMessage(`{}`), nil
	}
	if p.refused != nil {
		return nil, p.refused
	}
	var s wire.MachineFolderSend
	b, _ := json.Marshal(call.Params)
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	var out wire.MachineFolderSent
	var err error
	switch {
	case s.Facts != nil:
		out.Facts = &wire.SendFacts{}
		*out.Facts, err = p.d.sendFacts(*s.Facts)
	case s.Pack != nil:
		p.packs = append(p.packs, *s.Pack)
		out.Pack = &wire.SendPack{}
		*out.Pack, err = p.d.sendPack(*s.Pack)
	case s.Read != nil:
		out.Read = &wire.SendRead{}
		*out.Read, err = p.d.sendRead(*s.Read)
	}
	if err != nil {
		code, msg := errMessage(err)
		return nil, &controllink.AskError{Code: code, Message: msg}
	}
	return json.Marshal(out)
}

func (p *setupPlane) set(change func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change()
}

func setupMachine(t *testing.T) (*Daemon, *setupPlane, string, string) {
	t.Helper()
	d, _ := start(t, t.TempDir())
	src := repo(t)
	d.poolHome = canonical(t.TempDir())
	project := wire.NewProjectID()
	p := &setupPlane{d: d, work: wire.MachineSetupWork{Pool: true, Projects: []wire.MachineSetupProject{{Path: src, Project: &project,
		Sources: []wire.MachineSetupSource{{Machine: "mac-id", Name: "Mac", Online: true}}}}}}
	d.SetControlAsk(p.ask)
	return d, p, src, filepath.Join(d.poolHome, filepath.Base(src))
}

func putText(t *testing.T, path, text string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(text), 0o600))
}

func (d *Daemon) getNow() wire.MachineSetupProjects {
	return d.getSetupProjects(context.Background(), 50*time.Millisecond, 5*time.Millisecond, func(string) {})
}

// #6890: a pool machine gets each project at its last commit, in the project's picker; a
// claim brings it the commits since, and leaves what a Send put there.
func TestAPoolMachineGetsItsProjectsAtTheirLastCommit(t *testing.T) {
	t.Parallel()
	d, p, src, here := setupMachine(t)
	defer d.Shutdown()
	putText(t, filepath.Join(src, "notes.txt"), "not committed\n")
	got := d.getNow()
	if got.Problem != nil || !got.Pool || len(got.Folders) != 1 || got.Folders[0] != here {
		t.Fatalf("%s", mustJSON(got))
	}
	if isFile(filepath.Join(here, "notes.txt")) {
		t.Fatal("uncommitted work went to the pool machine")
	}
	p.set(func() {
		if len(p.packs) != 1 || p.packs[0].IncludeChanges {
			t.Fatalf("%+v", p.packs)
		}
	})
	joined := d.joined().Folders
	if len(joined) != 1 || joined[0].Project != *p.work.Projects[0].Project || store.FolderPath(joined[0].Folder) != here {
		t.Fatalf("the folder is not in the project: %+v", joined)
	}

	// The project id that the commit did not name is a change: commit it at the source, as a
	// repository in a project has it.
	must(t, os.MkdirAll(filepath.Join(src, ".agents"), 0o700))
	must(t, os.RemoveAll(filepath.Join(here, ".agents")))
	putText(t, filepath.Join(src, ".agents", "folder.json"), `{"project":"`+*p.work.Projects[0].Project+`"}`)
	sendGit(t, src, nil, "add", "-A")
	sendGit(t, src, nil, "commit", "-q", "-m", "second")
	p.set(func() { p.work.Claimed = true })
	if got = d.getNow(); got.Problem != nil || got.Detail != nil {
		t.Fatalf("%s", mustJSON(got))
	}
	if a, b := sendGit(t, here, nil, "rev-parse", "HEAD"), sendGit(t, src, nil, "rev-parse", "HEAD"); a != b {
		t.Fatalf("a claim brings the commits since: %s, not %s", a, b)
	}

	// A change here stays, and is no failure.
	putText(t, filepath.Join(here, "mine.txt"), "mine\n")
	sendGit(t, src, nil, "commit", "-q", "--allow-empty", "-m", "third")
	if got = d.getNow(); got.Problem != nil || !isFile(filepath.Join(here, "mine.txt")) {
		t.Fatalf("%s", mustJSON(got))
	}
	if d.receiveKeepsOn(t, here) {
		t.Fatal("a get replaced the folder's changes")
	}
}

// receiveKeepsOn says whether a receive that keeps changes went on.
func (d *Daemon) receiveKeepsOn(t *testing.T, folder string) bool {
	t.Helper()
	head := sendGit(t, folder, nil, "rev-parse", "HEAD")
	_, err := d.receiveInto(wire.ReceiveRequest{Folder: folder, Checkout: "main", Branches: []wire.SentBranch{{Name: "main", Commit: head}}}, true)
	return err == nil
}

func isFile(path string) bool {
	fi, err := os.Stat(path) //nolint:gosec // G703: a folder of the test
	return err == nil && !fi.IsDir()
}

// #6890: no get is a failed get with its reason; a claim with the pool's copy goes on.
func TestAPoolMachineSaysWhyAProjectDidNotCome(t *testing.T) {
	t.Parallel()
	d, p, src, _ := setupMachine(t)
	defer d.Shutdown()
	name := filepath.Base(src)
	problem := func(want string) {
		t.Helper()
		if got := d.getNow(); got.Problem == nil || *got.Problem != name+": "+want || len(got.Folders) != 0 {
			t.Fatalf("%s", mustJSON(got))
		}
	}
	source := &p.work.Projects[0].Sources[0]
	p.set(func() { source.Online = false })
	problem("Mac is offline")
	p.set(func() {
		source.Online = true
		p.refused = &controllink.AskError{Code: wire.FailureNotInLock, Message: "not in the lock"}
	})
	problem("Mac: not in the lock")
	p.set(func() { p.refused, p.work.Projects[0].Sources = nil, nil })
	problem("no machine of yours has " + src + " in a project")

	p.set(func() {
		p.work.Projects[0].Sources = []wire.MachineSetupSource{{Machine: "mac-id", Name: "Mac", Online: true}}
	})
	if got := d.getNow(); got.Problem != nil {
		t.Fatalf("%s", mustJSON(got))
	}
	p.set(func() { p.work.Claimed, p.work.Projects[0].Sources[0].Online = true, false })
	got := d.getNow()
	if got.Problem != nil || len(got.Folders) != 1 || got.Detail == nil || *got.Detail != name+" is as the pool got it (Mac is offline)" {
		t.Fatalf("%s", mustJSON(got))
	}
}

// #6896: the host of a claimed machine runs the steps that wait, through its own methods, and
// what it says is after what the installer said. A host that starts again says no line twice.
func TestAClaimedMachinesHostRunsTheStepsThatWait(t *testing.T) {
	t.Parallel()
	d, p, src, _ := setupMachine(t)
	defer d.Shutdown()
	must(t, os.MkdirAll(filepath.Join(src, ".agents"), 0o700))
	putText(t, filepath.Join(src, hostsetup.SetupScript), "exit 3\n")
	sendGit(t, src, nil, "add", "-A")
	sendGit(t, src, nil, "commit", "-q", "-m", "script")
	file := filepath.Join(d.Store.Loc.Root, hostsetup.ReportsFile)
	rep, err := hostsetup.NewReporter(d.Store.Loc.Root)
	must(t, err)
	rep.Say(wire.MachineSetupStepIDProjects, wire.MachineSetupStateDone, "in the pool", "")
	from := wire.MachineSetupStepIDProjects
	p.set(func() { p.work.Claimed, p.work.From = true, &from })
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() { defer close(ended); d.forwardSetupReports(ctx, file, time.Millisecond) }()
	failed := func() bool {
		ok := false
		p.set(func() {
			for _, r := range p.said {
				if r.Step == from && r.State == wire.MachineSetupStateFailed && strings.Contains(*r.Error, "setup.sh failed") {
					ok = true
					p.work.From = nil
				}
			}
		})
		return ok
	}
	waitFor(t, "the failed step 6", failed)
	cancel()
	<-ended
	p.set(func() { p.said = nil })
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	d.forwardSetupReports(ctx, file, time.Millisecond)
	p.set(func() {
		if len(p.said) != 0 {
			t.Fatalf("said again: %+v", p.said)
		}
	})
}

// #7416: the projects step waits while the control plane says that Your set-up is on its way or
// runs, and says its first line only when it goes on.
func TestTheProjectsWaitForYourSetUp(t *testing.T) {
	t.Parallel()
	d, p, _, here := setupMachine(t)
	defer d.Shutdown()
	p.set(func() { p.work.WaitUser = new(true) })
	var said []string
	var saidMu sync.Mutex
	done := make(chan wire.MachineSetupProjects, 1)
	go func() {
		done <- d.getSetupProjects(context.Background(), time.Second, 5*time.Millisecond, func(line string) {
			saidMu.Lock()
			defer saidMu.Unlock()
			said = append(said, line)
		})
	}()
	// It asks again while it waits.
	waitFor(t, "asked three times", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.asked >= 3
	})
	select {
	case got := <-done:
		t.Fatalf("the projects did not wait: %s", mustJSON(got))
	default:
	}
	saidMu.Lock()
	if len(said) != 0 {
		t.Fatalf("the step said %q while it waited", said)
	}
	saidMu.Unlock()
	p.set(func() { p.work.WaitUser = nil })
	got := <-done
	if got.Problem != nil || len(got.Folders) != 1 || got.Folders[0] != here {
		t.Fatalf("%s", mustJSON(got))
	}
	saidMu.Lock()
	defer saidMu.Unlock()
	if len(said) == 0 || said[0] != "Looking for projects" {
		t.Fatalf("%q", said)
	}
}
