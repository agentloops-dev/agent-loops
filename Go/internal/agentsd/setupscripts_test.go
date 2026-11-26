package agentsd

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentloops-dev/agent-loops/Go/internal/machinelock"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// approvalOf is a setup.script record of text, signed by k.
func approvalOf(t *testing.T, k *ecdsa.PrivateKey, s *machinelock.State, script, scope string, seq int64, text string) wire.LockRecord {
	t.Helper()
	content, err := json.Marshal(wire.SetupScriptApproval{Hash: scriptHash(text), Size: int64(len(text))})
	must(t, err)
	return signedRecord(t, k, s, script, scope, seq, string(content))
}

// senderWith is a locked machine (h-1) whose home has setup.sh with text.
func senderWith(t *testing.T, text string) (d *Daemon, phone, peer *ecdsa.PrivateKey, state *machinelock.State, write func(string)) {
	t.Helper()
	d, phone, peer, state = lockedMachine(t)
	t.Cleanup(d.Shutdown)
	d.PersonalHome = t.TempDir()
	folder := filepath.Join(d.PersonalHome, ".agents", "machine-setup")
	must(t, os.MkdirAll(folder, 0o700))
	write = func(text string) {
		t.Helper()
		must(t, os.WriteFile(filepath.Join(folder, "setup.sh"), []byte(text), 0o600)) //nolint:gosec // G703: the test's own temp folder
	}
	write(text)
	return d, phone, peer, state, write
}

func codeOf(err error) int {
	var re *Error
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}

// #7167: the sender keeps an approval that a client of the lock signed of the text that is
// there now, and refuses one from an agent's, a machine's or an unknown key, of another text, at
// a lower number, or of another script.
func TestASenderKeepsOnlyAClientsApprovalOfTheTextThere(t *testing.T) {
	t.Parallel()
	const text = "#!/bin/bash\nbrew install jq\n"
	d, phone, peer, state, write := senderWith(t, text)
	scripts, err := d.setupScripts()
	must(t, err)
	if len(scripts.Scripts) != 2 || scripts.Scripts[0].Present || !scripts.Scripts[1].Present || !scripts.Scripts[1].Changed ||
		scripts.Scripts[1].NextSeq != 1 || *scripts.Scripts[1].Hash != scriptHash(text) {
		t.Fatalf("%+v", scripts)
	}
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeMade); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a script no client approved is used: %v", err)
	}
	approve := func(r wire.LockRecord) (wire.SetupScript, error) {
		return d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: r})
	}
	refused := func(name string, r wire.LockRecord, code int) {
		t.Helper()
		if _, err := approve(r); codeOf(err) != code {
			t.Errorf("%s: %v", name, err)
		}
	}
	agent := newLockKey(t)
	refused("an agent's key", approvalOf(t, agent, state, "setup", "made", 1, text), wire.FailureNotInLock)
	refused("a machine's key", approvalOf(t, peer, state, "setup", "made", 1, text), wire.FailureNotInLock)
	refused("another text", approvalOf(t, phone, state, "setup", "made", 1, "echo other\n"), wire.FailureSetupScriptChanged)
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "root", Approval: approvalOf(t, phone, state, "setup", "made", 1, text)}); codeOf(err) != CodeInvalidParams {
		t.Errorf("an approval of another script: %v", err)
	}

	got, err := approve(approvalOf(t, phone, state, "setup", "made", 5, text))
	if err != nil || got.Changed || len(got.Approvals) != 1 || got.NextSeq != 6 || *got.ApprovedText != text {
		t.Fatalf("%+v %v", got, err)
	}
	used, r, err := d.approvedSetupScript("setup", machinelock.ScopeMade)
	if err != nil || used != text || r.Seq != 5 {
		t.Fatalf("%q %v", used, err)
	}
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeOf("h-9")); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("an approval for made machines went to a named one: %v", err)
	}
	refused("a lower number", approvalOf(t, phone, state, "setup", "made", 4, text), CodeInvalidParams)
	info, err := os.Stat(filepath.Join(d.Store.Loc.Root, approvedSetupFile))
	must(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}

	// The file changes: it is not used until a client approves it again, and the new text's
	// approval replaces the old one's.
	const next = "#!/bin/bash\nbrew install jq gh\n"
	write(next)
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeMade); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("a changed file is used: %v", err)
	}
	if s, _ := d.setupScripts(); !s.Scripts[1].Changed {
		t.Fatal("not changed")
	}
	got, err = approve(approvalOf(t, phone, state, "setup", "m:h-9", 6, next))
	if err != nil || got.Changed || len(got.Approvals) != 1 || got.Approvals[0].Scope != "m:h-9" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, _, err := d.approvedSetupScript("setup", machinelock.ScopeMade); err == nil {
		t.Fatal("the old text's approval stayed for the new text")
	}
	// An empty approval takes it away.
	got, err = approve(signedRecord(t, phone, state, "setup", "m:h-9", 7, ""))
	if err != nil || len(got.Approvals) != 0 || !got.Changed {
		t.Fatalf("%+v %v", got, err)
	}

	// With no lock, nothing is approved.
	d.ResetLock()
	refused("no lock", approvalOf(t, phone, state, "setup", "made", 8, next), wire.FailureNotInLock)
}

// #7167: a script over its limit is not one to approve.
func TestASetUpScriptOverItsLimitIsNotUsed(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("#", 64<<10+1)
	d, phone, _, state, _ := senderWith(t, text)
	s, err := d.setupScripts()
	must(t, err)
	if s.Scripts[1].Problem == nil || s.Scripts[1].Text != nil {
		t.Fatalf("%+v", s.Scripts[1])
	}
	if _, err := d.approveSetupScript(wire.SetupApprove{Script: "setup", Approval: approvalOf(t, phone, state, "setup", "made", 1, text)}); codeOf(err) != wire.FailureSetupScriptChanged {
		t.Fatalf("%v", err)
	}
}

// #7167: the target takes a delivered script only with a client's approval of that text for it,
// at a number not lower than the last, brought by a machine of the lock for it.
func TestATargetChecksADeliveredScript(t *testing.T) {
	t.Parallel()
	d, phone, peer, state := lockedMachine(t)
	defer d.Shutdown()
	const text = "#!/bin/bash\nbrew install jq\n"
	fromPeer := withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer), to: "h-1"})
	refused := func(name string, ctx context.Context, r wire.LockRecord, made bool) {
		t.Helper()
		if _, err := d.checkSetupDelivery(ctx, "setup", text, r, made); codeOf(err) != wire.FailureNotInLock {
			t.Errorf("%s: %v", name, err)
		}
	}
	good := approvalOf(t, phone, state, "setup", "m:h-1", 3, text)
	// Rule 4: who brings it.
	refused("no signer", context.Background(), good, false)
	refused("a client brings it", withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, phone), to: "h-1"}), good, false)
	refused("for no machine", withSigner(context.Background(), &requestSigner{member: lockMemberOf(t, state, peer)}), good, false)
	// Rule 1: who approved it, and of what.
	refused("an agent's key", fromPeer, approvalOf(t, newLockKey(t), state, "setup", "m:h-1", 3, text), false)
	refused("a machine's key", fromPeer, approvalOf(t, peer, state, "setup", "m:h-1", 3, text), false)
	refused("a wrong hash", fromPeer, approvalOf(t, phone, state, "setup", "m:h-1", 3, "echo other\n"), false)
	refused("another script", fromPeer, approvalOf(t, phone, state, "root", "m:h-1", 3, text), false)
	// Rule 2: where it may go.
	refused("another machine", fromPeer, approvalOf(t, phone, state, "setup", "m:h-2", 3, text), false)
	refused("made, to a machine in use", fromPeer, approvalOf(t, phone, state, "setup", "made", 3, text), false)

	client, err := d.takeSetupDelivery(fromPeer, "setup", text, good, false)
	if err != nil || client.ID != phoneClient {
		t.Fatalf("%q %v", client.ID, err)
	}
	if _, err := d.checkSetupDelivery(fromPeer, "setup", text, good, false); err != nil {
		t.Fatalf("the same one again: %v", err)
	}
	// Rule 3: a lower number.
	refused("a lower number", fromPeer, approvalOf(t, phone, state, "setup", "made", 2, text), true)
	if _, err := d.checkSetupDelivery(fromPeer, "setup", text, approvalOf(t, phone, state, "setup", "made", 4, text), true); err != nil {
		t.Fatalf("a made machine's higher one: %v", err)
	}
}

func lockMemberOf(t *testing.T, s *machinelock.State, k *ecdsa.PrivateKey) machinelock.Member {
	t.Helper()
	m, ok := s.Member(machinelock.PublicKey(k))
	if !ok {
		t.Fatal("not a member")
	}
	return m
}
