package agentsd

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// A pool machine gets the pool's projects itself (#6890): it asks the control plane which
// folders, and which of the person's machines have them, and pulls each at its last commit
// with the steps of bring_back (bringthere.go). The control plane cannot pull or push one: a
// machine that holds its person's lock takes folders/sendPack only as another machine of the
// lock signed it (lockcheck.go), and this one is in the lock.
//
// When a person claims it, this host runs steps 6 to 8 of its set-up (#6896), as the control
// plane says in machine/setupWork.

const (
	// setupProjectsPatience is how long a get waits for a machine that has the folder. It is
	// less than the control plane's limit for a pool machine's set-up (poolJoinGiveUp, 30
	// min), so the step fails with the reason.
	setupProjectsPatience = 20 * time.Minute
	// setupProjectsEvery is how often it looks again for that machine.
	setupProjectsEvery = 5 * time.Second
	// setupWorkEvery is how often a pool machine asks if it was claimed, until it is ready.
	setupWorkEvery = 5 * time.Second
)

type machineSetupBook struct {
	poolGetMu sync.Mutex
	// setupGet is the get under way, or ended and not yet said; nil for none.
	setupGet *wire.MachineSetupProjects
	// setupRuns says this host runs steps of its own set-up now.
	setupRuns atomic.Bool
	// poolHome is the folder the projects go in; "" is the home folder. A test sets it.
	poolHome string
}

func (d *Daemon) poolFolder(name string) (string, error) {
	home := d.poolHome
	if home == "" {
		home = "~"
	}
	return receiveFolder(home + "/" + name)
}

// setupProjects is setup/projects: it starts a get when none is under way, and says how it
// stands. An end is said once; the next call begins again.
func (d *Daemon) setupProjects() wire.MachineSetupProjects {
	d.poolGetMu.Lock()
	defer d.poolGetMu.Unlock()
	if d.setupGet == nil {
		get := &wire.MachineSetupProjects{Folders: []string{}}
		d.setupGet = get
		d.goJob("set-up projects", func() {
			done := d.getSetupProjects(d.hostCtx(), setupProjectsPatience, setupProjectsEvery, func(detail string) {
				d.poolGetMu.Lock()
				defer d.poolGetMu.Unlock()
				get.Pool, get.Detail = true, &detail
			})
			d.poolGetMu.Lock()
			defer d.poolGetMu.Unlock()
			done.Finished = true
			*get = done
		})
	}
	out := *d.setupGet
	if out.Finished {
		d.setupGet = nil
	}
	return out
}

func (d *Daemon) askSetupWork(ctx context.Context) (wire.MachineSetupWork, error) {
	var work wire.MachineSetupWork
	ask := d.askControl.Load()
	if ask == nil {
		return work, errors.New("this machine is not connected to the control plane")
	}
	ctx, cancel := context.WithTimeout(ctx, reportPatience)
	defer cancel()
	raw, err := (*ask)(ctx, wire.CallMachineSetupWork.Name, wire.Empty{})
	if err != nil {
		return work, err
	}
	return work, json.Unmarshal(raw, &work)
}

// noSetupWork says err is from a control plane that has no machine/setupWork.
func noSetupWork(err error) bool {
	var refused *controllink.AskError
	return errors.As(err, &refused) && refused.Code == CodeMethodNotFound
}

// getSetupProjects gets every project of the machine's pool. A project that cannot be got
// does not stop the others.
func (d *Daemon) getSetupProjects(ctx context.Context, patience, every time.Duration, say func(string)) wire.MachineSetupProjects {
	out := wire.MachineSetupProjects{Folders: []string{}}
	problem := func(words string) wire.MachineSetupProjects {
		out.Problem = &words
		return out
	}
	deadline := time.Now().Add(patience)
	pause := func() bool {
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
			return true
		}
	}
	work, err := d.askSetupWork(ctx)
	for err != nil && !noSetupWork(err) && pause() {
		work, err = d.askSetupWork(ctx)
	}
	switch {
	case noSetupWork(err):
		return out
	case err != nil:
		return problem("The control plane did not say which projects to get: " + err.Error())
	}
	out.Pool = work.Pool
	var problems, notes []string
	for i := range work.Projects {
		name := filepath.Base(work.Projects[i].Path)
		folder, err := d.poolFolder(name)
		if err != nil {
			_, why := errMessage(err)
			problems = append(problems, name+": "+why)
			continue
		}
		say("Getting " + name)
		err = d.getOrWait(ctx, &work, i, folder, say, pause)
		why := ""
		if err != nil {
			_, why = errMessage(err)
		}
		switch {
		case err == nil:
			out.Folders = append(out.Folders, folder)
		case work.Claimed && isDir(filepath.Join(folder, ".git")):
			// The pool's own copy is here: a claim does not wait for a newer one.
			out.Folders = append(out.Folders, folder)
			notes = append(notes, name+" is as the pool got it ("+why+")")
		default:
			problems = append(problems, name+": "+why)
		}
	}
	if len(notes) > 0 {
		detail := strings.Join(notes, "; ")
		out.Detail = &detail
	}
	if len(problems) > 0 {
		return problem(strings.Join(problems, "\n"))
	}
	return out
}

// getOrWait gets work's project i into folder. It waits for a source, but not on a claim that
// has the pool's copy.
func (d *Daemon) getOrWait(ctx context.Context, work *wire.MachineSetupWork, i int, folder string, say func(string), pause func() bool) error {
	for {
		wait, err := d.getSetupProject(work.Projects[i], folder, work.Claimed)
		if err == nil || !wait || work.Claimed && isDir(filepath.Join(folder, ".git")) || !pause() {
			return err
		}
		_, why := errMessage(err)
		say("Waiting for " + filepath.Base(folder) + ": " + why)
		// Which machines are online changes while this waits.
		if again, err := d.askSetupWork(ctx); err == nil && len(again.Projects) == len(work.Projects) {
			*work = again
		}
	}
}

// getSetupProject gets one project into folder from the first of its sources that gives it.
// wait says that the error can go away without a change: a source is offline, or this
// machine is not in its lock yet.
func (d *Daemon) getSetupProject(p wire.MachineSetupProject, folder string, claimed bool) (wait bool, err error) {
	if len(p.Sources) == 0 {
		return false, Invalid("no machine of yours has %s in a project", p.Path)
	}
	for _, s := range p.Sources {
		if !s.Online {
			if err == nil {
				wait, err = true, Invalid("%s is offline", s.Name)
			}
			continue
		}
		got := d.getFolderFrom(string(s.Machine), p, folder, claimed)
		if got == nil {
			return false, nil
		}
		code, msg := errMessage(got)
		wait = wait || code == wire.FailureHostOffline || code == wire.FailureNotInLock
		err = Invalid("%s: %s", s.Name, msg)
	}
	return wait, err
}

// getFolderFrom brings the branch that p's folder on machine is on into folder here, at its
// last commit: never with uncommitted changes. With keepChanges it leaves a folder that has
// changes as it is, also those a Send to ‹machine›… put there: they are newer.
func (d *Daemon) getFolderFrom(machine string, p wire.MachineSetupProject, folder string, keepChanges bool) error {
	state, err := d.receiveState(wire.ReceiveStateRequest{Folder: folder})
	if err != nil {
		return err
	}
	if state.Repository && keepChanges {
		if changed, _, err := changes(folder); err != nil || changed {
			return err
		}
	}
	receive, err := d.packedThere(machine, p.Path, folder, state.Heads)
	if err != nil {
		return err
	}
	if _, err := d.receiveInto(receive, keepChanges); err != nil {
		if errors.Is(err, errFolderChanged) {
			return nil
		}
		return err
	}
	// The project id that the source joined, when the last commit does not name it: else the
	// machine is not in that project's picker.
	if p.Project != nil && *p.Project != "" && projectOfFolder(folder) != *p.Project {
		if _, err := d.addFolderOf(store.FolderURL(folder), false, *p.Project); err != nil {
			return err
		}
		if now, err := worktreeFingerprint(folder); err == nil {
			if err := os.WriteFile(sentMark(folder), []byte(now+"\n"), 0o600); err != nil {
				log.Printf("set-up: could not mark %s as sent: %v", folder, err)
			}
		}
	}
	return nil
}

// packedThere has machine pack the branch that its folder at path is on, less heads, reads it
// here, and is the receive that puts it in folder.
func (d *Daemon) packedThere(machine, path, folder string, heads []string) (receive wire.ReceiveRequest, err error) {
	there := store.PathURL(path)
	var sent wire.MachineFolderSent
	if err := d.askSend(personSending, wire.MachineFolderSend{Host: machine, Facts: &wire.SendFactsRequest{Folder: there}}, &sent); err != nil {
		return receive, err
	}
	if sent.Facts == nil {
		return receive, Invalid("the control plane's answer could not be read")
	}
	branch := ""
	if sent.Facts.Current != nil {
		branch = *sent.Facts.Current
	} else if len(sent.Facts.Branches) > 0 {
		branch = sent.Facts.Branches[0]
	}
	if branch == "" {
		return receive, Invalid("%s has no commit", path)
	}
	origin := sent.Facts.Origin
	pack := wire.SendPackRequest{Folder: there, Branches: []string{branch}, LeavingOut: heads}
	sent = wire.MachineFolderSent{}
	if err := d.askSend(personSending, wire.MachineFolderSend{Host: machine, Pack: &pack}, &sent); err != nil {
		return receive, err
	}
	if sent.Pack == nil {
		return receive, Invalid("the control plane's answer could not be read")
	}
	receive = wire.ReceiveRequest{Folder: folder, Branches: sent.Pack.Commits, Checkout: branch, Origin: origin}
	if sent.Pack.Bundle != nil {
		id := store.NewUUID()
		if err := d.readThere(personSending, machine, sent.Pack.Bundle.ID, id, nil); err != nil {
			if file, gone := d.incomingFile(id); gone == nil {
				_ = os.Remove(file) // the sweep takes what is left
			}
			return receive, err
		}
		receive.Bundle = &id
	}
	return receive, nil
}

// continueSetup asks what the machine's set-up has left, and runs it. It says false when
// nothing is left to ask for: no pool made the machine, or it is ready.
func (d *Daemon) continueSetup(ctx context.Context) bool {
	if d.setupRuns.Load() {
		return true
	}
	work, err := d.askSetupWork(ctx)
	if err != nil {
		return !noSetupWork(err)
	}
	if !work.Pool || work.Done {
		return false
	}
	if !work.Claimed || work.From == nil || d.setupRuns.Swap(true) {
		return true
	}
	from := *work.From
	d.goJob("set-up steps", func() {
		defer d.setupRuns.Store(false)
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("set-up: no home folder: %v", err)
			return
		}
		o := hostsetup.Options{Name: d.ownName(), Home: home, FromPool: true}
		for _, p := range work.Projects {
			if folder, err := d.poolFolder(filepath.Base(p.Path)); err == nil && isDir(folder) {
				o.Projects = append(o.Projects, folder)
			}
		}
		deps := hostsetup.Deps{Call: d.callOwn}
		if err := hostsetup.Continue(d.hostCtx(), o, deps, d.Store.Loc.Root, from); err != nil {
			log.Printf("set-up: from %s: %v", from, err)
		}
	})
	return true
}

// ownConn is the connection of a call this host makes of itself: nothing is written to it.
type ownConn struct{}

func (ownConn) Write(string) error { return nil }
func (ownConn) In() <-chan string  { return nil }
func (ownConn) Close()             {}
func (ownConn) Device() string     { return "" }

// callOwn is one call of this host's own methods, as its installer makes on daemon.sock.
func (d *Daemon) callOwn(method string, params any) (json.RawMessage, error) {
	h := d.handlers[method]
	if h == nil {
		return nil, Invalid("there is no method %s", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	out, err := h(d.hostCtx(), &Client{ID: nextClient.Add(1), Role: RoleControl, conn: ownConn{}, d: d}, raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (d *Daemon) registerSetupProjects() {
	On(d, wire.CallSetupProjects, func(context.Context, *Client, wire.Empty) (wire.MachineSetupProjects, error) {
		return d.setupProjects(), nil
	})
}
