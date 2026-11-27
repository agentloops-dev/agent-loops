package agentsd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/hostsetup"
	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// userSetupEnded waits until d runs no setup.sh, and gives back machine-setup.json.
func userSetupEnded(t *testing.T, d *Daemon) userSetup {
	t.Helper()
	waitFor(t, "setup.sh ended", func() bool {
		d.userSetupMu.Lock()
		defer d.userSetupMu.Unlock()
		return !d.userSetupRuns
	})
	u, err := d.readUserSetup()
	must(t, err)
	return u
}

// userReports is what the host said of the step user, in order.
func userReports(t *testing.T, d *Daemon) []wire.MachineSetupReport {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.Store.Loc.Root, hostsetup.ReportsFile))
	must(t, err)
	var out []wire.MachineSetupReport
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var r wire.MachineSetupReport
		must(t, json.Unmarshal(sc.Bytes(), &r))
		if r.Step == wire.MachineSetupStepIDUser {
			out = append(out, r)
		}
	}
	return out
}

// #7155: a machine takes setup.sh that a machine of the lock brought with a client's approval
// for it, keeps it in the data folder, runs it as its user in the home folder with the set-up's
// names, says the step with the script's hash, and keeps its log. Run Again runs the copy again.
func TestATargetRunsTheSetUpScriptItWasSent(t *testing.T) {
	t.Parallel()
	d, phone, peer, state := lockedMachine(t)
	defer d.Shutdown()
	d.PersonalHome = t.TempDir()
	const text = "#!/bin/bash\necho \"$AGENTS_SETUP $AGENTS_MACHINE $AGENTS_FROM_POOL ${AGENTS_PROJECT-none}\" >> ran.txt\necho installed\n"
	fromPeer := withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer), to: "h-1"})
	deliver := wire.SetupDeliver{Script: "setup", Text: text, Approval: approvalOf(t, phone, state, "setup", "m:h-1", 2, text)}

	if err := d.takeUserSetup(context.Background(), deliver); codeOf(err) != wire.FailureNotInLock {
		t.Fatalf("taken with no machine's signature: %v", err)
	}
	must(t, d.takeUserSetup(fromPeer, deliver))
	u := userSetupEnded(t, d)
	if u.Running || u.Ran == nil || u.Error != "" || u.From != "h-2" || u.Hash != scriptHash(text) || u.TookMade {
		t.Fatalf("%+v", u)
	}
	ran, err := os.ReadFile(filepath.Join(d.PersonalHome, "ran.txt"))
	must(t, err)
	if got := strings.TrimSpace(string(ran)); got != "1 "+d.ownName()+" 0 none" {
		t.Fatalf("ran with %q", got)
	}
	logged, err := os.ReadFile(filepath.Join(d.Store.Loc.Root, "logs", hostsetup.UserScriptLog))
	must(t, err)
	if !strings.Contains(string(logged), "installed") {
		t.Fatalf("log: %q", logged)
	}
	if info, err := os.Stat(d.userSetupPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	said := userReports(t, d)
	if len(said) < 2 || said[0].State != wire.MachineSetupStateRunning || said[len(said)-1].State != wire.MachineSetupStateDone ||
		said[len(said)-1].Hash == nil || *said[len(said)-1].Hash != scriptHash(text) {
		t.Fatalf("%+v", said)
	}

	// Run Again.
	must(t, d.runUserSetupAgain())
	userSetupEnded(t, d)
	ran, _ = os.ReadFile(filepath.Join(d.PersonalHome, "ran.txt"))
	if strings.Count(string(ran), "\n") != 2 {
		t.Fatalf("Run Again did not run it: %q", ran)
	}
	// An older approval after a newer one is refused, and the copy stays.
	if err := d.takeUserSetup(fromPeer, wire.SetupDeliver{Script: "setup", Text: "echo old\n",
		Approval: approvalOf(t, phone, state, "setup", "m:h-1", 1, "echo old\n")}); err == nil {
		t.Fatal("an older approval was taken")
	}
	if u, _ := d.readUserSetup(); u.Hash != scriptHash(text) {
		t.Fatal("a refused copy replaced the one taken")
	}
}

// #7155: a run that fails says the step failed, with the last lines, and keeps why. A machine
// with no copy has nothing to run again.
func TestAFailedSetUpScriptSaysWhy(t *testing.T) {
	t.Parallel()
	d, phone, peer, state := lockedMachine(t)
	defer d.Shutdown()
	d.PersonalHome = t.TempDir()
	if err := d.runUserSetupAgain(); codeOf(err) != CodeInvalidParams {
		t.Fatalf("ran with no copy: %v", err)
	}
	const text = "echo 'X Failed to log in'\nexit 3\n"
	fromPeer := withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer), to: "h-1"})
	must(t, d.takeUserSetup(fromPeer, wire.SetupDeliver{Script: "setup", Text: text, Approval: approvalOf(t, phone, state, "setup", "m:h-1", 1, text)}))
	u := userSetupEnded(t, d)
	if u.Running || !strings.Contains(u.Error, "X Failed to log in") {
		t.Fatalf("%+v", u)
	}
	said := userReports(t, d)
	last := said[len(said)-1]
	if last.State != wire.MachineSetupStateFailed || last.Error == nil || !strings.Contains(*last.Error, "exit status 3") {
		t.Fatalf("%+v", last)
	}
}

// #7155, rule 2: an automatic send with an approval of scope made is taken by a machine that
// the installer set up, once. A run that the host's stop cut short runs again when it starts.
func TestAMadeMachineTakesOneAutomaticSendAndARunGoesOnAfterARestart(t *testing.T) {
	t.Parallel()
	d, phone, peer, state := lockedMachine(t)
	defer d.Shutdown()
	d.PersonalHome = t.TempDir()
	const text = "echo made >> made.txt\n"
	fromPeer := withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer), to: "h-1"})
	made := func(seq int64) wire.SetupDeliver {
		return wire.SetupDeliver{Script: "setup", Text: text, Approval: approvalOf(t, phone, state, "setup", "made", seq, text), Made: new(true)}
	}
	if err := d.takeUserSetup(fromPeer, made(1)); codeOf(err) != wire.FailureNotInLock {
		t.Fatalf("a machine the installer did not set up took a made send: %v", err)
	}
	reporter, err := hostsetup.NewReporter(d.Store.Loc.Root)
	must(t, err)
	reporter.Say(wire.MachineSetupStepIDInstall, wire.MachineSetupStateDone, "Installed", "")
	must(t, d.takeUserSetup(fromPeer, made(1)))
	if u := userSetupEnded(t, d); !u.TookMade || u.Error != "" {
		t.Fatalf("%+v", u)
	}
	if err := d.takeUserSetup(fromPeer, made(2)); codeOf(err) != wire.FailureNotInLock {
		t.Fatalf("a second automatic send was taken: %v", err)
	}

	// The host stopped while it ran: it runs again at the next start.
	u, _ := d.readUserSetup()
	u.Running = true
	must(t, d.keepUserSetup(u))
	d.resumeUserSetup()
	if u := userSetupEnded(t, d); u.Running {
		t.Fatalf("%+v", u)
	}
	got, _ := os.ReadFile(filepath.Join(d.PersonalHome, "made.txt"))
	if strings.Count(string(got), "made") != 2 {
		t.Fatalf("%q", got)
	}
}

// #7155: Send Set-up keeps the client's approval for that machine and refuses one for another,
// or a file that is not the approved text; it is not sent to the machine that has the scripts.
func TestSendSetUpNeedsAnApprovalForThatMachine(t *testing.T) {
	t.Parallel()
	const text = "#!/bin/bash\nbrew install jq\n"
	d, phone, _, state, write := senderWith(t, text)
	if err := d.sendSetup(context.Background(), wire.SetupSend{Host: "h-2", Approval: approvalOf(t, phone, state, "setup", "m:h-3", 1, text)}); codeOf(err) != CodeInvalidParams {
		t.Fatalf("an approval for another machine: %v", err)
	}
	if err := d.sendSetup(context.Background(), wire.SetupSend{Host: "h-1", Approval: approvalOf(t, phone, state, "setup", "m:h-1", 1, text)}); codeOf(err) != CodeInvalidParams {
		t.Fatalf("sent to itself: %v", err)
	}
	write("echo changed\n")
	if err := d.sendSetup(context.Background(), wire.SetupSend{Host: "h-2", Approval: approvalOf(t, phone, state, "setup", "m:h-2", 1, text)}); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a changed file: %v", err)
	}
	write(text)
	// With no control plane it is approved, then cannot go.
	if err := d.sendSetup(context.Background(), wire.SetupSend{Host: "h-2", Approval: approvalOf(t, phone, state, "setup", "m:h-2", 2, text)}); codeOf(err) != wire.FailureNotSupported {
		t.Fatalf("with no control plane: %v", err)
	}
	if _, r, err := d.approvedSetupScript("setup", "m:h-2"); err != nil || r.Seq != 2 {
		t.Fatalf("the approval was not kept: %v", err)
	}
}

// #7416: setup/wanted says at once whether the sender sends setup.sh: only with an approval for
// machines as they are made.
func TestSetupWantedSaysWhetherItSends(t *testing.T) {
	t.Parallel()
	const text = "#!/bin/bash\nbrew install jq\n"
	d, phone, _, state, _ := senderWith(t, text)
	if d.setupWanted("h-2") {
		t.Fatal("it sends with no approval for made machines")
	}
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: approvalOf(t, phone, state, "setup", machinelock.ScopeMade, 1, text)}); err != nil {
		t.Fatal(err)
	}
	if !d.setupWanted("h-2") {
		t.Fatal("it does not send with an approval for made machines")
	}
}

// #7528: the sender's last try says it is the last, so that the control plane fails the
// target's Your set-up when it is refused; a try that is tried again does not.
func TestTheLastTryOfASetupDeliverSaysItIsTheLast(t *testing.T) {
	t.Parallel()
	const text = "#!/bin/bash\nbrew install jq\n"
	d, phone, _, state, _ := senderWith(t, text)
	var lasts []bool
	ask := askControl(func(_ context.Context, method string, params any) (json.RawMessage, error) {
		b, _ := json.Marshal(params)
		var call wire.MachineWorkspaceCall
		mustUnmarshal(t, b, &call)
		var r wire.MachineSetupDeliver
		mustUnmarshal(t, call.Params, &r)
		lasts = append(lasts, r.Last != nil && *r.Last)
		return nil, &controllink.AskError{Code: wire.FailureSetupScriptChanged, Message: "refused"}
	})
	d.askControl.Store(&ask)
	approval := approvalOf(t, phone, state, "setup", machinelock.ScopeMade, 1, text)
	if err := d.deliverSetup(context.Background(), "h-2", text, approval, true, 1); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("one try: %v", err)
	}
	// A refusal that is not "not in the lock" is not tried again: the control plane fails the
	// step for it whatever last says.
	if err := d.deliverSetup(context.Background(), "h-2", text, approval, true, setupDeliverTries); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("twelve tries: %v", err)
	}
	if len(lasts) != 2 || !lasts[0] || lasts[1] {
		t.Fatalf("last: %v", lasts)
	}
}
