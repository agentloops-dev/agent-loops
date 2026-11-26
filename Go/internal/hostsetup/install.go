package hostsetup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// DefaultRuntimes are the runtimes installed when none is named: the two the host installs
// from the Node toolsets the control plane carries.
var DefaultRuntimes = []string{"claude", "codex"}

// Options is one run of the installer.
type Options struct {
	// Join is the host code (agents-control:…) the control plane made for this machine.
	Join string
	// Name is this machine's name; the host's name and AGENTS_MACHINE.
	Name string
	// Home is the machine user's home; the host lives in <Home>/.agents-server.
	Home string
	// Runtimes to install; DefaultRuntimes when empty.
	Runtimes []string
	// Projects are the folders on this machine whose set-up script runs.
	Projects []string
	// FromPool is a catch-up on a machine a warm pool made: AGENTS_FROM_POOL=1.
	FromPool bool
	// Executable is the running program, which becomes the host when none is installed.
	Executable string
	// Smoke runs step 8 after the others pass: one short session on this machine.
	Smoke bool
	// ControlOnly is a machine with the control role alone (#7314): the host joins with roles
	// ["control"] and takes no agent work, so steps 5 to 8 have nothing to do.
	ControlOnly bool
}

// Deps are what the installer does outside itself, so a test can stand in for each.
type Deps struct {
	// StartHost starts the host with its root and name, as a systemd user service or detached.
	StartHost func(binary, root, name string) (how string, err error)
	// Call makes one call on the running host's socket and returns its result.
	Call func(method string, params any) (json.RawMessage, error)
	// Running says whether a host holds the root.
	Running func(root string) bool
	// Poll is how often a wait looks again.
	Poll time.Duration
	// JoinWait and RuntimeWait bound the waits for the host to join and a runtime to install.
	JoinWait, RuntimeWait time.Duration
	// ScriptTimeout bounds one project's script.
	ScriptTimeout time.Duration
	// SignInWait bounds step 8's wait for its runtime to be available, after step 7 said it
	// was signed in (step 7 itself waits for the person with no limit, #6853); SmokeWait
	// bounds the smoke session's answer.
	SignInWait, SmokeWait time.Duration
	// KeyWait is how long step 7 is running, for a client to write a key it has, before it
	// says it needs the person.
	KeyWait time.Duration
	Out     io.Writer
}

func (d *Deps) defaults() {
	if d.Poll == 0 {
		d.Poll = time.Second
	}
	if d.JoinWait == 0 {
		d.JoinWait = time.Minute
	}
	if d.RuntimeWait == 0 {
		d.RuntimeWait = 10 * time.Minute
	}
	if d.ScriptTimeout == 0 {
		d.ScriptTimeout = ScriptTimeout
	}
	if d.SignInWait == 0 {
		d.SignInWait = 10 * time.Minute
	}
	if d.KeyWait == 0 {
		d.KeyWait = 15 * time.Second
	}
	if d.SmokeWait == 0 {
		d.SmokeWait = SmokeTimeout
	}
	if d.Out == nil {
		d.Out = io.Discard
	}
}

// Layout is where the host is on this machine.
type Layout struct{ Dir, Bin, Root, Logs string }

// LayoutOf is the host's folders under home, as install.sh makes them.
func LayoutOf(home string) Layout {
	d := filepath.Join(home, ".agents-server")
	return Layout{Dir: d, Bin: filepath.Join(d, "bin"), Root: filepath.Join(d, "root"), Logs: filepath.Join(d, "root", "logs")}
}

// Joined says whether a host of this install has joined a control plane.
func (l Layout) Joined() bool { return l.joined() }

func (l Layout) joined() bool {
	_, err := os.Stat(filepath.Join(l.Root, "control-host.json"))
	return err == nil
}

// Run does steps 3 to 6, then 8 when o.Smoke, and says each. It returns the first step's error of those that
// failed, or nil when all four are done. A step that failed does not stop the one beside it
// (5 and 6 run together), but steps 5 and 6 do not start when 3 or 4 failed.
func Run(ctx context.Context, o Options, d Deps) error {
	d.defaults()
	l := LayoutOf(o.Home)
	if o.Name == "" {
		o.Name, _ = os.Hostname()
	}
	rep, err := NewReporter(l.Root)
	if err != nil {
		return err
	}
	var outMu sync.Mutex
	say := func(format string, a ...any) {
		outMu.Lock()
		defer outMu.Unlock()
		fmt.Fprintf(d.Out, format+"\n", a...)
	}
	failed := func(step wire.MachineSetupStepID, err error) error {
		rep.Say(step, wire.MachineSetupStateFailed, "", err.Error())
		say("%s: %v", step, err)
		return fmt.Errorf("%s: %w", step, err)
	}

	// 3: Install Agents.
	rep.Say(wire.MachineSetupStepIDInstall, wire.MachineSetupStateRunning, "Checking this machine", "")
	detail, err := installAgents(ctx, o, d, l, rep)
	if err != nil {
		return failed(wire.MachineSetupStepIDInstall, err)
	}
	rep.Say(wire.MachineSetupStepIDInstall, wire.MachineSetupStateDone, detail, "")
	say("Install Agents: %s", detail)

	// 4: Compare digits. This machine says the digits of the code it joined with; the control
	// plane compares them with the ones it made for a machine it made (#5359, #5765).
	digits := ""
	if code, err := controllink.ParseCode(o.Join); err == nil {
		digits = code.Digits()
	}
	if digits != "" {
		rep.Compared("This machine shows "+digits, digits)
	} else {
		rep.Compared("Joined with the code the control plane made for this machine", "")
	}

	if o.ControlOnly {
		// No runtimes, projects, sign-in or smoke session: it runs no agents.
		for _, step := range []wire.MachineSetupStepID{wire.MachineSetupStepIDRuntimes, wire.MachineSetupStepIDProjects} {
			rep.Say(step, wire.MachineSetupStateDone, controlOnlyStep, "")
		}
		say("%s", controlOnlyStep)
		return nil
	}

	// 5 and 6, together.
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	pool := false
	keep := func(err error) {
		mu.Lock()
		if first == nil {
			first = err
		}
		mu.Unlock()
	}
	mine := o // step 6 puts the pool's folders in its own copy: step 5 reads o beside it
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := installRuntimes(ctx, o, d, rep, say); err != nil {
			keep(failed(wire.MachineSetupStepIDRuntimes, err))
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		if pool, err = prepareProjects(ctx, &mine, d, l.Logs, rep, say); err != nil {
			keep(failed(wire.MachineSetupStepIDProjects, err))
		}
	}()
	wg.Wait()
	if first != nil || !o.Smoke || pool {
		// A pool signs in to nothing: steps 7 and 8 are the claim's (#6896).
		return first
	}
	// 7: Sign in to runtimes: a client's work, which this step waits on.
	if err := SignIn(ctx, o, d, rep, say); err != nil {
		return err
	}
	// 8: Smoke session.
	return Smoke(ctx, o, d, rep, say)
}

// controlOnlyStep is what steps 5 and 6 say on a control-only machine.
const controlOnlyStep = "None: this machine runs the control plane and no agents"

// Continue runs the set-up from step from (projects, sign-in or smoke) to the end, inside the
// host, on a machine that a person claimed from a warm pool (#6896). root is the host's root.
func Continue(ctx context.Context, o Options, d Deps, root string, from wire.MachineSetupStepID) error {
	d.defaults()
	rep := moreReports(ctx, root)
	say := func(string, ...any) {}
	switch from {
	case wire.MachineSetupStepIDProjects:
		if _, err := prepareProjects(ctx, &o, d, filepath.Join(root, "logs"), rep, say); err != nil {
			rep.Say(wire.MachineSetupStepIDProjects, wire.MachineSetupStateFailed, "", err.Error())
			return err
		}
		fallthrough
	case wire.MachineSetupStepIDSignIn:
		if err := SignIn(ctx, o, d, rep, say); err != nil {
			return err
		}
		fallthrough
	case wire.MachineSetupStepIDSmoke:
		return Smoke(ctx, o, d, rep, say)
	}
	return fmt.Errorf("set-up does not go on from step %s", from)
}

// installAgents is step 3. The host is this program's own copy, placed in ~/.agents-server/bin
// at its checksum, and started with the code, unless it is running and has joined already.
func installAgents(ctx context.Context, o Options, d Deps, l Layout, rep *Reporter) (string, error) {
	if l.joined() && d.Running(l.Root) {
		return "Agents is installed and running here already", nil
	}
	if _, err := controllink.ParseCode(o.Join); err != nil && !l.joined() {
		return "", fmt.Errorf("that is not a host code: %v", err)
	}
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(l.Root, 0o700); err != nil {
		return "", err
	}
	sum, err := placeBinary(o.Executable, l)
	if err != nil {
		return "", fmt.Errorf("could not put the host in place: %w", err)
	}
	if !l.joined() {
		if o.ControlOnly {
			// Before the code: the host enrols with the control role alone (#7314).
			if err := controllink.MarkControlOnly(l.Root); err != nil {
				return "", err
			}
		}
		// The host enrols with this code when it starts; a code it was refused is replaced.
		_ = os.Remove(filepath.Join(l.Root, "control-join-ask"))
		// store-bounds: not a store: a one-time hand-off of the join code to the host, which reads it when it starts
		if err := os.WriteFile(filepath.Join(l.Root, "control-join-code"), []byte(strings.TrimSpace(o.Join)), 0o600); err != nil {
			return "", err
		}
	}
	how := "already running"
	if !d.Running(l.Root) {
		rep.Say(wire.MachineSetupStepIDInstall, wire.MachineSetupStateRunning, "Starting the host", "")
		if how, err = d.StartHost(filepath.Join(l.Bin, "current"), l.Root, o.Name); err != nil {
			return "", fmt.Errorf("the host did not start: %w", err)
		}
	}
	rep.Say(wire.MachineSetupStepIDInstall, wire.MachineSetupStateRunning, "Waiting for the host to join the control plane", "")
	deadline := time.Now().Add(d.JoinWait)
	for !l.joined() {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the host started but has not joined yet; its log is %s", filepath.Join(l.Root, "daemon.log"))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(d.Poll):
		}
	}
	return fmt.Sprintf("Host %s, %s, joined the control plane", sum[:8], how), nil
}

// placeBinary copies the program to bin/agentsd-<sha256> and points bin/current at it, unless
// current is that copy already. It returns the checksum.
func placeBinary(exe string, l Layout) (string, error) {
	data, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	sum := hex.EncodeToString(h[:])
	target := "agentsd-" + sum
	if cur, err := os.Readlink(filepath.Join(l.Bin, "current")); err == nil && cur == target {
		if _, err := os.Stat(filepath.Join(l.Bin, target)); err == nil {
			return sum, nil
		}
	}
	tmp := filepath.Join(l.Bin, target+".tmp")
	// store-bounds: not a store: an install of the host's program
	if err := os.WriteFile(tmp, data, 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(l.Bin, target)); err != nil {
		return "", err
	}
	link := filepath.Join(l.Bin, "current.tmp")
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		return "", err
	}
	return sum, os.Rename(link, filepath.Join(l.Bin, "current"))
}

// installRuntimes is step 5: each runtime through the host's own runtimes/install, which
// installs from the pinned toolsets, all at once, until each says it is available.
func installRuntimes(ctx context.Context, o Options, d Deps, rep *Reporter, say func(string, ...any)) error {
	ids := o.Runtimes
	if len(ids) == 0 {
		ids = DefaultRuntimes
	}
	step := wire.MachineSetupStepIDRuntimes
	rep.Say(step, wire.MachineSetupStateRunning, "Installing "+strings.Join(ids, ", "), "")
	versions := map[string]string{}
	var mu sync.Mutex
	progress := map[string]string{}
	line := func() string {
		var parts []string
		for _, id := range ids {
			if v, ok := versions[id]; ok {
				parts = append(parts, id+" "+v)
			} else if p := progress[id]; p != "" {
				parts = append(parts, id+": "+p)
			} else {
				parts = append(parts, id)
			}
		}
		return strings.Join(parts, " · ")
	}
	var wg sync.WaitGroup
	errs := make([]error, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			version, err := installRuntime(ctx, d, id, func(p string) {
				mu.Lock()
				progress[id] = p
				text := line()
				mu.Unlock()
				rep.Say(step, wire.MachineSetupStateRunning, text, "")
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", id, err)
				return
			}
			versions[id] = version
			rep.Say(step, wire.MachineSetupStateRunning, line(), "")
		}()
	}
	wg.Wait()
	var problems []string
	for _, err := range errs {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	rep.Say(step, wire.MachineSetupStateDone, line(), "")
	say("Install runtimes: %s", line())
	return nil
}

// installRuntime asks the host to install one runtime and waits until it says it is
// available, or why not. It returns the version, "" when the runtime does not say.
func installRuntime(ctx context.Context, d Deps, id string, progress func(string)) (string, error) {
	status := func(method string, params any) (wire.RuntimeStatus, error) {
		raw, err := d.Call(method, params)
		if err != nil {
			return wire.RuntimeStatus{}, err
		}
		var s wire.RuntimeStatus
		return s, json.Unmarshal(raw, &s)
	}
	s, err := status(wire.CallRuntimesInstall.Name, wire.RuntimeRequest{RuntimeID: id})
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(d.RuntimeWait)
	for {
		a := s.Availability
		switch {
		case a.Available != nil || a.NeedsSignIn != nil:
			// Installed; signing in is step 7.
			if s.Version != nil {
				return *s.Version, nil
			}
			return "installed", nil
		case a.InstallFailed != nil:
			return "", fmt.Errorf("%s", a.InstallFailed.Reason)
		case a.Failed != nil:
			return "", fmt.Errorf("it did not start after it was installed")
		case a.Installing != nil && a.Installing.Progress != nil:
			progress(*a.Installing.Progress)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("it was not installed in %s", d.RuntimeWait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(d.Poll):
		}
		raw, err := d.Call(wire.CallRuntimesList.Name, wire.Empty{})
		if err != nil {
			return "", err
		}
		var all []wire.RuntimeStatus
		if err := json.Unmarshal(raw, &all); err != nil {
			return "", err
		}
		for _, r := range all {
			if r.Runtime.ID == id {
				s = r
			}
		}
	}
}

// poolProjects has the host get the projects of the pool that made this machine (#6890), and
// puts their folders in o.Projects. pool is false for a machine that no pool made.
func poolProjects(ctx context.Context, o *Options, d Deps, rep *Reporter) (pool bool, note string, err error) {
	for {
		got, err := call[wire.MachineSetupProjects](d, wire.CallSetupProjects.Name, wire.Empty{})
		if err != nil {
			return false, "", fmt.Errorf("the host did not get the pool's projects: %w", err)
		}
		if got.Finished {
			if got.Problem != nil {
				return got.Pool, "", fmt.Errorf("%s", *got.Problem)
			}
			if got.Pool {
				o.Projects = got.Folders
			}
			if got.Detail != nil {
				note = *got.Detail
			}
			return got.Pool, note, nil
		}
		if got.Detail != nil {
			rep.Say(wire.MachineSetupStepIDProjects, wire.MachineSetupStateRunning, *got.Detail, "")
		}
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-time.After(d.Poll):
		}
	}
}

// prepareProjects is step 6: a pool's projects are got (poolProjects), then each project
// folder's .agents/setup.sh, one after another, so two scripts never fight over one cache. A
// folder that is not here fails the step, and the others still run. pool says that a warm
// pool made this machine.
func prepareProjects(ctx context.Context, o *Options, d Deps, logs string, rep *Reporter, say func(string, ...any)) (bool, error) {
	step := wire.MachineSetupStepIDProjects
	rep.Say(step, wire.MachineSetupStateRunning, "Looking for projects", "")
	pool, note, err := poolProjects(ctx, o, d, rep)
	if err != nil {
		return pool, err
	}
	if len(o.Projects) == 0 {
		rep.Say(step, wire.MachineSetupStateDone, "No projects to prepare", "")
		say("Send projects: none")
		return pool, nil
	}
	rep.Say(step, wire.MachineSetupStateRunning, fmt.Sprintf("0 of %d projects", len(o.Projects)), "")
	var problems []string
	ran := 0
	for i, project := range o.Projects {
		name := filepath.Base(project)
		if info, err := os.Stat(project); err != nil || !info.IsDir() {
			problems = append(problems, name+": the folder is not on this machine")
			continue
		}
		output, closeLog := OpenScriptLog(logs, project)
		had, err := RunScript(ctx, project, o.Name, o.FromPool, d.ScriptTimeout, output, func(line string) {
			rep.Say(step, wire.MachineSetupStateRunning, fmt.Sprintf("%s (%d of %d): %s", name, i+1, len(o.Projects), line), "")
		})
		closeLog()
		switch {
		case err != nil:
			problems = append(problems, name+": "+err.Error())
		case had:
			ran++
		}
	}
	if len(problems) > 0 {
		return pool, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	text := fmt.Sprintf("%d projects ready", len(o.Projects))
	if len(o.Projects) == 1 {
		text = filepath.Base(o.Projects[0]) + " ready"
	}
	if ran > 0 {
		text += fmt.Sprintf(", %d set-up scripts ran", ran)
	}
	if note != "" {
		text += "; " + note
	}
	rep.Say(step, wire.MachineSetupStateDone, text, "")
	say("Send projects: %s", text)
	return pool, nil
}
