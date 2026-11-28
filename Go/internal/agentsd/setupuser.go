package agentsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/store"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The person's setup.sh on the machine it is sent to, the target (#7155, gate 1 of #7151,
// sections 1 and 3). Another machine of the lock, the sender, gives it with setup/deliver: at a
// person's Send Set-up (setup/send, with an approval for this machine), or by itself once a
// machine it made, or one a person claimed from a warm pool, is in the lock (setup/wanted, with
// an approval of scope made). The target takes it by the rules of setupdelivery.go, keeps it in
// <root>/machine-setup.json (not in ~/.agents/machine-setup/, where this machine's own scripts
// are), and runs it as its user, as the step user (Your set-up). The host runs it, not the
// installer: a run that the host's stop cut short runs again when the host starts. Run Again
// (setup/run) runs the copy it has again.

const (
	userSetupFile = "machine-setup.json"
	// userSetupMostBytes bounds the file: one script (64 KiB) and its approval.
	userSetupMostBytes = 512 << 10
	// userSetupScript is the copy that runs, written before each run.
	userSetupScript = "machine-setup.sh"
	// userSetupProgressEvery is how often a running step says its last line.
	userSetupProgressEvery = 2 * time.Second
	// setupDeliverTries and setupDeliverPause: a delivery that the target refused as not in the
	// lock is tried again, as the sender may not have the new lock yet (section 3).
	setupDeliverTries = 12
	setupDeliverPause = 10 * time.Second
)

// userSetupTimeout bounds one run of setup.sh. A test sets it shorter.
var userSetupTimeout = hostsetup.ScriptTimeout

// userSetup is machine-setup.json: the copy that was sent last, and how its last run went.
type userSetup struct {
	Text     string          `json:"text"`
	Hash     string          `json:"hash"`
	Approval wire.LockRecord `json:"approval"`
	// From is the machine that sent it.
	From string `json:"from,omitempty"`
	// TookMade says this machine took its one automatic send (rule 2).
	TookMade bool `json:"tookMade,omitempty"`
	// Running says a run began and has not ended: the host runs it again when it starts.
	Running bool       `json:"running,omitempty"`
	Ran     *time.Time `json:"ranAt,omitempty"`
	Error   string     `json:"error,omitempty"`
}

type userSetupState struct {
	userSetupMu sync.Mutex
	// userSetupRuns says setup.sh runs now.
	userSetupRuns bool
	// userSetupChanged is closed, and made again, when a run begins or ends: it wakes the
	// projects step that waits for a failed Your set-up (#7546).
	userSetupChanged chan struct{}
}

// userSetupChanges is closed when setup.sh next begins or ends here.
func (d *Daemon) userSetupChanges() <-chan struct{} {
	d.userSetupMu.Lock()
	defer d.userSetupMu.Unlock()
	if d.userSetupChanged == nil {
		d.userSetupChanged = make(chan struct{})
	}
	return d.userSetupChanged
}

// userSetupRunning says setup.sh runs here now.
func (d *Daemon) userSetupRunning() bool {
	d.userSetupMu.Lock()
	defer d.userSetupMu.Unlock()
	return d.userSetupRuns
}

// userSetupChangedLocked wakes userSetupChanges. userSetupMu is held.
func (d *Daemon) userSetupChangedLocked() {
	if d.userSetupChanged != nil {
		close(d.userSetupChanged)
		d.userSetupChanged = nil
	}
}

func (d *Daemon) registerUserSetup() {
	On(d, wire.CallSetupDeliver, func(ctx context.Context, _ *Client, p wire.SetupDeliver) (wire.Empty, error) {
		return wire.Empty{}, d.takeUserSetup(ctx, p)
	})
	On(d, wire.CallSetupRun, func(context.Context, *Client, wire.Empty) (wire.Empty, error) {
		return wire.Empty{}, d.runUserSetupAgain()
	})
	On(d, wire.CallSetupSend, func(ctx context.Context, _ *Client, p wire.SetupSend) (wire.Empty, error) {
		return wire.Empty{}, d.sendSetup(ctx, p)
	})
	On(d, wire.CallSetupWanted, func(_ context.Context, _ *Client, p wire.SetupWanted) (wire.SetupWantedAnswer, error) {
		if p.Host == "" {
			return wire.SetupWantedAnswer{}, Invalid("No machine is named.")
		}
		return wire.SetupWantedAnswer{Sending: d.setupWanted(p.Host)}, nil
	})
}

func (d *Daemon) userSetupPath() string { return filepath.Join(d.Store.Loc.Root, userSetupFile) }

// readUserSetup reads machine-setup.json: an empty one when it is missing.
func (d *Daemon) readUserSetup() (userSetup, error) {
	var out userSetup
	b, err := os.ReadFile(d.userSetupPath())
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err == nil && len(b) > userSetupMostBytes {
		err = fmt.Errorf("%s is over %d KiB", userSetupFile, userSetupMostBytes>>10)
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return userSetup{}, fmt.Errorf("%s does not read: %w", userSetupFile, err)
	}
	return out, nil
}

func (d *Daemon) keepUserSetup(u userSetup) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if err := tooLarge(userSetupFile, b, userSetupMostBytes); err != nil {
		return err
	}
	// store-bounds: kind=document; count=1; size=512KiB; age=until-deleted; applied=on write (refused)
	return store.WriteAtomically(d.userSetupPath(), b, 0o600)
}

// setUpByInstaller says the installer set this machine up: it said its install step here. The
// host's own steps write to the same file, so the file alone does not say it.
func (d *Daemon) setUpByInstaller() bool {
	b, err := os.ReadFile(filepath.Join(d.Store.Loc.Root, hostsetup.ReportsFile))
	return err == nil && bytes.Contains(b, []byte(`"step":"`+string(wire.MachineSetupStepIDInstall)+`"`))
}

// fromPool says a warm pool made this machine, as its set-up said.
func (d *Daemon) fromPool() bool {
	b, err := os.ReadFile(filepath.Join(d.Store.Loc.Root, hostsetup.ReportsFile))
	return err == nil && bytes.Contains(b, []byte(`"fromPool":true`))
}

var errSetupRuns = Invalid("setup.sh runs on this machine now: wait for it to end.")

// takeUserSetup is setup/deliver: it checks the delivery, keeps the copy and starts its run.
func (d *Daemon) takeUserSetup(ctx context.Context, p wire.SetupDeliver) error {
	d.userSetupMu.Lock()
	defer d.userSetupMu.Unlock()
	if d.userSetupRuns {
		return errSetupRuns
	}
	had, err := d.readUserSetup()
	if err != nil {
		log.Printf("set-up: %v; a new copy replaces it", err)
		had = userSetup{}
	}
	made := p.Made != nil && *p.Made && d.setUpByInstaller() && !had.TookMade
	signer, err := d.requestFrom(ctx)
	if err != nil {
		return err
	}
	if _, err := d.takeSetupDelivery(ctx, p.Script, p.Text, p.Approval, made); err != nil {
		return err
	}
	u := userSetup{Text: p.Text, Hash: scriptHash(p.Text), Approval: p.Approval, From: signer, TookMade: had.TookMade || made, Running: true}
	if err := d.keepUserSetup(u); err != nil {
		return &Error{Code: wire.FailureCouldNotSave, Message: "setup.sh was not kept: " + err.Error()}
	}
	d.startUserSetupLocked(u)
	return nil
}

// requestFrom is the machine of the lock that signed the request ctx answers.
func (d *Daemon) requestFrom(ctx context.Context) (string, error) {
	s, ok := requestSignerOf(ctx)
	if !ok {
		return "", notInLock(errNotFromAMachine.Error())
	}
	return s.member.ID, nil
}

// runUserSetupAgain is setup/run: Run Again, and Try Again of the step user.
func (d *Daemon) runUserSetupAgain() error {
	d.userSetupMu.Lock()
	defer d.userSetupMu.Unlock()
	if d.userSetupRuns {
		return errSetupRuns
	}
	u, err := d.readUserSetup()
	if err != nil {
		return &Error{Code: CodeServer, Message: "The setup.sh this machine was sent cannot be read: " + err.Error()}
	}
	if u.Text == "" {
		return Invalid("No setup.sh was sent to this machine: use Send Set-up.")
	}
	u.Running = true
	if err := d.keepUserSetup(u); err != nil {
		return &Error{Code: wire.FailureCouldNotSave, Message: "setup.sh's run was not kept: " + err.Error()}
	}
	d.startUserSetupLocked(u)
	return nil
}

// resumeUserSetup runs again a run that the host's stop cut short.
func (d *Daemon) resumeUserSetup() {
	d.userSetupMu.Lock()
	defer d.userSetupMu.Unlock()
	u, err := d.readUserSetup()
	if err != nil || !u.Running || u.Text == "" || d.userSetupRuns {
		return
	}
	log.Printf("set-up: setup.sh had not ended when this host stopped; it runs again")
	d.startUserSetupLocked(u)
}

// startUserSetupLocked starts u's run in the background. userSetupMu is held.
func (d *Daemon) startUserSetupLocked(u userSetup) {
	d.userSetupRuns = true
	d.userSetupChangedLocked()
	d.goJob("your set-up", func() {
		defer func() {
			d.userSetupMu.Lock()
			d.userSetupRuns = false
			d.userSetupChangedLocked()
			d.userSetupMu.Unlock()
		}()
		d.runUserSetup(d.hostCtx(), u)
	})
}

// runUserSetup runs u's text as this machine's user, says the step as it goes, and keeps how it
// ended. A run that the host's end stopped says nothing: the next host runs it again.
func (d *Daemon) runUserSetup(ctx context.Context, u userSetup) {
	root := d.Store.Loc.Root
	say := hostsetup.MoreReports(ctx, root)
	step := wire.MachineSetupStepIDUser
	say.SayScript(step, wire.MachineSetupStateRunning, "Running setup.sh", "", u.Hash)
	end := func(problem string) {
		if ctx.Err() != nil {
			return
		}
		if problem == "" {
			say.SayScript(step, wire.MachineSetupStateDone, "setup.sh · exit 0", "", u.Hash)
		} else {
			say.SayScript(step, wire.MachineSetupStateFailed, "", problem, u.Hash)
		}
		d.userSetupMu.Lock()
		defer d.userSetupMu.Unlock()
		now, err := d.readUserSetup()
		if err != nil || now.Hash != u.Hash {
			return
		}
		at := time.Now()
		now.Running, now.Ran, now.Error = false, &at, problem
		if err := d.keepUserSetup(now); err != nil {
			log.Printf("set-up: how setup.sh ended was not kept: %v", err)
		}
	}
	names, err := declaredSecrets(u.Text)
	if err != nil {
		end(err.Error())
		return
	}
	waited := false
	err = d.waitForSetupSecrets(ctx, names, func(words string) {
		waited = true
		say.SayScript(step, wire.MachineSetupStateNeedsYou, words, "", u.Hash)
	})
	if err != nil {
		end(err.Error())
		return
	}
	if waited {
		say.SayScript(step, wire.MachineSetupStateRunning, "Running setup.sh", "", u.Hash)
	}
	script := filepath.Join(root, userSetupScript)
	// store-bounds: not a store: the copy of machine-setup.json's text that runs, written each run
	if err := os.WriteFile(script, []byte(u.Text), 0o600); err != nil {
		end("setup.sh could not be written to run: " + err.Error())
		return
	}
	home := d.personalHome()
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			end("This machine's user has no home folder.")
			return
		}
		home = h
	}
	output, closeLog := hostsetup.OpenUserScriptLog(filepath.Join(root, "logs"))
	defer closeLog()
	var lastSaid time.Time
	progress := func(line string) {
		if time.Since(lastSaid) >= userSetupProgressEvery {
			lastSaid = time.Now()
			say.SayScript(step, wire.MachineSetupStateRunning, line, "", u.Hash)
		}
	}
	env, endGrant, err := d.setupSecretEnv(hostsetup.UserScriptEnv(d.ownName(), d.fromPool()), names, u.Hash)
	if err != nil {
		end(err.Error())
		return
	}
	// The grant ends with the run; runBash has killed its process group by then.
	err = hostsetup.RunUserScript(ctx, script, home, env, userSetupTimeout, output, progress)
	endGrant()
	if err != nil {
		log.Printf("set-up: setup.sh: %v", err)
		end(err.Error())
		return
	}
	log.Printf("set-up: setup.sh ran")
	end("")
}

// MARK: the sender

// sendSetup is setup/send: Send Set-up from a person's client, with an approval of this
// machine's setup.sh for the machine p.Host.
func (d *Daemon) sendSetup(ctx context.Context, p wire.SetupSend) error {
	scope := machinelock.ScopeOf(p.Host)
	if p.Approval.Scope != scope {
		return Invalid("The approval is not for %s.", p.Host)
	}
	if p.Host == d.ownHostID() {
		return Invalid("This is the machine with your scripts: setup.sh is not sent to it.")
	}
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: p.Approval}); err != nil {
		return err
	}
	text, approval, err := d.approvedSetupScript("setup", scope)
	if err != nil {
		return err
	}
	return d.deliverSetup(ctx, p.Host, text, approval, false, 2)
}

// setupWanted is setup/wanted: a machine this one made, or one a person claimed from a warm pool,
// is in the lock. It is sent setup.sh when an approval of scope made is in force. It says at once
// whether it is (#7416), so that the control plane has that machine's projects wait for it, and
// sends it after.
func (d *Daemon) setupWanted(host string) bool {
	text, approval, err := d.approvedSetupScript("setup", machinelock.ScopeMade)
	if err != nil {
		// No setup.sh, or none approved for machines as they are made: nothing is sent.
		if s := d.readSetupScript("setup.sh", 64<<10); s.present {
			log.Printf("set-up: setup.sh is not sent to %s: %v", host, err)
		}
		return false
	}
	d.goJob("send set-up", func() {
		if err := d.deliverSetup(d.hostCtx(), host, text, approval, true, setupDeliverTries); err != nil {
			log.Printf("set-up: setup.sh was not sent to %s: %v", host, err)
			return
		}
		log.Printf("set-up: setup.sh sent to %s", host)
	})
	return true
}

// deliverSetup gives host setup.sh's text and approval, signed by this machine for host, through
// the control plane. A refusal as not in the lock is tried again, tries times in all. The last try
// says it is the last, so that the control plane fails the target's Your set-up when it fails
// (#7528).
func (d *Daemon) deliverSetup(ctx context.Context, host, text string, approval wire.LockRecord, made bool, tries int) error {
	for try := 1; ; try++ {
		err := d.deliverSetupOnce(ctx, host, text, approval, made, try >= tries)
		if err == nil || failureCode(err) != wire.FailureNotInLock || try >= tries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(setupDeliverPause):
		}
	}
}

func (d *Daemon) deliverSetupOnce(ctx context.Context, host, text string, approval wire.LockRecord, made, last bool) error {
	ask := d.askControl.Load()
	if ask == nil {
		return &Error{Code: wire.FailureNotSupported, Message: "This machine isn’t on a control plane."}
	}
	step := wire.SetupDeliver{Script: "setup", Text: text, Approval: approval}
	if made {
		step.Made = new(true)
	}
	relay := wire.MachineSetupDeliver{Host: host}
	if last {
		relay.Last = new(true)
	}
	return d.askPeerSigned(ctx, *ask, host, "set-up", wire.CallMachinesSetupDeliver.Name, relay,
		wire.CallSetupDeliver.Name, step)
}
